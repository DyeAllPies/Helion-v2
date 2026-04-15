// internal/nodeserver/service_prober.go
//
// Feature 17 — inference service readiness probe loop.
//
// Launched by Dispatch when a DispatchRequest carries a Service spec.
// Runs alongside the long-running job process and emits edge-triggered
// ServiceEvent RPCs to the coordinator on readiness transitions.
//
// Design:
//   - Bind target is always 127.0.0.1:<port><health_path>. The node
//     never probes a non-loopback address; a service that wants to
//     expose itself outside the pod is the caller's concern (Nginx
//     in front of the coordinator's /api/services lookup).
//   - Probes every 5 s. HTTP 200-299 = ready; anything else (including
//     connection refused, TCP RST, probe timeout) = unhealthy.
//   - Edge-triggered: we only call ReportServiceEvent on a state flip
//     (unknown → ready, ready → unhealthy, unhealthy → ready) so a
//     happy service doesn't spam the coordinator's audit log.
//   - A 2 s per-probe timeout bounds the prober's goroutine so a
//     hung HTTP handler on the service side doesn't keep the prober
//     alive forever after ctx is cancelled.

package nodeserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/DyeAllPies/Helion-v2/proto"
)

// probeInterval + probeTimeout are package-private so tests can drop
// the interval to something less sleepy. Production values are 5 s
// (per spec) and 2 s (probe-per-request cap).
var (
	probeInterval = 5 * time.Second
	probeTimeout  = 2 * time.Second
)

// probeService runs the service-readiness probe loop for one dispatched
// service job. Blocks until ctx is cancelled (typically by Dispatch
// returning after the underlying process exits). Never returns an
// error — every failure mode is a transient probe-miss recorded via
// ReportServiceEvent.
func (s *Server) probeService(ctx context.Context, jobID string, spec *pb.ServiceSpec) {
	if spec == nil || spec.Port == 0 {
		return
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", spec.Port, spec.HealthPath)
	client := &http.Client{Timeout: probeTimeout}

	// Initial grace period before the first probe.
	if spec.HealthInitialMs > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(spec.HealthInitialMs) * time.Millisecond):
		}
	}

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	var (
		lastReady        bool // state of the last emitted event; false on first run
		haveLastReady    bool // false until we have emitted once (so the first ready transition fires)
		consecutiveFails uint32
	)

	probe := func() bool {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		// Drain + close so the HTTP transport can reuse the
		// connection across probe ticks. Cap the drain at 4 KiB so a
		// misbehaving /healthz that streams megabytes of body cannot
		// slow the prober down.
		_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}

	for {
		ready := probe()
		if ready {
			consecutiveFails = 0
		} else {
			consecutiveFails++
		}
		if !haveLastReady || ready != lastReady {
			s.emitServiceEvent(ctx, jobID, spec, ready, consecutiveFails)
			lastReady = ready
			haveLastReady = true
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// emitServiceEvent fires one ServiceEvent RPC. Errors are logged but
// do not stop the prober — a transient coordinator-side hiccup must
// not prevent the next transition from being reported.
func (s *Server) emitServiceEvent(
	ctx context.Context,
	jobID string,
	spec *pb.ServiceSpec,
	ready bool,
	consecutiveFailures uint32,
) {
	if s.client == nil {
		return
	}
	evt := &pb.ServiceEvent{
		JobId:               jobID,
		NodeId:              s.nodeID,
		NodeAddress:         s.advertiseAddr(),
		Port:                spec.Port,
		HealthPath:          spec.HealthPath,
		Ready:               ready,
		ConsecutiveFailures: consecutiveFailures,
		OccurredAt:          timestamppb.New(time.Now()),
	}
	if err := s.client.ReportServiceEvent(ctx, evt); err != nil {
		s.log.Warn("ReportServiceEvent failed",
			slog.String("job_id", jobID),
			slog.Bool("ready", ready),
			slog.Any("err", err))
	}
}

// advertiseAddr returns the node's externally-reachable address as
// registered with the coordinator, or "" if it wasn't provided. The
// coordinator uses this plus the service port to build the upstream
// URL returned by GET /api/services/{id}.
func (s *Server) advertiseAddr() string {
	return s.address
}
