// src/app/features/ml/ml-datasets.component.ts
//
// Datasets view — reads GET /api/datasets and surfaces a paginated
// list with a register modal and delete confirmation.
//
// Scope for the feature-18 slice:
//   - List + pagination (re-uses the standard page/size ApiService pattern).
//   - Register via a JSON form modal — URI allowlist (file:// | s3://) is
//     enforced server-side; the form surfaces the error verbatim.
//   - Delete with a confirm prompt.
//
// Out of scope (deferred): tag-filter UI, upload-via-browser modal,
// lineage back-references (which models reference this dataset).

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { MatTableModule } from '@angular/material/table';
import { MatPaginatorModule, PageEvent } from '@angular/material/paginator';
import { MatDialogModule, MatDialog } from '@angular/material/dialog';

import { ApiService } from '../../core/services/api.service';
import { Dataset, DatasetRegisterRequest } from '../../shared/models';
import { RegisterDatasetDialogComponent } from './register-dataset-dialog.component';

@Component({
  selector: 'app-ml-datasets',
  standalone: true,
  imports: [CommonModule, FormsModule, MatTableModule, MatPaginatorModule, MatDialogModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">ML · DATASETS</h1>
      <p class="page-sub">Registered datasets — metadata only; bytes live in the artifact store</p>
    </div>
    <button class="btn-primary" (click)="openRegister()">
      <span class="material-icons" style="font-size:16px">add</span>
      Register
    </button>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="waiting" *ngIf="loading && !error">
    <span class="material-icons spin">sync</span>
    Loading datasets…
  </div>

  <div *ngIf="!loading && !error">
    <div class="empty-state" *ngIf="datasets.length === 0">
      No datasets registered yet.
    </div>

    <table mat-table [dataSource]="datasets" *ngIf="datasets.length > 0" class="ml-table">
      <ng-container matColumnDef="name">
        <th mat-header-cell *matHeaderCellDef>NAME</th>
        <td mat-cell *matCellDef="let d">{{ d.name }}</td>
      </ng-container>
      <ng-container matColumnDef="version">
        <th mat-header-cell *matHeaderCellDef>VERSION</th>
        <td mat-cell *matCellDef="let d" class="mono">{{ d.version }}</td>
      </ng-container>
      <ng-container matColumnDef="uri">
        <th mat-header-cell *matHeaderCellDef>URI</th>
        <td mat-cell *matCellDef="let d" class="mono ellipsis" [title]="d.uri">{{ d.uri }}</td>
      </ng-container>
      <ng-container matColumnDef="size">
        <th mat-header-cell *matHeaderCellDef class="num">SIZE</th>
        <td mat-cell *matCellDef="let d" class="num">{{ formatBytes(d.size_bytes) }}</td>
      </ng-container>
      <ng-container matColumnDef="created_at">
        <th mat-header-cell *matHeaderCellDef>REGISTERED</th>
        <td mat-cell *matCellDef="let d">{{ formatDate(d.created_at) }}</td>
      </ng-container>
      <ng-container matColumnDef="created_by">
        <th mat-header-cell *matHeaderCellDef>BY</th>
        <td mat-cell *matCellDef="let d">{{ d.created_by }}</td>
      </ng-container>
      <ng-container matColumnDef="actions">
        <th mat-header-cell *matHeaderCellDef></th>
        <td mat-cell *matCellDef="let d">
          <button class="btn-icon btn-danger"
                  (click)="onDelete(d)"
                  [attr.aria-label]="'Delete ' + d.name + ' ' + d.version"
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
export class MlDatasetsComponent implements OnInit {
  datasets: Dataset[] = [];
  total    = 0;
  page     = 0;
  pageSize = 25;
  loading  = true;
  error    = '';

  columns = ['name', 'version', 'uri', 'size', 'created_at', 'created_by', 'actions'];

  constructor(private api: ApiService, private dialog: MatDialog) {}

  ngOnInit(): void { this.reload(); }

  reload(): void {
    this.loading = true;
    this.error   = '';
    this.api.getDatasets(this.page, this.pageSize).subscribe({
      next: resp => {
        this.datasets = resp.datasets;
        this.total    = resp.total;
        this.loading  = false;
      },
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Failed to load datasets';
        this.loading = false;
      },
    });
  }

  onPage(e: PageEvent): void {
    this.page     = e.pageIndex;
    this.pageSize = e.pageSize;
    this.reload();
  }

  openRegister(): void {
    const ref = this.dialog.open(RegisterDatasetDialogComponent, { width: '560px' });
    ref.afterClosed().subscribe((req?: DatasetRegisterRequest) => {
      if (!req) return;
      this.loading = true;
      this.api.registerDataset(req).subscribe({
        next: () => { this.page = 0; this.reload(); },
        error: err => {
          this.error   = err?.error?.error ?? err?.message ?? 'Register failed';
          this.loading = false;
        },
      });
    });
  }

  onDelete(d: Dataset): void {
    if (!confirm(`Delete ${d.name} ${d.version}? This only removes the registry entry — the artifact bytes stay in the store.`)) {
      return;
    }
    this.loading = true;
    this.api.deleteDataset(d.name, d.version).subscribe({
      next: () => this.reload(),
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Delete failed';
        this.loading = false;
      },
    });
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
