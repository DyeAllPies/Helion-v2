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
	"log/slog"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DyeAllPies/Helion-v2/internal/api"
	"github.com/DyeAllPies/Helion-v2/internal/auth"
	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	"github.com/DyeAllPies/Helion-v2/internal/grpcserver"
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

	grpcSrv, err := grpcserver.New(bundle,
		grpcserver.WithRegistry(registry),
		grpcserver.WithJobStore(jobs),
		grpcserver.WithLogger(log),
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
	apiSrv := api.NewServer(jobs)
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	grpcSrv.Stop()
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP server shutdown", slog.Any("err", err))
	}

	log.Info("helion-coordinator stopped")
}

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
