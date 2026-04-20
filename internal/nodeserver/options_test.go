// internal/nodeserver/options_test.go
//
// Pure-function tests for mergeEnv — the private helper that
// layers the stager's env additions onto caller-supplied env,
// enforcing stager-precedence so a job can't shadow
// HELION_INPUT_* / HELION_OUTPUT_* keys by sending a same-named
// entry in req.Env.

package nodeserver

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/staging"
)

func TestMergeEnv_NilPrepared_PassesBaseThrough(t *testing.T) {
	base := map[string]string{"FOO": "bar"}
	got := mergeEnv(base, nil)
	// Contract: nil p → return base unchanged (same backing map).
	if got == nil {
		t.Fatal("nil p: got nil, want base back")
	}
	if len(got) != 1 || got["FOO"] != "bar" {
		t.Errorf("nil p: got %v, want %v", got, base)
	}
}

func TestMergeEnv_EmptyEnvAdditions_PassesBaseThrough(t *testing.T) {
	base := map[string]string{"FOO": "bar"}
	p := &staging.Prepared{EnvAdditions: nil}
	got := mergeEnv(base, p)
	if len(got) != 1 || got["FOO"] != "bar" {
		t.Errorf("empty additions: got %v", got)
	}
}

func TestMergeEnv_AdditionsLayeredOnTop(t *testing.T) {
	base := map[string]string{
		"FOO": "bar",
		"QUX": "user-supplied",
	}
	p := &staging.Prepared{
		EnvAdditions: map[string]string{
			"HELION_INPUT_0": "/stage/in/0",
			"BAZ":            "stager",
		},
	}
	got := mergeEnv(base, p)
	if got["FOO"] != "bar" {
		t.Errorf("base preserved: FOO=%q", got["FOO"])
	}
	if got["HELION_INPUT_0"] != "/stage/in/0" {
		t.Errorf("additions applied: HELION_INPUT_0=%q", got["HELION_INPUT_0"])
	}
	if got["BAZ"] != "stager" {
		t.Errorf("additions applied: BAZ=%q", got["BAZ"])
	}
	if got["QUX"] != "user-supplied" {
		t.Errorf("user env preserved: QUX=%q", got["QUX"])
	}
}

func TestMergeEnv_StagerWinsOnKeyClash(t *testing.T) {
	// The safety-critical property: a malicious job that tries
	// to shadow HELION_INPUT_0 with its own value must be
	// overridden by the stager. This is the stager-precedence
	// invariant from the docstring.
	base := map[string]string{
		"HELION_INPUT_0": "ATTACKER_VALUE",
	}
	p := &staging.Prepared{
		EnvAdditions: map[string]string{
			"HELION_INPUT_0": "/stage/in/0",
		},
	}
	got := mergeEnv(base, p)
	if got["HELION_INPUT_0"] != "/stage/in/0" {
		t.Errorf("stager precedence broken: got %q, want /stage/in/0",
			got["HELION_INPUT_0"])
	}
}

func TestMergeEnv_NilBase_StagerOnly(t *testing.T) {
	// A caller that passes a nil base + non-empty stager
	// additions should still get a working map.
	p := &staging.Prepared{
		EnvAdditions: map[string]string{
			"HELION_OUTPUT_0": "/stage/out/0",
		},
	}
	got := mergeEnv(nil, p)
	if got["HELION_OUTPUT_0"] != "/stage/out/0" {
		t.Errorf("nil base: got %v", got)
	}
}

func TestMergeEnv_ReturnsNewMap_DoesNotMutateBase(t *testing.T) {
	// Defensive: callers may share the base map across jobs.
	// mergeEnv must allocate a fresh map when there are
	// additions to apply, otherwise a second Dispatch would
	// see the first's stager keys.
	base := map[string]string{"FOO": "bar"}
	p := &staging.Prepared{
		EnvAdditions: map[string]string{"BAZ": "qux"},
	}
	got := mergeEnv(base, p)
	// Mutating the returned map must not change base.
	got["NEW"] = "inserted"
	if _, present := base["NEW"]; present {
		t.Error("mergeEnv returned the caller's base map (mutation leaked)")
	}
}
