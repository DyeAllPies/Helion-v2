// src/app/features/ml/ml-models.component.ts
//
// Models view — reads GET /api/models. Surfaces lineage (source job
// + source dataset) and free-form metrics alongside the standard
// name/version/URI columns.
//
// Scope for the feature-18 slice: list + lineage column + metrics
// column + delete. Register-from-UI intentionally omitted — models
// are expected to be registered by training jobs via the REST API,
// not by an operator clicking a form.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';

import { ApiService } from '../../core/services/api.service';
import { MLModel } from '../../shared/models';

@Component({
  selector: 'app-ml-models',
  standalone: true,
  imports: [CommonModule, RouterLink, MatTableModule, MatPaginatorModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">ML · MODELS</h1>
      <p class="page-sub">Trained models with lineage back to source job + dataset</p>
    </div>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="waiting" *ngIf="loading && !error">
    <span class="material-icons spin">sync</span>
    Loading models…
  </div>

  <div *ngIf="!loading && !error">
    <div class="empty-state" *ngIf="models.length === 0">
      No models registered yet. Training jobs register their outputs via
      <span class="mono">POST /api/models</span>.
    </div>

    <table mat-table [dataSource]="models" *ngIf="models.length > 0" class="ml-table">
      <ng-container matColumnDef="name">
        <th mat-header-cell *matHeaderCellDef>NAME</th>
        <td mat-cell *matCellDef="let m">{{ m.name }}</td>
      </ng-container>
      <ng-container matColumnDef="version">
        <th mat-header-cell *matHeaderCellDef>VERSION</th>
        <td mat-cell *matCellDef="let m" class="mono">{{ m.version }}</td>
      </ng-container>
      <ng-container matColumnDef="framework">
        <th mat-header-cell *matHeaderCellDef>FRAMEWORK</th>
        <td mat-cell *matCellDef="let m">{{ m.framework || '—' }}</td>
      </ng-container>
      <ng-container matColumnDef="lineage">
        <th mat-header-cell *matHeaderCellDef>LINEAGE</th>
        <td mat-cell *matCellDef="let m">
          <div class="lineage">
            <a *ngIf="m.source_job_id" [routerLink]="['/jobs', m.source_job_id]"
               [attr.aria-label]="'Source job ' + m.source_job_id">
              <span class="material-icons" style="font-size:12px;vertical-align:middle">link</span>
              job: {{ m.source_job_id }}
            </a>
            <span *ngIf="!m.source_job_id" class="muted">no source job</span>
            <span *ngIf="m.source_dataset as sd" class="muted">
              dataset: {{ sd.name }} {{ sd.version }}
            </span>
          </div>
        </td>
      </ng-container>
      <ng-container matColumnDef="metrics">
        <th mat-header-cell *matHeaderCellDef>METRICS</th>
        <td mat-cell *matCellDef="let m">
          <div class="metrics" *ngIf="hasMetrics(m); else noMetrics">
            <span class="metric-pill" *ngFor="let kv of metricsList(m)">
              {{ kv.key }} = {{ formatMetric(kv.value) }}
            </span>
          </div>
          <ng-template #noMetrics><span class="muted">—</span></ng-template>
        </td>
      </ng-container>
      <ng-container matColumnDef="size">
        <th mat-header-cell *matHeaderCellDef class="num">SIZE</th>
        <td mat-cell *matCellDef="let m" class="num">{{ formatBytes(m.size_bytes) }}</td>
      </ng-container>
      <ng-container matColumnDef="created_at">
        <th mat-header-cell *matHeaderCellDef>REGISTERED</th>
        <td mat-cell *matCellDef="let m">{{ formatDate(m.created_at) }}</td>
      </ng-container>
      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let m">
          <button class="btn-icon btn-danger"
                  (click)="onDelete(m)"
                  [attr.aria-label]="'Delete ' + m.name + ' ' + m.version"
                  title="Delete">
            <span class="material-icons" style="font-size:14px">delete</span>
          </button>
        </td>
      </ng-container>
      <tr mat-header-row *matHeaderRowDef="columns"></tr>
      <tr mat-row *matRowDef="let row; columns: columns;"></tr>
    </table>

    <mat-paginator *ngIf="total > pageSize"
                   [length]="total"
                   [pageSize]="pageSize"
                   [pageIndex]="page"
                   [pageSizeOptions]="[10, 25, 50, 100]"
                   (page)="onPage($event)">
    </mat-paginator>
  </div>
</div>
  `,
  styleUrls: ['./ml-shared.scss'],
})
export class MlModelsComponent implements OnInit {
  models: MLModel[] = [];
  total    = 0;
  page     = 0;
  pageSize = 25;
  loading  = true;
  error    = '';

  columns = ['name', 'version', 'framework', 'lineage', 'metrics', 'size', 'created_at', 'actions'];

  constructor(private api: ApiService) {}

  ngOnInit(): void { this.reload(); }

  reload(): void {
    this.loading = true;
    this.error   = '';
    this.api.getModels(this.page, this.pageSize).subscribe({
      next: resp => {
        this.models  = resp.models;
        this.total   = resp.total;
        this.loading = false;
      },
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Failed to load models';
        this.loading = false;
      },
    });
  }

  onPage(e: PageEvent): void {
    this.page     = e.pageIndex;
    this.pageSize = e.pageSize;
    this.reload();
  }

  onDelete(m: MLModel): void {
    if (!confirm(`Delete ${m.name} ${m.version}? Registry entry only — artifact bytes stay in the store.`)) {
      return;
    }
    this.loading = true;
    this.api.deleteModel(m.name, m.version).subscribe({
      next: () => this.reload(),
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Delete failed';
        this.loading = false;
      },
    });
  }

  hasMetrics(m: MLModel): boolean {
    return !!m.metrics && Object.keys(m.metrics).length > 0;
  }

  metricsList(m: MLModel): Array<{ key: string; value: number }> {
    if (!m.metrics) return [];
    return Object.entries(m.metrics)
      .map(([key, value]) => ({ key, value }))
      .sort((a, b) => a.key.localeCompare(b.key));
  }

  formatMetric(v: number): string {
    if (Number.isInteger(v)) return String(v);
    return v.toFixed(3);
  }

  formatDate(s: string): string {
    if (!s) return '—';
    const d = new Date(s);
    return isNaN(d.getTime()) ? s : d.toLocaleString();
  }

  formatBytes(n?: number): string {
    if (!n) return '—';
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
    return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
  }
}
