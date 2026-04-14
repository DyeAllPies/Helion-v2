package nodeserver

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/artifacts"
	"github.com/DyeAllPies/Helion-v2/internal/staging"
)

func TestArtifactOutputsToProto_NilAndEmpty(t *testing.T) {
	if got := artifactOutputsToProto(nil); got != nil {
		t.Errorf("nil in: %+v", got)
	}
	if got := artifactOutputsToProto([]staging.ResolvedOutput{}); got != nil {
		t.Errorf("empty in: %+v", got)
	}
}

func TestArtifactOutputsToProto_HappyPath(t *testing.T) {
	in := []staging.ResolvedOutput{
		{Name: "MODEL", URI: artifacts.URI("s3://b/m"), Size: 42, SHA256: "deadbeef", LocalPath: "out/m"},
		{Name: "METRICS", URI: artifacts.URI("file:///tmp/metrics.json"), Size: 7, SHA256: "cafe", LocalPath: "out/metrics.json"},
	}
	got := artifactOutputsToProto(in)
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[0].Name != "MODEL" || got[0].Uri != "s3://b/m" ||
		got[0].Size != 42 || got[0].Sha256 != "deadbeef" || got[0].LocalPath != "out/m" {
		t.Fatalf("row 0: %+v", got[0])
	}
	if got[1].Uri != "file:///tmp/metrics.json" {
		t.Fatalf("row 1 uri: %q", got[1].Uri)
	}
}
