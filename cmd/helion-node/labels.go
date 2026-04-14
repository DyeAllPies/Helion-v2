package main

import (
	"bytes"
	goruntime "runtime"
	"os"
	"os/exec"
	"strings"
)

// labelProbe is the injection seam for label auto-detection. Production
// code wires it to runNvidiaSmi; tests substitute a stub that returns a
// canned GPU model or an error. Keeping it as a variable (not a const
// string) lets unit tests exercise the label path without touching
// real hardware — see the project memory note about keeping GPU tests
// small and local.
var gpuProbe = runNvidiaSmi

// gpuCountProbe is the injection seam for GPU *count* detection. The
// heartbeat carries a total-GPU capacity that the scheduler uses as
// a bin-packing dimension; nodes run a `nvidia-smi --list-gpus`-style
// probe at startup and report the resulting count. Stubbed in unit
// tests the same way gpuProbe is — no real hardware touched on CI.
var gpuCountProbe = runNvidiaSmiCount

// gatherNodeLabels returns the label set this node should report at
// registration. Sources, in order of precedence (later wins):
//
//  1. Auto-detected: os=<goos>, arch=<goarch>, gpu=<model> when
//     `nvidia-smi` succeeds. Best-effort — probe failures are fine,
//     we just don't set the label.
//  2. Operator overrides: every HELION_LABEL_<KEY>=<VALUE> env var,
//     with <KEY> lower-cased and surrounding dashes / underscores
//     preserved verbatim. An explicit `HELION_LABEL_GPU=none` can
//     override auto-detection to hide a GPU from the scheduler.
//
// The coordinator re-sanitises whatever lands here, so malformed or
// oversize labels get dropped server-side rather than rejected here —
// the node should be generous with what it reports (log-visible
// labels) and let the coordinator enforce the shape rules.
func gatherNodeLabels() map[string]string {
	labels := make(map[string]string)

	// Layer 1: auto-detected baseline.
	labels["os"] = goruntime.GOOS
	labels["arch"] = goruntime.GOARCH
	if gpu, ok := gpuProbe(); ok && gpu != "" {
		labels["gpu"] = gpu
	}

	// Layer 2: HELION_LABEL_* overrides. Use os.Environ so we pick
	// up every prefixed entry, not just ones we remembered to name.
	const prefix = "HELION_LABEL_"
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(kv[len(prefix):eq])
		value := kv[eq+1:]
		if key == "" {
			continue
		}
		labels[key] = value
	}
	return labels
}

// runNvidiaSmiCount counts the visible GPUs by running `nvidia-smi
// --list-gpus` and tallying output lines. Any error (command
// missing, no device, permission denied) returns 0 — the node
// reports 0 GPUs to the coordinator, and ResourceAwarePolicy
// treats it as CPU-only. Kept separate from runNvidiaSmi because
// the heartbeat needs the count regardless of whether the label
// probe succeeded (they're different CLI invocations with slightly
// different failure modes).
func runNvidiaSmiCount() uint32 {
	cmd := exec.Command("nvidia-smi", "--list-gpus")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return 0
	}
	var n uint32
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// runNvidiaSmi is the production GPU probe. It runs `nvidia-smi
// --query-gpu=name --format=csv,noheader` and returns the first
// GPU model line on success. Any error (command missing, no device,
// permission denied) returns (_, false) and the caller simply omits
// the gpu label.
//
// Deliberately minimal — the full GPU bookkeeping (MIG, memory, per-
// device scheduling) is a follow-up slice. This function's job is
// just to populate a coarse `gpu=<model>` label for selector matching.
func runNvidiaSmi() (string, bool) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", false
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		return "", false
	}
	// Multi-GPU hosts: take the first line as representative. The
	// label is a selector hint ("this node has an A100"), not an
	// enumeration of every device.
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return line, true
}
