// src/app/features/analytics/analytics-dashboard.component.spec.ts
//
// Unit tests for AnalyticsDashboardComponent. The component has five data
// sources (throughput, queue-wait, node-reliability, retry, workflow) fetched
// via ApiService. We stub each observable and verify rendering, date-range
// handling, error paths, and subscription lifecycle.

import { ComponentFixture, TestBed, fakeAsync, tick } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { Observable, of, throwError } from 'rxjs';

import {
  AnalyticsDashboardComponent, formatBucketLabel, formatHourLabel,
  bucketKey, enumerateBuckets,
} from './analytics-dashboard.component';
import { ApiService } from '../../core/services/api.service';
import {
  AnalyticsThroughputResponse,
  AnalyticsNodeReliabilityRow,
  AnalyticsRetryRow,
  AnalyticsQueueWaitResponse,
  AnalyticsWorkflowOutcomesResponse,
} from '../../shared/models';

const emptyThroughput = (): AnalyticsThroughputResponse => ({
  from: '', to: '', data: [],
});
const emptyQueueWait = (): AnalyticsQueueWaitResponse => ({
  from: '', to: '', data: [],
});
const emptyWorkflow = (): AnalyticsWorkflowOutcomesResponse => ({
  from: '', to: '', data: [],
});

function mkApiSpy(): jasmine.SpyObj<ApiService> {
  const spy = jasmine.createSpyObj<ApiService>('ApiService', [
    'getAnalyticsThroughput',
    'getAnalyticsNodeReliability',
    'getAnalyticsRetryEffectiveness',
    'getAnalyticsQueueWait',
    'getAnalyticsWorkflowOutcomes',
  ]);
  spy.getAnalyticsThroughput.and.returnValue(of(emptyThroughput()));
  spy.getAnalyticsNodeReliability.and.returnValue(of({ data: [] }));
  spy.getAnalyticsRetryEffectiveness.and.returnValue(of({ data: [] }));
  spy.getAnalyticsQueueWait.and.returnValue(of(emptyQueueWait()));
  spy.getAnalyticsWorkflowOutcomes.and.returnValue(of(emptyWorkflow()));
  return spy;
}

describe('AnalyticsDashboardComponent', () => {
  let fixture:   ComponentFixture<AnalyticsDashboardComponent>;
  let component: AnalyticsDashboardComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = mkApiSpy();
    await TestBed.configureTestingModule({
      imports: [AnalyticsDashboardComponent],
      providers: [
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(AnalyticsDashboardComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  // ── Creation & init ────────────────────────────────────────────────────

  it('should create', () => expect(component).toBeTruthy());

  it('initialises date range to last 7 days', () => {
    expect(component.fromDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(component.toDate).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    const diff = (new Date(component.toDate).getTime() - new Date(component.fromDate).getTime())
               / (1000 * 60 * 60 * 24);
    expect(diff).toBeGreaterThanOrEqual(6);
    expect(diff).toBeLessThanOrEqual(8);
  });

  it('calls all five analytics endpoints on init', () => {
    expect(apiSpy.getAnalyticsThroughput).toHaveBeenCalled();
    expect(apiSpy.getAnalyticsNodeReliability).toHaveBeenCalled();
    expect(apiSpy.getAnalyticsRetryEffectiveness).toHaveBeenCalled();
    expect(apiSpy.getAnalyticsQueueWait).toHaveBeenCalled();
    expect(apiSpy.getAnalyticsWorkflowOutcomes).toHaveBeenCalled();
  });

  it('sends from/to RFC3339 timestamps to the API', () => {
    // First arg is a full ISO datetime like "2026-04-06T00:00:00Z".
    const [from, to] = apiSpy.getAnalyticsThroughput.calls.mostRecent().args as [string, string];
    expect(from).toMatch(/^\d{4}-\d{2}-\d{2}T00:00:00Z$/);
    expect(to).toMatch(/^\d{4}-\d{2}-\d{2}T23:59:59Z$/);
  });

  it('clears loading flag when all calls complete', () => {
    expect(component.loading).toBe(false);
  });

  it('does not set an error under normal conditions', () => {
    expect(component.error).toBe('');
  });

  // ── Throughput data ────────────────────────────────────────────────────

  it('processes throughput data into chart series', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-13T10:00:00Z', status: 'completed', job_count: 5, avg_duration_ms: 100, p95_duration_ms: 200 },
        { hour: '2026-04-13T10:00:00Z', status: 'failed',    job_count: 2, avg_duration_ms: 50,  p95_duration_ms: 80 },
        { hour: '2026-04-13T11:00:00Z', status: 'completed', job_count: 7, avg_duration_ms: 120, p95_duration_ms: 220 },
      ],
    }));
    component.reload();

    // Zero-fill now emits one label per bucket in the active window
    // whether or not the backend returned a row for it (so the
    // x-axis reads as a proper timeline instead of skipping quiet
    // periods). The meaningful assertion is "the non-zero buckets
    // line up with the rows we sent", not a specific total count.
    expect(component.throughputLabels.length).toBe(component.completedData.length);
    expect(component.throughputLabels.length).toBe(component.failedData.length);
    expect(component.completedData).toContain(5);
    expect(component.completedData).toContain(7);
    expect(component.failedData).toContain(2);
    // Non-zero completed buckets must be exactly two (5 and 7) in
    // the 10:00 and 11:00 slots; every other bucket zero-fills.
    expect(component.completedData.filter(v => v !== 0).length).toBe(2);
  });

  it('uses 0 for buckets with no data in that status', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-13T10:00:00Z', status: 'completed', job_count: 5, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.reload();
    // The completed bucket at 10:00 is 5; the failed series has no
    // data in any bucket so the whole series is zero.
    expect(component.completedData).toContain(5);
    expect(component.failedData.every(v => v === 0)).toBeTrue();
  });

  // ── Queue wait ─────────────────────────────────────────────────────────

  it('processes queue wait data', () => {
    apiSpy.getAnalyticsQueueWait.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-13T10:00:00Z', avg_wait_ms: 500, p95_wait_ms: 1500, job_count: 10 },
        { hour: '2026-04-13T11:00:00Z', avg_wait_ms: 300, p95_wait_ms: 900,  job_count: 8 },
      ],
    }));
    component.reload();

    // Same zero-fill shape as throughput: all three arrays share
    // the same bucket count; the two rows we sent surface as the
    // only non-zero entries in each series.
    expect(component.queueWaitLabels.length).toBe(component.avgWaitData.length);
    expect(component.queueWaitLabels.length).toBeGreaterThanOrEqual(2);
    expect(component.avgWaitData).toContain(500);
    expect(component.avgWaitData).toContain(300);
    expect(component.p95WaitData).toContain(1500);
    expect(component.p95WaitData).toContain(900);
  });

  // ── Node reliability ──────────────────────────────────────────────────

  it('passes node rows through unchanged', () => {
    const rows: AnalyticsNodeReliabilityRow[] = [{
      node_id: 'n1', address: '10.0.0.1', jobs_completed: 5, jobs_failed: 1,
      failure_rate_pct: 16.67, times_stale: 0, times_revoked: 0,
    }];
    apiSpy.getAnalyticsNodeReliability.and.returnValue(of({ data: rows }));
    component.reload();
    expect(component.nodeRows).toEqual(rows);
  });

  it('defines correct node table columns', () => {
    expect(component.nodeColumns).toEqual(
      ['node_id', 'address', 'jobs_completed', 'jobs_failed', 'failure_rate_pct', 'times_stale'],
    );
  });

  // ── Retry effectiveness ───────────────────────────────────────────────

  it('passes retry rows through unchanged', () => {
    const rows: AnalyticsRetryRow[] = [
      { category: 'retried',       status: 'completed', job_count: 3, avg_duration_ms: 200 },
      { category: 'first_attempt', status: 'completed', job_count: 15, avg_duration_ms: 100 },
    ];
    apiSpy.getAnalyticsRetryEffectiveness.and.returnValue(of({ data: rows }));
    component.reload();
    expect(component.retryRows).toEqual(rows);
  });

  // ── Workflow outcomes ─────────────────────────────────────────────────

  it('processes workflow outcomes split by type', () => {
    apiSpy.getAnalyticsWorkflowOutcomes.and.returnValue(of({
      from: '', to: '',
      data: [
        { event_type: 'workflow.completed', day: '2026-04-13T00:00:00Z', count: 4 },
        { event_type: 'workflow.failed',    day: '2026-04-13T00:00:00Z', count: 1 },
        { event_type: 'workflow.completed', day: '2026-04-14T00:00:00Z', count: 6 },
      ],
    }));
    component.reload();

    expect(component.workflowLabels.length).toBe(2);
    expect(component.wfCompletedData).toContain(4);
    expect(component.wfCompletedData).toContain(6);
    expect(component.wfFailedData).toContain(1);
    expect(component.wfFailedData).toContain(0); // 14th had no failed
  });

  // ── Null data handling ─────────────────────────────────────────────────

  it('handles null data fields (Go encodes nil slices as null)', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '', data: null as unknown as [],
    }));
    apiSpy.getAnalyticsNodeReliability.and.returnValue(of({ data: null as unknown as [] }));
    apiSpy.getAnalyticsRetryEffectiveness.and.returnValue(of({ data: null as unknown as [] }));
    apiSpy.getAnalyticsQueueWait.and.returnValue(of({
      from: '', to: '', data: null as unknown as [],
    }));
    apiSpy.getAnalyticsWorkflowOutcomes.and.returnValue(of({
      from: '', to: '', data: null as unknown as [],
    }));

    component.reload();
    // Zero-fill: even with no data the chart still enumerates
    // every bucket in the window (so the empty timeline is
    // visible). The meaningful invariant is "every bar is 0",
    // not that the arrays are empty.
    expect(component.completedData.every(v => v === 0)).toBeTrue();
    expect(component.failedData.every(v => v === 0)).toBeTrue();
    expect(component.avgWaitData.every(v => v === 0)).toBeTrue();
    expect(component.p95WaitData.every(v => v === 0)).toBeTrue();
    expect(component.nodeRows).toEqual([]);
    expect(component.retryRows).toEqual([]);
    // Workflow-outcomes still uses the old per-row label flow
    // (no zero-fill there); its labels are [] on empty input.
    expect(component.workflowLabels).toEqual([]);
  });

  it('fills missing hour buckets with 0 in throughput', () => {
    // hour X has ONLY failed; hour Y has ONLY completed. Each bucket's
    // opposite-status map lookup must fall through to the `?? 0` branch.
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-13T10:00:00Z', status: 'failed',    job_count: 3, avg_duration_ms: 0, p95_duration_ms: 0 },
        { hour: '2026-04-13T11:00:00Z', status: 'completed', job_count: 8, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.reload();
    // One label has completed=0 (fallback), one label has failed=0 (fallback).
    expect(component.completedData).toContain(0);
    expect(component.failedData).toContain(0);
  });

  it('fills missing day buckets with 0 in workflow outcomes', () => {
    // Day X has ONLY failed; day Y has ONLY completed.
    apiSpy.getAnalyticsWorkflowOutcomes.and.returnValue(of({
      from: '', to: '',
      data: [
        { event_type: 'workflow.failed',    day: '2026-04-13T00:00:00Z', count: 2 },
        { event_type: 'workflow.completed', day: '2026-04-14T00:00:00Z', count: 5 },
      ],
    }));
    component.reload();
    expect(component.wfCompletedData).toContain(0);
    expect(component.wfFailedData).toContain(0);
  });

  // ── Error handling ────────────────────────────────────────────────────

  it('shows an error message when an API call fails', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(throwError(() => new Error('500')));
    component.reload();
    expect(component.error).toContain('throughput');
  });

  it('error message does not leak raw HTTP error details', () => {
    apiSpy.getAnalyticsNodeReliability.and.returnValue(
      throwError(() => new Error('pg: conn busy — internal detail')));
    component.reload();
    // Only the friendly label shows up, not the raw message.
    expect(component.error).toContain('Failed to load');
    expect(component.error).not.toContain('conn busy');
    expect(component.error).not.toContain('pg:');
  });

  it('clears loading flag even when an API call errors', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(throwError(() => new Error('oops')));
    component.reload();
    expect(component.loading).toBe(false);
  });

  // ── Date range reload ─────────────────────────────────────────────────

  it('reload() uses the current fromDate and toDate', () => {
    apiSpy.getAnalyticsThroughput.calls.reset();
    component.fromDate = '2026-01-01';
    component.toDate   = '2026-01-31';
    component.reload();
    const [from, to] = apiSpy.getAnalyticsThroughput.calls.mostRecent().args as [string, string];
    expect(from).toBe('2026-01-01T00:00:00Z');
    expect(to).toBe('2026-01-31T23:59:59Z');
  });

  it('date-independent endpoints are called without range args', () => {
    apiSpy.getAnalyticsNodeReliability.calls.reset();
    apiSpy.getAnalyticsRetryEffectiveness.calls.reset();
    component.reload();
    expect(apiSpy.getAnalyticsNodeReliability).toHaveBeenCalledWith();
    expect(apiSpy.getAnalyticsRetryEffectiveness).toHaveBeenCalledWith();
  });

  // ── Chart data getters ────────────────────────────────────────────────

  it('throughputChartData exposes completed + failed series', () => {
    const d = component.throughputChartData;
    expect(d.datasets.length).toBe(2);
    expect(d.datasets[0].label).toBe('Completed');
    expect(d.datasets[1].label).toBe('Failed');
  });

  it('queueWaitChartData exposes avg + p95 series', () => {
    const d = component.queueWaitChartData;
    expect(d.datasets.length).toBe(2);
    expect(d.datasets[0].label).toContain('Avg');
    expect(d.datasets[1].label).toContain('P95');
  });

  it('workflowChartData exposes completed + failed series', () => {
    const d = component.workflowChartData;
    expect(d.datasets.length).toBe(2);
    expect(d.datasets[0].label).toBe('Completed');
    expect(d.datasets[1].label).toBe('Failed');
  });

  // ── Loading flag ──────────────────────────────────────────────────────

  it('sets loading=true during reload until all requests finish', fakeAsync(() => {
    // Stall one endpoint on a subject we manually resolve later.
    let resolve: (v: { data: [] }) => void = () => { /* noop */ };
    apiSpy.getAnalyticsNodeReliability.and.returnValue(
      new Observable<{ data: [] }>(sub => {
        resolve = (v) => { sub.next(v); sub.complete(); };
      }),
    );
    component.reload();
    expect(component.loading).toBe(true);

    resolve({ data: [] });
    tick();
    expect(component.loading).toBe(false);
  }));

  // ── Chart labels render the hour, not just the date ─────────────────

  it('throughput x-axis labels include the hour (regression: toLocaleDateString dropped `hour`)', () => {
    // Two hour buckets on the same day. The original bug used
    // toLocaleDateString, which ignored the `hour` option and
    // collapsed both rows to the same "Apr 18" label — the chart
    // showed one tick and the sub-hour-resolution view was
    // unreadable. Guard that each bucket now projects into its
    // own label.
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-18T14:00:00Z', status: 'completed', job_count: 3 },
        { hour: '2026-04-18T15:00:00Z', status: 'completed', job_count: 5 },
      ],
    } as AnalyticsThroughputResponse));
    component.reload();
    // Under zero-fill the labels array covers the whole window;
    // pull out the positions where there's data and assert on
    // those — the bug we're guarding against is "both rows
    // collapse to the same label because the hour was dropped
    // from toLocaleDateString", which would make the two non-
    // zero labels identical.
    const nonZeroLabels = component.throughputLabels.filter(
      (_, i) => component.completedData[i] !== 0,
    );
    expect(nonZeroLabels.length).toBe(2);
    expect(nonZeroLabels[0]).not.toBe(nonZeroLabels[1]);
    for (const label of nonZeroLabels) {
      expect(label).toMatch(/\d{1,2}/);
    }
    // Data arrays carry the two counts somewhere in order.
    const nonZeros = component.completedData.filter(v => v !== 0);
    expect(nonZeros).toEqual([3, 5]);
  });

  it('throughput labels are sorted chronologically by the underlying ISO hour', () => {
    // Input order deliberately shuffled. The sort key must be the
    // ISO string (chronological), not the localised display label
    // (which would put "May 1" lexicographically before "May 10").
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-18T16:00:00Z', status: 'completed', job_count: 2 },
        { hour: '2026-04-18T14:00:00Z', status: 'completed', job_count: 7 },
        { hour: '2026-04-18T15:00:00Z', status: 'completed', job_count: 4 },
      ],
    } as AnalyticsThroughputResponse));
    component.reload();
    // Under zero-fill the labels array spans the whole window,
    // but the non-zero entries must still land in chronological
    // order. Extract only the non-zero counts and check that
    // they appear earliest-to-latest (7 @ 14:00, 4 @ 15:00, 2
    // @ 16:00) — not the input order (2, 7, 4).
    const nonZeros = component.completedData.filter(v => v !== 0);
    expect(nonZeros).toEqual([7, 4, 2]);
  });

  it('queue-wait x-axis labels include the hour for each bucket', () => {
    apiSpy.getAnalyticsQueueWait.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-18T14:00:00Z', avg_wait_ms: 120, p95_wait_ms: 340 },
        { hour: '2026-04-18T15:00:00Z', avg_wait_ms: 200, p95_wait_ms: 500 },
      ],
    } as AnalyticsQueueWaitResponse));
    component.reload();
    // Pull out the labels whose avg_wait_ms is non-zero; each
    // input row should still produce a distinct rendered label.
    const nonZeroLabels = component.queueWaitLabels.filter(
      (_, i) => component.avgWaitData[i] !== 0,
    );
    expect(nonZeroLabels.length).toBe(2);
    expect(nonZeroLabels[0]).not.toBe(nonZeroLabels[1]);
  });
});

// ── Quick-range → bucket mapping ────────────────────────────────────────
//
// The MNIST walkthrough video (docs/e2e-mnist-run.mp4) clicks
// "LAST 10 MIN" to show jobs piling onto a minute-scale timeline.
// If someone quietly flips the mapping so 10m falls back to hour
// bucketing, the chart collapses to a single bar and the walkthrough
// loses its narrative beat. These tests pin each quick-range to
// the bucket + window width it currently produces.

describe('AnalyticsDashboardComponent quick-range → bucket mapping', () => {
  let fixture:   ComponentFixture<AnalyticsDashboardComponent>;
  let component: AnalyticsDashboardComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = mkApiSpy();
    await TestBed.configureTestingModule({
      imports: [AnalyticsDashboardComponent],
      providers: [
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();
    fixture   = TestBed.createComponent(AnalyticsDashboardComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('LAST 10 MIN selects second-bucket + a 10-minute window (walkthrough guard)', () => {
    apiSpy.getAnalyticsThroughput.calls.reset();
    apiSpy.getAnalyticsQueueWait.calls.reset();

    const before = Date.now();
    component.setQuickRange('10m');
    const after = Date.now();

    // Bucket picked by the quick-range: SECOND, not minute. At
    // minute resolution all four MNIST jobs (which complete
    // within a single minute) land in one bar and fuse into a
    // smooth bell curve — the viewer sees one event, not four.
    // Second resolution makes each completion a discrete spike.
    expect(component.activeBucket).toBe('second');
    expect(component.activeQuickRange).toBe('10m');

    // The API call carried the bucket param through.
    expect(apiSpy.getAnalyticsThroughput).toHaveBeenCalled();
    const throughputArgs = apiSpy.getAnalyticsThroughput.calls.mostRecent().args;
    expect(throughputArgs[2]).toBe('second'); // third arg is bucket
    expect(apiSpy.getAnalyticsQueueWait).toHaveBeenCalled();
    expect(apiSpy.getAnalyticsQueueWait.calls.mostRecent().args[2]).toBe('second');

    // Window width = 10 minutes within a small tolerance for the
    // wall clock passing between `before` and the call.
    const fromMs = Date.parse(throughputArgs[0] as string);
    const toMs   = Date.parse(throughputArgs[1] as string);
    expect(toMs - fromMs).toBe(10 * 60_000);
    // `to` should hug "now" — within a few ms of when the button
    // was clicked.
    expect(toMs).toBeGreaterThanOrEqual(before);
    expect(toMs).toBeLessThanOrEqual(after + 50); // jitter tolerance
  });

  it('LAST 1 MIN picks second-bucket; LAST HOUR picks minute-bucket; LAST 24 H picks hour-bucket', () => {
    // Sibling coverage: the 10-min branch above is the one the
    // walkthrough filmed, but all four branches matter and a
    // single shared table would trip everyone at once if the
    // mapping got mixed up.
    component.setQuickRange('1m');
    expect(component.activeBucket).toBe('second');

    component.setQuickRange('1h');
    expect(component.activeBucket).toBe('minute');

    component.setQuickRange('24h');
    expect(component.activeBucket).toBe('hour');
  });

  it('editing a day input reverts activeBucket to hour + clears the quick-range', () => {
    component.setQuickRange('10m');
    // LAST 10 MIN now auto-picks second-resolution so individual
    // job completions appear as discrete spikes rather than
    // fusing into one smoothed bump.
    expect(component.activeBucket).toBe('second');

    component.onDateInputChange('2026-04-01', 'from');
    expect(component.activeBucket).toBe('hour');
    expect(component.activeQuickRange).toBe('');
    expect(component.rangeFromISO).toBe('');
  });

  it('activeRefreshSeconds reflects the analyticsRefreshMs env var', () => {
    // The page header shows "refresh every N s" so the viewer
    // sees how fresh the numbers are. Value comes from the
    // environment config, which dev pins at 2 s. Prod is 5 s
    // but unit tests always load the dev environment.
    expect(component.activeRefreshSeconds()).toBe(2);
  });

  it('completed-bar total rises as more jobs complete across successive polls', () => {
    // Simulates the walkthrough beat: view opens at "T+0" with
    // one completed job in a minute bucket; the next poll tick
    // arrives after three more jobs have finished in that same
    // bucket. completedData must REPLACE, not accumulate, the
    // previous array (each poll is a fresh snapshot of the store)
    // but the total across successive responses must move
    // upward, which is what the user sees on the chart.
    //
    // Regression guard: if the component ever stopped re-assigning
    // completedData from the response (e.g. accidental no-op on
    // "same labels"), the chart would freeze at the first value
    // and the "jobs piling up" narrative would be dead.
    // Must fall inside the rolling 10-minute window the quick-range
    // button carves out starting at "now"; a fixed historic date
    // would land outside the window and zero-fill would drop it.
    const now            = Date.now();
    const bucketMinute   = new Date(
      Math.floor((now - 3 * 60_000) / 60_000) * 60_000,
    ).toISOString();
    const secondMinute   = new Date(
      Math.floor((now - 2 * 60_000) / 60_000) * 60_000,
    ).toISOString();

    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: bucketMinute, status: 'completed', job_count: 1, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.setQuickRange('10m'); // triggers one reload
    // Zero-fill puts ~10 labels on the x-axis; the non-zero entry
    // is the bar for bucketMinute.
    const initialTotal = component.completedData.reduce((a, b) => a + b, 0);
    expect(initialTotal).toBe(1);
    expect(component.completedData).toContain(1);

    // Next poll tick — three more jobs finished in the same minute.
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: bucketMinute, status: 'completed', job_count: 4, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.reload();
    const laterTotal = component.completedData.reduce((a, b) => a + b, 0);
    expect(laterTotal).toBe(4);
    expect(laterTotal).toBeGreaterThan(initialTotal);
    expect(component.completedData).toContain(4);

    // Third poll — a second bucket comes into view with 2 more
    // completions. Sorted chronologically, so the earlier minute
    // stays first in the array.
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: bucketMinute, status: 'completed', job_count: 4, avg_duration_ms: 0, p95_duration_ms: 0 },
        { hour: secondMinute, status: 'completed', job_count: 2, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.reload();
    expect(component.completedData.reduce((a, b) => a + b, 0)).toBe(6);
    expect(component.completedData.reduce((a, b) => a + b, 0)).toBeGreaterThan(laterTotal);
    // Non-zero entries carry the two counts in chronological
    // order (4 first, 2 second).
    const nonZeros = component.completedData.filter(v => v !== 0);
    expect(nonZeros).toEqual([4, 2]);
  });
});

// ── Zero-fill guard (the "before docker" test) ───────────────────────
//
// This is the test that pins the behaviour the walkthrough video
// depends on: a 10-minute window with jobs completing in only one
// or two minute buckets must still render 10 ticks on the x-axis,
// not 1-2. Without zero-fill the chart silently hid quiet minutes
// and the timeline looked empty even when jobs had just finished.
//
// Runs in ~5 ms via the component under test + spied ApiService —
// no Playwright, no docker. Any regression that removes the
// zero-fill path fails this test before anyone has to start a
// cluster.
describe('AnalyticsDashboardComponent zero-fill', () => {
  let fixture:   ComponentFixture<AnalyticsDashboardComponent>;
  let component: AnalyticsDashboardComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = mkApiSpy();
    await TestBed.configureTestingModule({
      imports: [AnalyticsDashboardComponent],
      providers: [
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();
    fixture   = TestBed.createComponent(AnalyticsDashboardComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('LAST 10 MIN fills all 600 second buckets even when the backend only returns 2', () => {
    // Before this test existed the repro loop was:
    //   1. code change
    //   2. docker compose rebuild (~60 s)
    //   3. full Playwright spec (~90 s)
    //   4. examine video frame
    //   5. see the chart has one floating bar and no x-axis ticks
    // which was 3+ minutes per iteration. This test reproduces
    // the exact pathology (sparse backend rows) and runs in ms.
    //
    // Arrange: the backend returns only two of the SIX HUNDRED
    // seconds that fall in a LAST 10 MIN window (now that the
    // quick-range auto-picks second-resolution so individual job
    // completions show as discrete spikes rather than fusing
    // into a single smoothed minute-long bump).
    const now = Date.now();
    const bucketAt = (secsBeforeNow: number): string =>
      new Date(
        Math.floor((now - secsBeforeNow * 1_000) / 1_000) * 1_000,
      ).toISOString();

    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        // Two distant seconds so the chart shows two separate
        // spikes rather than one blob.
        { hour: bucketAt(420), status: 'completed', job_count: 1, avg_duration_ms: 0, p95_duration_ms: 0 },
        { hour: bucketAt(180), status: 'completed', job_count: 2, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));

    // Act: switch to LAST 10 MIN — the walkthrough's quick-range
    // click. This drives `bucket='second'` AND a 10-minute window.
    component.setQuickRange('10m');

    // Assert: x-axis carries every second in the window so each
    // job completion becomes a visible discrete event. 10 min =
    // 600 seconds; allow a one-bucket slop for boundary rounding.
    expect(component.activeBucket).toBe('second');
    expect(component.throughputLabels.length).toBeGreaterThanOrEqual(599);
    expect(component.throughputLabels.length).toBeLessThanOrEqual(601);
    expect(component.completedData.length).toBe(component.throughputLabels.length);

    // The two non-empty buckets carry the counts we sent; every
    // other bucket is zero. This is exactly what the walkthrough
    // video's viewer should see: a flat baseline with two sharp
    // vertical spikes at the completion seconds.
    const nonZeros = component.completedData.filter(v => v !== 0);
    expect(nonZeros.sort()).toEqual([1, 2]);
    const zeroCount = component.completedData.filter(v => v === 0).length;
    expect(zeroCount).toBeGreaterThanOrEqual(595); // almost every second is empty
  });

  it('LAST 1 MIN zero-fills all 60 second buckets', () => {
    // The 1-minute view is the narrowest quick-range. With second
    // bucketing it produces 60 x-axis ticks; a single completed
    // job should render as one bar on a full timeline of zeros.
    const now = Date.now();
    const justNow = new Date(
      Math.floor((now - 5_000) / 1_000) * 1_000,
    ).toISOString();

    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: justNow, status: 'completed', job_count: 1, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.setQuickRange('1m');

    // 60 second buckets in 60 seconds (± 1 for boundary rounding).
    expect(component.throughputLabels.length).toBeGreaterThanOrEqual(59);
    expect(component.throughputLabels.length).toBeLessThanOrEqual(61);
    expect(component.completedData.filter(v => v !== 0).length).toBe(1);
    expect(component.completedData.filter(v => v === 0).length).toBeGreaterThanOrEqual(58);
  });
});

describe('enumerateBuckets', () => {
  // Focused pure-function tests for the bucket enumerator used
  // by zero-fill. Runs in microseconds with no DOM.
  it('produces one label per minute in a 10-minute window', () => {
    const from = '2026-04-18T14:00:00Z';
    const to   = '2026-04-18T14:10:00Z';
    const out  = enumerateBuckets(from, to, 'minute');
    expect(out.length).toBe(10);
    expect(out[0]).toBe('2026-04-18T14:00:00.000Z');
    expect(out[9]).toBe('2026-04-18T14:09:00.000Z');
  });

  it('produces one label per second in a 1-minute window', () => {
    const from = '2026-04-18T14:00:00Z';
    const to   = '2026-04-18T14:01:00Z';
    expect(enumerateBuckets(from, to, 'second').length).toBe(60);
  });

  it('rounds an unaligned "from" DOWN to the bucket boundary', () => {
    const out = enumerateBuckets('2026-04-18T14:00:30.500Z', '2026-04-18T14:02:00Z', 'minute');
    // Expect buckets at 14:00 and 14:01 — 14:00:30 rounds down
    // to 14:00 so the enumeration starts there.
    expect(out[0]).toBe('2026-04-18T14:00:00.000Z');
    expect(out.length).toBe(2);
  });

  it('returns [] on malformed / inverted / equal bounds', () => {
    expect(enumerateBuckets('not-a-date', '2026-04-18T14:00:00Z', 'minute')).toEqual([]);
    expect(enumerateBuckets('2026-04-18T14:10:00Z', '2026-04-18T14:00:00Z', 'minute')).toEqual([]);
    expect(enumerateBuckets('2026-04-18T14:00:00Z', '2026-04-18T14:00:00Z', 'minute')).toEqual([]);
  });
});

describe('bucketKey', () => {
  // Normalising the backend's `hour` field to the same bucket
  // boundary we enumerate locally is what keeps the zero-fill
  // lookup working — a backend row for 14:00:00 and an
  // enumerated bucket at 14:00:00.000Z MUST hash to the same key.
  it('truncates to the matching bucket start', () => {
    expect(bucketKey('2026-04-18T14:23:45.678Z', 'hour'))
      .toBe('2026-04-18T14:00:00.000Z');
    expect(bucketKey('2026-04-18T14:23:45.678Z', 'minute'))
      .toBe('2026-04-18T14:23:00.000Z');
    expect(bucketKey('2026-04-18T14:23:45.678Z', 'second'))
      .toBe('2026-04-18T14:23:45.000Z');
  });

  it('returns the original string on malformed input', () => {
    expect(bucketKey('not-a-date', 'minute')).toBe('not-a-date');
  });
});

describe('formatBucketLabel', () => {
  it('renders second-resolution labels with hh:mm:ss', () => {
    const label = formatBucketLabel('2026-04-18T14:32:05Z', 'second');
    expect(label).toMatch(/\d{1,2}:\d{2}:\d{2}/);
  });

  it('renders minute-resolution labels with hh:mm (no seconds)', () => {
    const label = formatBucketLabel('2026-04-18T14:32:00Z', 'minute');
    expect(label).toMatch(/\d{1,2}:\d{2}/);
    // "14:32:00" would slip through the previous regex, so also
    // assert the trailing :SS isn't there.
    expect(label).not.toMatch(/\d{1,2}:\d{2}:\d{2}/);
  });

  it('renders hour-resolution labels with a date prefix', () => {
    // Hour buckets can straddle midnight in a 24h window, so the
    // label has to carry a date token. Check for "Apr" (month
    // abbreviation) or "04" (numeric month) to be locale-neutral.
    const label = formatBucketLabel('2026-04-18T14:00:00Z', 'hour');
    expect(label).toMatch(/Apr|04/);
    expect(label).toMatch(/\d{1,2}:\d{2}/);
  });

  it('returns the original string on malformed input', () => {
    expect(formatBucketLabel('not-a-date', 'second')).toBe('not-a-date');
    expect(formatBucketLabel('', 'minute')).toBe('');
  });

  it('same instant renders distinctly at different resolutions', () => {
    const iso = '2026-04-18T14:32:05Z';
    // Expectation: second-label (hh:mm:ss) differs from minute
    // (hh:mm) and minute differs from hour (Apr 18 hh:mm). A
    // regression that ignored the bucket arg would collapse all
    // three to the same string.
    const sec = formatBucketLabel(iso, 'second');
    const min = formatBucketLabel(iso, 'minute');
    const hr  = formatBucketLabel(iso, 'hour');
    expect(sec).not.toBe(min);
    expect(min).not.toBe(hr);
  });
});

describe('formatHourLabel', () => {
  it('projects an ISO hour into a label that carries both date and hour', () => {
    const label = formatHourLabel('2026-04-18T14:00:00Z');
    // Exact format depends on the Karma host's locale, so don't
    // pin the string — just require that it's not the old buggy
    // shape (date only) by asserting BOTH a month token *and* an
    // hour token land in the output.
    expect(label).toMatch(/Apr|04/);
    expect(label).toMatch(/\d{1,2}:\d{2}/);
  });

  it('returns the original string when the input is unparseable', () => {
    // Shouldn't throw on malformed input — the chart might receive
    // a junk row from a misconfigured analytics backend, and the
    // panel should still render.
    expect(formatHourLabel('not-a-date')).toBe('not-a-date');
  });

  it('distinct ISO hours produce distinct labels', () => {
    expect(formatHourLabel('2026-04-18T14:00:00Z'))
      .not.toBe(formatHourLabel('2026-04-18T15:00:00Z'));
  });
});
