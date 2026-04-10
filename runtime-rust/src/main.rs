// helion-runtime — Rust job execution runtime for the Helion distributed
// job coordinator.
//
// Listens on a Unix domain socket for RunRequest / CancelRequest frames from
// the Go node agent (internal/runtime/rust_client.go).  For each RunRequest:
//
//   1. Creates a cgroup v2 scope (Linux) with memory + CPU limits.
//   2. Forks the job command; in the child, installs a seccomp-bpf allowlist
//      before exec (Linux).
//   3. Waits for the child; enforces a wall-clock timeout.
//   4. Returns a RunResponse with exit code, stdout/stderr, and kill reason.
//
// Usage:
//   helion-runtime [--socket <path>]
//
// Environment:
//   RUST_LOG   tracing filter (default: info)

#[cfg(target_os = "linux")]
mod cgroups;
mod executor;
mod ipc;
mod proto;
#[cfg(target_os = "linux")]
mod seccomp_filter;

use anyhow::Result;
use clap::Parser;
use executor::Executor;
use std::sync::Arc;
use tokio::net::UnixListener;

#[derive(Parser)]
#[command(name = "helion-runtime", about = "Helion Rust job execution runtime")]
struct Args {
    /// Unix domain socket path to listen on.
    #[arg(long, default_value = "/run/helion/runtime.sock")]
    socket: String,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info".into()),
        )
        .init();

    let args = Args::parse();

    // Remove stale socket file from a previous run.
    let _ = std::fs::remove_file(&args.socket);

    // Ensure the parent directory exists.
    if let Some(parent) = std::path::Path::new(&args.socket).parent() {
        std::fs::create_dir_all(parent)?;
    }

    let listener = UnixListener::bind(&args.socket)?;
    tracing::info!(socket = %args.socket, "helion-runtime listening");

    let executor = Arc::new(Executor::new());

    loop {
        let (stream, _) = listener.accept().await?;
        let exec = Arc::clone(&executor);
        tokio::spawn(async move {
            if let Err(e) = ipc::handle_connection(stream, exec).await {
                tracing::error!("connection error: {:#}", e);
            }
        });
    }
}
