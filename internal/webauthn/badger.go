// internal/webauthn/badger.go
//
// BadgerDB-backed CredentialStore. Shares the coordinator's
// main BadgerDB (same pattern the registry + groups +
// revocation packages use).
//
// Key layout
// ──────────
//   webauthn/credentials/<b64url_credential_id>       → JSON(CredentialRecord)
//   webauthn/by-operator/<b64url_user_handle>/<b64url_credential_id> → empty marker
//
// The by-operator reverse index is the load-bearing
// structure for ListByOperator — every login-begin call
// resolves the operator's subject → user handle →
// credential IDs in O(k) where k is the number of
// credentials for that operator (typically 1–3).

package webauthn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	badger "github.com/dgraph-io/badger/v4"
)

const (
	credentialKeyPrefix = "webauthn/credentials/"
	byOperatorKeyPrefix = "webauthn/by-operator/"
)

// BadgerStore satisfies CredentialStore against a shared
// *badger.DB.
type BadgerStore struct {
	db *badger.DB
}

// NewBadgerStore wraps the caller's Badger handle. The
// coordinator hands in its existing persister.DB().
func NewBadgerStore(db *badger.DB) *BadgerStore {
	return &BadgerStore{db: db}
}

func credentialKey(id []byte) []byte {
	return []byte(credentialKeyPrefix + EncodeCredentialID(id))
}

// byOperatorKey composes a reverse-index key. We use a
// separator byte (`\x1f` — ASCII unit-separator) between
// the user handle and credential ID segments so our scan
// logic can parse them back out.
func byOperatorKey(userHandle, credentialID []byte) []byte {
	var b strings.Builder
	b.WriteString(byOperatorKeyPrefix)
	b.WriteString(EncodeCredentialID(userHandle))
	b.WriteByte(0x1f)
	b.WriteString(EncodeCredentialID(credentialID))
	return []byte(b.String())
}

func byOperatorPrefix(userHandle []byte) []byte {
	var b strings.Builder
	b.WriteString(byOperatorKeyPrefix)
	b.WriteString(EncodeCredentialID(userHandle))
	b.WriteByte(0x1f)
	return []byte(b.String())
}

// Create persists the record + reverse index in a single
// Badger transaction. A duplicate credential ID → error.
func (s *BadgerStore) Create(_ context.Context, rec *CredentialRecord) error {
	if rec == nil {
		return fmt.Errorf("BadgerStore.Create: nil record")
	}
	if len(rec.Credential.ID) == 0 {
		return fmt.Errorf("BadgerStore.Create: empty credential ID")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("BadgerStore.Create: marshal: %w", err)
	}
	credKey := credentialKey(rec.Credential.ID)
	revKey := byOperatorKey(rec.UserHandle, rec.Credential.ID)
	return s.db.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(credKey); err == nil {
			return fmt.Errorf("BadgerStore.Create: credential %s already exists",
				EncodeCredentialID(rec.Credential.ID))
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return fmt.Errorf("BadgerStore.Create: probe: %w", err)
		}
		if err := txn.Set(credKey, raw); err != nil {
			return fmt.Errorf("BadgerStore.Create: set cred: %w", err)
		}
		if err := txn.Set(revKey, []byte{}); err != nil {
			return fmt.Errorf("BadgerStore.Create: set reverse: %w", err)
		}
		return nil
	})
}

// Get reads a record by credential ID.
func (s *BadgerStore) Get(_ context.Context, credentialID []byte) (*CredentialRecord, error) {
	credKey := credentialKey(credentialID)
	var rec CredentialRecord
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(credKey)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrCredentialNotFound
		}
		if err != nil {
			return fmt.Errorf("Get: probe: %w", err)
		}
		return item.Value(func(v []byte) error {
			return json.Unmarshal(v, &rec)
		})
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListByOperator walks the reverse-index prefix and pulls
// full records in a single read transaction.
func (s *BadgerStore) ListByOperator(_ context.Context, userHandle []byte) ([]*CredentialRecord, error) {
	var out []*CredentialRecord
	prefix := byOperatorPrefix(userHandle)
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().KeyCopy(nil)
			sep := len(prefix)
			credIDEncoded := string(k[sep:])
			credID, err := DecodeCredentialID(credIDEncoded)
			if err != nil {
				continue
			}
			credItem, err := txn.Get(credentialKey(credID))
			if err != nil {
				if errors.Is(err, badger.ErrKeyNotFound) {
					continue
				}
				return fmt.Errorf("ListByOperator: fetch: %w", err)
			}
			var rec CredentialRecord
			if err := credItem.Value(func(v []byte) error {
				return json.Unmarshal(v, &rec)
			}); err != nil {
				return fmt.Errorf("ListByOperator: unmarshal %s: %w", credIDEncoded, err)
			}
			out = append(out, &rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	SortByRegistered(out)
	return out, nil
}

// List reads every credential record. Admin-facing only;
// the caller (handler) gates on ActionAdmin.
func (s *BadgerStore) List(_ context.Context) ([]*CredentialRecord, error) {
	var out []*CredentialRecord
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(credentialKeyPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var rec CredentialRecord
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &rec)
			}); err != nil {
				return fmt.Errorf("List: unmarshal %q: %w", it.Item().Key(), err)
			}
			out = append(out, &rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	SortByRegistered(out)
	return out, nil
}

// Delete removes the credential + its reverse-index entry.
// Idempotent.
func (s *BadgerStore) Delete(_ context.Context, credentialID []byte) error {
	credKey := credentialKey(credentialID)
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(credKey)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("Delete: probe: %w", err)
		}
		var rec CredentialRecord
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &rec)
		}); err != nil {
			return fmt.Errorf("Delete: unmarshal: %w", err)
		}
		if err := txn.Delete(credKey); err != nil {
			return fmt.Errorf("Delete: cred: %w", err)
		}
		if len(rec.UserHandle) > 0 {
			if err := txn.Delete(byOperatorKey(rec.UserHandle, credentialID)); err != nil {
				return fmt.Errorf("Delete: reverse: %w", err)
			}
		}
		return nil
	})
}

// UpdateSignCount persists the advanced counter.
func (s *BadgerStore) UpdateSignCount(_ context.Context, credentialID []byte, signCount uint32) error {
	credKey := credentialKey(credentialID)
	return s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(credKey)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrCredentialNotFound
		}
		if err != nil {
			return fmt.Errorf("UpdateSignCount: probe: %w", err)
		}
		var rec CredentialRecord
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &rec)
		}); err != nil {
			return fmt.Errorf("UpdateSignCount: unmarshal: %w", err)
		}
		if err := verifyNotReplay(rec.Credential.Authenticator.SignCount, signCount); err != nil {
			return err
		}
		rec.Credential.Authenticator.SignCount = signCount
		raw, err := json.Marshal(&rec)
		if err != nil {
			return fmt.Errorf("UpdateSignCount: marshal: %w", err)
		}
		return txn.Set(credKey, raw)
	})
}
