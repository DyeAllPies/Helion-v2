// cmd/helion-coordinator/main.go
//
// Helion coordinator process entry point.
//
// Wires together:
//   BadgerJSONPersister → Registry + JobStore
//   gRPC server (mTLS) with Registry + JobStore
//   HTTP API server with JobStore
//   Background goroutines: PruneLoop
//
// Environment variables
// ─────────────────────
//   HELION_DB_PATH          BadgerDB directory (default: /var/lib/helion/db)
//   HELION_GRPC_ADDR        gRPC listen address   (default: 0.0.0.0:9090)
//   HELION_HTTP_ADDR        HTTP API listen address (default: 0.0.0.0:8080)
//   HELION_HEARTBEAT_SEC    Heartbeat interval in seconds (default: 10)
//   HELION_ROTATE_TOKEN     Rotate root token on startup: "true" or "false" (default: true)
//   HELION_TOKEN_FILE       Path to write the root token (default: /var/lib/helion/root-token)
//   HELION_NODE_PINS        Pre-configured cert pins (AUDIT M5), format:
//                             nodeID1:sha256hex,nodeID2:sha256hex,...
//                           Unconfigured nodes fall back to first-seen (dev mode).

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/events"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	"github.com/DyeAllPies/Helion-v2/internal/metrics"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/ratelimit"
)

func main() {
	log := slog.Default()

	// ── Configuration ─────────────────────────────────────────────────────
	dbPath := envOr("HELION_DB_PATH", "/var/lib/helion/db")
	grpcAddr := envOr("HELION_GRPC_ADDR", "0.0.0.0:9090")
	httpAddr := envOr("HELION_HTTP_ADDR", "0.0.0.0:8080")
	heartbeatSec := envInt("HELION_HEARTBEAT_SEC", 10)
	heartbeatInterval := time.Duration(heartbeatSec) * time.Second

	log.Info("helion-coordinator starting",
		slog.String("grpc_addr", grpcAddr),
		slog.String("http_addr", httpAddr),
		slog.String("db_path", dbPath),
		slog.Duration("heartbeat_interval", heartbeatInterval),
	)

	// ── Persistence ───────────────────────────────────────────────────────
	persister, err := cluster.NewBadgerJSONPersister(dbPath, heartbeatInterval)
	if err != nil {
		log.Error("open BadgerDB", slog.Any("err", err))
		os.Exit(1)
	}
	defer func() {
		if err := persister.Close(); err != nil {
			log.Error("close BadgerDB", slog.Any("err", err))
		}
	}()

	// ── Business logic ────────────────────────────────────────────────────
	// ── Event bus ─────────────────────────────────────────────────────────
	eventBus := events.NewBus(256, log)
	log.Info("event bus initialized")

	registry := cluster.NewRegistry(persister, heartbeatInterval, log)
	jobs := cluster.NewJobStore(persister, log)
	jobs.SetEventBus(eventBus)
	workflows := cluster.NewWorkflowStore(persister, log)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Restore persisted state before serving any traffic.
	if err := registry.Restore(ctx); err != nil {
		log.Error("restore registry", slog.Any("err", err))
		os.Exit(1)
	}
	if err := jobs.Restore(ctx); err != nil {
		log.Error("restore job store", slog.Any("err", err))
		os.Exit(1)
	}
	if err := workflows.Restore(ctx); err != nil {
		log.Error("restore workflow store", slog.Any("err", err))
		os.Exit(1)
	}

	// ── gRPC server ───────────────────────────────────────────────────────
	bundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		log.Error("create auth bundle", slog.Any("err", err))
		os.Exit(1)
	}

	// ── Phase 4: Enhance CA with Post-Quantum Cryptography ───────────────
	log.Info("enhancing CA with post-quantum cryptography")

	// Add ML-DSA (Dilithium-3) signing capability to CA
	if err := bundle.CA.EnhanceWithMLDSA(); err != nil {
		log.Error("enhance CA with ML-DSA", slog.Any("err", err))
		os.Exit(1)
	}

	// Add Hybrid KEM (X25519 + ML-KEM-768) capability
	bundle.CA.EnhanceWithHybridKEM()

	log.Info("post-quantum cryptography enabled",
		slog.String("kem", "X25519+ML-KEM-768"),
		slog.String("signature", "ECDSA+ML-DSA-65"))

	// ── Export CA cert for node agents in separate containers ─────────────
	if caFile := os.Getenv("HELION_CA_FILE"); caFile != "" {
		if err := auth.WriteCAFile(bundle.CA.CertPEM, caFile); err != nil {
			log.Error("write CA file", slog.Any("err", err))
			os.Exit(1)
		}
		log.Info("CA cert exported", slog.String("path", caFile))
	}

	// ── Phase 4: Initialize Authentication & Audit ───────────────────────
	log.Info("initializing Phase 4 security components")

	// Create auth store adapter for BadgerDB
	// NewStoreAdapter wraps BadgerDB and returns TokenStore interface
	tokenStore := auth.NewStoreAdapter(persister)

	// Initialize token manager
	tokenManager, err := auth.NewTokenManager(ctx, tokenStore)
	if err != nil {
		log.Error("create token manager", slog.Any("err", err))
		os.Exit(1)
	}

	// AUDIT L2 (fixed): root token is rotated on every startup by default,
	// invalidating any token leaked from a prior run. Set HELION_ROTATE_TOKEN=false
	// only in automation contexts where the token must remain stable across restarts.
	// Rotation revokes the previous token so a leaked token from a prior run is
	// immediately invalid. Disable rotation only when a stable token is required
	// (e.g. in automation that cannot capture stdout on every restart).
	//
	// AUDIT 2026-04-11/L4 (fixed): parse the env var strictly with
	// strconv.ParseBool so that HELION_ROTATE_TOKEN=0 / no / False also
	// disable rotation as the operator expects. Unknown values are a
	// fatal startup error rather than the previous
	// "anything-not-literal-false rotates" behaviour, so a typo surfaces
	// immediately.
	rotateToken := true
	if v := os.Getenv("HELION_ROTATE_TOKEN"); v != "" {
		parsed, perr := strconv.ParseBool(v)
		if perr != nil {
			log.Error("invalid HELION_ROTATE_TOKEN value",
				slog.String("value", v), slog.Any("err", perr))
			os.Exit(1)
		}
		rotateToken = parsed
	}
	var rootToken string
	if rotateToken {
		rootToken, err = tokenManager.RotateRootToken(ctx)
		if err != nil {
			log.Error("rotate root token", slog.Any("err", err))
			os.Exit(1)
		}
	} else {
		rootToken, err = tokenManager.GetRootToken(ctx)
		if err != nil {
			// No existing token — issue one for the first time.
			rootToken, err = tokenManager.RotateRootToken(ctx)
			if err != nil {
				log.Error("issue initial root token", slog.Any("err", err))
				os.Exit(1)
			}
		}
	}
	// AUDIT H1 (fixed): write the token to a file with mode 0600 instead of
	// printing it to stdout. HELION_TOKEN_FILE defaults to a well-known path
	// under /var/lib/helion; override for containers/dev environments.
	tokenFilePath := envOr("HELION_TOKEN_FILE", "/var/lib/helion/root-token")
	if err := auth.WriteRootToken(rootToken, tokenFilePath); err != nil {
		log.Error("write root token", slog.Any("err", err))
		os.Exit(1)
	}

	// Initialize audit logger (90-day retention)
	auditLogger := audit.NewLogger(persister, 90*24*time.Hour)

	// Log coordinator startup
	if err := auditLogger.LogCoordinatorStart(ctx, "v2.0-phase4"); err != nil {
		log.Warn("failed to log coordinator start", slog.Any("err", err))
	}

	// Initialize rate limiter
	rateLimiter := ratelimit.NewNodeLimiter()
	log.Info("rate limiter initialized",
		slog.Float64("limit_rps", rateLimiter.GetRate()))

	// ── gRPC server with Phase 4 security ────────────────────────────────
	grpcSrv, err := grpcserver.New(bundle,
		grpcserver.WithRegistry(registry),
		grpcserver.WithJobStore(jobs),
		grpcserver.WithLogger(log),
		grpcserver.WithRateLimiter(rateLimiter),
		grpcserver.WithAuditLogger(auditLogger),
		grpcserver.WithRevocationChecker(registry),
		grpcserver.WithJobCompletionCallback(func(cbCtx context.Context, jobID string, status cpb.JobStatus) {
			workflows.OnJobCompleted(cbCtx, jobID, status, jobs)
		}),
		grpcserver.WithRetryChecker(jobs),
	)
	if err != nil {
		log.Error("create gRPC server", slog.Any("err", err))
		os.Exit(1)
	}

	// Wire stream revocation: when RevokeNode is called the gRPC server
	// immediately closes the target node's active heartbeat stream.
	registry.SetStreamRevoker(grpcSrv)

	// AUDIT M5 (fixed): parse HELION_NODE_PINS to pre-provision expected cert
	// fingerprints. Nodes not in the map fall back to first-seen (TOFU) — an
	// acceptable dev-mode path but NOT hardened. Production deployments should
	// enumerate every known node ID in the env var.
	parsedPins, err := parseNodePins(os.Getenv("HELION_NODE_PINS"))
	if err != nil {
		log.Error("parse HELION_NODE_PINS", slog.Any("err", err))
		os.Exit(1)
	}
	if len(parsedPins) > 0 {
		log.Info("cert pinner: using pre-configured pins",
			slog.Int("count", len(parsedPins)))
	}
	registry.SetCertPinner(cluster.NewConfiguredCertPinner(parsedPins))

	// Wire ML-DSA verifier: at Register time the coordinator checks that the
	// node cert carries a valid out-of-band ML-DSA signature from this CA.
	registry.SetCertVerifier(bundle.CA)

	// AUDIT 2026-04-12/H1: wire cert issuer so Register returns a
	// coordinator-signed cert for the node's gRPC server.
	registry.SetCertIssuer(bundle.CA)

	go func() {
		log.Info("gRPC server listening", slog.String("addr", grpcAddr))
		if err := grpcSrv.Serve(grpcAddr); err != nil {
			log.Error("gRPC server stopped", slog.Any("err", err))
		}
	}()

	// ── HTTP API server ───────────────────────────────────────────────────
	// Wrap JobStore with adapter to provide paginated List method
	jobsAdapter := api.NewJobStoreAdapter(jobs)

	// AUDIT H5 (fixed): use real adapters backed by the live cluster.Registry
	// and JobStore so GET /nodes and GET /metrics report actual cluster state.
	// Previously wired NewStubNodeRegistry() / NewStubMetricsProvider() which
	// returned empty/fabricated data.
	nodeRegistry := api.NewRegistryNodeAdapter(registry)
	metricsProvider := api.NewRegistryMetricsAdapter(registry, jobs)

	// ── Prometheus metrics ────────────────────────────────────────────────────
	_, promHandler := metrics.NewRegistry(jobs, registry, jobsAdapter)

	readiness := &coordinatorReadiness{db: persister, reg: registry}
	apiSrv := api.NewServer(jobsAdapter, nodeRegistry, metricsProvider, auditLogger, tokenManager, rateLimiter, readiness, promHandler)
	apiSrv.SetWorkflowStore(workflows, jobs)
	apiSrv.SetEventBus(eventBus)
	go func() {
		log.Info("HTTP API listening", slog.String("addr", httpAddr))
		if err := apiSrv.Serve(httpAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server stopped", slog.Any("err", err))
		}
	}()

	// ── Job dispatch loop ────────────────────────────────────────────────
	policy := cluster.PolicyFromEnv()
	scheduler := cluster.NewScheduler(registry, policy)
	log.Info("scheduler initialized", slog.String("policy", scheduler.PolicyName()))

	// AUDIT 2026-04-12/H1 (fixed): Build a TLS config for dialing node agents.
	// Nodes now present coordinator-signed certificates on their gRPC server
	// (issued during Register and returned in RegisterResponse.SignedCertificate).
	// The coordinator verifies the cert chain against its own CA.
	//
	// InsecureSkipVerify=true is still needed because each node has a unique
	// CN/SAN (its nodeID) and the dispatcher connects to different nodes.
	// VerifyConnection manually checks the CA chain — this is the standard
	// pattern for services connecting to peers with dynamic hostnames.
	dispatchTLS, err := bundle.RawTLSConfig("helion-node")
	if err != nil {
		log.Error("dispatch TLS config", slog.Any("err", err))
		os.Exit(1)
	}
	caPool := dispatchTLS.RootCAs
	dispatchTLS.InsecureSkipVerify = true
	dispatchTLS.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("dispatch TLS: no peer certificates presented")
		}
		opts := x509.VerifyOptions{
			Roots:         caPool,
			Intermediates: x509.NewCertPool(),
		}
		for _, ic := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(ic)
		}
		if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {
			return fmt.Errorf("dispatch TLS: cert not signed by coordinator CA: %w", err)
		}
		return nil
	}
	nodeDispatcher := cluster.NewGRPCNodeDispatcher(dispatchTLS)
	dispatchLoop := cluster.NewDispatchLoop(jobs, scheduler, nodeDispatcher, 2*time.Second, log)
	dispatchLoop.SetWorkflowStore(workflows)
	go dispatchLoop.Run(ctx)

	// ── Background goroutines ─────────────────────────────────────────────
	go registry.RunPruneLoop(ctx)

	// AUDIT M1 (fixed): rate-limiter map was previously unbounded — each node
	// that ever connected would accumulate an entry forever. This GC goroutine
	// evicts entries that have been idle for 2× the heartbeat interval, bounding
	// the map to O(active nodes) rather than O(all nodes ever seen).
	// Stale threshold = 2× heartbeat interval; GC runs every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		staleThreshold := 2 * heartbeatInterval
		for {
			select {
			case <-ticker.C:
				evicted := rateLimiter.GarbageCollect(staleThreshold)
				if evicted > 0 {
					log.Info("rate limiter GC", slog.Int("evicted", evicted))
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// ── Wait for shutdown signal ──────────────────────────────────────────
	<-ctx.Done()
	log.Info("shutdown signal received")

	// Log coordinator shutdown to audit log
	if err := auditLogger.LogCoordinatorStop(context.Background(), "graceful shutdown"); err != nil {
		log.Warn("failed to log coordinator stop", slog.Any("err", err))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	grpcSrv.Stop()
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP server shutdown", slog.Any("err", err))
	}

	// AUDIT 2026-04-11/M1 (fixed): drain in-flight audit writes and async
	// node persists before returning so the last-second events are not
	// lost on SIGTERM.
	jobs.Close(5 * time.Second)
	registry.Close(5 * time.Second)

	log.Info("helion-coordinator stopped")
}

// coordinatorReadiness implements api.ReadinessChecker using the real
// BadgerDB persister and node registry.
type coordinatorReadiness struct {
	db  interface{ Ping() error }
	reg interface{ Len() int }
}

func (r *coordinatorReadiness) Ping() error    { return r.db.Ping() }
func (r *coordinatorReadiness) RegistryLen() int { return r.reg.Len() }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			slog.Warn("invalid integer env var, using default",
				slog.String("key", key), slog.String("value", v), slog.Int("default", def))
			return def
		}
		return n
	}
	return def
}

// parseNodePins parses HELION_NODE_PINS into a nodeID → fingerprint map.
// Format: "nodeID1:sha256hex,nodeID2:sha256hex,...".  Whitespace around
// entries and their components is trimmed; empty entries are skipped.
//
// Each fingerprint must be exactly 64 hex characters (the output of
// cluster.CertFingerprint). Any malformed entry is a fatal configuration
// error so the operator finds out at startup rather than at 3am when a
// node fails to register.
//
// An empty or unset value returns a nil map with no error, meaning
// "no pre-configured pins — fall back to first-seen".
func parseNodePins(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	pins := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		nodeID, fp, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("HELION_NODE_PINS: entry %q missing ':' separator", entry)
		}
		nodeID = strings.TrimSpace(nodeID)
		fp = strings.TrimSpace(fp)
		if nodeID == "" {
			return nil, fmt.Errorf("HELION_NODE_PINS: entry %q has empty node ID", entry)
		}
		if len(fp) != 64 {
			return nil, fmt.Errorf("HELION_NODE_PINS: entry %q: fingerprint must be 64 hex chars (got %d)", entry, len(fp))
		}
		if !isLowerHex(fp) {
			return nil, fmt.Errorf("HELION_NODE_PINS: entry %q: fingerprint contains non-hex characters", entry)
		}
		pins[nodeID] = fp
	}
	if len(pins) == 0 {
		return nil, nil
	}
	return pins, nil
}

// isLowerHex reports whether s contains only lowercase hex digits 0-9 a-f.
// cluster.CertFingerprint emits lowercase via hex.EncodeToString, so the
// pinned value must use the same encoding for exact string comparison.
func isLowerHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
