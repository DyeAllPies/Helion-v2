// cmd/helion-node/main.go
//
// Helion node agent entry point.
//
// Starts a gRPC server that exposes the standard gRPC Health Checking Protocol
// so Kubernetes liveness probes can confirm the process is alive.
//
// Environment variables
// ─────────────────────
//   PORT                  gRPC listen port (default: 8080)
//   HELION_COORDINATOR    Coordinator address (default: coordinator:9090)

package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	log := slog.Default()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	coordinator := os.Getenv("HELION_COORDINATOR")
	if coordinator == "" {
		coordinator = "coordinator:9090"
	}

	log.Info("helion-node starting",
		slog.String("port", port),
		slog.String("coordinator", coordinator),
	)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Error("listen failed", slog.Any("err", err))
		os.Exit(1)
	}

	srv := grpc.NewServer()

	// Standard gRPC health service — used by Kubernetes grpc liveness probe.
	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Info("helion-node listening", slog.String("addr", lis.Addr().String()))
	if err := srv.Serve(lis); err != nil {
		log.Error("serve failed", slog.Any("err", err))
		os.Exit(1)
	}
}
