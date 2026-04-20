// internal/cluster/persistence_encrypt.go
//
// Feature 30 — persistence-boundary helpers that translate
// between the in-memory Job shape (plaintext Env) and the
// on-disk Job shape (EncryptedEnv with secret values stripped
// from Env).
//
// Persistence-boundary encryption is deliberately narrow: the
// in-memory JobStore / WorkflowStore keep holding plaintext
// Env, so every reader (dispatch, reveal-secret, log-scrub,
// response-redaction) works unchanged. The crypto seam lives
// ONLY in SaveJob/LoadAllJobs and SaveWorkflow/LoadAllWorkflows.
//
// Failure policy
// ──────────────
// A Save-time encrypt failure blocks the write — the record
// never lands on disk, the caller gets the error, and no
// silent plaintext fallback occurs.
//
// A Load-time decrypt failure also blocks the load — the Job
// can't be reconstructed faithfully without its secrets, so
// we'd rather surface the error and have the operator
// intervene than silently skip it into a broken state.
//
// A record with BOTH Env[k] and EncryptedEnv[k] populated is a
// contract violation. We fail-closed at load time rather than
// picking one — the caller's operator tree has a bug and
// silently preferring one side would mask it.

package cluster

import (
	"fmt"

	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// encryptSecretsInPlace mutates the provided env map by
// removing every key in secretKeys whose value is non-empty,
// and returns the EncryptedEnv map of envelopes. Intended to
// be called on a SHALLOW COPY of Env so the caller's in-memory
// Job is not mutated.
//
// Returns (encrypted, nil) on success. Returns (nil, err)
// without partial-mutation side effects on failure; the env
// passed in is unchanged in that case.
//
// Pre-condition: every key in secretKeys either appears in
// env with a string value or does not appear at all. A missing
// key is silently skipped (the job declared it as a secret but
// never actually set a value — unusual but not an error).
func encryptSecretsInPlace(env map[string]string, secretKeys []string, ring *secretstore.KeyRing) (map[string]*secretstore.EncryptedEnvValue, error) {
	if ring == nil || len(secretKeys) == 0 || len(env) == 0 {
		return nil, nil
	}

	// First pass: encrypt all values. If ANY fail we bail
	// before mutating env, so the caller's map stays intact.
	encrypted := make(map[string]*secretstore.EncryptedEnvValue, len(secretKeys))
	for _, k := range secretKeys {
		v, present := env[k]
		if !present {
			continue
		}
		blob, err := ring.Encrypt([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("encrypt secret %q: %w", k, err)
		}
		encrypted[k] = blob
	}

	// Second pass: strip now-encrypted values from env.
	// Guaranteed to succeed — we're only deleting.
	for k := range encrypted {
		delete(env, k)
	}

	if len(encrypted) == 0 {
		return nil, nil
	}
	return encrypted, nil
}

// decryptSecretsInto rehydrates plaintext env values from the
// EncryptedEnv map. Mutates the provided env in place. Returns
// an error if any envelope fails to decrypt OR if both forms
// are present for the same key (contract violation — see the
// file doc).
//
// A record with no EncryptedEnv (legacy / pre-feature-30) is a
// no-op.
func decryptSecretsInto(env map[string]string, encrypted map[string]*secretstore.EncryptedEnvValue, ring *secretstore.KeyRing) error {
	if len(encrypted) == 0 {
		return nil
	}
	if ring == nil {
		return fmt.Errorf("record has encrypted_env but no keyring configured — cannot recover secrets")
	}

	// First pass: decrypt all. If ANY fail we surface the
	// error without partial mutation. (Env is the caller's
	// map; leaving it partially populated would be worse than
	// the error.)
	plaintexts := make(map[string]string, len(encrypted))
	for k, blob := range encrypted {
		if _, already := env[k]; already {
			return fmt.Errorf("record has both env[%q] and encrypted_env[%q] — refusing to pick one", k, k)
		}
		pt, err := ring.Decrypt(blob)
		if err != nil {
			return fmt.Errorf("decrypt secret %q: %w", k, err)
		}
		plaintexts[k] = string(pt)
	}

	// Second pass: populate env.
	if env == nil {
		// Defensive — caller should always pass a non-nil
		// map, but if a JSON unmarshal produced nil Env we
		// can't write into it. Caller decides whether to
		// allocate; we document by returning an error.
		return fmt.Errorf("cannot decrypt into nil env map — caller must allocate")
	}
	for k, v := range plaintexts {
		env[k] = v
	}
	return nil
}

// jobOnDiskCopy prepares a Job for persistence when envelope
// encryption is configured. Returns a shallow copy with Env
// reduced to non-secret entries and EncryptedEnv populated with
// the sealed secret values. The caller's input Job is NOT
// mutated.
//
// When the keyring is nil or the Job has no SecretKeys, the
// caller should skip this helper and marshal the Job directly.
func jobOnDiskCopy(j *cpb.Job, ring *secretstore.KeyRing) (*cpb.Job, error) {
	if ring == nil || len(j.SecretKeys) == 0 || len(j.Env) == 0 {
		return j, nil
	}
	// Shallow copy the struct first.
	onDisk := *j
	// Deep copy the Env map so in-place mutations don't leak
	// back to the in-memory record.
	envCopy := make(map[string]string, len(j.Env))
	for k, v := range j.Env {
		envCopy[k] = v
	}
	encrypted, err := encryptSecretsInPlace(envCopy, j.SecretKeys, ring)
	if err != nil {
		return nil, err
	}
	if encrypted == nil {
		// Nothing actually encrypted (e.g. all SecretKeys
		// were missing from Env). Use the plain Job.
		return j, nil
	}
	onDisk.Env = envCopy
	onDisk.EncryptedEnv = encrypted
	return &onDisk, nil
}

// jobInMemoryForm reverses the on-disk transform: takes a Job
// unmarshaled from Badger, decrypts any EncryptedEnv entries
// back into Env, clears the EncryptedEnv field. Mutates the
// Job in place — caller owns the struct.
//
// Safe to call on legacy records with no EncryptedEnv (no-op).
func jobInMemoryForm(j *cpb.Job, ring *secretstore.KeyRing) error {
	if len(j.EncryptedEnv) == 0 {
		return nil
	}
	if j.Env == nil {
		// Legacy path may produce a nil Env; ensure there's
		// a map to decrypt into.
		j.Env = make(map[string]string, len(j.EncryptedEnv))
	}
	if err := decryptSecretsInto(j.Env, j.EncryptedEnv, ring); err != nil {
		return err
	}
	j.EncryptedEnv = nil
	return nil
}

// workflowOnDiskCopy is the Workflow-side counterpart to
// jobOnDiskCopy. Each child WorkflowJob is encrypted
// independently — secret keys are declared per child, not per
// workflow.
func workflowOnDiskCopy(wf *cpb.Workflow, ring *secretstore.KeyRing) (*cpb.Workflow, error) {
	if ring == nil {
		return wf, nil
	}
	anyEncrypted := false
	// Deep-copy the Jobs slice since we may mutate per-child
	// Env / EncryptedEnv. Shallow copies of the other fields
	// are fine.
	newJobs := make([]cpb.WorkflowJob, len(wf.Jobs))
	copy(newJobs, wf.Jobs)

	for i := range newJobs {
		child := &newJobs[i]
		if len(child.SecretKeys) == 0 || len(child.Env) == 0 {
			continue
		}
		envCopy := make(map[string]string, len(child.Env))
		for k, v := range child.Env {
			envCopy[k] = v
		}
		encrypted, err := encryptSecretsInPlace(envCopy, child.SecretKeys, ring)
		if err != nil {
			return nil, fmt.Errorf("workflow %q child %q: %w", wf.ID, child.Name, err)
		}
		if encrypted == nil {
			continue
		}
		child.Env = envCopy
		child.EncryptedEnv = encrypted
		anyEncrypted = true
	}
	if !anyEncrypted {
		return wf, nil
	}
	onDisk := *wf
	onDisk.Jobs = newJobs
	return &onDisk, nil
}

// workflowInMemoryForm reverses the workflow on-disk transform
// in place on a loaded workflow record.
func workflowInMemoryForm(wf *cpb.Workflow, ring *secretstore.KeyRing) error {
	for i := range wf.Jobs {
		child := &wf.Jobs[i]
		if len(child.EncryptedEnv) == 0 {
			continue
		}
		if child.Env == nil {
			child.Env = make(map[string]string, len(child.EncryptedEnv))
		}
		if err := decryptSecretsInto(child.Env, child.EncryptedEnv, ring); err != nil {
			return fmt.Errorf("workflow %q child %q: %w", wf.ID, child.Name, err)
		}
		child.EncryptedEnv = nil
	}
	return nil
}
