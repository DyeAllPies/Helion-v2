package metrics_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DyeAllPies/Helion-v2/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// ── Mock DurationSource ───────────────────────────────────────────────────────

type mockDurationSource struct {
	durations []float64
	err       error
}

func (m *mockDurationSource) TerminalJobDurations(_ context.Context) ([]float64, error) {
	return m.durations, m.err
}

// ── NewCollector ──────────────────────────────────────────────────────────────

func TestNewCollector_ReturnsNonNil(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	c := metrics.NewCollector(jobs, nodes, nil)
	if c == nil {
		t.Fatal("expected non-nil Collector")
	}
}

// ── Describe ──────────────────────────────────────────────────────────────────

func TestCollector_Describe_SendsDescriptors(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	c := metrics.NewCollector(jobs, nodes, nil)

	// Buffer sized above the current descriptor count (11 at the time
	// of writing: jobs counter + 3 job gauges + 2 node gauges + duration
	// histogram + 2 registry gauges + services gauge). Grow when new
	// descriptors land.
	ch := make(chan *prometheus.Desc, 32)
	c.Describe(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	if count == 0 {
		t.Error("expected at least one descriptor from Describe")
	}
}

// ── Collect ───────────────────────────────────────────────────────────────────

func TestCollector_Collect_SendsMetrics(t *testing.T) {
	jobs := &mockJobCounter{
		byStatus: map[string]int{
			"RUNNING":   2,
			"PENDING":   1,
			"COMPLETED": 5,
			"FAILED":    1,
		},
		total: 9,
	}
	nodes := &mockNodeCounter{total: 3, healthy: 2}
	c := metrics.NewCollector(jobs, nodes, nil)

	ch := make(chan prometheus.Metric, 20)
	c.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}
	if count == 0 {
		t.Error("expected at least one metric from Collect")
	}
}

func TestCollector_Collect_WithDurations_SendsHistogram(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	dur := &mockDurationSource{durations: []float64{0.5, 1.2, 3.7}}

	c := metrics.NewCollector(jobs, nodes, dur)
	ch := make(chan prometheus.Metric, 20)
	c.Collect(ch)
	close(ch)

	// Should produce at least one histogram metric.
	count := 0
	for range ch {
		count++
	}
	if count == 0 {
		t.Error("expected histogram metric from Collect with durations")
	}
}

func TestCollector_Collect_DurationError_SkipsHistogram(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	dur := &mockDurationSource{err: errors.New("db error")}

	c := metrics.NewCollector(jobs, nodes, dur)
	ch := make(chan prometheus.Metric, 20)
	// Should not panic — duration error is silently ignored.
	c.Collect(ch)
	close(ch)
}

func TestCollector_Collect_EmptyDurations_SkipsHistogram(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	dur := &mockDurationSource{durations: nil}

	c := metrics.NewCollector(jobs, nodes, dur)
	ch := make(chan prometheus.Metric, 20)
	c.Collect(ch)
	close(ch)
}

// ── NewRegistry ───────────────────────────────────────────────────────────────

func TestNewRegistry_ReturnsRegistryAndHandler(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}

	reg, handler := metrics.NewRegistry(jobs, nodes, nil, nil, nil)
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if handler == nil {
		t.Fatal("expected non-nil handler")
	}
}

// mockRegistryCounter is the minimal RegistryCounter for scrape tests.
type mockRegistryCounter struct {
	datasets int
	models   int
	err      error
}

func (m *mockRegistryCounter) CountDatasets(_ context.Context) (int, error) {
	return m.datasets, m.err
}
func (m *mockRegistryCounter) CountModels(_ context.Context) (int, error) {
	return m.models, m.err
}

func TestNewRegistry_RegistryGauges_Emitted(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	rc := &mockRegistryCounter{datasets: 3, models: 7}

	_, handler := metrics.NewRegistry(jobs, nodes, nil, rc, nil)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "helion_datasets_total 3") {
		t.Errorf("expected helion_datasets_total 3, body: %s", body)
	}
	if !strings.Contains(body, "helion_models_total 7") {
		t.Errorf("expected helion_models_total 7, body: %s", body)
	}
}

func TestNewRegistry_RegistryGauges_OmittedWhenNilCounter(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}

	_, handler := metrics.NewRegistry(jobs, nodes, nil, nil, nil)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "helion_datasets_total") {
		t.Errorf("expected no registry gauges when counter is nil, body: %s", body)
	}
}

// mockServiceCounter is the minimal ServiceCounter for scrape tests.
// Mirrors mockRegistryCounter; satisfies metrics.ServiceCounter via Count().
type mockServiceCounter struct{ n int }

func (m *mockServiceCounter) Count() int { return m.n }

// TestNewRegistry_ServicesGauge_Emitted pins the feature-17
// `helion_services_total` gauge emission. The Collector's
// `if c.services != nil { ... }` branch at prometheus.go:219-221
// is load-bearing for operational dashboards that track live
// inference-service count; without this test, a refactor dropping
// the emission (or the SetServiceCounter wiring) would silently
// omit the gauge from scrape output. Mirrors
// TestNewRegistry_RegistryGauges_Emitted's shape.
func TestNewRegistry_ServicesGauge_Emitted(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}
	sc := &mockServiceCounter{n: 4}

	_, handler := metrics.NewRegistry(jobs, nodes, nil, nil, sc)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "helion_services_total 4") {
		t.Errorf("expected helion_services_total 4, body: %s", body)
	}
}

// TestNewRegistry_ServicesGauge_OmittedWhenNilCounter is the
// complementary alarm: a coordinator that never opts into feature
// 17 (SetServiceCounter(nil) / never called) must not emit the
// gauge — otherwise dashboards on non-ML deployments would see
// misleading zeroes. Pins the `if c.services != nil` guard.
func TestNewRegistry_ServicesGauge_OmittedWhenNilCounter(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}

	_, handler := metrics.NewRegistry(jobs, nodes, nil, nil, nil)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "helion_services_total") {
		t.Errorf("expected no services gauge when counter is nil, body: %s", body)
	}
}

func TestNewRegistry_Handler_Returns200(t *testing.T) {
	jobs := &mockJobCounter{byStatus: map[string]int{}, total: 0}
	nodes := &mockNodeCounter{}

	_, handler := metrics.NewRegistry(jobs, nodes, nil, nil, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Errorf("want 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "helion_") {
		t.Error("expected helion_ metrics in response")
	}
}
