package nodeserver

import (
	"testing"

	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// TestJobFromDispatch_ForwardsSHA256 guards the wire-to-internal
// conversion for artifact integrity: the coordinator commits a
// resolved SHA-256 onto pb.ArtifactBinding, the node lifts it onto
// the internal cpb.ArtifactBinding, and the stager eventually
// consumes it via GetAndVerify. A regression anywhere in this chain
// silently degrades every verified download to a plain Get, which
// is exactly the integrity gap this feature slice closed.
func TestJobFromDispatch_ForwardsSHA256(t *testing.T) {
	req := &pb.DispatchRequest{
		JobId: "j",
		Inputs: []*pb.ArtifactBinding{
			{
				Name:      "DATA",
				Uri:       "s3://b/jobs/wf/pre/out/train",
				Sha256:    "abcdef123",
				LocalPath: "in/train",
			},
		},
	}
	job := jobFromDispatch(req)
	if len(job.Inputs) != 1 {
		t.Fatalf("inputs len: %d", len(job.Inputs))
	}
	if job.Inputs[0].SHA256 != "abcdef123" {
		t.Fatalf("SHA256 not forwarded: %q", job.Inputs[0].SHA256)
	}
	if job.Inputs[0].URI != "s3://b/jobs/wf/pre/out/train" ||
		job.Inputs[0].LocalPath != "in/train" ||
		job.Inputs[0].Name != "DATA" {
		t.Fatalf("other fields corrupted: %+v", job.Inputs[0])
	}
}

func TestJobFromDispatch_EmptySHA_Passes(t *testing.T) {
	// Plain-URI inputs (no upstream committed a digest) go through
	// jobFromDispatch with empty SHA; the stager falls back to Get.
	req := &pb.DispatchRequest{
		JobId: "j",
		Inputs: []*pb.ArtifactBinding{
			{Name: "X", Uri: "s3://b/x", LocalPath: "in/x"},
		},
	}
	job := jobFromDispatch(req)
	if job.Inputs[0].SHA256 != "" {
		t.Fatalf("empty SHA corrupted: %q", job.Inputs[0].SHA256)
	}
}
