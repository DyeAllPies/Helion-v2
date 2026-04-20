// internal/cluster/persistence_rotate.go
//
// Feature 30 — rewrap sweep for KEK rotation. Iterates every
// persisted Job and Workflow, rewraps each EncryptedEnv entry
// under the current active KEK, and re-saves. Used by the
// admin rotation endpoint.
//
// Safety
// ──────
// The sweep reads every record in a single Badger View
// transaction, builds a list of (key, rewrapped-bytes) pairs
// in memory, then writes each back in its own Update
// transaction. This avoids holding a read + write lock across
// the crypto calls and keeps per-record writes small.
//
// A partial failure is surfaced via the (rewrapped, scanned,
// err) return but does NOT roll back prior successes —
// rotation is idempotent, so an operator can retry and the
// already-rewrapped records are no-ops on their second pass.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
	"github.com/DyeAllPies/Helion-v2/internal/secretstore"
)

// RewrapAll rewraps every encrypted secret envelope under the
// keyring's active KEK. Returns (rewrapped, scanned, err).
// `rewrapped` counts ENVELOPES (not records) that were
// advanced to the new KEK version. `scanned` is every
// persisted Job + WorkflowJob that had at least one envelope.
//
// Safe to call concurrently with live writes: each record is
// read, rewrapped, and rewritten in its own Badger
// transaction, so a concurrent SaveJob for the same key sees
// linearisable semantics (either its write or ours wins;
// both produce a valid record under the new KEK because any
// new write goes through the SAME keyring's active KEK).
//
// No-op when no keyring is configured.
func (p *BadgerJSONPersister) RewrapAll(ctx context.Context) (rewrapped, scanned int, err error) {
	if p.keyring == nil {
		return 0, 0, nil
	}

	// Phase 1: collect the per-record plan. Read-only view
	// avoids blocking writers for the duration of the crypto.
	type plan struct {
		key      []byte
		body     []byte
	}
	var plans []plan
	err = p.db.View(func(txn *badger.Txn) error {
		for _, prefix := range [][]byte{[]byte("jobs/"), []byte("workflows/")} {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = prefix
			it := txn.NewIterator(opts)
			for it.Rewind(); it.Valid(); it.Next() {
				if err := ctx.Err(); err != nil {
					it.Close()
					return err
				}
				var body []byte
				if err := it.Item().Value(func(v []byte) error {
					body = append([]byte(nil), v...)
					return nil
				}); err != nil {
					it.Close()
					return fmt.Errorf("read %q: %w", it.Item().Key(), err)
				}
				plans = append(plans, plan{
					key:  it.Item().KeyCopy(nil),
					body: body,
				})
			}
			it.Close()
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	// Phase 2: for each plan, rewrap its envelopes and
	// overwrite the key.
	for _, pl := range plans {
		if err := ctx.Err(); err != nil {
			return rewrapped, scanned, err
		}
		isJob := len(pl.key) >= 5 && string(pl.key[:5]) == "jobs/"
		var rewrappedHere int
		var newBody []byte
		if isJob {
			rewrappedHere, newBody, err = rewrapJobRecord(pl.body, p.keyring)
		} else {
			rewrappedHere, newBody, err = rewrapWorkflowRecord(pl.body, p.keyring)
		}
		if err != nil {
			return rewrapped, scanned, fmt.Errorf("rewrap %s: %w", pl.key, err)
		}
		if rewrappedHere == 0 {
			continue
		}
		scanned++
		rewrapped += rewrappedHere
		if err := p.db.Update(func(txn *badger.Txn) error {
			return txn.Set(pl.key, newBody)
		}); err != nil {
			return rewrapped, scanned, fmt.Errorf("write %s: %w", pl.key, err)
		}
	}
	return rewrapped, scanned, nil
}

// rewrapJobRecord unmarshals a Job record, rewraps every
// EncryptedEnv entry not already under the active KEK, and
// returns (count, newBody, err). A record with no envelopes
// or all envelopes already at the active version produces
// count=0 — the caller skips the write to save a Badger
// round-trip.
func rewrapJobRecord(body []byte, ring *secretstore.KeyRing) (int, []byte, error) {
	var j cpb.Job
	if err := json.Unmarshal(body, &j); err != nil {
		return 0, nil, fmt.Errorf("unmarshal: %w", err)
	}
	if len(j.EncryptedEnv) == 0 {
		return 0, nil, nil
	}
	active := ring.ActiveVersion()
	count := 0
	for k, blob := range j.EncryptedEnv {
		if blob.KEKVersion == active {
			continue
		}
		newBlob, err := ring.Rewrap(blob)
		if err != nil {
			return 0, nil, fmt.Errorf("rewrap %q: %w", k, err)
		}
		j.EncryptedEnv[k] = newBlob
		count++
	}
	if count == 0 {
		return 0, nil, nil
	}
	out, err := json.Marshal(&j)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal: %w", err)
	}
	return count, out, nil
}

// rewrapWorkflowRecord is the Workflow-side counterpart. Each
// child WorkflowJob may carry its own EncryptedEnv; we rewrap
// every non-active entry.
func rewrapWorkflowRecord(body []byte, ring *secretstore.KeyRing) (int, []byte, error) {
	var w cpb.Workflow
	if err := json.Unmarshal(body, &w); err != nil {
		return 0, nil, fmt.Errorf("unmarshal: %w", err)
	}
	active := ring.ActiveVersion()
	count := 0
	for i := range w.Jobs {
		child := &w.Jobs[i]
		if len(child.EncryptedEnv) == 0 {
			continue
		}
		for k, blob := range child.EncryptedEnv {
			if blob.KEKVersion == active {
				continue
			}
			newBlob, err := ring.Rewrap(blob)
			if err != nil {
				return 0, nil, fmt.Errorf("child %q rewrap %q: %w", child.Name, k, err)
			}
			child.EncryptedEnv[k] = newBlob
			count++
		}
	}
	if count == 0 {
		return 0, nil, nil
	}
	out, err := json.Marshal(&w)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal: %w", err)
	}
	return count, out, nil
}
