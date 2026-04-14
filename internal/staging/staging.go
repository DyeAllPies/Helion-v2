// Package staging bridges the artifact store and the runtime.
//
// A job's submit-time ArtifactBinding records names, URIs, and
// working-directory-relative paths, but none of the actual data lives
// in the submit. The node agent needs to:
//
//  1. Mint a working directory (unless the caller specified one).
//  2. For each input, download the artifact from the store to
//     <workdir>/<local_path> and expose HELION_INPUT_<NAME> = absolute
//     path to the running job.
//  3. Invoke the runtime.
//  4. For each declared output, upload <workdir>/<local_path> back to
//     the store (only on success) and record the resulting URI so the
//     node agent can surface it to the coordinator.
//  5. Tear the working directory down, unless the operator has asked
//     to keep it for debugging (HELION_KEEP_WORKDIR=1).
//
// The Stager is the only place in the node agent that speaks to the
// artifact store, keeping the runtime blissfully ignorant of URIs.
package staging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// MaxInputDownloadBytes caps a single input download so a malicious or
// misconfigured artifact cannot fill the node's disk. Applied per
// binding; total job budget is this × len(Inputs). Adjust upward once
// real ML checkpoints force the conversation.
//
// Exposed as a var (not const) so tests can lower it without having to
// stream multi-GiB payloads to exercise the cap. Production code never
// writes to it.
var MaxInputDownloadBytes int64 = 2 << 30 // 2 GiB per input

// MaxOutputUploadBytes caps a single output upload for the same
// reason — a runaway job producing an unbounded output file would
// otherwise eat the artifact store.
var MaxOutputUploadBytes int64 = 2 << 30 // 2 GiB per output

// Stager prepares working directories, stages inputs, and finalises
// outputs against an artifact Store. One Stager per node agent.
type Stager struct {
	store    artifacts.Store
	workRoot string
	keep     bool // HELION_KEEP_WORKDIR=1 — preserve workdirs on success for debug
	log      *slog.Logger
}

// NewStager returns a Stager that places per-job working directories
// under workRoot. If workRoot is empty it falls back to the OS temp
// directory with a "helion-jobs" suffix, ensuring workdirs are always
// under a path the operator has control over.
func NewStager(store artifacts.Store, workRoot string, keepWorkdir bool, log *slog.Logger) *Stager {
	if log == nil {
		log = slog.Default()
	}
	if workRoot == "" {
		workRoot = filepath.Join(os.TempDir(), "helion-jobs")
	}
	return &Stager{store: store, workRoot: workRoot, keep: keepWorkdir, log: log}
}

// Prepared captures the per-run bookkeeping a Stager hands back after
// Prepare succeeds. Callers must always call Cleanup (typically via
// defer) to release the working directory.
type Prepared struct {
	WorkingDir   string            // absolute path the runtime should cd into
	EnvAdditions map[string]string // HELION_INPUT_<NAME> etc. to merge into the job's env
	jobID        string
	outputs      []cpb.ArtifactBinding
	cleanup      func()
}

// ResolvedOutput is what Finalize returns per successful upload.
type ResolvedOutput struct {
	Name      string
	URI       artifacts.URI
	Size      int64
	SHA256    string
	LocalPath string // relative, as declared on the binding
}

// Cleanup tears down anything Prepare created. Safe to call multiple
// times.
func (p *Prepared) Cleanup() {
	if p != nil && p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// Prepare creates the working directory (if the job did not specify
// one) and stages every input. Returns a Prepared handle the caller
// must Cleanup, plus the WorkingDir and env additions to merge into
// the runtime's RunRequest.
//
// On any failure Prepare rolls back: no partial workdir is left behind
// so a retry sees a clean slate.
func (s *Stager) Prepare(ctx context.Context, job *cpb.Job) (*Prepared, error) {
	if job == nil {
		return nil, errors.New("staging: nil job")
	}

	// Determine workdir. The API layer has already rejected absolute
	// paths and NUL bytes in job.WorkingDir, but we re-check here so
	// a Stager used via library API (no handler validation) stays
	// safe. A caller-supplied workdir is treated as a suffix under
	// workRoot to keep every job's files under a common operator-
	// controlled root.
	workdir, cleanup, err := s.mintWorkDir(job.ID, job.WorkingDir)
	if err != nil {
		return nil, err
	}

	p := &Prepared{
		WorkingDir:   workdir,
		EnvAdditions: make(map[string]string, len(job.Inputs)),
		jobID:        job.ID,
		outputs:      append([]cpb.ArtifactBinding(nil), job.Outputs...),
		cleanup:      cleanup,
	}
	// If any input staging step fails, make sure the workdir is gone
	// before returning so the caller does not have a half-populated
	// directory to deal with.
	rollback := func(err error) (*Prepared, error) {
		p.Cleanup()
		return nil, err
	}

	for _, in := range job.Inputs {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		dest, err := safeJoin(workdir, in.LocalPath)
		if err != nil {
			return rollback(fmt.Errorf("staging: input %s: %w", in.Name, err))
		}
		if err := s.download(ctx, artifacts.URI(in.URI), in.SHA256, dest); err != nil {
			return rollback(fmt.Errorf("staging: input %s: %w", in.Name, err))
		}
		p.EnvAdditions["HELION_INPUT_"+in.Name] = dest
	}
	// Pre-create parent directories for declared outputs so the job
	// can open them for writing without needing to mkdir itself. Also
	// export HELION_OUTPUT_<NAME> so the job knows where to write.
	for _, out := range job.Outputs {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		dest, err := safeJoin(workdir, out.LocalPath)
		if err != nil {
			return rollback(fmt.Errorf("staging: output %s: %w", out.Name, err))
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return rollback(fmt.Errorf("staging: output %s: %w", out.Name, err))
		}
		p.EnvAdditions["HELION_OUTPUT_"+out.Name] = dest
	}

	return p, nil
}

// Finalize uploads every declared output (unless runSucceeded is
// false, in which case it skips uploads and only cleans up) and
// returns the resolved URIs so the node agent can surface them in
// the job's terminal event payload.
//
// Finalize always calls Cleanup, so the caller does not need to.
func (s *Stager) Finalize(ctx context.Context, p *Prepared, runSucceeded bool) ([]ResolvedOutput, error) {
	if p == nil {
		return nil, nil
	}
	defer p.Cleanup()
	if !runSucceeded {
		return nil, nil
	}

	resolved := make([]ResolvedOutput, 0, len(p.outputs))
	for _, out := range p.outputs {
		if err := ctx.Err(); err != nil {
			return resolved, err
		}
		src, err := safeJoin(p.WorkingDir, out.LocalPath)
		if err != nil {
			return resolved, fmt.Errorf("staging: output %s: %w", out.Name, err)
		}
		r, err := s.upload(ctx, p.jobID, out.Name, out.LocalPath, src)
		if err != nil {
			return resolved, fmt.Errorf("staging: output %s: %w", out.Name, err)
		}
		resolved = append(resolved, r)
	}
	return resolved, nil
}

// mintWorkDir creates the working directory. suffix may be empty (in
// which case we use a job-id-derived name). Returns an absolute path
// plus a cleanup func that removes the whole tree unless s.keep is
// set. Cleanup is safe to call even if creation partially failed.
func (s *Stager) mintWorkDir(jobID, suffix string) (string, func(), error) {
	if err := os.MkdirAll(s.workRoot, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("staging: create work root: %w", err)
	}
	// Names must be unique per job. Use the job ID if safe, otherwise
	// fall back to os.MkdirTemp. The job ID is UUID-shaped at submit
	// time, but this path gets exercised by tests with shorter IDs too.
	base := filepath.Join(s.workRoot, sanitiseJobDirName(jobID, suffix))
	if err := os.Mkdir(base, 0o700); err != nil {
		if os.IsExist(err) {
			// A retry on the same job id must not be fooled into
			// reusing a stale workdir. Fail loud; the caller decides
			// whether to clean up and retry.
			return "", func() {}, fmt.Errorf("staging: workdir %s already exists", base)
		}
		return "", func() {}, fmt.Errorf("staging: create workdir: %w", err)
	}
	cleanup := func() {
		if s.keep {
			s.log.Info("staging: keeping workdir", slog.String("path", base))
			return
		}
		if err := os.RemoveAll(base); err != nil {
			s.log.Warn("staging: remove workdir",
				slog.String("path", base), slog.Any("err", err))
		}
	}
	return base, cleanup, nil
}

// sanitiseJobDirName composes a collision-resistant directory name from
// the job id (and optional user-supplied suffix). Non-alphanumeric
// bytes collapse to '_' so no shell quoting or path traversal is
// possible — the input has already been validated, this is belt-and-
// braces.
func sanitiseJobDirName(jobID, suffix string) string {
	clean := func(s string) string {
		b := make([]byte, 0, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
				c == '-', c == '_', c == '.':
				b = append(b, c)
			default:
				b = append(b, '_')
			}
		}
		return string(b)
	}
	name := clean(jobID)
	if suffix != "" {
		name = name + "-" + clean(suffix)
	}
	if name == "" {
		name = "job"
	}
	return name
}

// safeJoin resolves rel against root and rejects any result that
// escapes root. filepath.Clean plus prefix-check is the same approach
// LocalStore uses for URI resolution (internal/artifacts/local.go).
func safeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("local_path is required")
	}
	// Reject paths that are absolute under either POSIX or Windows
	// semantics so callers cannot work around the check by using a
	// leading slash on Windows (which filepath.IsAbs considers
	// drive-relative, not absolute).
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return "", errors.New("local_path must be relative")
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return "", errors.New("local_path must be relative")
	}
	if strings.ContainsRune(rel, '\x00') {
		return "", errors.New("local_path must not contain NUL")
	}
	// Normalise forward slashes to the OS separator; then Clean.
	clean := filepath.Clean(filepath.FromSlash(rel))
	full := filepath.Join(root, clean)
	rootClean := filepath.Clean(root) + string(filepath.Separator)
	if !strings.HasPrefix(full+string(filepath.Separator), rootClean) {
		return "", errors.New("local_path escapes working directory")
	}
	return full, nil
}

// download pulls uri from the store into dest. Enforces
// MaxInputDownloadBytes with io.LimitReader so a malicious artifact
// cannot fill the disk. dest's parent directory is created lazily.
//
// When expectedSHA256 is non-empty, the download is routed through
// artifacts.GetAndVerify so the staged bytes are digest-checked
// before they land. The digest comes from the coordinator's
// attested ResolvedOutputs record for the upstream — catching
// store-side tamper, bit rot, and TLS-layer corruption that
// slipped past the hybrid PQ channel's MAC. A mismatch returns
// artifacts.ErrChecksumMismatch; the caller fails the job.
func (s *Stager) download(ctx context.Context, uri artifacts.URI, expectedSHA256, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Verified path: digest known up-front. GetAndVerify reads into
	// memory (bounded by MaxInputDownloadBytes), checks the hash,
	// and only then writes the bytes out to disk. The extra
	// in-memory buffer is the cost of the integrity guarantee —
	// acceptable given the cap.
	if expectedSHA256 != "" {
		buf, err := artifacts.GetAndVerify(ctx, s.store, uri, expectedSHA256, MaxInputDownloadBytes)
		if err != nil {
			return fmt.Errorf("verified get: %w", err)
		}
		if err := os.WriteFile(dest, buf, 0o600); err != nil {
			return fmt.Errorf("write verified dest: %w", err)
		}
		return nil
	}

	// Unverified path: no digest committed. Falls back to a streaming
	// read so huge unverified artifacts don't have to fit in RAM.
	rc, err := s.store.Get(ctx, uri)
	if err != nil {
		return fmt.Errorf("store get: %w", err)
	}
	defer rc.Close()

	// Open with O_EXCL so we never overwrite a file a previous input
	// already staged (would indicate a name collision in the spec).
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer f.Close()

	// +1 so a file that is exactly at the cap still produces an
	// over-limit error, not a lucky pass.
	lr := &io.LimitedReader{R: rc, N: MaxInputDownloadBytes + 1}
	n, err := io.Copy(f, lr)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if n > MaxInputDownloadBytes {
		return fmt.Errorf("input exceeds %d-byte cap", MaxInputDownloadBytes)
	}
	return nil
}

// upload reads src, pushes it into the store under a key derived from
// the job id and the binding's declared local_path, and reports back
// size + sha256. Symlinks are refused: an output path that happens to
// point at /etc/shadow because the job created a symlink must not
// surreptitiously end up in the artifact store.
func (s *Stager) upload(ctx context.Context, jobID, name, localPath, src string) (ResolvedOutput, error) {
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return ResolvedOutput{}, fmt.Errorf("output file missing: %s", localPath)
		}
		return ResolvedOutput{}, fmt.Errorf("lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ResolvedOutput{}, fmt.Errorf("output %q is a symlink", localPath)
	}
	if !info.Mode().IsRegular() {
		return ResolvedOutput{}, fmt.Errorf("output %q is not a regular file", localPath)
	}
	if info.Size() > MaxOutputUploadBytes {
		return ResolvedOutput{}, fmt.Errorf("output %q exceeds %d-byte cap", localPath, MaxOutputUploadBytes)
	}

	f, err := os.Open(src)
	if err != nil {
		return ResolvedOutput{}, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	// Keys live under a per-job prefix so that concurrent jobs in the
	// same bucket never collide, and so the spec's eventual GC step
	// can identify "this run's outputs" cheaply.
	key := "jobs/" + jobID + "/" + filepath.ToSlash(localPath)
	uri, err := s.store.Put(ctx, key, f, info.Size())
	if err != nil {
		return ResolvedOutput{}, fmt.Errorf("store put: %w", err)
	}
	// Stat after Put to pick up the store's recorded SHA256. LocalStore
	// computes it on demand; S3Store streams the object. Both match
	// the actual stored bytes, so analytics lineage stays faithful
	// even if the file changed between Put and Stat (it cannot — Put
	// just closed the file handle).
	md, err := s.store.Stat(ctx, uri)
	if err != nil {
		return ResolvedOutput{}, fmt.Errorf("store stat: %w", err)
	}
	return ResolvedOutput{
		Name:      name,
		URI:       uri,
		Size:      md.Size,
		SHA256:    md.SHA256,
		LocalPath: localPath,
	}, nil
}
