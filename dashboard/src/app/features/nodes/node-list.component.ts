// src/app/features/nodes/node-list.component.ts
//
// NodeListComponent polls GET /nodes every 10 s (configurable via environment.tokenRefreshMs).
// Displays a Material table with health badge, last-seen, running-job count, CPU %, mem %.

import { Component, OnInit, OnDestroy } from '@angular/core';
import { CommonModule } from '@angular/common';
import { MatTableModule } from '@angular/material/table';
import { MatTooltipModule } from '@angular/material/tooltip';
import { interval, Subscription, startWith, switchMap } from 'rxjs';

import { ApiService } from '../../core/services/api.service';
import { Node } from '../../shared/models';
import { environment } from '../../../environments/environment';

@Component({
  selector: 'app-node-list',
  standalone: true,
  imports: [CommonModule, MatTableModule, MatTooltipModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">NODES</h1>
      <p class="page-sub">{{ nodes.length }} registered &nbsp;·&nbsp; {{ healthyCount }} healthy</p>
    </div>
    <div class="refresh-indicator" [class.refreshing]="refreshing">
      <span class="material-icons">sync</span>
      <span>{{ refreshing ? 'REFRESHING' : 'AUTO ×10s' }}</span>
    </div>
  </header>

  <!-- Error banner -->
  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <!-- Loading skeleton -->
  <div class="skeleton-rows" *ngIf="loading && nodes.length === 0">
    <div class="skeleton-row" *ngFor="let _ of [0,1,2]"></div>
  </div>

  <!-- Table -->
  <div class="table-wrap" *ngIf="nodes.length > 0">
    <table mat-table [dataSource]="nodes">

      <!-- Status -->
      <ng-container matColumnDef="status">
        <th mat-header-cell *matHeaderCellDef>STATUS</th>
        <td mat-cell *matCellDef="let n">
          <span class="badge" [class]="n.healthy ? 'badge-healthy' : 'badge-unhealthy'">
            {{ n.healthy ? 'HEALTHY' : 'UNHEALTHY' }}
          </span>
        </td>
      </ng-container>

      <!-- Node ID -->
      <ng-container matColumnDef="node_id">
        <th mat-header-cell *matHeaderCellDef>NODE ID</th>
        <td mat-cell *matCellDef="let n">
          <span class="mono-id">{{ n.node_id }}</span>
        </td>
      </ng-container>

      <!-- Address -->
      <ng-container matColumnDef="address">
        <th mat-header-cell *matHeaderCellDef>ADDRESS</th>
        <td mat-cell *matCellDef="let n">{{ n.address }}</td>
      </ng-container>

      <!-- Running jobs -->
      <ng-container matColumnDef="running_jobs">
        <th mat-header-cell *matHeaderCellDef>RUNNING</th>
        <td mat-cell *matCellDef="let n">
          <span [class.text-accent]="n.running_jobs > 0">{{ n.running_jobs }}</span>
        </td>
      </ng-container>

      <!-- CPU -->
      <ng-container matColumnDef="cpu_percent">
        <th mat-header-cell *matHeaderCellDef>CPU %</th>
        <td mat-cell *matCellDef="let n">
          <div class="mini-bar-wrap" [matTooltip]="n.cpu_percent.toFixed(1) + '%'">
            <div class="mini-bar" [style.width.%]="n.cpu_percent"
              [class.mini-bar--warn]="n.cpu_percent > 70"
              [class.mini-bar--crit]="n.cpu_percent > 90">
            </div>
            <span>{{ n.cpu_percent.toFixed(0) }}%</span>
          </div>
        </td>
      </ng-container>

      <!-- Memory -->
      <ng-container matColumnDef="mem_percent">
        <th mat-header-cell *matHeaderCellDef>MEM %</th>
        <td mat-cell *matCellDef="let n">
          <div class="mini-bar-wrap" [matTooltip]="n.mem_percent.toFixed(1) + '%'">
            <div class="mini-bar" [style.width.%]="n.mem_percent"
              [class.mini-bar--warn]="n.mem_percent > 70"
              [class.mini-bar--crit]="n.mem_percent > 90">
            </div>
            <span>{{ n.mem_percent.toFixed(0) }}%</span>
          </div>
        </td>
      </ng-container>

      <!-- Last seen -->
      <ng-container matColumnDef="last_seen">
        <th mat-header-cell *matHeaderCellDef>LAST SEEN</th>
        <td mat-cell *matCellDef="let n">
          <span [class.text-error]="!n.healthy">{{ relativeTime(n.last_seen) }}</span>
        </td>
      </ng-container>

      <!-- Registered at -->
      <ng-container matColumnDef="registered_at">
        <th mat-header-cell *matHeaderCellDef>REGISTERED</th>
        <td mat-cell *matCellDef="let n">{{ n.registered_at | date:'yyyy-MM-dd HH:mm' }}</td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="cols"></tr>
      <tr mat-row *matRowDef="let row; columns: cols;"></tr>
    </table>
  </div>

  <p class="empty-state" *ngIf="!loading && nodes.length === 0 && !error">
    No nodes registered yet. Start a node agent to see it here.
  </p>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      margin-bottom: 24px;
    }

    .page-title {
      font-family: var(--font-ui);
      font-size: 20px;
      letter-spacing: 0.1em;
      color: #e8edf2;
      margin: 0 0 4px;
    }

    .page-sub {
      font-size: 11px;
      color: var(--color-muted);
      margin: 0;
    }

    .refresh-indicator {
      display: flex;
      align-items: center;
      gap: 6px;
      font-size: 10px;
      letter-spacing: 0.08em;
      color: var(--color-muted);
      padding: 6px 10px;
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);

      .material-icons { font-size: 14px; transition: transform 0.5s; }

      &.refreshing {
        color: var(--color-accent);
        border-color: rgba(192,132,252,0.3);
        .material-icons { animation: spin 0.6s linear infinite; }
      }
    }

    @keyframes spin { to { transform: rotate(360deg); } }

    .error-banner {
      display: flex;
      align-items: center;
      gap: 8px;
      background: rgba(255,82,82,0.08);
      border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm);
      color: var(--color-error);
      font-size: 12px;
      padding: 10px 14px;
      margin-bottom: 16px;
    }

    .skeleton-row {
      height: 44px;
      background: var(--color-surface);
      border-radius: var(--radius-sm);
      margin-bottom: 2px;
      animation: shimmer 1.4s infinite;
      background: linear-gradient(90deg, var(--color-surface) 25%, var(--color-surface-2) 50%, var(--color-surface) 75%);
      background-size: 200% 100%;
    }

    @keyframes shimmer { to { background-position: -200% 0; } }

    .table-wrap {
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      overflow: hidden;
    }

    .mono-id {
      font-size: 11px;
      color: var(--color-info);
      letter-spacing: 0.02em;
    }

    .mini-bar-wrap {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 11px;
    }

    .mini-bar {
      height: 4px;
      min-width: 2px;
      max-width: 60px;
      width: 60px;
      border-radius: 2px;
      background: var(--color-accent);
      transition: width 0.3s;
      position: relative;

      &::before {
        content: '';
        position: absolute;
        inset: 0;
        background: var(--color-accent);
        border-radius: 2px;
        opacity: 0.3;
        width: 60px;
      }

    }

    .mini-bar--warn { background: var(--color-warning); }
    .mini-bar--crit { background: var(--color-error); }

    .empty-state {
      text-align: center;
      color: var(--color-muted);
      font-size: 12px;
      margin-top: 60px;
    }
  `]
})
export class NodeListComponent implements OnInit, OnDestroy {

  nodes:        Node[] = [];
  loading       = true;
  refreshing    = false;
  error         = '';
  readonly cols = ['status','node_id','address','running_jobs','cpu_percent','mem_percent','last_seen','registered_at'];

  get healthyCount(): number { return this.nodes.filter(n => n.healthy).length; }

  private sub?: Subscription;

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    this.sub = interval(environment.tokenRefreshMs).pipe(
      startWith(0),
      switchMap(() => {
        this.refreshing = true;
        return this.api.getNodes();
      })
    ).subscribe({
      next: nodes => {
        this.nodes     = nodes;
        this.loading   = false;
        this.refreshing = false;
        this.error     = '';
      },
      error: err => {
        this.loading    = false;
        this.refreshing = false;
        console.error('Failed to load nodes:', err);
        this.error      = 'Failed to load nodes. Please try again or contact your administrator.';
      }
    });
  }

  ngOnDestroy(): void { this.sub?.unsubscribe(); }

  relativeTime(isoStr: string): string {
    const diff = Date.now() - new Date(isoStr).getTime();
    const s    = Math.floor(diff / 1000);
    if (s < 5)    return 'just now';
    if (s < 60)   return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    return `${Math.floor(s/3600)}h ago`;
  }
}
