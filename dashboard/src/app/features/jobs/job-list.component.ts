// src/app/features/jobs/job-list.component.ts
import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';
import { MatSelectModule } from '@angular/material/select';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatButtonModule } from '@angular/material/button';
import { MatTooltipModule } from '@angular/material/tooltip';

import { ApiService } from '../../core/services/api.service';
import { Job, JobStatus } from '../../shared/models';

@Component({
  selector: 'app-job-list',
  standalone: true,
  imports: [
    CommonModule, FormsModule, RouterLink,
    MatTableModule, MatPaginatorModule, MatSelectModule,
    MatFormFieldModule, MatButtonModule, MatTooltipModule,
  ],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">JOBS</h1>
      <p class="page-sub">{{ total }} total jobs</p>
    </div>

    <!-- Status filter -->
    <div class="filter-row">
      <label class="filter-label">FILTER</label>
      <select class="status-select" [(ngModel)]="statusFilter" (ngModelChange)="onFilterChange()">
        <option value="">ALL</option>
        <option *ngFor="let s of statuses" [value]="s">{{ s | uppercase }}</option>
      </select>

      <button class="refresh-btn" (click)="load()" [disabled]="loading">
        <span class="material-icons">refresh</span>
      </button>
    </div>
  </header>

  <!-- Error -->
  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <!-- Table -->
  <div class="table-wrap">
    <table mat-table [dataSource]="jobs">

      <ng-container matColumnDef="status">
        <th mat-header-cell *matHeaderCellDef>STATUS</th>
        <td mat-cell *matCellDef="let j">
          <span class="badge" [class]="'badge-' + j.status">{{ j.status | uppercase }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="id">
        <th mat-header-cell *matHeaderCellDef>JOB ID</th>
        <td mat-cell *matCellDef="let j">
          <a class="job-link" [routerLink]="['/jobs', j.id]">{{ j.id }}</a>
        </td>
      </ng-container>

      <ng-container matColumnDef="command">
        <th mat-header-cell *matHeaderCellDef>COMMAND</th>
        <td mat-cell *matCellDef="let j">
          <span class="cmd-cell">{{ j.command }} {{ j.args?.join(' ') }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="node_id">
        <th mat-header-cell *matHeaderCellDef>NODE</th>
        <td mat-cell *matCellDef="let j">
          <span class="text-info" style="font-size:11px">{{ j.node_id || '—' }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="runtime">
        <th mat-header-cell *matHeaderCellDef>RUNTIME</th>
        <td mat-cell *matCellDef="let j">
          <span class="badge" [class]="'badge-rt-' + (j.runtime || 'unknown')">{{ (j.runtime || '—') | uppercase }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="created_at">
        <th mat-header-cell *matHeaderCellDef>CREATED</th>
        <td mat-cell *matCellDef="let j">{{ j.created_at | date:'MM-dd HH:mm:ss' }}</td>
      </ng-container>

      <ng-container matColumnDef="finished_at">
        <th mat-header-cell *matHeaderCellDef>FINISHED</th>
        <td mat-cell *matCellDef="let j">{{ j.finished_at ? (j.finished_at | date:'MM-dd HH:mm:ss') : '—' }}</td>
      </ng-container>

      <ng-container matColumnDef="exit_code">
        <th mat-header-cell *matHeaderCellDef>EXIT</th>
        <td mat-cell *matCellDef="let j">
          <span [class.text-error]="j.exit_code !== 0 && j.exit_code != null">
            {{ j.exit_code ?? '—' }}
          </span>
        </td>
      </ng-container>

      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let j">
          <a class="detail-link" [routerLink]="['/jobs', j.id]" matTooltip="View details">
            <span class="material-icons">chevron_right</span>
          </a>
        </td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="cols"></tr>
      <tr mat-row *matRowDef="let row; columns: cols;" class="clickable-row"
          [routerLink]="['/jobs', row.id]"></tr>
    </table>

    <mat-paginator
      [length]="total"
      [pageSize]="pageSize"
      [pageSizeOptions]="[10, 25, 50]"
      (page)="onPage($event)"
    ></mat-paginator>
  </div>

  <p class="empty-state" *ngIf="!loading && jobs.length === 0 && !error">
    No jobs found{{ statusFilter ? ' with status "' + statusFilter + '"' : '' }}.
  </p>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      margin-bottom: 20px;
      flex-wrap: wrap;
      gap: 16px;
    }

    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 11px; color: var(--color-muted); margin: 0; }

    .filter-row {
      display: flex;
      align-items: center;
      gap: 10px;
    }

    .filter-label {
      font-size: 10px;
      letter-spacing: 0.1em;
      color: var(--color-accent);
    }

    .status-select {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #c8d0dc;
      font-family: var(--font-mono);
      font-size: 11px;
      padding: 6px 10px;
      outline: none;
      cursor: pointer;

      &:focus { border-color: var(--color-accent-dim); }
    }

    .refresh-btn {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #c8d0dc;
      padding: 5px 8px;
      cursor: pointer;
      display: flex;
      align-items: center;
      transition: border-color 0.15s, color 0.15s;

      .material-icons { font-size: 16px; }

      &:hover:not(:disabled) { border-color: var(--color-accent-dim); color: var(--color-accent); }
      &:disabled { opacity: 0.4; cursor: not-allowed; }
    }

    .error-banner {
      display: flex; align-items: center; gap: 8px;
      background: rgba(255,82,82,0.08); border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm); color: var(--color-error);
      font-size: 12px; padding: 10px 14px; margin-bottom: 16px;
    }

    .table-wrap { border: 1px solid var(--color-border); border-radius: var(--radius-sm); overflow: hidden; }

    .badge-rt-go   { font-size: 10px; letter-spacing: 0.07em; padding: 2px 7px; border-radius: var(--radius-sm); color: var(--color-info); background: rgba(64,196,255,0.1); }
    .badge-rt-rust { font-size: 10px; letter-spacing: 0.07em; padding: 2px 7px; border-radius: var(--radius-sm); color: #ff8a65; background: rgba(255,138,101,0.1); }
    .badge-rt-unknown { font-size: 10px; letter-spacing: 0.07em; padding: 2px 7px; border-radius: var(--radius-sm); color: var(--color-muted); background: rgba(136,150,170,0.08); }

    .job-link {
      color: var(--color-info);
      text-decoration: none;
      font-size: 11px;
      letter-spacing: 0.02em;
      &:hover { text-decoration: underline; }
    }

    .cmd-cell {
      font-size: 12px;
      color: #c8d0dc;
      max-width: 260px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      display: block;
    }

    .detail-link {
      color: var(--color-muted);
      display: flex;
      align-items: center;
      text-decoration: none;
      &:hover { color: var(--color-accent); }
      .material-icons { font-size: 18px; }
    }

    ::ng-deep .clickable-row { cursor: pointer; }

    .empty-state {
      text-align: center;
      color: var(--color-muted);
      font-size: 12px;
      margin-top: 60px;
    }
  `]
})
export class JobListComponent implements OnInit {

  jobs:        Job[]  = [];
  total        = 0;
  loading      = false;
  error        = '';
  pageIndex    = 0;
  pageSize     = 25;
  statusFilter = '';

  readonly cols     = ['status','id','command','node_id','runtime','created_at','finished_at','exit_code','actions'];
  readonly statuses: JobStatus[] = ['pending','dispatching','running','completed','failed','timeout','lost','retrying'];

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.loading = true;
    this.api.getJobs(this.pageIndex, this.pageSize, this.statusFilter || undefined).subscribe({
      next: page => {
        this.jobs    = page.jobs;
        this.total   = page.total;
        this.loading = false;
        this.error   = '';
      },
      error: err => {
        this.loading = false;
        console.error('Failed to load jobs:', err);
        this.error   = 'Failed to load jobs. Please try again or contact your administrator.';
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
}
