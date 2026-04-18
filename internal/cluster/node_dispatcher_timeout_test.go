// internal/cluster/node_dispatcher_timeout_test.go
//
// Coverage for dispatchRPCTimeout. The full RPC path is exercised in
// tests/integration/security/dispatch_tls_test.go; this file pins
// the timeout-derivation policy so a future refactor can't silently
// reintroduce the 10 s ceiling that used to cancel MNIST mid-flight
// (regression fixed alongside feature 21).

package cluster

import (
	"testing"
	"time"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

func TestDispatchRPCTimeout(t *testing.T) {
	cases := []struct {
		name string
		job  *cpb.Job
		want time.Duration
	}{
		{
			name: "batch with explicit TimeoutSeconds gets timeout + buffer",
			job:  &cpb.Job{TimeoutSeconds: 180},
			want: 180*time.Second + dispatchRPCBuffer,
		},
		{
			name: "batch with short TimeoutSeconds still gets timeout + buffer",
			job:  &cpb.Job{TimeoutSeconds: 5},
			want: 5*time.Second + dispatchRPCBuffer,
		},
		{
			name: "batch with zero TimeoutSeconds falls back to floor",
			job:  &cpb.Job{TimeoutSeconds: 0},
			want: minDispatchRPCTimeout,
		},
		{
			name: "service job always uses floor (handler ACKs from goroutine)",
			job: &cpb.Job{
				TimeoutSeconds: 3600,
				Service:        &cpb.ServiceSpec{Port: 8000},
			},
			want: minDispatchRPCTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dispatchRPCTimeout(tc.job)
			if got != tc.want {
				t.Fatalf("dispatchRPCTimeout = %s; want %s", got, tc.want)
			}
		})
	}
}
