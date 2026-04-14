package main

import (
	goruntime "runtime"
	"testing"
)

// The node agent's label gathering is a one-shot function run at
// registration. Unit tests stub gpuProbe so no real hardware is
// touched — matches the project memory note about keeping GPU tests
// small and local (no GPU on CI).

func withStubbedProbe(t *testing.T, stub func() (string, bool), fn func()) {
	t.Helper()
	orig := gpuProbe
	gpuProbe = stub
	t.Cleanup(func() { gpuProbe = orig })
	fn()
}

func TestGatherNodeLabels_AutoDetectedBaseline(t *testing.T) {
	withStubbedProbe(t, func() (string, bool) { return "", false }, func() {
		labels := gatherNodeLabels()
		if labels["os"] != goruntime.GOOS {
			t.Fatalf("os: %q", labels["os"])
		}
		if labels["arch"] != goruntime.GOARCH {
			t.Fatalf("arch: %q", labels["arch"])
		}
		if _, hasGPU := labels["gpu"]; hasGPU {
			t.Fatalf("gpu label present when probe failed: %+v", labels)
		}
	})
}

func TestGatherNodeLabels_GPUProbeSucceeds(t *testing.T) {
	withStubbedProbe(t, func() (string, bool) { return "NVIDIA A100-SXM4-80GB", true }, func() {
		labels := gatherNodeLabels()
		if labels["gpu"] != "NVIDIA A100-SXM4-80GB" {
			t.Fatalf("gpu label: %q", labels["gpu"])
		}
	})
}

func TestGatherNodeLabels_EnvOverridesAutoDetection(t *testing.T) {
	// An operator reporting `HELION_LABEL_GPU=none` should win over
	// a successful nvidia-smi probe — useful for pinning a node as
	// CPU-only despite the card being physically present.
	t.Setenv("HELION_LABEL_GPU", "none")
	withStubbedProbe(t, func() (string, bool) { return "RTX-4090", true }, func() {
		labels := gatherNodeLabels()
		if labels["gpu"] != "none" {
			t.Fatalf("env override should win: %q", labels["gpu"])
		}
	})
}

func TestGatherNodeLabels_EnvPrefixCaseFolding(t *testing.T) {
	// HELION_LABEL_Zone_West=... becomes labels["zone_west"] — case
	// folded so operators don't accidentally publish both "zone"
	// and "Zone" as distinct labels.
	t.Setenv("HELION_LABEL_Zone_West", "yes")
	withStubbedProbe(t, func() (string, bool) { return "", false }, func() {
		labels := gatherNodeLabels()
		if labels["zone_west"] != "yes" {
			t.Fatalf("case folding: %+v", labels)
		}
	})
}

func TestGatherNodeLabels_EmptyKeyIgnored(t *testing.T) {
	// HELION_LABEL_=value has an empty key — must be dropped, not
	// stored under "".
	t.Setenv("HELION_LABEL_", "orphan")
	withStubbedProbe(t, func() (string, bool) { return "", false }, func() {
		labels := gatherNodeLabels()
		if _, stuck := labels[""]; stuck {
			t.Fatalf("empty-key label survived: %+v", labels)
		}
	})
}
