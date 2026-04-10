// src/executor.rs — job execution engine for the Rust runtime.
//
// Executor::run() forks a child process for each job:
//   1. (Linux) Creates a cgroup v2 scope with memory + CPU limits.
//   2. (Linux) Installs a seccomp-bpf allowlist in the child via pre_exec.
//   3. Spawns the child and adds its PID to the cgroup.
//   4. Waits for the child; enforces a wall-clock timeout via a kill thread.
//   5. Returns a RunResponse with exit code, stdout, stderr, and kill reason.
//
// Executor::cancel() sends SIGKILL to a running job via a stored channel.

use crate::proto::{CancelResponse, RunRequest, RunResponse};
use std::collections::HashMap;
use std::process::Stdio;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::oneshot;

/// Shared job registry: job_id → cancel sender.
type CancelMap = Arc<Mutex<HashMap<String, oneshot::Sender<()>>>>;

pub struct Executor {
    cancels: CancelMap,
}

impl Executor {
    pub fn new() -> Self {
        Executor {
            cancels: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    /// Execute a job synchronously (call via `tokio::task::spawn_blocking`).
    pub fn run(&self, req: RunRequest) -> RunResponse {
        let (cancel_tx, cancel_rx) = oneshot::channel::<()>();
        self.cancels
            .lock()
            .unwrap()
            .insert(req.job_id.clone(), cancel_tx);

        let resp = self.run_inner(&req, cancel_rx);

        self.cancels.lock().unwrap().remove(&req.job_id);
        resp
    }

    /// Cancel a running job by job ID.
    pub fn cancel(&self, job_id: &str) -> CancelResponse {
        if let Some(tx) = self.cancels.lock().unwrap().remove(job_id) {
            let _ = tx.send(());
            CancelResponse { ok: true, error: String::new() }
        } else {
            CancelResponse {
                ok: false,
                error: format!("job {} not found", job_id),
            }
        }
    }

    fn run_inner(&self, req: &RunRequest, cancel_rx: oneshot::Receiver<()>) -> RunResponse {
        let timeout_secs = if req.timeout_seconds > 0 {
            req.timeout_seconds as u64
        } else {
            1800 // 30-minute safety default
        };

        // ── Linux: cgroup v2 setup ────────────────────────────────────────────
        #[cfg(target_os = "linux")]
        let cgroup = req.limits.as_ref().and_then(|lim| {
            match crate::cgroups::CgroupHandle::create(
                &req.job_id,
                lim.memory_bytes,
                lim.cpu_quota_us,
                lim.cpu_period_us,
            ) {
                Ok(cg) => Some(cg),
                Err(e) => {
                    tracing::warn!("cgroup setup failed: {}", e);
                    None
                }
            }
        });

        // ── Linux: compile seccomp filter ─────────────────────────────────────
        #[cfg(target_os = "linux")]
        let seccomp_prog = match crate::seccomp_filter::build_allowlist() {
            Ok(p) => Some(p),
            Err(e) => {
                tracing::warn!("seccomp compile failed: {}", e);
                None
            }
        };

        // ── build Command ─────────────────────────────────────────────────────
        let mut cmd = std::process::Command::new(&req.command);
        cmd.args(&req.args);
        cmd.env_clear();
        for (k, v) in &req.env {
            cmd.env(k, v);
        }
        cmd.stdout(Stdio::piped());
        cmd.stderr(Stdio::piped());

        // ── Linux/Unix: install seccomp in child via pre_exec ─────────────────
        #[cfg(target_os = "linux")]
        if let Some(prog) = seccomp_prog {
            use std::os::unix::process::CommandExt;
            // SAFETY: runs after fork in child process; only async-signal-safe
            // operations (prctl) are performed.
            unsafe {
                cmd.pre_exec(move || crate::seccomp_filter::apply(&prog));
            }
        }

        // ── spawn ─────────────────────────────────────────────────────────────
        let mut child = match cmd.spawn() {
            Ok(c) => c,
            Err(e) => {
                return RunResponse {
                    job_id: req.job_id.clone(),
                    exit_code: -1,
                    error: format!("exec failed: {}", e),
                    ..Default::default()
                }
            }
        };

        let pid = child.id();

        // ── Linux: move child into cgroup ─────────────────────────────────────
        #[cfg(target_os = "linux")]
        if let Some(ref cg) = cgroup {
            if let Err(e) = cg.add_pid(pid) {
                tracing::warn!("add_pid to cgroup: {}", e);
            }
        }

        // ── timeout thread ────────────────────────────────────────────────────
        // Races against process completion via a channel.
        let (done_tx, done_rx) = std::sync::mpsc::channel::<()>();
        let timeout_dur = Duration::from_secs(timeout_secs);

        let killer = std::thread::spawn(move || -> bool {
            match done_rx.recv_timeout(timeout_dur) {
                Ok(_) => false, // process finished normally
                Err(_) => {
                    // Timed out — kill the child.
                    kill_pid(pid);
                    true // timed out
                }
            }
        });

        // ── cancel thread: forward cancel signal to SIGKILL ───────────────────
        let pid_for_cancel = pid;
        std::thread::spawn(move || {
            if cancel_rx.blocking_recv().is_ok() {
                kill_pid(pid_for_cancel);
            }
        });

        // ── wait for child output ─────────────────────────────────────────────
        let output = match child.wait_with_output() {
            Ok(o) => o,
            Err(e) => {
                let _ = done_tx.send(());
                return RunResponse {
                    job_id: req.job_id.clone(),
                    exit_code: -1,
                    error: format!("wait failed: {}", e),
                    ..Default::default()
                };
            }
        };
        let _ = done_tx.send(()); // signal killer that process exited
        let timed_out = killer.join().unwrap_or(false);

        // ── determine kill reason (before cgroup is dropped) ─────────────────
        let exit_code = output.status.code().unwrap_or(-1) as i32;

        #[cfg(target_os = "linux")]
        let oom_killed = cgroup.as_ref().map(|cg| cg.was_oom_killed()).unwrap_or(false);
        #[cfg(not(target_os = "linux"))]
        let oom_killed = false;

        // Drop cgroup now (removes /sys/fs/cgroup/helion/{job_id}/).
        #[cfg(target_os = "linux")]
        drop(cgroup);

        let kill_reason = if timed_out {
            "Timeout".to_string()
        } else if oom_killed {
            "OOMKilled".to_string()
        } else {
            seccomp_kill_reason(&output)
        };

        RunResponse {
            job_id: req.job_id.clone(),
            exit_code,
            stdout: output.stdout,
            stderr: output.stderr,
            error: String::new(),
            kill_reason,
        }
    }
}

/// Send SIGKILL to `pid` (Unix) or silently skip (non-Unix).
fn kill_pid(pid: u32) {
    #[cfg(unix)]
    unsafe {
        libc::kill(pid as libc::pid_t, libc::SIGKILL);
    }
    #[cfg(not(unix))]
    let _ = pid;
}

/// Returns "Seccomp" if the process was killed by SIGSYS (seccomp default
/// action on Linux), otherwise returns an empty string.
#[allow(dead_code)] // called only on Linux inside kill_reason()
fn seccomp_kill_reason(output: &std::process::Output) -> String {
    #[cfg(target_os = "linux")]
    {
        use std::os::unix::process::ExitStatusExt;
        // The kernel sends SIGSYS (signal 31) when a seccomp KILL filter fires.
        if output.status.signal() == Some(31) {
            return "Seccomp".to_string();
        }
    }
    #[cfg(not(target_os = "linux"))]
    let _ = output;
    String::new()
}

// ── unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn executor_new() {
        let exec = Executor::new();
        // Should have an empty cancel map.
        assert!(exec.cancels.lock().unwrap().is_empty());
    }

    #[test]
    #[cfg(unix)]
    fn run_true_succeeds() {
        let exec = Executor::new();
        let req = RunRequest {
            job_id: "test-true".into(),
            command: "/bin/true".into(),
            args: vec![],
            env: Default::default(),
            timeout_seconds: 5,
            limits: None,
        };
        let resp = exec.run(req);
        assert_eq!(resp.exit_code, 0, "stderr: {}", String::from_utf8_lossy(&resp.stderr));
        assert!(resp.kill_reason.is_empty());
    }

    #[test]
    #[cfg(unix)]
    fn run_false_fails() {
        let exec = Executor::new();
        let req = RunRequest {
            job_id: "test-false".into(),
            command: "/bin/false".into(),
            args: vec![],
            env: Default::default(),
            timeout_seconds: 5,
            limits: None,
        };
        let resp = exec.run(req);
        assert_ne!(resp.exit_code, 0);
    }

    #[test]
    #[cfg(unix)]
    fn run_captures_stdout() {
        let exec = Executor::new();
        let req = RunRequest {
            job_id: "test-echo".into(),
            command: "/usr/bin/echo".into(),
            args: vec!["hello-rust".into()],
            env: Default::default(),
            timeout_seconds: 5,
            limits: None,
        };
        let resp = exec.run(req);
        assert_eq!(resp.exit_code, 0);
        assert!(
            String::from_utf8_lossy(&resp.stdout).contains("hello-rust"),
            "stdout: {:?}",
            resp.stdout
        );
    }

    #[test]
    #[cfg(unix)]
    fn run_timeout_kills_job() {
        let exec = Executor::new();
        let req = RunRequest {
            job_id: "test-timeout".into(),
            command: "/bin/sleep".into(),
            args: vec!["60".into()],
            env: Default::default(),
            timeout_seconds: 1,
            limits: None,
        };
        let resp = exec.run(req);
        assert_eq!(resp.kill_reason, "Timeout", "expected Timeout kill_reason");
    }

    #[test]
    fn cancel_unknown_job_returns_error() {
        let exec = Executor::new();
        let resp = exec.cancel("does-not-exist");
        assert!(!resp.ok);
        assert!(!resp.error.is_empty());
    }

    #[test]
    fn run_bad_command_returns_error() {
        let exec = Executor::new();
        let req = RunRequest {
            job_id: "test-bad-cmd".into(),
            command: "/nonexistent/binary".into(),
            args: vec![],
            env: Default::default(),
            timeout_seconds: 5,
            limits: None,
        };
        let resp = exec.run(req);
        assert_ne!(resp.exit_code, 0);
        assert!(!resp.error.is_empty(), "expected error message for missing binary");
    }
}
