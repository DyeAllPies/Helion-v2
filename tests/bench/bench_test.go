// tests/bench/bench_test.go
//
// Benchmarks comparing the Go runtime vs the Rust runtime (when available).
//
// Run with:
//
//	go test -bench=. -benchtime=10s ./tests/bench/
//
// The Rust runtime benchmarks are skipped automatically unless the Rust binary
// is running and HELION_RUNTIME_SOCKET points to its socket. To run them:
//
//	# terminal 1 — start Rust runtime
//	./runtime-rust/target/release/helion-runtime --socket /tmp/helion-bench.sock
//
//	# terminal 2 — run benchmarks
//	HELION_RUNTIME_SOCKET=/tmp/helion-bench.sock \
//	  go test -bench=. -benchtime=10s -count=1 ./tests/bench/
//
// Benchmarks measure end-to-end job execution latency and throughput as
// seen from the Go caller (runtime.Run round-trip, including fork+exec).
// They do NOT require a live coordinator.
package bench

import (
	"context"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	helionruntime "github.com/DyeAllPies/Helion-v2/internal/runtime"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newGoRuntime() helionruntime.Runtime {
	return helionruntime.NewGoRuntime()
}

func newRustRuntime(t testing.TB) helionruntime.Runtime {
	t.Helper()
	sock := os.Getenv("HELION_RUNTIME_SOCKET")
	if sock == "" {
		t.Skip("HELION_RUNTIME_SOCKET not set — skipping Rust runtime benchmark")
	}
	// Quick connectivity check before starting the benchmark.
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Skipf("Rust runtime socket %s not reachable: %v — skipping", sock, err)
	}
	conn.Close()
	return helionruntime.NewRustClient(sock)
}

func trueReq(jobID string) helionruntime.RunRequest {
	return helionruntime.RunRequest{
		JobID:          jobID,
		Command:        trueCmd(),
		TimeoutSeconds: 10,
	}
}

func echoReq(jobID string) helionruntime.RunRequest {
	return helionruntime.RunRequest{
		JobID:          jobID,
		Command:        echoCmd(),
		Args:           []string{"hello"},
		TimeoutSeconds: 10,
	}
}

// trueCmd returns the path to /bin/true (or equivalent on this OS).
func trueCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/true"
}

func echoCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/usr/bin/echo"
}

// ── startup latency ───────────────────────────────────────────────────────────

// BenchmarkGoRuntime_StartupLatency measures how long a single `/bin/true`
// job takes from Run() call to return (fork+exec+wait).
func BenchmarkGoRuntime_StartupLatency(b *testing.B) {
	rt := newGoRuntime()
	defer rt.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := trueReq(jobID(i))
		_, _ = rt.Run(ctx, req)
	}
}

// BenchmarkRustRuntime_StartupLatency measures the same for the Rust backend.
func BenchmarkRustRuntime_StartupLatency(b *testing.B) {
	rt := newRustRuntime(b)
	defer rt.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := trueReq(jobID(i))
		_, _ = rt.Run(ctx, req)
	}
}

// ── throughput ────────────────────────────────────────────────────────────────

const concurrency = 10

// BenchmarkGoRuntime_Throughput measures throughput at saturation with
// `concurrency` concurrent jobs each running `echo hello`.
func BenchmarkGoRuntime_Throughput(b *testing.B) {
	rt := newGoRuntime()
	defer rt.Close()
	benchThroughput(b, rt)
}

// BenchmarkRustRuntime_Throughput measures the same for the Rust backend.
func BenchmarkRustRuntime_Throughput(b *testing.B) {
	rt := newRustRuntime(b)
	defer rt.Close()
	benchThroughput(b, rt)
}

func benchThroughput(b *testing.B, rt helionruntime.Runtime) {
	b.Helper()
	ctx := context.Background()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	b.ResetTimer()
	b.SetParallelism(concurrency)
	b.RunParallel(func(pb *testing.PB) {
		var i int
		for pb.Next() {
			i++
			sem <- struct{}{}
			wg.Add(1)
			go func(n int) {
				defer func() { <-sem; wg.Done() }()
				req := echoReq(jobID(n))
				_, _ = rt.Run(ctx, req)
			}(i)
		}
	})
	wg.Wait()
}

// ── memory footprint snapshot ─────────────────────────────────────────────────

// BenchmarkGoRuntime_MemFootprint reports the Go heap growth per job by
// running 100 sequential `/bin/true` jobs and reading runtime.MemStats.
func BenchmarkGoRuntime_MemFootprint(b *testing.B) {
	rt := newGoRuntime()
	defer rt.Close()
	benchMemFootprint(b, rt)
}

func BenchmarkRustRuntime_MemFootprint(b *testing.B) {
	rt := newRustRuntime(b)
	defer rt.Close()
	benchMemFootprint(b, rt)
}

func benchMemFootprint(b *testing.B, rt helionruntime.Runtime) {
	b.Helper()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := trueReq(jobID(i))
		_, _ = rt.Run(ctx, req)
	}
}

// ── latency percentiles ───────────────────────────────────────────────────────

// BenchmarkGoRuntime_LatencyPercentiles collects individual durations and
// reports p50/p95/p99 via b.ReportMetric.
func BenchmarkGoRuntime_LatencyPercentiles(b *testing.B) {
	rt := newGoRuntime()
	defer rt.Close()
	benchLatencyPercentiles(b, rt)
}

func BenchmarkRustRuntime_LatencyPercentiles(b *testing.B) {
	rt := newRustRuntime(b)
	defer rt.Close()
	benchLatencyPercentiles(b, rt)
}

func benchLatencyPercentiles(b *testing.B, rt helionruntime.Runtime) {
	b.Helper()
	ctx := context.Background()
	durations := make([]time.Duration, 0, b.N)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, _ = rt.Run(ctx, trueReq(jobID(i)))
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	if len(durations) == 0 {
		return
	}
	sortDurations(durations)
	p50 := durations[len(durations)*50/100]
	p95 := durations[len(durations)*95/100]
	p99 := durations[len(durations)*99/100]

	b.ReportMetric(float64(p50.Milliseconds()), "p50_ms")
	b.ReportMetric(float64(p95.Milliseconds()), "p95_ms")
	b.ReportMetric(float64(p99.Milliseconds()), "p99_ms")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jobID(i int) string {
	return "bench-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// insertion sort — fine for benchmark sample sizes (≤ b.N which is usually ≤ 100k).
func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		key := d[i]
		j := i - 1
		for j >= 0 && d[j] > key {
			d[j+1] = d[j]
			j--
		}
		d[j+1] = key
	}
}
