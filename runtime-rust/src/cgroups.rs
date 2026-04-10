// src/cgroups.rs — cgroup v2 resource limits for job processes.
//
// Creates a per-job cgroup under /sys/fs/cgroup/helion/{job_id}/,
// writes memory.max and cpu.max limits, then adds the child PID to
// cgroup.procs.  The cgroup is removed when CgroupHandle is dropped.
//
// Only compiled on Linux (guarded by #[cfg(target_os = "linux")] in
// executor.rs).  On other platforms a no-op stub is used instead.

use anyhow::{Context, Result};
use std::fs;
use std::path::{Path, PathBuf};

/// A cgroup v2 scope created for one job.  Cleaned up on drop.
pub struct CgroupHandle {
    path: PathBuf,
}

impl CgroupHandle {
    /// Create a cgroup at /sys/fs/cgroup/helion/{job_id} and apply limits.
    ///
    /// `memory_bytes` — 0 means no limit.
    /// `cpu_quota_us` — microseconds of CPU per `cpu_period_us`; 0 = no limit.
    /// `cpu_period_us` — accounting period; 0 defaults to 100 000 µs (100 ms).
    pub fn create(
        job_id: &str,
        memory_bytes: u64,
        cpu_quota_us: u64,
        cpu_period_us: u64,
    ) -> Result<Self> {
        let path = PathBuf::from(format!("/sys/fs/cgroup/helion/{}", job_id));
        fs::create_dir_all(&path)
            .with_context(|| format!("create cgroup dir {:?}", path))?;

        // Ensure the subtree controller delegation for memory and cpu is
        // enabled on the parent cgroup (/sys/fs/cgroup/helion/).
        // Best-effort: may already be set or may require root.
        let _ = fs::write(
            "/sys/fs/cgroup/helion/cgroup.subtree_control",
            "+memory +cpu",
        );

        if memory_bytes > 0 {
            fs::write(path.join("memory.max"), format!("{}\n", memory_bytes))
                .context("write memory.max")?;
            // Enable OOM kill so the kernel kills the process (not the whole
            // cgroup) and we can detect it via the exit status.
            let _ = fs::write(path.join("memory.oom.group"), "0\n");
        }

        if cpu_quota_us > 0 {
            let period = if cpu_period_us > 0 { cpu_period_us } else { 100_000 };
            // cpu.max format: "quota period" (both in microseconds)
            fs::write(
                path.join("cpu.max"),
                format!("{} {}\n", cpu_quota_us, period),
            )
            .context("write cpu.max")?;
        }

        Ok(CgroupHandle { path })
    }

    /// Add `pid` to cgroup.procs, moving the process into this cgroup.
    pub fn add_pid(&self, pid: u32) -> Result<()> {
        fs::write(self.path.join("cgroup.procs"), format!("{}\n", pid))
            .with_context(|| format!("add pid {} to cgroup", pid))
    }

    /// Check whether an OOM kill occurred in this cgroup by reading
    /// memory.events.  Returns true if oom_kill count > 0.
    pub fn was_oom_killed(&self) -> bool {
        let events = match fs::read_to_string(self.path.join("memory.events")) {
            Ok(s) => s,
            Err(_) => return false,
        };
        for line in events.lines() {
            if let Some(rest) = line.strip_prefix("oom_kill ") {
                return rest.trim().parse::<u64>().unwrap_or(0) > 0;
            }
        }
        false
    }

    pub fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for CgroupHandle {
    fn drop(&mut self) {
        // Remove the cgroup directory.  This will fail if there are still
        // processes inside it, but by the time we drop the handle the job
        // process should already have exited.
        let _ = fs::remove_dir(&self.path);
        // Also try to clean up the parent helion/ scope if now empty.
        let _ = fs::remove_dir("/sys/fs/cgroup/helion");
    }
}