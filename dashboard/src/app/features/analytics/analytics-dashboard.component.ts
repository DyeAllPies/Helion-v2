// src/app/features/analytics/analytics-dashboard.component.ts
//
// Historical analytics dashboard — queries PostgreSQL via /api/analytics/*.
// Shows: throughput chart, node reliability table, retry effectiveness,
// queue wait times, and workflow outcomes.
//
// All views share a date-range picker (default: last 7 days).

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { BaseChartDirective } from 'ng2-charts';
import { ChartConfiguration, ChartData } from 'chart.js';
import {
  Chart, LineElement, PointElement, LineController,
  BarElement, BarController,
  CategoryScale, LinearScale, Filler, Tooltip, Legend
} from 'chart.js';
import { MatTableModule } from '@angular/material/table';
import { MatSortModule } from '@angular/material/sort';

import { ApiService } from '../../core/services/api.service';
import {
  AnalyticsThroughputRow,
  AnalyticsNodeReliabilityRow,
  AnalyticsRetryRow,
  AnalyticsQueueWaitRow,
  AnalyticsWorkflowOutcomeRow,
} from '../../shared/models';

Chart.register(
  LineElement, PointElement, LineController,
  BarElement, BarController,
  CategoryScale, LinearScale, Filler, Tooltip, Legend
);

@Component({
  selector: 'app-analytics-dashboard',
  standalone: true,
  imports: [CommonModule, FormsModule, BaseChartDirective, MatTableModule, MatSortModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">ANALYTICS</h1>
      <p class="page-sub">Historical metrics from the analytics database</p>
    </div>
    <div class="date-range">
      <!--
        Quick-range buttons for minute-scale views. The existing
        date inputs stay for day-level drilldowns; clicking a
        quick-range sets an ISO-timestamp override so sub-day
        windows (e.g. "last 10 min") reach the backend intact
        instead of being rounded to day boundaries.
      -->
      <div class="quick-ranges" role="group" aria-label="Quick range">
        <button type="button" class="quick-range"
                [class.quick-range--active]="activeQuickRange === '1m'"
                (click)="setQuickRange('1m')">LAST&nbsp;1&nbsp;MIN</button>
        <button type="button" class="quick-range"
                [class.quick-range--active]="activeQuickRange === '10m'"
                (click)="setQuickRange('10m')">LAST&nbsp;10&nbsp;MIN</button>
        <button type="button" class="quick-range"
                [class.quick-range--active]="activeQuickRange === '1h'"
                (click)="setQuickRange('1h')">LAST&nbsp;HOUR</button>
        <button type="button" class="quick-range"
                [class.quick-range--active]="activeQuickRange === '24h'"
                (click)="setQuickRange('24h')">LAST&nbsp;24&nbsp;H</button>
      </div>
      <label class="range-label">
        FROM
        <input type="date" class="range-input" [ngModel]="fromDate" (ngModelChange)="onDateInputChange($event, 'from')">
      </label>
      <label class="range-label">
        TO
        <input type="date" class="range-input" [ngModel]="toDate" (ngModelChange)="onDateInputChange($event, 'to')">
      </label>
    </div>
  </header>

  <!-- Error -->
  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <!-- Loading -->
  <div class="waiting" *ngIf="loading && !error">
    <span class="material-icons spin">sync</span>
    Loading analytics data...
  </div>

  <div *ngIf="!loading && !error">

    <!-- ── Throughput chart ── -->
    <div class="chart-panel" *ngIf="throughputLabels.length > 0">
      <div class="chart-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">bar_chart</span>
        JOB THROUGHPUT — HOURLY
      </div>
      <div class="chart-wrap">
        <canvas baseChart [data]="throughputChartData" [options]="lineChartOptions" type="line"></canvas>
      </div>
    </div>
    <div class="empty-state" *ngIf="throughputLabels.length === 0">
      No throughput data for the selected range.
    </div>

    <!-- ── Queue wait chart ── -->
    <div class="chart-panel" *ngIf="queueWaitLabels.length > 0">
      <div class="chart-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">schedule</span>
        QUEUE WAIT TIME — PENDING TO RUNNING
      </div>
      <div class="chart-wrap">
        <canvas baseChart [data]="queueWaitChartData" [options]="lineChartOptions" type="line"></canvas>
      </div>
    </div>

    <!-- ── Node reliability table ── -->
    <div class="table-panel" *ngIf="nodeRows.length > 0">
      <div class="chart-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">dns</span>
        NODE RELIABILITY
      </div>
      <table mat-table [dataSource]="nodeRows" class="analytics-table">
        <ng-container matColumnDef="node_id">
          <th mat-header-cell *matHeaderCellDef>NODE</th>
          <td mat-cell *matCellDef="let r">{{ r.node_id }}</td>
        </ng-container>
        <ng-container matColumnDef="address">
          <th mat-header-cell *matHeaderCellDef>ADDRESS</th>
          <td mat-cell *matCellDef="let r">{{ r.address }}</td>
        </ng-container>
        <ng-container matColumnDef="jobs_completed">
          <th mat-header-cell *matHeaderCellDef>COMPLETED</th>
          <td mat-cell *matCellDef="let r" class="num">{{ r.jobs_completed }}</td>
        </ng-container>
        <ng-container matColumnDef="jobs_failed">
          <th mat-header-cell *matHeaderCellDef>FAILED</th>
          <td mat-cell *matCellDef="let r" class="num text-error">{{ r.jobs_failed }}</td>
        </ng-container>
        <ng-container matColumnDef="failure_rate_pct">
          <th mat-header-cell *matHeaderCellDef>FAILURE %</th>
          <td mat-cell *matCellDef="let r" class="num"
              [class.text-error]="r.failure_rate_pct > 10">{{ r.failure_rate_pct }}%</td>
        </ng-container>
        <ng-container matColumnDef="times_stale">
          <th mat-header-cell *matHeaderCellDef>STALE</th>
          <td mat-cell *matCellDef="let r" class="num">{{ r.times_stale }}</td>
        </ng-container>
        <tr mat-header-row *matHeaderRowDef="nodeColumns"></tr>
        <tr mat-row *matRowDef="let row; columns: nodeColumns;"></tr>
      </table>
    </div>

    <!-- ── Retry effectiveness ── -->
    <div class="chart-panel" *ngIf="retryRows.length > 0">
      <div class="chart-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">replay</span>
        RETRY EFFECTIVENESS
      </div>
      <div class="retry-grid">
        <div class="retry-card" *ngFor="let r of retryRows">
          <span class="retry-category">{{ r.category | uppercase }}</span>
          <span class="retry-status" [class.text-completed]="r.status === 'completed'"
                                     [class.text-error]="r.status === 'failed'">{{ r.status }}</span>
          <span class="retry-count">{{ r.job_count }} jobs</span>
          <span class="retry-duration">avg {{ r.avg_duration_ms | number:'1.0-0' }} ms</span>
        </div>
      </div>
    </div>

    <!-- ── Workflow outcomes ── -->
    <div class="chart-panel" *ngIf="workflowLabels.length > 0">
      <div class="chart-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">account_tree</span>
        WORKFLOW OUTCOMES — DAILY
      </div>
      <div class="chart-wrap">
        <canvas baseChart [data]="workflowChartData" [options]="barChartOptions" type="bar"></canvas>
      </div>
    </div>

  </div>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex; align-items: flex-start; justify-content: space-between;
      margin-bottom: 24px; gap: 16px; flex-wrap: wrap;
    }
    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 11px; color: var(--color-muted); margin: 0; }

    .date-range {
      display: flex; gap: 12px; align-items: flex-end; flex-wrap: wrap;
    }
    .range-label {
      display: flex; flex-direction: column; gap: 4px;
      font-size: 9px; letter-spacing: 0.1em; color: var(--color-accent);
    }
    .range-input {
      background: var(--color-surface); border: 1px solid var(--color-border);
      border-radius: var(--radius-sm); color: #c8d0dc;
      font-family: var(--font-mono); font-size: 12px;
      padding: 6px 10px;
      &:focus { outline: none; border-color: var(--color-accent); }
    }

    .quick-ranges {
      display: flex; gap: 4px; align-items: stretch;
    }
    .quick-range {
      background: var(--color-surface); border: 1px solid var(--color-border);
      border-radius: var(--radius-sm); color: var(--color-muted);
      font-family: var(--font-mono); font-size: 10px; letter-spacing: 0.08em;
      padding: 6px 10px;
      cursor: pointer;
      transition: background 0.12s, border-color 0.12s, color 0.12s;
    }
    .quick-range:hover {
      color: #e8edf2; border-color: var(--color-accent-dim);
    }
    .quick-range--active {
      background: rgba(192, 132, 252, 0.12);
      border-color: var(--color-accent);
      color: var(--color-accent);
    }

    .error-banner {
      display:flex;align-items:center;gap:8px;
      background:rgba(255,82,82,0.08);border:1px solid rgba(255,82,82,0.3);
      border-radius:var(--radius-sm);color:var(--color-error);
      font-size:12px;padding:10px 14px;margin-bottom:16px;
    }

    .waiting {
      display: flex; align-items: center; gap: 8px;
      color: var(--color-muted); font-size: 12px; margin-top: 60px;
      justify-content: center;
    }
    .spin { animation: spin 0.8s linear infinite; }
    @keyframes spin { to { transform: rotate(360deg); } }

    .empty-state {
      color: var(--color-muted); font-size: 12px;
      text-align: center; padding: 40px 0;
    }

    /* Charts */
    .chart-panel {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
      margin-bottom: 20px;
    }
    .chart-panel__header {
      display: flex; align-items: center; gap: 8px;
      padding: 12px 16px;
      background: var(--color-surface-2);
      border-bottom: 1px solid var(--color-border);
      font-size: 11px; letter-spacing: 0.07em; color: #8896aa;
    }
    .chart-wrap { padding: 16px; height: 280px; }

    /* Node table */
    .table-panel {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
      margin-bottom: 20px;
    }
    .analytics-table { width: 100%; }
    .num { text-align: right; font-variant-numeric: tabular-nums; }
    .text-error { color: var(--color-error); }
    .text-completed { color: var(--color-completed); }

    /* Retry grid */
    .retry-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
      gap: 12px; padding: 16px;
    }
    .retry-card {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      padding: 14px;
      display: flex; flex-direction: column; gap: 4px;
    }
    .retry-category { font-size: 9px; letter-spacing: 0.1em; color: var(--color-accent); }
    .retry-status   { font-size: 14px; font-weight: 700; color: #e8edf2; text-transform: capitalize; }
    .retry-count    { font-size: 11px; color: #8896aa; }
    .retry-duration { font-size: 10px; color: var(--color-muted); }
  `]
})
export class AnalyticsDashboardComponent implements OnInit {

  fromDate = '';
  toDate   = '';
  loading  = true;
  error    = '';

  /**
   * When a quick-range button is active we round-trip ISO instants
   * (`rangeFromISO` / `rangeToISO`) to the backend instead of the
   * day-truncated fromDate/toDate. Empty strings mean "fall back to
   * the day inputs" — the existing behaviour. Editing a day input
   * clears these so the UI never lies about which range is active.
   */
  rangeFromISO = '';
  rangeToISO   = '';
  activeQuickRange: '' | '1m' | '10m' | '1h' | '24h' = '';

  // Throughput
  throughputLabels:  string[] = [];
  completedData:     number[] = [];
  failedData:        number[] = [];

  // Queue wait
  queueWaitLabels: string[] = [];
  avgWaitData:     number[] = [];
  p95WaitData:     number[] = [];

  // Node reliability
  nodeRows: AnalyticsNodeReliabilityRow[] = [];
  nodeColumns = ['node_id', 'address', 'jobs_completed', 'jobs_failed', 'failure_rate_pct', 'times_stale'];

  // Retry effectiveness
  retryRows: AnalyticsRetryRow[] = [];

  // Workflow outcomes
  workflowLabels:    string[] = [];
  wfCompletedData:   number[] = [];
  wfFailedData:      number[] = [];

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    const now = new Date();
    const weekAgo = new Date(now);
    weekAgo.setDate(weekAgo.getDate() - 7);
    this.fromDate = this.toDateStr(weekAgo);
    this.toDate   = this.toDateStr(now);
    this.reload();
  }

  /**
   * Snap the range to a rolling window ending at "now". Emits a
   * full ISO-8601 timestamp so sub-day windows (e.g. 1 min) reach
   * the backend intact instead of being rounded to midnight. Also
   * updates the day inputs so the displayed FROM/TO still show a
   * human-friendly date for the current window.
   */
  setQuickRange(key: '1m' | '10m' | '1h' | '24h'): void {
    const now = new Date();
    const deltaMs: Record<typeof key, number> = {
      '1m':  60_000,
      '10m': 600_000,
      '1h':  3_600_000,
      '24h': 86_400_000,
    };
    const from = new Date(now.getTime() - deltaMs[key]);
    this.rangeFromISO = from.toISOString();
    this.rangeToISO   = now.toISOString();
    // Mirror into the day inputs so the visible FROM/TO still
    // bracket the quick window. For sub-day ranges both end up
    // equal to today; for 24h they may straddle midnight.
    this.fromDate = this.toDateStr(from);
    this.toDate   = this.toDateStr(now);
    this.activeQuickRange = key;
    this.reload();
  }

  /**
   * When the user types into a day input we switch back to day-
   * level mode. Clearing the ISO overrides ensures reload() builds
   * the query from the day strings instead of stale instants.
   */
  onDateInputChange(value: string, field: 'from' | 'to'): void {
    if (field === 'from') this.fromDate = value;
    else                  this.toDate   = value;
    this.rangeFromISO     = '';
    this.rangeToISO       = '';
    this.activeQuickRange = '';
    this.reload();
  }

  reload(): void {
    this.loading = true;
    this.error   = '';

    // Prefer the ISO instants a quick-range button set (minute
    // granularity); fall back to the day inputs otherwise.
    const from = this.rangeFromISO || this.fromDate + 'T00:00:00Z';
    const to   = this.rangeToISO   || this.toDate   + 'T23:59:59Z';

    let pending = 5;
    const done = () => { if (--pending === 0) this.loading = false; };
    const fail = (msg: string) => (err: unknown) => {
      console.error(msg, err);
      this.error = `Failed to load ${msg}`;
      done();
    };

    this.api.getAnalyticsThroughput(from, to).subscribe({
      next: resp => { this.processThroughput(resp.data ?? []); done(); },
      error: fail('throughput'),
    });

    this.api.getAnalyticsNodeReliability().subscribe({
      next: resp => { this.nodeRows = resp.data ?? []; done(); },
      error: fail('node reliability'),
    });

    this.api.getAnalyticsRetryEffectiveness().subscribe({
      next: resp => { this.retryRows = resp.data ?? []; done(); },
      error: fail('retry effectiveness'),
    });

    this.api.getAnalyticsQueueWait(from, to).subscribe({
      next: resp => { this.processQueueWait(resp.data ?? []); done(); },
      error: fail('queue wait'),
    });

    this.api.getAnalyticsWorkflowOutcomes(from, to).subscribe({
      next: resp => { this.processWorkflowOutcomes(resp.data ?? []); done(); },
      error: fail('workflow outcomes'),
    });
  }

  // ── Data processors ────────────────────────────────────────────────────

  private processThroughput(rows: AnalyticsThroughputRow[]): void {
    // Index by the raw ISO hour (the key the backend emits from
    // date_trunc('hour', ...)). We aggregate on that key, then
    // project it into a display label *after* sorting so the
    // x-axis is chronologically ordered (sorting the localised
    // label is wrong: it puts "Apr 4" after "Apr 30").
    const hours      = new Set<string>();
    const completed  = new Map<string, number>();
    const failed     = new Map<string, number>();
    for (const r of rows) {
      hours.add(r.hour);
      if (r.status === 'completed') completed.set(r.hour, r.job_count);
      if (r.status === 'failed')    failed.set(r.hour, r.job_count);
    }
    const sortedHours       = [...hours].sort();
    this.throughputLabels   = sortedHours.map(formatHourLabel);
    this.completedData      = sortedHours.map(h => completed.get(h) ?? 0);
    this.failedData         = sortedHours.map(h => failed.get(h) ?? 0);
  }

  private processQueueWait(rows: AnalyticsQueueWaitRow[]): void {
    // The backend already returns rows ordered by hour; we project
    // each row's ISO hour into the same display label as the
    // throughput chart so both panels share an x-axis format.
    this.queueWaitLabels = rows.map(r => formatHourLabel(r.hour));
    this.avgWaitData = rows.map(r => r.avg_wait_ms);
    this.p95WaitData = rows.map(r => r.p95_wait_ms);
  }

  private processWorkflowOutcomes(rows: AnalyticsWorkflowOutcomeRow[]): void {
    const daySet = new Set<string>();
    const comp   = new Map<string, number>();
    const fail   = new Map<string, number>();

    for (const r of rows) {
      const d = new Date(r.day).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
      daySet.add(d);
      if (r.event_type === 'workflow.completed') comp.set(d, r.count);
      if (r.event_type === 'workflow.failed')    fail.set(d, r.count);
    }

    this.workflowLabels  = [...daySet].sort();
    this.wfCompletedData = this.workflowLabels.map(d => comp.get(d) ?? 0);
    this.wfFailedData    = this.workflowLabels.map(d => fail.get(d) ?? 0);
  }

  // ── Chart configs ──────────────────────────────────────────────────────

  readonly lineChartOptions: ChartConfiguration['options'] = {
    responsive: true,
    maintainAspectRatio: false,
    animation: { duration: 300 },
    scales: {
      x: {
        ticks: { color: '#4a5568', font: { family: "'JetBrains Mono'", size: 10 }, maxRotation: 45 },
        grid:  { color: 'rgba(42,48,64,0.6)' },
      },
      y: {
        ticks: { color: '#4a5568', font: { family: "'JetBrains Mono'", size: 10 } },
        grid:  { color: 'rgba(42,48,64,0.6)' },
        min: 0,
      }
    },
    plugins: {
      legend: { labels: { color: '#8896aa', font: { family: "'JetBrains Mono'", size: 11 }, boxWidth: 12 } },
      tooltip: { backgroundColor: '#111418', borderColor: '#2a3040', borderWidth: 1,
                 titleFont: { family: "'JetBrains Mono'" }, bodyFont: { family: "'JetBrains Mono'" } }
    }
  };

  readonly barChartOptions: ChartConfiguration['options'] = {
    ...this.lineChartOptions,
    scales: {
      ...this.lineChartOptions!.scales,
      y: { ...((this.lineChartOptions!.scales as any).y), stacked: true },
      x: { ...((this.lineChartOptions!.scales as any).x), stacked: true },
    }
  };

  get throughputChartData(): ChartData<'line'> {
    return {
      labels: this.throughputLabels,
      datasets: [
        {
          label: 'Completed', data: this.completedData,
          borderColor: '#66bb6a', backgroundColor: 'rgba(102,187,106,0.08)',
          fill: true, tension: 0.3, pointRadius: 2, pointBackgroundColor: '#66bb6a',
        },
        {
          label: 'Failed', data: this.failedData,
          borderColor: '#ff5252', backgroundColor: 'rgba(255,82,82,0.08)',
          fill: true, tension: 0.3, pointRadius: 2, pointBackgroundColor: '#ff5252',
        },
      ]
    };
  }

  get queueWaitChartData(): ChartData<'line'> {
    return {
      labels: this.queueWaitLabels,
      datasets: [
        {
          label: 'Avg Wait (ms)', data: this.avgWaitData,
          borderColor: '#c084fc', backgroundColor: 'rgba(192,132,252,0.08)',
          fill: true, tension: 0.3, pointRadius: 2, pointBackgroundColor: '#c084fc',
        },
        {
          label: 'P95 Wait (ms)', data: this.p95WaitData,
          borderColor: '#ffab40', backgroundColor: 'rgba(255,171,64,0.06)',
          fill: true, tension: 0.3, pointRadius: 2, pointBackgroundColor: '#ffab40',
        },
      ]
    };
  }

  get workflowChartData(): ChartData<'bar'> {
    return {
      labels: this.workflowLabels,
      datasets: [
        {
          label: 'Completed', data: this.wfCompletedData,
          backgroundColor: 'rgba(102,187,106,0.7)', borderColor: '#66bb6a', borderWidth: 1,
        },
        {
          label: 'Failed', data: this.wfFailedData,
          backgroundColor: 'rgba(255,82,82,0.7)', borderColor: '#ff5252', borderWidth: 1,
        },
      ]
    };
  }

  // ── Helpers ────────────────────────────────────────────────────────────

  private toDateStr(d: Date): string {
    return d.toISOString().slice(0, 10);
  }
}

/**
 * Format a backend-side hourly bucket into a compact x-axis label
 * (e.g. "Apr 18, 14:00").
 *
 * Bug this fixes: the original call
 *
 *   toLocaleDateString(undefined, { month, day, hour })
 *
 * silently drops the `hour` option — `toLocaleDateString` only
 * renders date fields. Every bucket collapsed to the same "Apr 18"
 * string, so multi-hour windows showed one x-axis tick and
 * sub-hour windows showed a naked date with no time context.
 * `toLocaleString` honours the `hour` field, and we pin `hour12:
 * false` so the label is unambiguous in 24-hour locales too.
 *
 * Exported for unit testing; callers use it to project a sorted
 * list of ISO hours into the displayed label array.
 */
export function formatHourLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    month:   'short',
    day:     'numeric',
    hour:    '2-digit',
    minute:  '2-digit',
    hour12:  false,
  });
}
