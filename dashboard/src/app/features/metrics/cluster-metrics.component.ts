// src/app/features/metrics/cluster-metrics.component.ts
//
// Subscribes to GET /ws/metrics (pushes snapshots every 5 s).
// Renders summary KPI cards and a rolling time-series line chart
// showing healthy nodes and running jobs over the last 60 samples.

import { Component, OnInit, OnDestroy } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Subscription } from 'rxjs';
import { BaseChartDirective } from 'ng2-charts';
import { ChartConfiguration, ChartData } from 'chart.js';
import {
  Chart, LineElement, PointElement, LineController,
  CategoryScale, LinearScale, Filler, Tooltip, Legend
} from 'chart.js';

import { WebSocketService } from '../../core/services/websocket.service';
import { ClusterMetrics } from '../../shared/models';

// Register Chart.js modules (standalone usage)
Chart.register(LineElement, PointElement, LineController, CategoryScale, LinearScale, Filler, Tooltip, Legend);

const MAX_POINTS = 60;

@Component({
  selector: 'app-cluster-metrics',
  standalone: true,
  imports: [CommonModule, BaseChartDirective],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">METRICS</h1>
      <p class="page-sub">Live cluster metrics via WebSocket · 5 s interval</p>
    </div>
    <div class="ws-indicator" [class.ws-indicator--live]="connected">
      <span class="ws-dot"></span>
      {{ connected ? 'WS CONNECTED' : 'CONNECTING...' }}
    </div>
  </header>

  <!-- Error -->
  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <!-- KPI Cards -->
  <div class="kpi-grid" *ngIf="latest">
    <div class="kpi-card">
      <span class="kpi-label">TOTAL NODES</span>
      <span class="kpi-value">{{ latest.total_nodes }}</span>
    </div>
    <div class="kpi-card kpi-card--accent">
      <span class="kpi-label">HEALTHY NODES</span>
      <span class="kpi-value text-accent">{{ latest.healthy_nodes }}</span>
      <span class="kpi-sub">{{ nodeHealthPct }}% healthy</span>
    </div>
    <div class="kpi-card">
      <span class="kpi-label">TOTAL JOBS</span>
      <span class="kpi-value">{{ latest.total_jobs }}</span>
    </div>
    <div class="kpi-card kpi-card--running">
      <span class="kpi-label">RUNNING</span>
      <span class="kpi-value" style="color:var(--color-running)">{{ latest.running_jobs }}</span>
    </div>
    <div class="kpi-card">
      <span class="kpi-label">PENDING</span>
      <span class="kpi-value" style="color:var(--color-pending)">{{ latest.pending_jobs }}</span>
    </div>
    <div class="kpi-card">
      <span class="kpi-label">COMPLETED</span>
      <span class="kpi-value" style="color:var(--color-completed)">{{ latest.completed_jobs }}</span>
    </div>
    <div class="kpi-card">
      <span class="kpi-label">FAILED</span>
      <span class="kpi-value" style="color:var(--color-error)">{{ latest.failed_jobs }}</span>
    </div>
  </div>

  <!-- Time-series chart -->
  <div class="chart-panel" *ngIf="labels.length > 0">
    <div class="chart-panel__header">
      <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">timeline</span>
      CLUSTER ACTIVITY — LAST {{ labels.length }} SNAPSHOTS
    </div>
    <div class="chart-wrap">
      <canvas baseChart
        [data]="chartData"
        [options]="chartOptions"
        type="line"
      ></canvas>
    </div>
  </div>

  <div class="waiting" *ngIf="!latest && !error">
    <span class="material-icons spin">sync</span>
    Waiting for first metrics snapshot...
  </div>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex; align-items: flex-start; justify-content: space-between;
      margin-bottom: 24px; gap: 16px;
    }
    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 11px; color: var(--color-muted); margin: 0; }

    .ws-indicator {
      display: flex; align-items: center; gap: 7px;
      font-size: 10px; letter-spacing: 0.08em;
      color: var(--color-muted);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      padding: 6px 12px;
    }
    .ws-dot {
      width: 7px; height: 7px; border-radius: 50%;
      background: var(--color-muted);
    }
    .ws-indicator--live {
      color: var(--color-accent); border-color: rgba(192,132,252,0.3);
      .ws-dot { background: var(--color-accent); animation: blink 1.2s infinite; }
    }
    @keyframes blink { 0%,100%{opacity:1} 50%{opacity:0.2} }

    .error-banner {
      display:flex;align-items:center;gap:8px;
      background:rgba(255,82,82,0.08);border:1px solid rgba(255,82,82,0.3);
      border-radius:var(--radius-sm);color:var(--color-error);
      font-size:12px;padding:10px 14px;margin-bottom:16px;
    }

    /* KPI grid */
    .kpi-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
      gap: 12px;
      margin-bottom: 24px;
    }

    .kpi-card {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      padding: 16px;
      display: flex;
      flex-direction: column;
      gap: 4px;

    }

    .kpi-card--accent  { border-top: 2px solid var(--color-accent); }
    .kpi-card--running { border-top: 2px solid var(--color-running); }

    .kpi-label {
      font-size: 9px;
      letter-spacing: 0.12em;
      color: var(--color-accent);
    }

    .kpi-value {
      font-family: var(--font-ui);
      font-size: 28px;
      font-weight: 700;
      color: #e8edf2;
      line-height: 1;
    }

    .kpi-sub { font-size: 10px; color: var(--color-muted); }

    /* Chart */
    .chart-panel {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
    }

    .chart-panel__header {
      display: flex; align-items: center; gap: 8px;
      padding: 12px 16px;
      background: var(--color-surface-2);
      border-bottom: 1px solid var(--color-border);
      font-size: 11px; letter-spacing: 0.07em; color: #8896aa;
    }

    .chart-wrap { padding: 16px; height: 280px; }

    .waiting {
      display: flex; align-items: center; gap: 8px;
      color: var(--color-muted); font-size: 12px; margin-top: 60px;
      justify-content: center;
    }

    .spin { animation: spin 0.8s linear infinite; }
    @keyframes spin { to { transform: rotate(360deg); } }
  `]
})
export class ClusterMetricsComponent implements OnInit, OnDestroy {

  latest:    ClusterMetrics | null = null;
  connected  = false;
  error      = '';

  labels:       string[]   = [];
  healthyData:  number[]   = [];
  runningData:  number[]   = [];

  get nodeHealthPct(): string {
    if (!this.latest || this.latest.total_nodes === 0) return '0';
    return ((this.latest.healthy_nodes / this.latest.total_nodes) * 100).toFixed(0);
  }

  readonly chartOptions: ChartConfiguration['options'] = {
    responsive: true,
    maintainAspectRatio: false,
    animation: { duration: 200 },
    scales: {
      x: {
        ticks: { color: '#4a5568', font: { family: "'JetBrains Mono'", size: 10 } },
        grid:  { color: 'rgba(42,48,64,0.6)' },
      },
      y: {
        ticks: { color: '#4a5568', font: { family: "'JetBrains Mono'", size: 10 }, stepSize: 1 },
        grid:  { color: 'rgba(42,48,64,0.6)' },
        min: 0,
      }
    },
    plugins: {
      legend: {
        labels: { color: '#8896aa', font: { family: "'JetBrains Mono'", size: 11 }, boxWidth: 12 }
      },
      tooltip: {
        backgroundColor: '#111418',
        borderColor: '#2a3040',
        borderWidth: 1,
        titleFont: { family: "'JetBrains Mono'" },
        bodyFont:  { family: "'JetBrains Mono'" },
      }
    }
  };

  get chartData(): ChartData<'line'> {
    return {
      labels: this.labels,
      datasets: [
        {
          label: 'Healthy Nodes',
          data: this.healthyData,
          borderColor: '#c084fc',
          backgroundColor: 'rgba(192,132,252,0.08)',
          fill: true,
          tension: 0.3,
          pointRadius: 2,
          pointBackgroundColor: '#c084fc',
        },
        {
          label: 'Running Jobs',
          data: this.runningData,
          borderColor: '#40c4ff',
          backgroundColor: 'rgba(64,196,255,0.06)',
          fill: true,
          tension: 0.3,
          pointRadius: 2,
          pointBackgroundColor: '#40c4ff',
        },
      ]
    };
  }

  private sub?: Subscription;

  constructor(private ws: WebSocketService) {}

  ngOnInit(): void {
    this.sub = this.ws.metrics().subscribe({
      next: snapshot => {
        this.latest    = snapshot;
        this.connected = true;
        this.error     = '';
        this.addPoint(snapshot);
      },
      error: err => {
        this.connected = false;
        console.error('Metrics WebSocket error:', err);
        this.error     = 'Metrics connection lost. Retrying automatically\u2026';
      },
      complete: () => { this.connected = false; }
    });
  }

  ngOnDestroy(): void { this.sub?.unsubscribe(); }

  private addPoint(m: ClusterMetrics): void {
    const ts = new Date(m.timestamp).toLocaleTimeString();
    this.labels.push(ts);
    this.healthyData.push(m.healthy_nodes);
    this.runningData.push(m.running_jobs);

    // Rolling window
    if (this.labels.length > MAX_POINTS) {
      this.labels.shift();
      this.healthyData.shift();
      this.runningData.shift();
    }
  }
}
