// internal/persistence/keys.go
//
// All BadgerDB key definitions live here.  No other file constructs raw key
// strings — every key is built through one of the typed functions below.
// That rule means a typo or a prefix change is a one-file fix, not a grep.
//
// Key schema (§4.5 of the v2 design document):
//
//   nodes/{addr}                  – Node record, written on every heartbeat.
//   jobs/{id}                     – Job record, written on every state transition.
//   certs/{nodeID}                – X.509 DER bytes for a node's certificate.
//   audit/{unix-nano}-{eventID}   – Audit event; append-only, never mutated.
//   tokens/{jti}                  – JWT metadata for revocation; carries a TTL.
//
// The separator is "/" so that BadgerDB's byte-order prefix scan returns all
// keys under a prefix in a single sequential seek.

package persistence

import "fmt"

// ---- Prefix constants -------------------------------------------------------

// These are the bare prefixes.  Use the NodeKey / JobKey / … functions to
// build complete keys; use the Prefix* constants for prefix-scans in List().

const (
	PrefixNodes  = "nodes/"
	PrefixJobs   = "jobs/"
	PrefixCerts  = "certs/"
	PrefixAudit  = "audit/"
	PrefixTokens = "tokens/"
)

// ---- Key constructors -------------------------------------------------------

// NodeKey returns the BadgerDB key for a node record.
//
// addr is the node's address string, e.g. "10.0.0.1:8080".
func NodeKey(addr string) []byte {
	return []byte(PrefixNodes + addr)
}

// JobKey returns the BadgerDB key for a job record.
//
// id is the job's unique string identifier.
func JobKey(id string) []byte {
	return []byte(PrefixJobs + id)
}

// CertKey returns the BadgerDB key for a node's X.509 certificate.
//
// nodeID is the stable identifier assigned to the node at registration.
func CertKey(nodeID string) []byte {
	return []byte(PrefixCerts + nodeID)
}

// AuditKey returns the BadgerDB key for an audit event.
//
// Keys are ordered by creation time because BadgerDB stores keys in
// lexicographic (byte) order and the timestamp is left-padded with zeros via
// %020d so that numeric order and lexicographic order agree for any timestamp
// that fits in 64 bits (valid until year 2554).
//
// eventID is an additional disambiguator so that two events emitted within the
// same nanosecond do not collide.
func AuditKey(unixNano int64, eventID string) []byte {
	return []byte(fmt.Sprintf("%s%020d-%s", PrefixAudit, unixNano, eventID))
}

// TokenKey returns the BadgerDB key for a JWT revocation record.
//
// jti is the "JWT ID" claim — a unique token identifier.
func TokenKey(jti string) []byte {
	return []byte(PrefixTokens + jti)
}
