// internal/webauthn/tier.go
//
// Feature 34 — enforcement-tier parsing for
// HELION_AUTH_WEBAUTHN_REQUIRED. Mirrors the feature-27
// cert-tier shape (off / warn / on) so operators have a
// single mental model for phased rollouts.

package webauthn

import (
	"fmt"
	"strings"
)

// Tier controls whether admin endpoints require a WebAuthn-
// backed JWT (auth_method == "webauthn").
type Tier int

const (
	// TierOff disables enforcement entirely. Admin endpoints
	// accept any valid JWT. Legacy / pre-feature-34 behaviour.
	TierOff Tier = iota

	// TierWarn emits EventWebAuthnRequired on every admin
	// request whose token is not WebAuthn-backed, but still
	// serves the request. Used for staged rollouts: flip to
	// `warn`, identify operators still on bearer-only tokens
	// via the audit log, then flip to `on`.
	TierWarn

	// TierOn refuses any admin request whose token lacks
	// `auth_method == "webauthn"`. 401 + audit event.
	TierOn
)

// String renders a tier back to its env-var form so logs +
// responses can surface the current setting.
func (t Tier) String() string {
	switch t {
	case TierOff:
		return "off"
	case TierWarn:
		return "warn"
	case TierOn:
		return "on"
	default:
		return "off"
	}
}

// ParseTier parses a HELION_AUTH_WEBAUTHN_REQUIRED value.
// Unrecognised values produce an error — the coordinator
// treats that as fatal so a typo never silently downgrades
// security.
func ParseTier(raw string) (Tier, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off":
		return TierOff, nil
	case "warn":
		return TierWarn, nil
	case "on":
		return TierOn, nil
	default:
		return TierOff, fmt.Errorf("webauthn: invalid tier %q (want off / warn / on)", raw)
	}
}
