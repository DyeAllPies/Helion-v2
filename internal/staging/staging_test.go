package staging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// ── fixtures ────────────────────────────────────────────────────────────

func newStager(t *testing.T) (*Stager, artifacts.Store, string) {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	store, err := artifacts.NewLocalStore(storeDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	workRoot := filepath.Join(t.TempDir(), "work")
	return NewStager(store, workRoot, false, slog.Default()), store, workRoot
}

func putArtifact(t *testing.T, store artifacts.Store, key string, payload []byte) artifacts.URI {
	t.Helper()
	uri, err := store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	return uri
}

// ── safeJoin ────────────────────────────────────────────────────────────

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, ""); err == nil {
		t.Error("empty path should fail")
	}
	if _, err := safeJoin(root, "/abs"); err == nil {
		t.Error("absolute path should fail")
	}
	if _, err := safeJoin(root, "a/../../escape"); err == nil {
		t.Error("traversal should fail")
	}
	if _, err := safeJoin(root, "has\x00nul"); err == nil {
		t.Error("NUL should fail")
	}
	p, err := safeJoin(root, "a/b.txt")
	if err != nil {
		t.Fatalf("safeJoin: %v", err)
	}
	if !strings.HasPrefix(p, filepath.Clean(root)) {
		t.Fatalf("path escaped root: %q", p)
	}
}

// ── Prepare happy path ──────────────────────────────────────────────────

func TestPrepare_InputsStaged_EnvExported(t *testing.T) {
	s, store, _ := newStager(t)
	uri := putArtifact(t, store, "ds/train.bin", []byte("payload-1"))

	job := &cpb.Job{
		ID: "job-123",
		Inputs: []cpb.ArtifactBinding{
			{Name: "TRAIN", URI: string(uri), LocalPath: "in/train.bin"},
		},
		Outputs: []cpb.ArtifactBinding{
			{Name: "MODEL", LocalPath: "out/model.pt"},
		},
	}

	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(p.Cleanup)

	// Input file staged at the right path.
	got, err := os.ReadFile(filepath.Join(p.WorkingDir, "in", "train.bin"))
	if err != nil {
		t.Fatalf("read staged input: %v", err)
	}
	if string(got) != "payload-1" {
		t.Fatalf("content: %q", got)
	}
	// Env additions point at absolute paths.
	if v := p.EnvAdditions["HELION_INPUT_TRAIN"]; !filepath.IsAbs(v) {
		t.Fatalf("HELION_INPUT_TRAIN not absolute: %q", v)
	}
	if v := p.EnvAdditions["HELION_OUTPUT_MODEL"]; !filepath.IsAbs(v) {
		t.Fatalf("HELION_OUTPUT_MODEL not absolute: %q", v)
	}
	// Output parent dir exists so the job can open-for-write.
	outDir := filepath.Join(p.WorkingDir, "out")
	if info, err := os.Stat(outDir); err != nil || !info.IsDir() {
		t.Fatalf("output parent dir not created: %v", err)
	}
}

// ── Prepare rollback ────────────────────────────────────────────────────

func TestPrepare_MissingInputURI_RollsBackWorkdir(t *testing.T) {
	s, _, workRoot := newStager(t)
	job := &cpb.Job{
		ID: "job-missing",
		Inputs: []cpb.ArtifactBinding{
			// URI references a key the store doesn't have.
			{Name: "X", URI: "file:///does-not-exist", LocalPath: "in/x"},
		},
	}
	if _, err := s.Prepare(context.Background(), job); err == nil {
		t.Fatal("expected error")
	}
	// No leftover directories under workRoot.
	entries, _ := os.ReadDir(workRoot)
	for _, e := range entries {
		if strings.Contains(e.Name(), "job-missing") {
			t.Fatalf("workdir not rolled back: %s", e.Name())
		}
	}
}

func TestPrepare_TraversalInLocalPath_Rejected(t *testing.T) {
	s, store, _ := newStager(t)
	uri := putArtifact(t, store, "x", []byte("a"))

	job := &cpb.Job{
		ID: "j",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", URI: string(uri), LocalPath: "../escape"},
		},
	}
	if _, err := s.Prepare(context.Background(), job); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

// ── input size cap ──────────────────────────────────────────────────────

// oversizedStore streams more bytes than MaxInputDownloadBytes on Get,
// simulating a misbehaving backend or malicious artifact.
type oversizedStore struct {
	artifacts.Store
}

func (o oversizedStore) Get(ctx context.Context, uri artifacts.URI) (io.ReadCloser, error) {
	// Always stream the current cap + 10; tests lower the cap to keep
	// the fixture cheap.
	return io.NopCloser(&infiniteReader{n: MaxInputDownloadBytes + 10}), nil
}

type infiniteReader struct{ n int64 }

func (r *infiniteReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	toRead := int64(len(p))
	if toRead > r.n {
		toRead = r.n
	}
	for i := int64(0); i < toRead; i++ {
		p[i] = 'A'
	}
	r.n -= toRead
	return int(toRead), nil
}

func TestPrepare_InputSizeCapEnforced(t *testing.T) {
	// Lower the cap so we don't stream multi-GiB to exercise it.
	prev := MaxInputDownloadBytes
	MaxInputDownloadBytes = 64
	t.Cleanup(func() { MaxInputDownloadBytes = prev })

	// Wrap the local store so Get streams past the cap.
	workRoot := filepath.Join(t.TempDir(), "work")
	base, err := artifacts.NewLocalStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	s := NewStager(oversizedStore{Store: base}, workRoot, false, slog.Default())

	job := &cpb.Job{
		ID: "cap",
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", URI: "file:///ignored", LocalPath: "in/x"},
		},
	}
	_, err = s.Prepare(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size-cap error, got: %v", err)
	}
}

// ── Finalize ────────────────────────────────────────────────────────────

func TestFinalize_UploadsOutputs(t *testing.T) {
	s, store, _ := newStager(t)
	job := &cpb.Job{
		ID: "up",
		Outputs: []cpb.ArtifactBinding{
			{Name: "MODEL", LocalPath: "out/model.bin"},
		},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Simulate the job writing its output.
	outPath := filepath.Join(p.WorkingDir, "out", "model.bin")
	if err := os.WriteFile(outPath, []byte("final"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}

	resolved, err := s.Finalize(context.Background(), p, true)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "MODEL" {
		t.Fatalf("resolved: %+v", resolved)
	}
	if resolved[0].Size != 5 {
		t.Fatalf("size: %d", resolved[0].Size)
	}
	// The uploaded bytes round-trip via the store.
	rc, err := store.Get(context.Background(), resolved[0].URI)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "final" {
		t.Fatalf("content: %q", got)
	}
	// Workdir gone after Finalize.
	if _, err := os.Stat(p.WorkingDir); !os.IsNotExist(err) {
		t.Fatalf("workdir not cleaned: %v", err)
	}
}

func TestFinalize_FailedRun_SkipsUpload_CleansUp(t *testing.T) {
	s, store, _ := newStager(t)
	job := &cpb.Job{
		ID:      "fail",
		Outputs: []cpb.ArtifactBinding{{Name: "M", LocalPath: "out/m"}},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	outPath := filepath.Join(p.WorkingDir, "out", "m")
	_ = os.WriteFile(outPath, []byte("partial"), 0o600)

	res, err := s.Finalize(context.Background(), p, false)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("unexpected uploads on failure: %+v", res)
	}
	// Nothing under jobs/fail/ in the store.
	entries, _ := os.ReadDir(filepath.Join(store.(*artifacts.LocalStore).Root(), "jobs", "fail"))
	if len(entries) > 0 {
		t.Fatalf("upload happened despite failed run: %+v", entries)
	}
	if _, err := os.Stat(p.WorkingDir); !os.IsNotExist(err) {
		t.Fatalf("workdir not cleaned: %v", err)
	}
}

func TestFinalize_MissingOutput_Errors(t *testing.T) {
	s, _, _ := newStager(t)
	job := &cpb.Job{
		ID:      "mout",
		Outputs: []cpb.ArtifactBinding{{Name: "M", LocalPath: "out/m"}},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Do NOT create the output file; Finalize must error.
	_, err = s.Finalize(context.Background(), p, true)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-output error: %v", err)
	}
}

// ── symlink attack ──────────────────────────────────────────────────────

// TestFinalize_OversizeOutput_Rejected parallels
// TestPrepare_InputSizeCapEnforced on the upload side. The stager
// enforces `MaxOutputUploadBytes` via an `os.Lstat` pre-flight in
// `upload()` — a regression that dropped that guard would let a
// runaway job fill the artifact store (same threat-model severity
// as the input cap, just the other direction). No test alarmed on
// this path; this one does.
//
// Lowers the cap globally for the test rather than writing a
// multi-GiB file, then restores it on cleanup. The cap is a
// package-level var specifically for this kind of test tweak —
// the comment on `MaxOutputUploadBytes` says so.
func TestFinalize_OversizeOutput_Rejected(t *testing.T) {
	// Tighten the cap so the test writes ~2 KiB instead of 2 GiB.
	orig := MaxOutputUploadBytes
	MaxOutputUploadBytes = 1024
	t.Cleanup(func() { MaxOutputUploadBytes = orig })

	s, store, _ := newStager(t)
	job := &cpb.Job{
		ID:      "oversize-out",
		Outputs: []cpb.ArtifactBinding{{Name: "BIG", LocalPath: "big.bin"}},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer p.Cleanup()

	// 2 KiB — comfortably above the 1 KiB cap we just set.
	outPath := filepath.Join(p.WorkingDir, "big.bin")
	if err := os.WriteFile(outPath, bytes.Repeat([]byte("A"), 2048), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = s.Finalize(context.Background(), p, true)
	if err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("expected oversize-cap rejection, got: %v", err)
	}

	// The oversize output must not have been uploaded. No entry
	// with the expected key prefix should live in the store.
	// Walking the LocalStore root is enough — if Put fired, a file
	// under jobs/oversize-out/ would exist.
	localStore, ok := store.(interface{ Root() string })
	if !ok {
		return // not a LocalStore; skip the filesystem sanity check
	}
	jobDir := filepath.Join(localStore.Root(), "jobs", "oversize-out")
	if entries, err := os.ReadDir(jobDir); err == nil && len(entries) > 0 {
		t.Errorf("oversize output reached the store anyway: %v", entries)
	}
}

func TestFinalize_SymlinkOutputRejected(t *testing.T) {
	s, _, _ := newStager(t)
	job := &cpb.Job{
		ID:      "sym",
		Outputs: []cpb.ArtifactBinding{{Name: "M", LocalPath: "out/m"}},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// Create a target outside the workdir that we do NOT want uploaded.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("shadow-file"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	out := filepath.Join(p.WorkingDir, "out", "m")
	_ = os.Remove(out) // Finalize expects a fresh file; Prepare didn't create m
	if err := os.Symlink(secret, out); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	_, err = s.Finalize(context.Background(), p, true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got: %v", err)
	}
}

// ── workdir lifecycle ───────────────────────────────────────────────────

func TestPrepare_WorkdirUnderWorkRoot(t *testing.T) {
	s, _, workRoot := newStager(t)
	job := &cpb.Job{ID: "wd-under"}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer p.Cleanup()
	abs, _ := filepath.Abs(workRoot)
	if !strings.HasPrefix(p.WorkingDir, abs) {
		t.Fatalf("workdir not under workRoot: %q not under %q", p.WorkingDir, abs)
	}
}

func TestPrepare_KeepFlag_PreservesWorkdir(t *testing.T) {
	store, err := artifacts.NewLocalStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	workRoot := filepath.Join(t.TempDir(), "work")
	s := NewStager(store, workRoot, true /* keep */, slog.Default())
	job := &cpb.Job{ID: "keep"}
	p, _ := s.Prepare(context.Background(), job)
	wd := p.WorkingDir
	p.Cleanup()
	if _, err := os.Stat(wd); err != nil {
		t.Fatalf("workdir removed despite keep flag: %v", err)
	}
}

func TestPrepare_ContextCancelledBetweenInputs(t *testing.T) {
	s, store, _ := newStager(t)
	u1 := putArtifact(t, store, "a", []byte("a"))
	u2 := putArtifact(t, store, "b", []byte("b"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	job := &cpb.Job{
		ID: "ctx",
		Inputs: []cpb.ArtifactBinding{
			{Name: "A", URI: string(u1), LocalPath: "in/a"},
			{Name: "B", URI: string(u2), LocalPath: "in/b"},
		},
	}
	if _, err := s.Prepare(ctx, job); err == nil {
		t.Fatal("expected cancellation error")
	}
}

// ── env merge precedence (documents stager-wins rule) ───────────────────

func TestEnvPrecedence_StagerOverridesUserEnv(t *testing.T) {
	// The node server's mergeEnv lives in nodeserver, but the rule it
	// implements is a *staging* invariant: a user-supplied env map must
	// never be able to shadow HELION_INPUT_* / HELION_OUTPUT_*. Assert
	// here as a guard against a future refactor accidentally flipping
	// the precedence — exercises Prepare rather than mergeEnv directly.
	s, store, _ := newStager(t)
	uri := putArtifact(t, store, "x", []byte("v"))

	job := &cpb.Job{
		ID: "env",
		Env: map[string]string{"HELION_INPUT_X": "/attacker/controlled"},
		Inputs: []cpb.ArtifactBinding{
			{Name: "X", URI: string(uri), LocalPath: "in/x"},
		},
	}
	p, err := s.Prepare(context.Background(), job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer p.Cleanup()
	// The stager's HELION_INPUT_X points at the staged absolute path,
	// which lives under p.WorkingDir — not at /attacker/controlled.
	got := p.EnvAdditions["HELION_INPUT_X"]
	if got == "/attacker/controlled" {
		t.Fatal("stager did not produce its own value")
	}
	if !strings.HasPrefix(got, p.WorkingDir) {
		t.Fatalf("HELION_INPUT_X not under workdir: %q", got)
	}
}

// ── concurrent Prepare on distinct jobs ─────────────────────────────────

func TestPrepare_ConcurrentDistinctJobs(t *testing.T) {
	s, store, _ := newStager(t)
	uri := putArtifact(t, store, "k", []byte("k"))

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := &cpb.Job{
				ID: "concurrent-" + string(rune('A'+i)),
				Inputs: []cpb.ArtifactBinding{
					{Name: "X", URI: string(uri), LocalPath: "in/x"},
				},
			}
			p, err := s.Prepare(context.Background(), job)
			if err != nil {
				errs <- err
				return
			}
			p.Cleanup()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent prepare: %v", err)
		}
	}
}

// ── sanity: nil job ─────────────────────────────────────────────────────

func TestPrepare_NilJob(t *testing.T) {
	s, _, _ := newStager(t)
	if _, err := s.Prepare(context.Background(), nil); !errors.Is(err, err) || err == nil {
		t.Fatal("expected error on nil job")
	}
}

// TestPrepared_Cleanup_Idempotent locks in the double-call safety
// of `Prepared.Cleanup`. The code (`if p != nil && p.cleanup != nil`
// guards) is correct today, but the only callers that matter —
// defer-based cleanup + an explicit Cleanup in test code — routinely
// end up calling it twice (deferred + manual). A regression that
// dropped the nil guard would panic on the second call. Better to
// have an alarm than to find it in a production panic trace.
//
// Also confirms the workdir is gone after the first call — the
// second call must not resurrect it or throw on the missing tree.
func TestPrepared_Cleanup_Idempotent(t *testing.T) {
	s, _, _ := newStager(t)
	p, err := s.Prepare(context.Background(), &cpb.Job{ID: "cleanup-twice"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	workdir := p.WorkingDir

	// First call tears down the workdir. Record its absence so the
	// second call's expected no-op doesn't accidentally observe
	// unrelated state.
	p.Cleanup()
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("workdir still exists after first Cleanup: %v", err)
	}

	// Second call must not panic or mis-behave. Calling this under
	// t.Run subtest sandbox so a panic doesn't take down sibling
	// tests in a -count=N loop.
	t.Run("second-call-is-safe", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("double Cleanup panicked: %v", r)
			}
		}()
		p.Cleanup()
	})
}

func TestFinalize_NilPrepared_NoOp(t *testing.T) {
	s, _, _ := newStager(t)
	res, err := s.Finalize(context.Background(), nil, true)
	if err != nil || res != nil {
		t.Fatalf("nil prepared should be no-op: %v %v", err, res)
	}
}
