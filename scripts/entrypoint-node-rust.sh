#!/bin/sh
# entrypoint-node-rust.sh
#
# Starts the Rust runtime in the background, waits for the socket,
# then starts the Go node agent in the foreground.

set -e

SOCKET="${HELION_RUNTIME_SOCKET:-/run/helion/runtime.sock}"

# Start Rust runtime in background
echo "Starting Rust runtime (socket: $SOCKET)..."
helion-runtime --socket "$SOCKET" &
RUST_PID=$!

# Wait for the socket to appear (up to 10s)
for i in $(seq 1 50); do
  if [ -S "$SOCKET" ]; then
    echo "Rust runtime ready."
    break
  fi
  if ! kill -0 "$RUST_PID" 2>/dev/null; then
    echo "FATAL: Rust runtime exited unexpectedly."
    exit 1
  fi
  sleep 0.2
done

if [ ! -S "$SOCKET" ]; then
  echo "FATAL: Rust runtime socket not found after 10s."
  exit 1
fi

# Start Go node agent in foreground
echo "Starting Go node agent..."
exec helion-node
