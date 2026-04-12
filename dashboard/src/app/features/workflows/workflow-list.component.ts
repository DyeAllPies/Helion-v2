// src/app/features/workflows/workflow-list.component.ts
import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';
import { MatButtonModule } from '@angular/material/button';
import { MatTooltipModule } from '@angular/material/tooltip';

import { ApiService } from '../../core/services/api.service';
import { Workflow } from '../../shared/models';

@Component({
  selector: 'app-workflow-list',
  standalone: true,
  imports: [
    CommonModule, RouterLink,
    MatTableModule, MatPaginatorModule, MatButtonModule, MatTooltipModule,
  ],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">WORKFLOWS</h1>
      <p class="page-sub">{{ total }} total workflows</p>
    </div>
    <button class="refresh-btn" (click)="load()" [disabled]="loading">
      <span class="material-icons">refresh</span>
    </button>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="table-wrap">
    <table mat-table [dataSource]="workflows">

      <ng-container matColumnDef="status">
        <th mat-header-cell *matHeaderCellDef>STATUS</th>
        <td mat-cell *matCellDef="let w">
          <span class="badge" [class]="'badge-' + w.status">{{ w.status | uppercase }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="id">
        <th mat-header-cell *matHeaderCellDef>WORKFLOW ID</th>
        <td mat-cell *matCellDef="let w">
          <a class="wf-link" [routerLink]="['/workflows', w.id]">{{ w.id }}</a>
        </td>
      </ng-container>

      <ng-container matColumnDef="name">
        <th mat-header-cell *matHeaderCellDef>NAME</th>
        <td mat-cell *matCellDef="let w">
          <span class="name-cell">{{ w.name || '—' }}</span>
        </td>
      </ng-container>

      <ng-container matColumnDef="jobs">
        <th mat-header-cell *matHeaderCellDef>JOBS</th>
        <td mat-cell *matCellDef="let w">{{ w.jobs?.length || 0 }}</td>
      </ng-container>

      <ng-container matColumnDef="created_at">
        <th mat-header-cell *matHeaderCellDef>CREATED</th>
        <td mat-cell *matCellDef="let w">{{ w.created_at | date:'MM-dd HH:mm:ss' }}</td>
      </ng-container>

      <ng-container matColumnDef="finished_at">
        <th mat-header-cell *matHeaderCellDef>FINISHED</th>
        <td mat-cell *matCellDef="let w">{{ w.finished_at ? (w.finished_at | date:'MM-dd HH:mm:ss') : '—' }}</td>
      </ng-container>

      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let w">
          <a class="detail-link" [routerLink]="['/workflows', w.id]" matTooltip="View details">
            <span class="material-icons">chevron_right</span>
          </a>
        </td>
      </ng-container>

      <tr mat-header-row *matHeaderRowDef="cols"></tr>
      <tr mat-row *matRowDef="let row; columns: cols;" class="clickable-row"
          [routerLink]="['/workflows', row.id]"></tr>
    </table>

    <mat-paginator
      [length]="total"
      [pageSize]="pageSize"
      [pageSizeOptions]="[10, 25, 50]"
      (page)="onPage($event)"
    ></mat-paginator>
  </div>

  <p class="empty-state" *ngIf="!loading && workflows.length === 0 && !error">
    No workflows found.
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

    .wf-link {
      color: var(--color-info);
      text-decoration: none;
      font-size: 11px;
      letter-spacing: 0.02em;
      &:hover { text-decoration: underline; }
    }

    .name-cell {
      font-size: 12px;
      color: #c8d0dc;
      max-width: 200px;
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
export class WorkflowListComponent implements OnInit {

  workflows: Workflow[] = [];
  total     = 0;
  loading   = false;
  error     = '';
  pageIndex = 0;
  pageSize  = 20;

  readonly cols = ['status', 'id', 'name', 'jobs', 'created_at', 'finished_at', 'actions'];

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.load(); }

  load(): void {
    this.loading = true;
    this.api.getWorkflows(this.pageIndex, this.pageSize).subscribe({
      next: page => {
        this.workflows = page.workflows;
        this.total     = page.total;
        this.loading   = false;
        this.error     = '';
      },
      error: () => {
        this.loading = false;
        this.error   = 'Failed to load workflows.';
      }
    });
  }

  onPage(e: PageEvent): void {
    this.pageIndex = e.pageIndex;
    this.pageSize  = e.pageSize;
    this.load();
  }
}
