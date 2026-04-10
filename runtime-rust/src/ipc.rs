// src/ipc.rs — Unix socket connection handler and frame I/O.
//
// Frame format (identical to Go side in internal/runtime/ipc.go):
//
//   [4 bytes big-endian msg_type][4 bytes big-endian payload_length][payload]
//
// Payload is a prost-encoded message matching proto/runtime.proto.
//
// Message type constants:
//   1 = RunRequest   2 = RunResponse
//   3 = CancelRequest  4 = CancelResponse

use crate::executor::Executor;
use crate::proto::{CancelRequest, RunRequest};
use anyhow::{bail, Result};
use prost::Message;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;

const MSG_RUN_REQUEST: u32 = 1;
const MSG_RUN_RESPONSE: u32 = 2;
const MSG_CANCEL_REQUEST: u32 = 3;
const MSG_CANCEL_RESPONSE: u32 = 4;
const MAX_FRAME_BYTES: u32 = 64 * 1024 * 1024; // 64 MiB

/// Handle one accepted Unix socket connection.
///
/// Each connection carries exactly one request–response exchange.
/// The connection is closed after the response is sent.
pub async fn handle_connection(mut stream: UnixStream, executor: Arc<Executor>) -> Result<()> {
    let (msg_type, payload) = read_frame(&mut stream).await?;

    match msg_type {
        MSG_RUN_REQUEST => {
            let req = RunRequest::decode(payload.as_slice())?;
            tracing::debug!(job_id = %req.job_id, command = %req.command, "run request");

            // Execute synchronously on a blocking thread so we don't stall
            // the async runtime during process execution.
            let exec = Arc::clone(&executor);
            let resp = tokio::task::spawn_blocking(move || exec.run(req))
                .await
                .unwrap_or_else(|e| crate::proto::RunResponse {
                    error: format!("spawn_blocking panic: {}", e),
                    exit_code: -1,
                    ..Default::default()
                });

            let encoded = resp.encode_to_vec();
            write_frame(&mut stream, MSG_RUN_RESPONSE, &encoded).await?;
        }

        MSG_CANCEL_REQUEST => {
            let req = CancelRequest::decode(payload.as_slice())?;
            tracing::debug!(job_id = %req.job_id, "cancel request");

            let resp = executor.cancel(&req.job_id);
            let encoded = resp.encode_to_vec();
            write_frame(&mut stream, MSG_CANCEL_RESPONSE, &encoded).await?;
        }

        other => {
            bail!("unknown msg_type {}", other);
        }
    }

    Ok(())
}

// ── frame I/O ────────────────────────────────────────────────────────────────

async fn read_frame(stream: &mut UnixStream) -> Result<(u32, Vec<u8>)> {
    let mut hdr = [0u8; 8];
    stream.read_exact(&mut hdr).await?;

    let msg_type = u32::from_be_bytes(hdr[0..4].try_into().unwrap());
    let length = u32::from_be_bytes(hdr[4..8].try_into().unwrap());

    if length > MAX_FRAME_BYTES {
        bail!("frame too large: {} bytes", length);
    }

    let mut payload = vec![0u8; length as usize];
    stream.read_exact(&mut payload).await?;
    Ok((msg_type, payload))
}

async fn write_frame(stream: &mut UnixStream, msg_type: u32, payload: &[u8]) -> Result<()> {
    let mut hdr = [0u8; 8];
    hdr[0..4].copy_from_slice(&msg_type.to_be_bytes());
    hdr[4..8].copy_from_slice(&(payload.len() as u32).to_be_bytes());
    stream.write_all(&hdr).await?;
    stream.write_all(payload).await?;
    Ok(())
}

// ── unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Encode a frame into a Vec<u8> synchronously (for testing without a socket).
    fn encode_frame(msg_type: u32, payload: &[u8]) -> Vec<u8> {
        let mut out = Vec::with_capacity(8 + payload.len());
        out.extend_from_slice(&msg_type.to_be_bytes());
        out.extend_from_slice(&(payload.len() as u32).to_be_bytes());
        out.extend_from_slice(payload);
        out
    }

    #[test]
    fn frame_header_big_endian() {
        let frame = encode_frame(MSG_RUN_REQUEST, b"hello");
        // Bytes 0-3: msg_type = 1 in big-endian
        assert_eq!(&frame[0..4], &[0, 0, 0, 1]);
        // Bytes 4-7: payload length = 5 in big-endian
        assert_eq!(&frame[4..8], &[0, 0, 0, 5]);
        // Remaining bytes: payload
        assert_eq!(&frame[8..], b"hello");
    }

    #[test]
    fn frame_empty_payload() {
        let frame = encode_frame(MSG_CANCEL_RESPONSE, &[]);
        assert_eq!(frame.len(), 8);
        assert_eq!(&frame[4..8], &[0, 0, 0, 0]);
    }

    #[test]
    fn msg_type_constants_match_go() {
        // These values MUST stay in sync with internal/runtime/ipc.go.
        assert_eq!(MSG_RUN_REQUEST, 1);
        assert_eq!(MSG_RUN_RESPONSE, 2);
        assert_eq!(MSG_CANCEL_REQUEST, 3);
        assert_eq!(MSG_CANCEL_RESPONSE, 4);
    }

    #[test]
    fn run_request_round_trip_via_prost() {
        use crate::proto::RunRequest;
        use prost::Message;

        let req = RunRequest {
            job_id: "job-1".into(),
            command: "/bin/echo".into(),
            args: vec!["hello".into()],
            env: std::collections::HashMap::from([("K".into(), "V".into())]),
            timeout_seconds: 30,
            limits: None,
        };

        let encoded = req.encode_to_vec();
        let decoded = RunRequest::decode(encoded.as_slice()).expect("decode");

        assert_eq!(decoded.job_id, "job-1");
        assert_eq!(decoded.command, "/bin/echo");
        assert_eq!(decoded.args, vec!["hello"]);
        assert_eq!(decoded.timeout_seconds, 30);
        assert_eq!(decoded.env.get("K").map(String::as_str), Some("V"));
    }

    #[test]
    fn run_response_round_trip_via_prost() {
        use crate::proto::RunResponse;
        use prost::Message;

        let resp = RunResponse {
            job_id: "job-2".into(),
            exit_code: 42,
            stdout: b"out".to_vec(),
            stderr: b"err".to_vec(),
            error: String::new(),
            kill_reason: "Timeout".into(),
        };

        let encoded = resp.encode_to_vec();
        let decoded = RunResponse::decode(encoded.as_slice()).expect("decode");

        assert_eq!(decoded.job_id, "job-2");
        assert_eq!(decoded.exit_code, 42);
        assert_eq!(decoded.stdout, b"out");
        assert_eq!(decoded.stderr, b"err");
        assert_eq!(decoded.kill_reason, "Timeout");
    }

    #[test]
    fn cancel_request_round_trip() {
        use crate::proto::CancelRequest;
        use prost::Message;

        let req = CancelRequest { job_id: "cancel-me".into() };
        let encoded = req.encode_to_vec();
        let decoded = CancelRequest::decode(encoded.as_slice()).expect("decode");
        assert_eq!(decoded.job_id, "cancel-me");
    }
}
