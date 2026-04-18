// src/app/features/ml/ml-pipelines.component.ts
//
// Pipelines list view — shows workflows with a "View DAG" link.
// The server-side filter "only workflows that produced a registered
// model" would require walking every workflow's lineage; the
// dashboard currently shows the full workflow list and lets the
// operator click into each one. A future enhancement can add a
// coordinator-side `GET /api/workflows?produced_model=true`
// endpoint to narrow the list cheaply.

import { Component, OnDestroy, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';
import { Subscription, interval, startWith, switchMap } from 'rxjs';

import { ApiService } from '../../core/services/api.service';
import { Workflow, WorkflowJob } from '../../shared/models';
import { environment } from '../../../environments/environment';

@Component({
  selector: 'app-ml-pipelines',
  standalone: true,
  imports: [CommonModule, RouterLink, MatTableModule, MatPaginatorModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">ML · PIPELINES</h1>
      <p class="page-sub">Workflows with a DAG lineage view — artifact edges show which outputs feed which downstream jobs</p>
    </div>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="waiting" *ngIf="loading && !error">
    <span class="material-icons spin">sync</span>
    Loading workflows…
  </div>

  <div *ngIf="!loading && !error">
    <div class="empty-state" *ngIf="workflows.length === 0">
      No workflows have been submitted yet.
    </div>

    <table mat-table [dataSource]="workflows" *ngIf="workflows.length > 0" class="ml-table">
      <ng-container matColumnDef="id">
        <th mat-header-cell *matHeaderCellDef>ID</th>
        <td mat-cell *matCellDef="let w" class="mono">{{ w.id }}</td>
      </ng-container>
      <ng-container matColumnDef="name">
        <th mat-header-cell *matHeaderCellDef>NAME</th>
        <td mat-cell *matCellDef="let w">{{ w.name || '—' }}</td>
      </ng-container>
      <ng-container matColumnDef="status">
        <th mat-header-cell *matHeaderCellDef>STATUS</th>
        <td mat-cell *matCellDef="let w">
          <span class="chip" [class]="statusChipClass(w.status)">{{ w.status }}</span>
        </td>
      </ng-container>
      <ng-container matColumnDef="jobs">
        <th mat-header-cell *matHeaderCellDef class="num">JOBS</th>
        <td mat-cell *matCellDef="let w" class="num jobs-progress">
          <span class="jobs-done">{{ countCompleted(w.jobs) }}</span>
          <span class="jobs-sep">/</span>
          <span class="jobs-total">{{ w.jobs?.length ?? 0 }}</span>
        </td>
      </ng-container>
      <ng-container matColumnDef="created_at">
        <th mat-header-cell *matHeaderCellDef>SUBMITTED</th>
        <td mat-cell *matCellDef="let w">{{ formatDate(w.created_at) }}</td>
      </ng-container>
      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let w">
          <a [routerLink]="['/ml/pipelines', w.id]"
             class="btn-link"
             [attr.aria-label]="'View DAG for ' + w.id">
            <span class="material-icons" style="font-size:14px;vertical-align:middle">account_tree</span>
            View DAG
          </a>
        </td>
      </ng-container>
      <tr mat-header-row *matHeaderRowDef="columns"></tr>
      <tr mat-row *matRowDef="let row; columns: columns;"></tr>
    </table>

    <mat-paginator *ngIf="total > pageSize"
                   [length]="total"
                   [pageSize]="pageSize"
                   [pageIndex]="page"
                   [pageSizeOptions]="[10, 20, 50]"
                   (page)="onPage($event)">
    </mat-paginator>
  </div>
</div>
  `,
  styleUrls: ['./ml-shared.scss'],
  styles: [`
    .btn-link {
      color: var(--color-accent);
      text-decoration: none;
      font-size: 12px;
      display: inline-flex;
      align-items: center;
      gap: 4px;
    }
    .btn-link:hover { text-decoration: underline; }

    .jobs-progress {
      font-variant-numeric: tabular-nums;
      font-family: var(--font-mono);
    }
    .jobs-done  { color: var(--color-completed); font-weight: 600; }
    .jobs-sep   { color: var(--color-muted); margin: 0 2px; }
    .jobs-total { color: var(--color-muted); }

    /* Status-chip palette mirrors ml-pipeline-detail so the list
       row and the DAG cards on the detail view share colours: a
       workflow transitioning through states reads identically
       wherever it is rendered. */
    .chip.chip-pending     { background: rgba(255, 171, 64, 0.12); color: var(--color-pending); }
    .chip.chip-scheduled   { background: rgba(255, 171, 64, 0.12); color: var(--color-pending); }
    .chip.chip-dispatching { background: rgba(129, 140, 248, 0.15); color: var(--color-dispatching); }
    .chip.chip-running     { background: rgba(68, 160, 200, 0.18); color: #68b4d4; }
    .chip.chip-completed   { background: rgba(68, 181, 95, 0.15); color: var(--color-completed); }
    .chip.chip-failed      { background: rgba(244, 67, 54, 0.15); color: var(--color-error); }
    .chip.chip-default     { background: var(--color-surface-2); color: var(--color-muted); }
  `],
})
export class MlPipelinesComponent implements OnInit, OnDestroy {
  workflows: Workflow[] = [];
  total    = 0;
  page     = 0;
  pageSize = 20;
  loading  = true;
  error    = '';

  columns = ['id', 'name', 'status', 'jobs', 'created_at', 'actions'];

  private sub?: Subscription;

  constructor(private api: ApiService) {}

  /**
   * Poll `GET /workflows` on the same cadence as the Nodes list
   * (environment.tokenRefreshMs — 5 s dev, 10 s prod). The Pipelines
   * list used to fetch once on init, so a workflow progressing from
   * pending → running → completed looked static until the user
   * manually re-navigated. Polling keeps the list fresh for both
   * the live walkthrough (docs/e2e-mnist-run.mp4) and day-to-day
   * operator use.
   *
   * switchMap cancels an in-flight request if the next tick fires
   * first (protects against slow coordinator responses stacking
   * up). Subscription is stored and unsubscribed on destroy so
   * navigating away stops the poll.
   */
  ngOnInit(): void {
    this.sub = interval(environment.tokenRefreshMs).pipe(
      startWith(0),
      switchMap(() => this.api.getWorkflows(this.page, this.pageSize)),
    ).subscribe({
      next: resp => {
        this.workflows = resp.workflows ?? [];
        this.total     = resp.total ?? this.workflows.length;
        this.loading   = false;
        this.error     = '';
      },
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Failed to load workflows';
        this.loading = false;
      },
    });
  }

  ngOnDestroy(): void { this.sub?.unsubscribe(); }

  /**
   * Restart the poll from the current (page, pageSize). Pagination
   * changes and the walkthrough-spec first load both need a fresh
   * stream rather than waiting up to tokenRefreshMs for the next
   * tick — that's what startWith(0) gives us on resubscribe.
   * Public because unit tests drive it to simulate page-size
   * changes + re-fetches without waiting on the real interval.
   */
  reload(): void {
    this.sub?.unsubscribe();
    this.ngOnInit();
  }

  onPage(e: PageEvent): void {
    this.page     = e.pageIndex;
    this.pageSize = e.pageSize;
    this.reload();
  }

  /**
   * Count jobs already in a terminal-success state so the list can
   * render "2/4" instead of just "4". The running / pending /
   * dispatching jobs don't count — only `completed`. Skipped jobs
   * (a finished branch under a conditional workflow) also count as
   * done: the workflow won't re-run them, so from the operator's
   * progress-bar perspective they're no longer outstanding.
   */
  countCompleted(jobs?: WorkflowJob[]): number {
    if (!jobs) return 0;
    return jobs.filter(j => {
      const s = (j.job_status ?? '').toLowerCase();
      return s === 'completed' || s === 'skipped';
    }).length;
  }

  statusChipClass(status: string): string {
    const s = (status || '').toLowerCase();
    if (s === 'running')      return 'chip chip-running';
    if (s === 'completed')    return 'chip chip-completed';
    if (s === 'failed' || s === 'cancelled' || s === 'timeout' || s === 'lost') {
      return 'chip chip-failed';
    }
    if (s === 'dispatching')  return 'chip chip-dispatching';
    if (s === 'scheduled')    return 'chip chip-scheduled';
    if (s === 'pending')      return 'chip chip-pending';
    return 'chip chip-default';
  }

  formatDate(s: string): string {
    if (!s) return '—';
    const d = new Date(s);
    return isNaN(d.getTime()) ? s : d.toLocaleString();
  }
}
