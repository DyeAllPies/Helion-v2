// cmd/helion-node/main.go
//
// Helion node agent entry point.
//
// The node agent:
//  1. Creates a TLS certificate bundle (self-signed for bootstrap).
//  2. Connects to the coordinator and registers itself.
//  3. Starts a gRPC server exposing NodeService (Dispatch / Cancel / GetMetrics).
//  4. Starts a heartbeat loop that reports load to the coordinator.
//  5. Shuts down gracefully on SIGTERM / SIGINT.
//
// Environment variables
// ─────────────────────
//   HELION_NODE_ID          Stable node identifier (default: hostname:port)
//   HELION_NODE_ADDR        Address advertised to the coordinator (default: hostname:PORT)
//   PORT                    gRPC listen port (default: 8080)
//   HELION_COORDINATOR      Coordinator gRPC address (default: coordinator:9090)
//   HELION_RUNTIME          Runtime backend: "go" (default) or "rust"
//   HELION_RUNTIME_SOCKET   Unix socket path for Rust runtime (default: /run/helion/runtime.sock)

package main

import (
	"context"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	goruntime "runtime"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	grpcclient "github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	"github.com/DyeAllPies/Helion-v2/internal/nodeserver"
	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	"github.com/DyeAllPies/Helion-v2/internal/staging"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// nodeConfig is the resolved runtime configuration for the node agent,
// derived from environment variables and the local hostname. Extracting
// this into a named type lets the config-parsing logic be unit-tested
// independently of the side-effectful bootstrap flow in main().
type nodeConfig struct {
	Port            string
	CoordinatorAddr string
	RuntimeBackend  string
	RuntimeSocket   string
	NodeID          string
	NodeAddr        string
}

// loadNodeConfig resolves the node agent's configuration from environment
// variables, falling back to sensible defaults and deriving the ID/address
// from hostname when they are not explicitly set.
func loadNodeConfig(hostname string) nodeConfig {
	cfg := nodeConfig{
		Port:            envOr("PORT", "8080"),
		CoordinatorAddr: envOr("HELION_COORDINATOR", "coordinator:9090"),
		RuntimeBackend:  envOr("HELION_RUNTIME", "go"),
		RuntimeSocket:   envOr("HELION_RUNTIME_SOCKET", "/run/helion/runtime.sock"),
	}
	cfg.NodeID = envOr("HELION_NODE_ID", fmt.Sprintf("%s:%s", hostname, cfg.Port))
	cfg.NodeAddr = envOr("HELION_NODE_ADDR", fmt.Sprintf("%s:%s", hostname, cfg.Port))
	return cfg
}

// selectRuntime constructs the Runtime implementation named by backend,
// defaulting to the Go runtime for any unknown value. Extracted so the
// backend-selection logic can be unit-tested without wiring a full agent.
//
// totalGPUs sizes the Go runtime's device-index allocator. Passing 0
// disables GPU allocation (the allocator rejects any request > 0,
// matching the scheduler's filterByGPU contract that a CPU-only node
// never sees a GPU job). Ignored on the Rust backend — GPU allocation
// lives in the Rust executor there.
func selectRuntime(backend, socket string, totalGPUs uint32, log *slog.Logger) runtime.Runtime {
	switch backend {
	case "rust":
		log.Info("using Rust runtime",
			slog.String("socket", socket),
			slog.Uint64("total_gpus", uint64(totalGPUs)))
		// GPU allocation lives Go-side: the RustClient claims
		// device indices before IPC and stamps CUDA_VISIBLE_DEVICES
		// into req.Env. The Rust executor inherits the env
		// unchanged — no Rust-side wire schema or codepath
		// changes needed.
		return runtime.NewRustClientWithGPUs(socket, totalGPUs)
	default:
		log.Info("using Go runtime",
			slog.Uint64("total_gpus", uint64(totalGPUs)))
		return runtime.NewGoRuntimeWithGPUs(totalGPUs)
	}
}

func main() {
	log := slog.Default()

	// ── configuration ─────────────────────────────────────────────────────────
	hostname, _ := os.Hostname()
	cfg := loadNodeConfig(hostname)
	port := cfg.Port
	coordinatorAddr := cfg.CoordinatorAddr
	runtimeBackend := cfg.RuntimeBackend
	runtimeSocket := cfg.RuntimeSocket
	nodeID := cfg.NodeID
	nodeAddr := cfg.NodeAddr

	log.Info("helion-node starting",
		slog.String("node_id", nodeID),
		slog.String("addr", nodeAddr),
		slog.String("coordinator", coordinatorAddr),
		slog.String("runtime", runtimeBackend),
	)

	// ── GPU probe ─────────────────────────────────────────────────────────────
	// Count once at startup; the result feeds both the runtime's
	// device-index allocator and the heartbeat capacity report so
	// coordinator-side scheduling matches what the runtime can
	// actually satisfy. 0 on CPU-only hosts (the common case on CI).
	totalGPUs := gpuCountProbe()

	// ── runtime selection ─────────────────────────────────────────────────────
	rt := selectRuntime(runtimeBackend, runtimeSocket, totalGPUs, log)
	defer rt.Close()

	// ── TLS certificate bundle (bootstrap) ────────────────────────────────────
	// If HELION_CA_CERT points to the coordinator's exported CA cert, the node
	// trusts that CA for TLS verification.  Otherwise it creates a fully
	// self-signed bundle (single-process / test mode).
	var (
		bundle *auth.Bundle
		err    error
	)
	if caPath := os.Getenv("HELION_CA_CERT"); caPath != "" {
		// Wait for the coordinator to write the CA file (may not exist yet
		// if the coordinator is still starting up).
		for i := 0; i < 30; i++ {
			bundle, err = auth.NewNodeBundleFromCAFile(caPath)
			if err == nil {
				break
			}
			log.Info("waiting for CA cert file", slog.String("path", caPath), slog.Int("attempt", i+1))
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			log.Error("load CA cert", slog.String("path", caPath), slog.Any("err", err))
			os.Exit(1)
		}
		log.Info("using coordinator CA cert", slog.String("path", caPath))
	} else {
		bundle, err = auth.NewCoordinatorBundle()
		if err != nil {
			log.Error("create TLS bundle", slog.Any("err", err))
			os.Exit(1)
		}
	}

	// ── coordinator registration ───────────────────────────────────────────────
	coordinatorName := "coordinator"
	client, err := grpcclient.New(coordinatorAddr, coordinatorName, bundle)
	if err != nil {
		log.Error("dial coordinator", slog.Any("err", err))
		os.Exit(1)
	}
	defer client.Close()

	regCtx, regCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer regCancel()
	nodeLabels := gatherNodeLabels()
	if len(nodeLabels) > 0 {
		log.Info("node labels for registration",
			slog.Int("count", len(nodeLabels)),
			slog.Any("labels", nodeLabels))
	}
	regResp, err := client.RegisterWithLabels(regCtx, nodeID, nodeAddr, nodeLabels)
	if err != nil {
		log.Error("register with coordinator", slog.Any("err", err))
		os.Exit(1)
	}

	// AUDIT 2026-04-12/H1: if the coordinator returned a signed certificate,
	// use it for the node's gRPC server so the coordinator can verify the
	// node's cert during dispatch (proper CA chain verification).
	if payload := regResp.GetSignedCertificate(); len(payload) > 0 {
		certPEM, keyPEM := splitCertKeyPEM(payload)
		if len(certPEM) > 0 && len(keyPEM) > 0 {
			bundle.CertPEM = certPEM
			bundle.KeyPEM = keyPEM
			log.Info("using coordinator-signed certificate for gRPC server")
		}
	}

	// Heartbeat interval is fixed at 10 s; full negotiation is a Phase 5 item
	// (certificate rotation / coordinator-driven config).
	heartbeatInterval := 10 * time.Second
	log.Info("registered", slog.Duration("heartbeat_interval", heartbeatInterval))

	// ── optional artifact stager (step 2 of the ML pipeline) ──────────────
	// Opt-in: if HELION_ARTIFACTS_BACKEND is unset the node starts
	// without a stager and refuses ML jobs. This keeps non-ML
	// deployments unchanged.
	var stager *staging.Stager
	if os.Getenv("HELION_ARTIFACTS_BACKEND") != "" {
		store, err := artifacts.Open(artifacts.ConfigFromEnv())
		if err != nil {
			log.Error("artifact store open", slog.Any("err", err))
			os.Exit(1)
		}
		// Probe the configured store with a Put→Get→Delete round-trip
		// so a misconfig (typo'd bucket, bad creds, unreachable
		// endpoint, missing write permission) fails loud *here*
		// rather than silently when the first ML job lands. A cold
		// restart against a still-warming-up MinIO can fail the
		// probe; the node exits, the orchestrator retries it, which
		// is the intended behaviour.
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if perr := artifacts.VerifyStore(probeCtx, store); perr != nil {
			cancel()
			log.Error("artifact store probe failed",
				slog.String("backend", os.Getenv("HELION_ARTIFACTS_BACKEND")),
				slog.Any("err", perr))
			os.Exit(1)
		}
		cancel()
		keep := os.Getenv("HELION_KEEP_WORKDIR") == "1"
		stager = staging.NewStager(store, os.Getenv("HELION_WORK_ROOT"), keep, log)
		// Reap anything the previous node-agent lifetime left behind.
		// HELION_KEEP_WORKDIR=1 disables the sweep too (same opt-in
		// switch operators already know for live cleanup).
		if !keep {
			if removed, serr := stager.SweepStaleWorkdirs(staging.DefaultSweepAge); serr != nil {
				log.Warn("workdir sweep errors",
					slog.Int("removed", removed), slog.Any("err", serr))
			} else if removed > 0 {
				log.Info("workdir sweep",
					slog.Int("removed", removed),
					slog.Duration("older_than", staging.DefaultSweepAge))
			}
		}
		log.Info("artifact stager enabled",
			slog.String("backend", os.Getenv("HELION_ARTIFACTS_BACKEND")))
	}

	// ── gRPC server (NodeService) ─────────────────────────────────────────────
	nodeSrv := nodeserver.New(rt, stager, client, nodeID, runtimeBackend, log)

	serverCreds, err := bundle.ServerCredentials()
	if err != nil {
		log.Error("server credentials", slog.Any("err", err))
		os.Exit(1)
	}

	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterNodeServiceServer(grpcSrv, nodeSrv)

	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Error("listen", slog.Any("err", err))
		os.Exit(1)
	}

	go func() {
		log.Info("gRPC server listening", slog.String("addr", lis.Addr().String()))
		if err := grpcSrv.Serve(lis); err != nil {
			log.Error("gRPC serve", slog.Any("err", err))
		}
	}()

	// ── detect node capacity ──────────────────────────────────────────────────
	var memStats goruntime.MemStats
	goruntime.ReadMemStats(&memStats)
	nodeCapacity := &grpcclient.NodeCapacity{
		CpuMillicores: uint32(goruntime.NumCPU()) * 1000,
		TotalMemBytes: memStats.Sys, // approximate total memory available to Go
		MaxSlots:      uint32(goruntime.NumCPU()) * 2,
		TotalGpus:     totalGPUs,
	}
	log.Info("node capacity detected",
		slog.Uint64("cpu_millicores", uint64(nodeCapacity.CpuMillicores)),
		slog.Uint64("total_mem_bytes", nodeCapacity.TotalMemBytes),
		slog.Uint64("max_slots", uint64(nodeCapacity.MaxSlots)),
		slog.Uint64("total_gpus", uint64(nodeCapacity.TotalGpus)),
	)

	// ── heartbeat loop ────────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			err := client.SendHeartbeats(ctx, nodeID, heartbeatInterval,
				nodeSrv.RunningJobs,
				nodeCapacity,
				func(ack *pb.HeartbeatAck) {
					if ack != nil && ack.Command != pb.NodeCommand_NODE_COMMAND_NONE {
						log.Info("coordinator command", slog.String("command", ack.Command.String()))
					}
				},
			)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Warn("heartbeat error — reconnecting", slog.Any("err", err))
				time.Sleep(5 * time.Second)
			}
		}
	}()

	// ── shutdown ──────────────────────────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("shutting down")
	cancel()
	grpcSrv.GracefulStop()
	log.Info("stopped")
}

// splitCertKeyPEM splits a concatenated PEM payload into certificate and
// private key components by scanning for the PEM block types.
func splitCertKeyPEM(payload []byte) (certPEM, keyPEM []byte) {
	rest := payload
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		encoded := pem.EncodeToMemory(block)
		switch block.Type {
		case "CERTIFICATE":
			certPEM = append(certPEM, encoded...)
		case "EC PRIVATE KEY", "PRIVATE KEY":
			keyPEM = append(keyPEM, encoded...)
		}
	}
	return certPEM, keyPEM
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}