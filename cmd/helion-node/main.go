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
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/nodeserver"
	"github.com/DyeAllPies/Helion-v2/internal/runtime"
	grpcclient "github.com/DyeAllPies/Helion-v2/internal/grpcclient"
	pb "github.com/DyeAllPies/Helion-v2/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	log := slog.Default()

	// ── configuration ─────────────────────────────────────────────────────────
	port := envOr("PORT", "8080")
	coordinatorAddr := envOr("HELION_COORDINATOR", "coordinator:9090")
	runtimeBackend := envOr("HELION_RUNTIME", "go")
	runtimeSocket := envOr("HELION_RUNTIME_SOCKET", "/run/helion/runtime.sock")

	hostname, _ := os.Hostname()
	nodeID := envOr("HELION_NODE_ID", fmt.Sprintf("%s:%s", hostname, port))
	nodeAddr := envOr("HELION_NODE_ADDR", fmt.Sprintf("%s:%s", hostname, port))

	log.Info("helion-node starting",
		slog.String("node_id", nodeID),
		slog.String("addr", nodeAddr),
		slog.String("coordinator", coordinatorAddr),
		slog.String("runtime", runtimeBackend),
	)

	// ── runtime selection ─────────────────────────────────────────────────────
	var rt runtime.Runtime
	switch runtimeBackend {
	case "rust":
		log.Info("using Rust runtime", slog.String("socket", runtimeSocket))
		rt = runtime.NewRustClient(runtimeSocket)
	default:
		log.Info("using Go runtime")
		rt = runtime.NewGoRuntime()
	}
	defer rt.Close()

	// ── TLS certificate bundle (bootstrap) ────────────────────────────────────
	// The node creates a self-signed bundle for the initial coordinator
	// connection. The coordinator CA-signs the node cert on registration;
	// certificate rotation (Phase 5) will handle the full bootstrap flow.
	bundle, err := auth.NewCoordinatorBundle()
	if err != nil {
		log.Error("create TLS bundle", slog.Any("err", err))
		os.Exit(1)
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
	_, err = client.Register(regCtx, nodeID, nodeAddr)
	if err != nil {
		log.Error("register with coordinator", slog.Any("err", err))
		os.Exit(1)
	}
	// Heartbeat interval is fixed at 10 s; full negotiation is a Phase 5 item
	// (certificate rotation / coordinator-driven config).
	heartbeatInterval := 10 * time.Second
	log.Info("registered", slog.Duration("heartbeat_interval", heartbeatInterval))

	// ── gRPC server (NodeService) ─────────────────────────────────────────────
	nodeSrv := nodeserver.New(rt, client, nodeID, log)

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

	// ── heartbeat loop ────────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			err := client.SendHeartbeats(ctx, nodeID, heartbeatInterval,
				nodeSrv.RunningJobs,
				func(cmd *pb.NodeCommand) {
					if cmd != nil {
						log.Info("coordinator command", slog.String("type", cmd.GetType().String()))
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}