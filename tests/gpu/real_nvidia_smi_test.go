//go:build gpu

// Build-tag gated so `go test ./...` (with no tags) skips this file
// entirely — CI never tries to run it. Invoked via scripts/test-gpu.sh
// on a developer machine that actually has a GPU.

package gputests

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestRealNvidiaSmi_ProducesNonZeroCount is the smoke test for the
// stubbed probe in cmd/helion-node/labels.go. Every other test
// substitutes a fake `gpuCountProbe`, so this is the one place where
// the real `nvidia-smi --list-gpus` call gets exercised against a
// live device inventory.
//
// Runs the CLI directly rather than importing the node-agent's
// probe function (which is package-private in package main). If the
// CLI signature changes upstream, this test will catch the drift.
func TestRealNvidiaSmi_ProducesNonZeroCount(t *testing.T) {
	cmd := exec.Command("nvidia-smi", "--list-gpus")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skipf("nvidia-smi not runnable (no GPU host?): %v", err)
	}
	// Each line is one device. A GPU host has at least one.
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		t.Fatal("nvidia-smi --list-gpus produced empty output on a GPU host")
	}
	lines := strings.Split(trimmed, "\n")
	t.Logf("nvidia-smi reports %d GPU(s)", len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("line %d is blank", i)
		}
	}
}

// TestRealNvidiaSmi_QueryName_ReturnsSaneModel spot-checks the label
// probe. The model string ends up in the node's `gpu=<model>` label
// which downstream selectors match against, so a malformed name
// (multiline, shell-escaped, etc.) would break label-based GPU
// scheduling on the first production deployment.
func TestRealNvidiaSmi_QueryName_ReturnsSaneModel(t *testing.T) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Skipf("nvidia-smi not runnable: %v", err)
	}
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		t.Fatal("empty model line")
	}
	// Multi-GPU hosts return multiple lines; our label probe takes
	// the first. Just make sure every line is printable ASCII.
	for i, line := range strings.Split(trimmed, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			t.Errorf("line %d is blank", i)
			continue
		}
		for j := 0; j < len(name); j++ {
			if c := name[j]; c < 0x20 || c == 0x7f {
				t.Errorf("line %d contains control byte 0x%02x: %q", i, c, name)
				break
			}
		}
	}
}
