// src/app/features/analytics/analytics-dashboard.component.spec.ts
//
// Unit tests for AnalyticsDashboardComponent. The component has five data
// sources (throughput, queue-wait, node-reliability, retry, workflow) fetched
// via ApiService. We stub each observable and verify rendering, date-range
// handling, error paths, and subscription lifecycle.

import { ComponentFixture, TestBed, fakeAsync, tick } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { Observable, of, throwError } from 'rxjs';

import { AnalyticsDashboardComponent } from './analytics-dashboard.component';
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

    expect(component.throughputLabels.length).toBe(2);
    expect(component.completedData.length).toBe(2);
    expect(component.failedData.length).toBe(2);
    // The sorted labels' completed counts must contain 5 and 7.
    expect(component.completedData).toContain(5);
    expect(component.completedData).toContain(7);
    expect(component.failedData).toContain(2);
  });

  it('uses 0 for buckets with no data in that status', () => {
    apiSpy.getAnalyticsThroughput.and.returnValue(of({
      from: '', to: '',
      data: [
        { hour: '2026-04-13T10:00:00Z', status: 'completed', job_count: 5, avg_duration_ms: 0, p95_duration_ms: 0 },
      ],
    }));
    component.reload();
    expect(component.failedData).toEqual([0]); // no failed events → bucket is 0
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

    expect(component.queueWaitLabels.length).toBe(2);
    expect(component.avgWaitData).toEqual([500, 300]);
    expect(component.p95WaitData).toEqual([1500, 900]);
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
    expect(component.throughputLabels).toEqual([]);
    expect(component.nodeRows).toEqual([]);
    expect(component.retryRows).toEqual([]);
    expect(component.queueWaitLabels).toEqual([]);
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
});
