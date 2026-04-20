// internal/cluster/persistence_encrypt_test.go
//
// Feature 30 — persister-level envelope-encryption tests. The
// secretstore package has its own unit tests; these verify the
// integration seam: SaveJob + SaveWorkflow translate to the
// on-disk form, LoadAllJobs + LoadAllWorkflows reverse it, and
// the Badger record on disk carries NO plaintext secret bytes.

package cluster_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"

	"github.com/DyeAllPies/Helion-v2/internal/cluster"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

// ── fixture helpers ───────────────────────────────────────

func newKeyring(t *testing.T) *secretstore.KeyRing {
	t.Helper()
	k := make([]byte, secretstore.KEKSize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	r, err := secretstore.NewKeyRing(1, k)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	return r
}

// newEncryptedPersister returns a fresh persister with a
// configured keyring and the raw Badger DB for out-of-band
// inspection.
func newEncryptedPersister(t *testing.T) (*cluster.BadgerJSONPersister, *badger.DB, *secretstore.KeyRing) {
	t.Helper()
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("NewBadgerJSONPersister: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	ring := newKeyring(t)
	p.SetKeyRing(ring)
	return p, p.DB(), ring
}

// rawRecord fetches the on-disk Badger bytes under key for
// out-of-band inspection (the whole point of feature 30 is
// asserting the ciphertext shape on disk).
func rawRecord(t *testing.T, db *badger.DB, key string) []byte {
	t.Helper()
	var out []byte
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			out = append([]byte(nil), v...)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("rawRecord %s: %v", key, err)
	}
	return out
}

// ── SaveJob / LoadAllJobs ─────────────────────────────────

func TestEncryptedPersistence_SaveJob_OnDiskCiphertextDoesNotLeakPlaintext(t *testing.T) {
	p, db, _ := newEncryptedPersister(t)
	ctx := context.Background()

	job := &cpb.Job{
		ID:      "j-e1",
		Command: "python",
		Args:    []string{"train.py"},
		Env: map[string]string{
			"HF_TOKEN": "hf_superSecret",
			"PUBLIC":   "visible-plaintext",
		},
		SecretKeys:     []string{"HF_TOKEN"},
		OwnerPrincipal: "user:alice",
	}
	if err := p.SaveJob(ctx, job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	// In-memory Job is unchanged — plaintext stays for
	// dispatch/reveal/log-scrub readers.
	if got := job.Env["HF_TOKEN"]; got != "hf_superSecret" {
		t.Errorf("in-memory Env mutated: %q", got)
	}
	if job.EncryptedEnv != nil {
		t.Errorf("in-memory EncryptedEnv populated: %+v", job.EncryptedEnv)
	}

	// On-disk record MUST NOT contain the plaintext bytes.
	raw := rawRecord(t, db, "jobs/j-e1")
	if bytes.Contains(raw, []byte("hf_superSecret")) {
		t.Fatalf("on-disk record leaks plaintext secret: %s", string(raw))
	}
	// Non-secret env values stay visible (the feature is
	// secrets-only).
	if !bytes.Contains(raw, []byte("visible-plaintext")) {
		t.Errorf("non-secret env stripped too: %s", string(raw))
	}
	// On-disk shape should carry the encrypted_env field.
	if !bytes.Contains(raw, []byte("encrypted_env")) {
		t.Errorf("on-disk record missing encrypted_env field: %s", string(raw))
	}
	// ciphertext sanity — should decode as JSON.
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["encrypted_env"] == nil {
		t.Fatalf("encrypted_env null")
	}
}

func TestEncryptedPersistence_LoadAllJobs_RestoresPlaintextEnv(t *testing.T) {
	p, _, _ := newEncryptedPersister(t)
	ctx := context.Background()

	original := &cpb.Job{
		ID:      "j-e2",
		Command: "python",
		Env: map[string]string{
			"HF_TOKEN":   "hf_superSecret",
			"AWS_SECRET": "aws_otherSecret",
			"PUBLIC":     "visible-plaintext",
		},
		SecretKeys:     []string{"HF_TOKEN", "AWS_SECRET"},
		OwnerPrincipal: "user:alice",
	}
	if err := p.SaveJob(ctx, original); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	got := jobs[0]
	if got.Env["HF_TOKEN"] != "hf_superSecret" {
		t.Errorf("HF_TOKEN plaintext round-trip: got %q", got.Env["HF_TOKEN"])
	}
	if got.Env["AWS_SECRET"] != "aws_otherSecret" {
		t.Errorf("AWS_SECRET plaintext round-trip: got %q", got.Env["AWS_SECRET"])
	}
	if got.Env["PUBLIC"] != "visible-plaintext" {
		t.Errorf("PUBLIC env unchanged: got %q", got.Env["PUBLIC"])
	}
	if got.EncryptedEnv != nil {
		t.Errorf("in-memory EncryptedEnv should be nil after load, got %+v", got.EncryptedEnv)
	}
}

func TestEncryptedPersistence_LegacyPlaintextRecord_StillLoads(t *testing.T) {
	// Pre-feature-30 records with plaintext Env and no
	// EncryptedEnv must continue to load unchanged.
	p, db, _ := newEncryptedPersister(t)
	ctx := context.Background()

	legacy := &cpb.Job{
		ID:      "j-legacy",
		Command: "echo",
		Env: map[string]string{
			"HF_TOKEN": "hf_legacyPlaintext",
		},
		SecretKeys:     []string{"HF_TOKEN"},
		OwnerPrincipal: "user:alice",
	}
	// Write the legacy shape directly — bypass SaveJob so we
	// don't encrypt the record.
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("jobs/j-legacy"), raw)
	}); err != nil {
		t.Fatalf("raw set: %v", err)
	}

	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	if jobs[0].Env["HF_TOKEN"] != "hf_legacyPlaintext" {
		t.Errorf("legacy record load: got %q", jobs[0].Env["HF_TOKEN"])
	}
}

func TestEncryptedPersistence_WrongKeyringAtLoad_FailsClosed(t *testing.T) {
	// Save with ring A, swap to ring B, attempt to load.
	// The records must NOT decrypt — fail-closed is the load-
	// bearing property here.
	tempDir := t.TempDir()
	p, err := cluster.NewBadgerJSONPersister(tempDir, 30*time.Second)
	if err != nil {
		t.Fatalf("persister: %v", err)
	}
	ringA := newKeyring(t)
	p.SetKeyRing(ringA)

	job := &cpb.Job{
		ID:             "j-wrong",
		Command:        "echo",
		Env:            map[string]string{"HF_TOKEN": "hf_sekret"},
		SecretKeys:     []string{"HF_TOKEN"},
		OwnerPrincipal: "user:alice",
	}
	if err := p.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen with ring B.
	p2, err := cluster.NewBadgerJSONPersister(tempDir, 30*time.Second)
	if err != nil {
		t.Fatalf("persister reopen: %v", err)
	}
	defer func() { _ = p2.Close() }()
	ringB := newKeyring(t)
	p2.SetKeyRing(ringB)

	_, err = p2.LoadAllJobs(context.Background())
	if err == nil {
		t.Fatalf("LoadAllJobs with wrong KEK should have failed")
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("want decrypt error, got %v", err)
	}
}

func TestEncryptedPersistence_NoKeyring_StoresPlaintext(t *testing.T) {
	// Deployments without HELION_SECRETSTORE_KEK still run —
	// the persister falls back to plaintext (matches
	// pre-feature-30 behaviour). This is the legacy-compat
	// path.
	p, err := cluster.NewBadgerJSONPersister(t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("persister: %v", err)
	}
	defer func() { _ = p.Close() }()
	// NB: no SetKeyRing call.

	job := &cpb.Job{
		ID:             "j-plain",
		Command:        "echo",
		Env:            map[string]string{"HF_TOKEN": "hf_sekret"},
		SecretKeys:     []string{"HF_TOKEN"},
		OwnerPrincipal: "user:alice",
	}
	if err := p.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	raw := rawRecord(t, p.DB(), "jobs/j-plain")
	if !bytes.Contains(raw, []byte("hf_sekret")) {
		t.Fatalf("no-keyring deployment: want plaintext on disk, got %s", raw)
	}

	jobs, err := p.LoadAllJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadAllJobs: %v", err)
	}
	if jobs[0].Env["HF_TOKEN"] != "hf_sekret" {
		t.Errorf("plaintext roundtrip: got %q", jobs[0].Env["HF_TOKEN"])
	}
}

// ── SaveWorkflow / LoadAllWorkflows ──────────────────────

func TestEncryptedPersistence_SaveWorkflow_OnDiskNoPlaintext(t *testing.T) {
	p, db, _ := newEncryptedPersister(t)
	ctx := context.Background()

	wf := &cpb.Workflow{
		ID:             "wf-e1",
		Name:           "secret-pipe",
		OwnerPrincipal: "user:alice",
		Jobs: []cpb.WorkflowJob{
			{
				Name:       "train",
				Command:    "python",
				Env:        map[string]string{"HF_TOKEN": "wf_childSecret", "PUBLIC": "ok"},
				SecretKeys: []string{"HF_TOKEN"},
			},
			{
				Name:    "report",
				Command: "cat",
				// No secrets on this child.
				Env: map[string]string{"PUBLIC": "visible"},
			},
		},
	}
	if err := p.SaveWorkflow(ctx, wf); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}

	// In-memory WorkflowJob stays plaintext.
	if wf.Jobs[0].Env["HF_TOKEN"] != "wf_childSecret" {
		t.Errorf("in-memory WorkflowJob mutated: %q", wf.Jobs[0].Env["HF_TOKEN"])
	}

	// On-disk form never contains the plaintext.
	raw := rawRecord(t, db, "workflows/wf-e1")
	if bytes.Contains(raw, []byte("wf_childSecret")) {
		t.Fatalf("workflow on-disk leaks plaintext secret: %s", raw)
	}
	if !bytes.Contains(raw, []byte("visible")) {
		t.Errorf("non-secret env stripped: %s", raw)
	}
}

func TestEncryptedPersistence_LoadAllWorkflows_RestoresPlaintext(t *testing.T) {
	p, _, _ := newEncryptedPersister(t)
	ctx := context.Background()

	original := &cpb.Workflow{
		ID:             "wf-e2",
		OwnerPrincipal: "user:alice",
		Jobs: []cpb.WorkflowJob{
			{
				Name:       "a",
				Command:    "echo",
				Env:        map[string]string{"HF_TOKEN": "child-secret"},
				SecretKeys: []string{"HF_TOKEN"},
			},
		},
	}
	if err := p.SaveWorkflow(ctx, original); err != nil {
		t.Fatalf("SaveWorkflow: %v", err)
	}

	wfs, err := p.LoadAllWorkflows(ctx)
	if err != nil {
		t.Fatalf("LoadAllWorkflows: %v", err)
	}
	if len(wfs) != 1 {
		t.Fatalf("want 1 wf, got %d", len(wfs))
	}
	child := wfs[0].Jobs[0]
	if child.Env["HF_TOKEN"] != "child-secret" {
		t.Errorf("child round-trip: got %q", child.Env["HF_TOKEN"])
	}
	if child.EncryptedEnv != nil {
		t.Errorf("child EncryptedEnv should be nil after load, got %+v", child.EncryptedEnv)
	}
}

// ── Rotation end-to-end ──────────────────────────────────

func TestEncryptedPersistence_Rotation_OldRecordsStillLoad(t *testing.T) {
	// Write a record under KEK v1, add v2 + make active,
	// load — the record must decrypt (v1 KEK still in ring).
	// Then save the same record — it should be stamped v2
	// (active KEK). Then remove v1 and load — still decrypts
	// under v2. Classic rotation lifecycle.
	p, db, ring := newEncryptedPersister(t)
	ctx := context.Background()

	j := &cpb.Job{
		ID:             "j-rot",
		Command:        "echo",
		Env:            map[string]string{"HF_TOKEN": "rot_secret"},
		SecretKeys:     []string{"HF_TOKEN"},
		OwnerPrincipal: "user:alice",
	}
	if err := p.SaveJob(ctx, j); err != nil {
		t.Fatalf("SaveJob v1: %v", err)
	}

	// Add v2 + make active.
	kekV2 := make([]byte, secretstore.KEKSize)
	if _, err := io.ReadFull(rand.Reader, kekV2); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := ring.AddKEK(2, kekV2); err != nil {
		t.Fatalf("AddKEK: %v", err)
	}
	if err := ring.SetActive(2); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// Load still works under v1.
	jobs, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs after rotate: %v", err)
	}
	if jobs[0].Env["HF_TOKEN"] != "rot_secret" {
		t.Errorf("post-rotate load: got %q", jobs[0].Env["HF_TOKEN"])
	}

	// Re-save — stamps v2.
	if err := p.SaveJob(ctx, jobs[0]); err != nil {
		t.Fatalf("resave: %v", err)
	}
	raw := rawRecord(t, db, "jobs/j-rot")
	if !bytes.Contains(raw, []byte(`"kek_version":2`)) {
		t.Errorf("re-save should stamp v2, got: %s", raw)
	}

	// Remove v1 KEK. The re-saved record must still decrypt
	// under v2.
	if err := ring.RemoveKEK(1); err != nil {
		t.Fatalf("RemoveKEK: %v", err)
	}
	jobs2, err := p.LoadAllJobs(ctx)
	if err != nil {
		t.Fatalf("LoadAllJobs post-remove: %v", err)
	}
	if jobs2[0].Env["HF_TOKEN"] != "rot_secret" {
		t.Errorf("post-remove load: got %q", jobs2[0].Env["HF_TOKEN"])
	}
}
