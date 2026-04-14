// internal/metrics/prometheus.go
//
// Prometheus collector for Helion cluster metrics.
//
// Metrics exposed
// ───────────────
//   helion_jobs_total{status}       Counter  — terminal jobs by status
//   helion_running_jobs             Gauge    — jobs currently running
//   helion_pending_jobs             Gauge    — jobs currently pending
//   helion_healthy_nodes            Gauge    — healthy node agents
//   helion_total_nodes              Gauge    — all registered nodes
//   helion_job_duration_seconds     Histogram — durations of terminal jobs
//
// The Collector is a pull-based prometheus.Collector: all values are computed
// on each scrape from the live in-memory state, so no background goroutine
// is needed.

package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DurationSource returns the elapsed durations (seconds) of all terminal jobs.
// Implemented by JobStoreAdapter in the api package.
type DurationSource interface {
	TerminalJobDurations(ctx context.Context) ([]float64, error)
}

// RegistryCounter reports dataset + model entry counts. Backs the
// helion_datasets_total / helion_models_total gauges. Decoupled from
// the registry package via this narrow interface so the metrics
// package stays free of BadgerDB imports. Optional — collector treats
// a nil counter as "registry not enabled on this coordinator" and
// simply omits the gauges from scrape output.
type RegistryCounter interface {
	CountDatasets(ctx context.Context) (int, error)
	CountModels(ctx context.Context) (int, error)
}

// Collector implements prometheus.Collector for Helion cluster metrics.
type Collector struct {
	jobs      JobCounter
	nodes     NodeCounter
	durations DurationSource
	registry  RegistryCounter // optional — nil means registry not wired

	descJobsTotal          *prometheus.Desc
	descRunningJobs        *prometheus.Desc
	descPendingJobs        *prometheus.Desc
	descHealthyNodes       *prometheus.Desc
	descTotalNodes         *prometheus.Desc
	descJobDurationSeconds *prometheus.Desc
	descRetryingJobs       *prometheus.Desc
	descScheduledJobs      *prometheus.Desc
	descDatasetsTotal      *prometheus.Desc
	descModelsTotal        *prometheus.Desc
}

// SetRegistryCounter attaches a dataset/model counter after
// construction so existing callers of NewCollector / NewRegistry that
// don't use the registry don't need to change. Calling with nil
// disables registry gauges.
func (c *Collector) SetRegistryCounter(r RegistryCounter) { c.registry = r }

// NewCollector creates a Collector backed by the given counters and duration source.
func NewCollector(jobs JobCounter, nodes NodeCounter, dur DurationSource) *Collector {
	return &Collector{
		jobs:      jobs,
		nodes:     nodes,
		durations: dur,
		descJobsTotal: prometheus.NewDesc(
			"helion_jobs_total",
			"Total number of terminal jobs by status.",
			[]string{"status"}, nil,
		),
		descRunningJobs: prometheus.NewDesc(
			"helion_running_jobs",
			"Number of jobs currently running.",
			nil, nil,
		),
		descPendingJobs: prometheus.NewDesc(
			"helion_pending_jobs",
			"Number of jobs currently pending.",
			nil, nil,
		),
		descHealthyNodes: prometheus.NewDesc(
			"helion_healthy_nodes",
			"Number of healthy node agents.",
			nil, nil,
		),
		descTotalNodes: prometheus.NewDesc(
			"helion_total_nodes",
			"Total number of registered node agents.",
			nil, nil,
		),
		descJobDurationSeconds: prometheus.NewDesc(
			"helion_job_duration_seconds",
			"Histogram of completed/failed job durations in seconds.",
			nil, nil,
		),
		descRetryingJobs: prometheus.NewDesc(
			"helion_retrying_jobs",
			"Number of jobs currently in retrying state.",
			nil, nil,
		),
		descScheduledJobs: prometheus.NewDesc(
			"helion_scheduled_jobs",
			"Number of jobs currently in scheduled state.",
			nil, nil,
		),
		descDatasetsTotal: prometheus.NewDesc(
			"helion_datasets_total",
			"Number of datasets currently registered in the registry.",
			nil, nil,
		),
		descModelsTotal: prometheus.NewDesc(
			"helion_models_total",
			"Number of models currently registered in the registry.",
			nil, nil,
		),
	}
}

// Describe sends all metric descriptors to ch.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descJobsTotal
	ch <- c.descRunningJobs
	ch <- c.descPendingJobs
	ch <- c.descHealthyNodes
	ch <- c.descTotalNodes
	ch <- c.descJobDurationSeconds
	ch <- c.descRetryingJobs
	ch <- c.descScheduledJobs
	ch <- c.descDatasetsTotal
	ch <- c.descModelsTotal
}

// Collect computes current metric values and sends them to ch.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Terminal job counts (counter semantics — monotonically increasing totals).
	for _, status := range []string{"COMPLETED", "FAILED", "TIMEOUT", "LOST", "CANCELLED", "SKIPPED"} {
		if n, err := c.jobs.CountByStatus(ctx, status); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.descJobsTotal, prometheus.CounterValue, float64(n), status,
			)
		}
	}

	// In-flight job counts.
	if n, err := c.jobs.CountByStatus(ctx, "RUNNING"); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descRunningJobs, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.jobs.CountByStatus(ctx, "PENDING"); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descPendingJobs, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.jobs.CountByStatus(ctx, "RETRYING"); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descRetryingJobs, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.jobs.CountByStatus(ctx, "SCHEDULED"); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descScheduledJobs, prometheus.GaugeValue, float64(n))
	}

	// Node counts.
	if n, err := c.nodes.CountHealthy(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descHealthyNodes, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.nodes.CountTotal(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.descTotalNodes, prometheus.GaugeValue, float64(n))
	}

	// Job duration histogram — built from all terminal job records on each scrape.
	if c.durations != nil {
		c.collectDurationHistogram(ctx, ch)
	}

	// Registry counts — only when a counter is wired. A coordinator
	// with no registry enabled simply omits the gauges so scrapers
	// don't see misleading zeroes.
	if c.registry != nil {
		if n, err := c.registry.CountDatasets(ctx); err == nil {
			ch <- prometheus.MustNewConstMetric(c.descDatasetsTotal, prometheus.GaugeValue, float64(n))
		}
		if n, err := c.registry.CountModels(ctx); err == nil {
			ch <- prometheus.MustNewConstMetric(c.descModelsTotal, prometheus.GaugeValue, float64(n))
		}
	}
}

// buckets are the upper bounds used for helion_job_duration_seconds.
var buckets = []float64{0.01, 0.1, 0.5, 1, 5, 10, 30, 60, 300}

func (c *Collector) collectDurationHistogram(ctx context.Context, ch chan<- prometheus.Metric) {
	durs, err := c.durations.TerminalJobDurations(ctx)
	if err != nil || len(durs) == 0 {
		return
	}

	// cumCounts[upperBound] = number of observations with value <= upperBound.
	cumCounts := make(map[float64]uint64, len(buckets))
	var sum float64
	var count uint64

	for _, d := range durs {
		sum += d
		count++
		for _, b := range buckets {
			if d <= b {
				cumCounts[b]++
			}
		}
	}

	ch <- prometheus.MustNewConstHistogram(
		c.descJobDurationSeconds, count, sum, cumCounts,
	)
}

// NewRegistry creates a prometheus.Registry with Go runtime, process, and
// Helion collectors registered, and returns the matching HTTP handler for
// the /metrics endpoint. Pass a non-nil registryCounter to also emit
// helion_datasets_total / helion_models_total — pass nil on deployments
// that don't enable the dataset/model registry.
func NewRegistry(jobs JobCounter, nodes NodeCounter, dur DurationSource, registryCounter RegistryCounter) (*prometheus.Registry, http.Handler) {
	reg := prometheus.NewRegistry()
	coll := NewCollector(jobs, nodes, dur)
	coll.SetRegistryCounter(registryCounter)
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		coll,
	)
	return reg, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
