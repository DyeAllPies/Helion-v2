package cluster

import (
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/events"
	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestClassifyUnschedulable(t *testing.T) {
	// Three healthy-ish nodes, two with the gpu=a100 label.
	nodes := []*cpb.Node{
		{NodeID: "n1", Labels: map[string]string{"gpu": "a100"}},
		{NodeID: "n2", Labels: map[string]string{"gpu": "a100", "zone": "us"}},
		{NodeID: "n3", Labels: map[string]string{"arch": "arm64"}},
	}

	cases := []struct {
		name     string
		nodes    []*cpb.Node
		selector map[string]string
		want     string
	}{
		{
			name:     "matching node exists in snapshot -> all_matching_stale",
			nodes:    nodes,
			selector: map[string]string{"gpu": "a100"},
			want:     events.UnschedulableReasonAllMatchingStale,
		},
		{
			name:     "no matching node anywhere -> no_matching_label",
			nodes:    nodes,
			selector: map[string]string{"gpu": "h100"},
			want:     events.UnschedulableReasonNoMatchingLabel,
		},
		{
			name:     "empty selector falls back to no_matching_label",
			nodes:    nodes,
			selector: nil,
			want:     events.UnschedulableReasonNoMatchingLabel,
		},
		{
			name:     "empty snapshot -> no_matching_label",
			nodes:    nil,
			selector: map[string]string{"gpu": "a100"},
			want:     events.UnschedulableReasonNoMatchingLabel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyUnschedulable(tc.nodes, tc.selector)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstFromRef(t *testing.T) {
	cases := []struct {
		name           string
		inputs         []cpb.ArtifactBinding
		wantUpstream   string
		wantOutputName string
	}{
		{
			name:           "empty inputs",
			inputs:         nil,
			wantUpstream:   "",
			wantOutputName: "",
		},
		{
			name: "first From ref is returned",
			inputs: []cpb.ArtifactBinding{
				{Name: "MODEL", URI: "s3://x"},
				{Name: "DATA", From: "train.model"},
				{Name: "X", From: "other.ignored"},
			},
			wantUpstream:   "train",
			wantOutputName: "model",
		},
		{
			name: "malformed From (no dot) returns whole string",
			inputs: []cpb.ArtifactBinding{
				{Name: "D", From: "nodothere"},
			},
			wantUpstream:   "nodothere",
			wantOutputName: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up, out := firstFromRef(tc.inputs)
			if up != tc.wantUpstream || out != tc.wantOutputName {
				t.Errorf("got (%q, %q), want (%q, %q)", up, out, tc.wantUpstream, tc.wantOutputName)
			}
		})
	}
}
