// src/app/features/audit/audit-log.component.ts
import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';

import { ApiService } from '../../core/services/api.service';
import { AuditEvent, AuditEventType } from '../../shared/models';

@Component({
  selector: 'app-audit-log',
  standalone: true,
  imports: [CommonModule, FormsModule, MatTableModule, MatPaginatorModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">AUDIT LOG</h1>
      <p class="page-sub">{{ total }} events · read-only</p>
    </div>

    <div class="filter-row">
      <label class="filter-label">TYPE</label>
      <select class="status-select" [(ngModel)]="typeFilter" (ngModelChange)="onFilterChange()">
        <option value="">ALL EVENTS</option>
        <option *ngFor="let t of eventTypes" [value]="t">{{ t | uppercase }}</option>
      </select>
      <button class="refresh-btn" (click)="load()">
        <span class="material-icons">refresh</span>
      </button>
    </div>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="table-wrap">
    <table mat-table [dataSource]="events">

      <ng-container matColumnDef="timestamp">
        <th mat-header-cell *matHeaderCellDef>TIMESTAMP</th>
        <td mat-cell *matCellDef="let e">{{ e.timestamp | date:'yyyy-MM-dd HH:mm:ss' }}</td>
      </ng-container>

      <ng-container matColumnDef="type">
        <th mat-header-cell *matHeaderCellDef>EVENT TYPE</th>
        <td mat-cell *matCellDef="let e">
          <span class="event-type" [ngClass]="eventClass(e.type)">
            {{ e.type | uppercase }}
          </span>
        </td>
      </ng-container>

      <ng-container matColumnDef="actor">
        <th mat-header-cell *matHeaderCellDef>ACTOR</th>
        <td mat-cell *matCellDef="let e">
          <!-- Feature 35 — if the event carries a typed Principal,
               render the Kind as a small pill before the display
               name. Falls back to the legacy bare-string actor for
               pre-feature-35 events. -->
          <span *ngIf="e.principal_kind" class="principal-pill"
                [ngClass]="'principal-pill--' + e.principal_kind"
                [title]="e.principal || ''">
            {{ e.principal_kind }}
          </span>
          <span class="actor-text">{{ e.actor || '—' }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="target_id">
        <th mat-header-cell *matHeaderCellDef>TARGET ID</th>
        <td mat-cell *matCellDef="let e">
          <span class="text-info" style="font-size:11px">{{ e.target_id || '—' }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="message">
        <th mat-header-cell *matHeaderCellDef>MESSAGE</th>
        <td mat-cell *matCellDef="let e">
          <span class="msg-cell">{{ e.message }}</span>
        </td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="cols"></tr>
      <tr mat-row *matRowDef="let row; columns: cols;"></tr>
    </table>

    <mat-paginator
      [length]="total"
      [pageSize]="pageSize"
      [pageSizeOptions]="[25, 50, 100]"
      (page)="onPage($event)"
    ></mat-paginator>
  </div>

  <p class="empty-state" *ngIf="!loading && events.length === 0 && !error">
    No audit events found.
  </p>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex; align-items: flex-start; justify-content: space-between;
      margin-bottom: 20px; flex-wrap: wrap; gap: 16px;
    }
    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 11px; color: var(--color-muted); margin: 0; }

    .filter-row { display: flex; align-items: center; gap: 10px; }
    .filter-label { font-size: 10px; letter-spacing: 0.1em; color: var(--color-accent); }

    .status-select {
      background: var(--color-surface-2); border: 1px solid var(--color-border);
      border-radius: var(--radius-sm); color: #c8d0dc;
      font-family: var(--font-mono); font-size: 11px; padding: 6px 10px;
      outline: none; cursor: pointer;
      &:focus { border-color: var(--color-accent-dim); }
    }

    .refresh-btn {
      background: var(--color-surface-2); border: 1px solid var(--color-border);
      border-radius: var(--radius-sm); color: #c8d0dc; padding: 5px 8px;
      cursor: pointer; display: flex; align-items: center;
      transition: border-color 0.15s, color 0.15s;
      .material-icons { font-size: 16px; }
      &:hover { border-color: var(--color-accent-dim); color: var(--color-accent); }
    }

    .error-banner {
      display: flex; align-items: center; gap: 8px;
      background: rgba(255,82,82,0.08); border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm); color: var(--color-error);
      font-size: 12px; padding: 10px 14px; margin-bottom: 16px;
    }

    .table-wrap { border: 1px solid var(--color-border); border-radius: var(--radius-sm); overflow: hidden; }

    .event-type {
      font-size: 10px;
      letter-spacing: 0.07em;
      padding: 2px 7px;
      border-radius: var(--radius-sm);

      &.evt-job         { color: var(--color-info);    background: rgba(64,196,255,0.1);  }
      &.evt-node        { color: var(--color-accent);  background: rgba(192,132,252,0.1); }
      &.evt-auth        { color: var(--color-warning); background: rgba(255,171,64,0.1);  }
      &.evt-rate        { color: var(--color-warning); background: rgba(255,171,64,0.1);  }
      &.evt-coordinator { color: var(--color-muted);   background: rgba(136,150,170,0.1); }
      &.evt-security    {
        color: var(--color-error);
        background: rgba(255,82,82,0.12);
        border: 1px solid rgba(255,82,82,0.3);
        font-weight: 600;
      }
    }

    /* Feature 35 — principal-kind pill shown before the actor
       display name on each audit row. Lets a reviewer filter
       visually on Kind without parsing the ID prefix. */
    .principal-pill {
      display: inline-block;
      margin-right: 6px;
      padding: 1px 6px;
      font-size: 9px;
      letter-spacing: 0.08em;
      font-weight: 600;
      border-radius: 10px;
      text-transform: uppercase;
    }
    .principal-pill--user      { color: var(--color-info);    background: rgba(64,196,255,0.12); }
    .principal-pill--operator  { color: #7eb6f0;              background: rgba(100,170,240,0.15); }
    .principal-pill--node      { color: var(--color-accent);  background: rgba(192,132,252,0.12); }
    .principal-pill--service   { color: var(--color-muted);   background: rgba(136,150,170,0.12); }
    .principal-pill--job       { color: #d9a649;              background: rgba(217,166,73,0.15); }
    .principal-pill--anonymous { color: var(--color-error);   background: rgba(255,82,82,0.12); }
    .actor-text                { font-family: var(--font-mono, monospace); font-size: 12px; }

    .msg-cell {
      display: block; max-width: 400px;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font-size: 12px;
    }

    .empty-state { text-align: center; color: var(--color-muted); font-size: 12px; margin-top: 60px; }
  `]
})
export class AuditLogComponent implements OnInit {

  events:      AuditEvent[] = [];
  total        = 0;
  loading      = false;
  error        = '';
  pageIndex    = 0;
  pageSize     = 50;
  typeFilter   = '';

  readonly cols        = ['timestamp','type','actor','target_id','message'];
  // Must stay in sync with EventXxx constants in internal/audit/logger.go.
  readonly eventTypes: AuditEventType[] = [
    'job_submit', 'job_state_transition',
    'node_register', 'node_revoke',
    'security_violation', 'auth_failure', 'rate_limit_hit',
    'coordinator_start', 'coordinator_stop',
  ];

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.loading = true;
    this.api.getAudit(this.pageIndex, this.pageSize, this.typeFilter || undefined).subscribe({
      next: page => {
        this.events  = page.events;
        this.total   = page.total;
        this.loading = false;
        this.error   = '';
      },
      error: err => {
        this.loading = false;
        console.error('Failed to load audit log:', err);
        this.error   = 'Failed to load audit log. Please try again or contact your administrator.';
      }
    });
  }

  onPage(e: PageEvent): void {
    this.pageIndex = e.pageIndex;
    this.pageSize  = e.pageSize;
    this.load();
  }

  onFilterChange(): void {
    this.pageIndex = 0;
    this.load();
  }

  // Returns the CSS class for a given event type badge.
  // security_violation gets a dedicated red class; everything else is
  // derived from the first word of the underscore-separated type name.
  eventClass(type: string): string {
    if (type === 'security_violation') return 'evt-security';
    if (type === 'rate_limit_hit')     return 'evt-rate';
    return 'evt-' + type.split('_')[0];
  }
}
