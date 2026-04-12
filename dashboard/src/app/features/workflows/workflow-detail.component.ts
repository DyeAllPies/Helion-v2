// src/app/features/workflows/workflow-detail.component.ts
import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { MatButtonModule } from '@angular/material/button';
import { MatTooltipModule } from '@angular/material/tooltip';

import { ApiService } from '../../core/services/api.service';
import { Workflow } from '../../shared/models';

@Component({
  selector: 'app-workflow-detail',
  standalone: true,
  imports: [CommonModule, RouterLink, MatButtonModule, MatTooltipModule],
  template: `
<div class="page" *ngIf="workflow">

  <!-- Breadcrumb -->
  <nav class="breadcrumb">
    <a routerLink="/workflows">Workflows</a>
    <span class="sep">/</span>
    <span>{{ workflow.id }}</span>
  </nav>

  <!-- Header -->
  <header class="detail-header">
    <div>
      <h1 class="detail-title">{{ workflow.name || workflow.id }}</h1>
      <span class="badge" [class]="'badge-' + workflow.status">{{ workflow.status | uppercase }}</span>
    </div>
    <div class="header-actions">
      <button class="refresh-btn" (click)="load()" matTooltip="Refresh">
        <span class="material-icons">refresh</span>
      </button>
      <button class="cancel-btn" *ngIf="workflow.status === 'running' || workflow.status === 'pending'"
              (click)="cancel()" matTooltip="Cancel workflow">
        <span class="material-icons">cancel</span> CANCEL
      </button>
    </div>
  </header>

  <!-- Metadata -->
  <div class="meta-grid">
    <div class="meta-item">
      <span class="meta-label">ID</span>
      <span class="meta-value">{{ workflow.id }}</span>
    </div>
    <div class="meta-item">
      <span class="meta-label">CREATED</span>
      <span class="meta-value">{{ workflow.created_at | date:'yyyy-MM-dd HH:mm:ss' }}</span>
    </div>
    <div class="meta-item" *ngIf="workflow.started_at">
      <span class="meta-label">STARTED</span>
      <span class="meta-value">{{ workflow.started_at | date:'yyyy-MM-dd HH:mm:ss' }}</span>
    </div>
    <div class="meta-item" *ngIf="workflow.finished_at">
      <span class="meta-label">FINISHED</span>
      <span class="meta-value">{{ workflow.finished_at | date:'yyyy-MM-dd HH:mm:ss' }}</span>
    </div>
    <div class="meta-item text-error" *ngIf="workflow.error">
      <span class="meta-label">ERROR</span>
      <span class="meta-value">{{ workflow.error }}</span>
    </div>
  </div>

  <!-- DAG visualization -->
  <h2 class="section-title">JOB DAG</h2>

  <div class="dag-container">
    <div class="job-card" *ngFor="let job of workflow.jobs" [class]="'job-card--' + (job.job_status || 'pending')">
      <div class="job-card__header">
        <span class="badge" [class]="'badge-' + (job.job_status || 'pending')">{{ (job.job_status || 'pending') | uppercase }}</span>
        <span class="job-card__name">{{ job.name }}</span>
      </div>
      <div class="job-card__body">
        <div class="job-card__cmd">{{ job.command }} {{ job.args?.join(' ') }}</div>
        <div class="job-card__deps" *ngIf="job.depends_on?.length">
          <span class="dep-label">DEPENDS ON:</span>
          <span class="dep-name" *ngFor="let dep of job.depends_on">{{ dep }}</span>
        </div>
        <div class="job-card__condition" *ngIf="job.condition && job.condition !== 'on_success'">
          <span class="condition-badge">{{ job.condition }}</span>
        </div>
      </div>
      <div class="job-card__footer" *ngIf="job.job_id">
        <a class="job-link" [routerLink]="['/jobs', job.job_id]">View job details</a>
      </div>
    </div>
  </div>

</div>

<div class="page" *ngIf="error">
  <div class="error-banner">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>
</div>

<div class="page" *ngIf="loading && !workflow && !error">
  <p class="loading-text">Loading workflow...</p>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .breadcrumb {
      font-size: 11px;
      color: var(--color-muted);
      margin-bottom: 16px;
      a { color: var(--color-info); text-decoration: none; &:hover { text-decoration: underline; } }
      .sep { margin: 0 6px; }
    }

    .detail-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 24px;
      flex-wrap: wrap;
      gap: 12px;
    }

    .detail-title {
      font-family: var(--font-ui);
      font-size: 20px;
      letter-spacing: 0.08em;
      color: #e8edf2;
      margin: 0 12px 0 0;
      display: inline;
    }

    .header-actions { display: flex; gap: 8px; align-items: center; }

    .refresh-btn {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #c8d0dc;
      padding: 5px 8px;
      cursor: pointer;
      display: flex;
      align-items: center;
      .material-icons { font-size: 16px; }
      &:hover { border-color: var(--color-accent-dim); color: var(--color-accent); }
    }

    .cancel-btn {
      background: rgba(255,82,82,0.1);
      border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm);
      color: var(--color-error);
      padding: 5px 12px;
      cursor: pointer;
      font-family: var(--font-mono);
      font-size: 11px;
      letter-spacing: 0.06em;
      display: flex;
      align-items: center;
      gap: 4px;
      .material-icons { font-size: 14px; }
      &:hover { background: rgba(255,82,82,0.18); }
    }

    .meta-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
      gap: 16px;
      margin-bottom: 32px;
      padding: 16px;
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
    }

    .meta-label {
      display: block;
      font-size: 9px;
      letter-spacing: 0.1em;
      color: var(--color-accent-dim);
      margin-bottom: 4px;
    }

    .meta-value {
      font-size: 12px;
      color: #c8d0dc;
      word-break: break-all;
    }

    .section-title {
      font-family: var(--font-ui);
      font-size: 13px;
      letter-spacing: 0.1em;
      color: var(--color-accent-dim);
      margin: 0 0 16px;
    }

    .dag-container {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: 12px;
    }

    .job-card {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      padding: 14px;
      transition: border-color 0.15s;

      &--completed { border-left: 3px solid var(--color-success); }
      &--failed, &--timeout, &--lost { border-left: 3px solid var(--color-error); }
      &--running { border-left: 3px solid var(--color-info); }
      &--pending { border-left: 3px solid var(--color-muted); }
      &--dispatching { border-left: 3px solid var(--color-warn); }
    }

    .job-card__header {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-bottom: 8px;
    }

    .job-card__name {
      font-family: var(--font-ui);
      font-size: 13px;
      font-weight: 600;
      color: #e8edf2;
      letter-spacing: 0.03em;
    }

    .job-card__cmd {
      font-size: 11px;
      color: var(--color-muted);
      margin-bottom: 6px;
    }

    .job-card__deps {
      font-size: 10px;
      display: flex;
      flex-wrap: wrap;
      gap: 4px;
      align-items: center;
    }

    .dep-label {
      color: var(--color-accent-dim);
      letter-spacing: 0.06em;
    }

    .dep-name {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: 2px;
      padding: 1px 6px;
      color: #c8d0dc;
      font-size: 10px;
    }

    .job-card__condition { margin-top: 4px; }

    .condition-badge {
      font-size: 9px;
      letter-spacing: 0.06em;
      padding: 2px 6px;
      border-radius: 2px;
      color: var(--color-warn);
      background: rgba(255,179,0,0.1);
    }

    .job-card__footer {
      margin-top: 8px;
      padding-top: 8px;
      border-top: 1px solid var(--color-border);
    }

    .job-link {
      font-size: 10px;
      color: var(--color-info);
      text-decoration: none;
      letter-spacing: 0.04em;
      &:hover { text-decoration: underline; }
    }

    .error-banner {
      display: flex; align-items: center; gap: 8px;
      background: rgba(255,82,82,0.08); border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm); color: var(--color-error);
      font-size: 12px; padding: 10px 14px;
    }

    .loading-text {
      text-align: center;
      color: var(--color-muted);
      font-size: 12px;
      margin-top: 60px;
    }
  `]
})
export class WorkflowDetailComponent implements OnInit {

  workflow: Workflow | null = null;
  loading = false;
  error   = '';

  private workflowId = '';

  constructor(
    private api:   ApiService,
    private route: ActivatedRoute,
  ) {}

  ngOnInit(): void {
    this.workflowId = this.route.snapshot.paramMap.get('id') ?? '';
    this.load();
  }

  load(): void {
    if (!this.workflowId) return;
    this.loading = true;
    this.api.getWorkflow(this.workflowId).subscribe({
      next: wf => {
        this.workflow = wf;
        this.loading  = false;
        this.error    = '';
      },
      error: () => {
        this.loading = false;
        this.error   = 'Workflow not found or failed to load.';
      }
    });
  }

  cancel(): void {
    this.api.cancelWorkflow(this.workflowId).subscribe({
      next: () => this.load(),
      error: () => { this.error = 'Failed to cancel workflow.'; }
    });
  }
}
