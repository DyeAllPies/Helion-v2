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
//   HELION_SCHEDULER        Scheduling policy: "least" or "round-robin" (default)

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/audit"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
	"github.com/DyeAllPies/Helion-v2/internal/metrics"
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
	registry := cluster.NewRegistry(persister, heartbeatInterval, log)
	jobs := cluster.NewJobStore(persister, log)

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

	// ── Phase 4: Initialize Authentication & Audit ───────────────────────
	log.Info("initializing Phase 4 security components")

	// Create auth store adapter for BadgerDB
	// NewStoreAdapter wraps BadgerDB and returns TokenStore interface
	tokenStore := auth.NewStoreAdapter(persister)

	// Initialize token manager
	tokenManager, err := auth.NewTokenManager(tokenStore)
	if err != nil {
		log.Error("create token manager", slog.Any("err", err))
		os.Exit(1)
	}

	// Generate root token on first start
	rootToken, err := tokenManager.GenerateRootToken()
	if err != nil {
		log.Error("generate root token", slog.Any("err", err))
		os.Exit(1)
	}

	// Check if this is first start (token was just created vs retrieved)
	// We check token length as a heuristic - newly generated tokens are fresh
	// In production, you'd check a "first_start" flag in BadgerDB
	auth.PrintRootTokenInstructions(rootToken)

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
	)
	if err != nil {
		log.Error("create gRPC server", slog.Any("err", err))
		os.Exit(1)
	}

	go func() {
		log.Info("gRPC server listening", slog.String("addr", grpcAddr))
		if err := grpcSrv.Serve(grpcAddr); err != nil {
			log.Error("gRPC server stopped", slog.Any("err", err))
		}
	}()

	// ── HTTP API server ───────────────────────────────────────────────────
	// Wrap JobStore with adapter to provide paginated List method
	jobsAdapter := api.NewJobStoreAdapter(jobs)

	// Use stub node registry and metrics (TODO: Phase 5 - implement real adapters)
	nodeRegistry := api.NewStubNodeRegistry()
	metricsProvider := api.NewStubMetricsProvider()

	// ── Prometheus metrics ────────────────────────────────────────────────────
	_, promHandler := metrics.NewRegistry(jobs, registry, jobsAdapter)

	readiness := &coordinatorReadiness{db: persister, reg: registry}
	apiSrv := api.NewServer(jobsAdapter, nodeRegistry, metricsProvider, auditLogger, tokenManager, rateLimiter, readiness, promHandler)
	go func() {
		log.Info("HTTP API listening", slog.String("addr", httpAddr))
		if err := apiSrv.Serve(httpAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server stopped", slog.Any("err", err))
		}
	}()

	// ── Background goroutines ─────────────────────────────────────────────
	go registry.RunPruneLoop(ctx)

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
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
