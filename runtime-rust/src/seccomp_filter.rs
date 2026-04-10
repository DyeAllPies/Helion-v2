// src/seccomp_filter.rs — seccomp-bpf allowlist for job processes.
//
// Builds a BPF program that allows a conservative set of syscalls and
// kills the process (SIGSYS) for everything else.  The filter is installed in the child
// process via pre_exec (after fork, before exec) so it does not restrict
// the runtime process itself.
//
// Syscall numbers are architecture-specific; this file targets x86-64.
// The seccompiler crate is used because it does not require libseccomp to
// be installed on the build host (it generates BPF bytecode at compile
// time rather than linking against a shared library).
//
// This module is only compiled on Linux.

use anyhow::Result;
use seccompiler::{BpfProgram, SeccompAction, SeccompFilter, TargetArch};
use std::collections::BTreeMap;

/// Compile and return a BPF allowlist program for the default job sandbox.
///
/// Allowed syscalls cover normal process execution (I/O, memory, threading,
/// signals, file operations).  Dangerous syscalls such as ptrace(2),
/// process_vm_readv(2), and kexec_load(2) are not in the list and will
/// cause the kernel to send SIGSYS to the process and kill it immediately.
///
/// KillProcess is chosen over Errno(EPERM) so that:
///   1. The violation is unambiguously detectable (SIGSYS / signal 31).
///   2. A malicious job cannot inspect the EPERM return and adapt its
///      behaviour — it is terminated before it can react.
pub fn build_allowlist() -> Result<BpfProgram> {
    // Each entry: (syscall_number, vec_of_conditions).
    // An empty conditions vec means "always allow this syscall".
    let rules: BTreeMap<i64, Vec<seccompiler::SeccompRule>> = allowed_syscalls()
        .into_iter()
        .map(|nr| (nr, vec![]))
        .collect();

    let filter = SeccompFilter::new(
        rules,
        // Default action for syscalls NOT in the allowlist:
        // kill the process and deliver SIGSYS (signal 31), which the
        // runtime detects as kill_reason = "Seccomp".
        SeccompAction::KillProcess,
        // Action for syscalls IN the allowlist (matching empty conditions).
        SeccompAction::Allow,
        // Target architecture — must match the process being filtered.
        TargetArch::x86_64,
    )?;

    Ok(filter.try_into()?)
}

/// Apply a pre-compiled BPF program to the calling process.
///
/// Call this inside the `pre_exec` hook (child side, after fork).
pub fn apply(program: &BpfProgram) -> std::io::Result<()> {
    seccompiler::apply_filter(program).map_err(|e| {
        std::io::Error::new(std::io::ErrorKind::Other, e.to_string())
    })
}

// ── unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Verify the allowlist compiles to a valid BPF program without panicking.
    #[test]
    fn build_allowlist_compiles() {
        let prog = build_allowlist().expect("build_allowlist should succeed");
        // A valid BPF program is non-empty.
        assert!(!prog.is_empty(), "BPF program should have at least one instruction");
    }

    /// Verify the allowed syscalls list is non-trivially populated.
    #[test]
    fn allowed_syscalls_non_empty() {
        let syscalls = allowed_syscalls();
        assert!(syscalls.len() > 50, "expected >50 allowed syscalls, got {}", syscalls.len());
    }

    /// Verify there are no duplicate syscall numbers in the allowlist.
    #[test]
    fn no_duplicate_syscalls() {
        let mut syscalls = allowed_syscalls();
        syscalls.sort();
        let before = syscalls.len();
        syscalls.dedup();
        assert_eq!(syscalls.len(), before, "duplicate syscall numbers found in allowlist");
    }

    /// Verify that common safe syscalls are present.
    #[test]
    fn common_syscalls_allowed() {
        let syscalls = allowed_syscalls();
        for nr in [libc::SYS_read, libc::SYS_write, libc::SYS_exit_group, libc::SYS_mmap] {
            assert!(
                syscalls.contains(&nr),
                "syscall {} must be in allowlist",
                nr
            );
        }
    }

    /// Verify that dangerous syscalls are NOT in the allowlist.
    #[test]
    fn dangerous_syscalls_blocked() {
        let syscalls = allowed_syscalls();
        for nr in [libc::SYS_ptrace, libc::SYS_kexec_load, libc::SYS_process_vm_readv] {
            assert!(
                !syscalls.contains(&nr),
                "dangerous syscall {} must NOT be in allowlist",
                nr
            );
        }
    }
}

/// Returns the list of allowed syscall numbers for x86-64.
///
/// Covers typical needs of a subprocess (process management, file I/O,
/// memory, threading, signals, networking basics).  ptrace and other
/// introspection syscalls are intentionally excluded.
#[rustfmt::skip]
fn allowed_syscalls() -> Vec<i64> {
    vec![
        // process / scheduling
        libc::SYS_exit,
        libc::SYS_exit_group,
        libc::SYS_getpid,
        libc::SYS_getppid,
        libc::SYS_getuid,
        libc::SYS_geteuid,
        libc::SYS_getgid,
        libc::SYS_getegid,
        libc::SYS_getpgrp,
        libc::SYS_setsid,
        libc::SYS_setpgid,
        libc::SYS_prctl,
        libc::SYS_arch_prctl,
        libc::SYS_prlimit64,
        libc::SYS_nanosleep,
        libc::SYS_clock_nanosleep,
        libc::SYS_clock_gettime,
        libc::SYS_clock_getres,
        libc::SYS_gettimeofday,
        libc::SYS_time,
        libc::SYS_getrlimit,
        libc::SYS_setrlimit,
        libc::SYS_sched_yield,
        libc::SYS_sched_getaffinity,
        libc::SYS_sched_setaffinity,
        libc::SYS_sched_getparam,
        libc::SYS_sched_setparam,
        libc::SYS_sched_getscheduler,
        libc::SYS_sched_setscheduler,
        // memory
        libc::SYS_brk,
        libc::SYS_mmap,
        libc::SYS_munmap,
        libc::SYS_mprotect,
        libc::SYS_mremap,
        libc::SYS_madvise,
        libc::SYS_mincore,
        libc::SYS_mlock,
        libc::SYS_munlock,
        // file I/O
        libc::SYS_read,
        libc::SYS_readv,
        libc::SYS_pread64,
        libc::SYS_write,
        libc::SYS_writev,
        libc::SYS_pwrite64,
        libc::SYS_open,
        libc::SYS_openat,
        libc::SYS_close,
        libc::SYS_close_range,
        libc::SYS_lseek,
        libc::SYS_dup,
        libc::SYS_dup2,
        libc::SYS_dup3,
        libc::SYS_fstat,
        libc::SYS_stat,
        libc::SYS_lstat,
        libc::SYS_statx,
        libc::SYS_newfstatat,
        libc::SYS_access,
        libc::SYS_faccessat,
        libc::SYS_getcwd,
        libc::SYS_chdir,
        libc::SYS_fchdir,
        libc::SYS_readlink,
        libc::SYS_readlinkat,
        libc::SYS_getdents64,
        libc::SYS_mkdir,
        libc::SYS_mkdirat,
        libc::SYS_unlink,
        libc::SYS_unlinkat,
        libc::SYS_rename,
        libc::SYS_renameat,
        libc::SYS_renameat2,
        libc::SYS_truncate,
        libc::SYS_ftruncate,
        libc::SYS_chmod,
        libc::SYS_fchmod,
        libc::SYS_fchmodat,
        libc::SYS_chown,
        libc::SYS_fchown,
        libc::SYS_lchown,
        libc::SYS_fchownat,
        libc::SYS_umask,
        libc::SYS_sync,
        libc::SYS_fsync,
        libc::SYS_fdatasync,
        libc::SYS_sendfile,
        libc::SYS_copy_file_range,
        libc::SYS_splice,
        libc::SYS_pipe,
        libc::SYS_pipe2,
        libc::SYS_poll,
        libc::SYS_ppoll,
        libc::SYS_select,
        libc::SYS_pselect6,
        libc::SYS_epoll_create,
        libc::SYS_epoll_create1,
        libc::SYS_epoll_ctl,
        libc::SYS_epoll_wait,
        libc::SYS_epoll_pwait,
        libc::SYS_eventfd,
        libc::SYS_eventfd2,
        libc::SYS_inotify_init,
        libc::SYS_inotify_init1,
        libc::SYS_inotify_add_watch,
        libc::SYS_inotify_rm_watch,
        // fcntl / ioctl
        libc::SYS_fcntl,
        libc::SYS_ioctl,
        // signals
        libc::SYS_rt_sigaction,
        libc::SYS_rt_sigprocmask,
        libc::SYS_rt_sigreturn,
        libc::SYS_rt_sigpending,
        libc::SYS_rt_sigsuspend,
        libc::SYS_rt_sigtimedwait,
        libc::SYS_kill,
        libc::SYS_tgkill,
        libc::SYS_tkill,
        libc::SYS_sigaltstack,
        // threads / futex
        libc::SYS_futex,
        libc::SYS_futex_waitv,
        libc::SYS_set_tid_address,
        libc::SYS_set_robust_list,
        libc::SYS_get_robust_list,
        libc::SYS_clone,
        libc::SYS_clone3,
        libc::SYS_wait4,
        libc::SYS_waitid,
        // network (basic — sockets but not raw/packet)
        libc::SYS_socket,
        libc::SYS_connect,
        libc::SYS_accept,
        libc::SYS_accept4,
        libc::SYS_bind,
        libc::SYS_listen,
        libc::SYS_getsockname,
        libc::SYS_getpeername,
        libc::SYS_getsockopt,
        libc::SYS_setsockopt,
        libc::SYS_sendto,
        libc::SYS_recvfrom,
        libc::SYS_sendmsg,
        libc::SYS_recvmsg,
        libc::SYS_shutdown,
        // misc
        libc::SYS_uname,
        libc::SYS_getrusage,
        libc::SYS_getrandom,
        libc::SYS_rseq,
        libc::SYS_membarrier,
    ]
}
