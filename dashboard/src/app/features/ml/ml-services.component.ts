// src/app/features/ml/ml-services.component.ts
//
// Live inference-service endpoints view. Reads GET /api/services
// every 5s (matches the node-side probe interval so the dashboard
// is never more than one tick stale). Shows the upstream URL,
// ready state, and a back-link to the job detail page.
//
// Services come and go — a completed/cancelled service job is
// removed from the registry by the coordinator's JobCompletionCallback,
// so the table naturally shrinks without the dashboard having to
// filter terminated jobs itself.

import { Component, OnDestroy, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { MatTableModule } from '@angular/material/table';
import { Subscription, interval, startWith, switchMap, of, catchError } from 'rxjs';

import { ApiService } from '../../core/services/api.service';
import { ServiceEndpoint } from '../../shared/models';

@Component({
  selector: 'app-ml-services',
  standalone: true,
  imports: [CommonModule, RouterLink, MatTableModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">ML · SERVICES</h1>
      <p class="page-sub">
        Live inference endpoints · refreshes every 5&nbsp;s (matches node probe interval)
      </p>
    </div>
    <button class="btn-primary" (click)="reload()" [disabled]="loading">
      <span class="material-icons" style="font-size:16px">refresh</span>
      Refresh
    </button>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div *ngIf="!error">
    <div class="empty-state" *ngIf="!loading && services.length === 0">
      No live inference services right now.
    </div>

    <table mat-table [dataSource]="services" *ngIf="services.length > 0" class="ml-table">
      <ng-container matColumnDef="job">
        <th mat-header-cell *matHeaderCellDef>JOB</th>
        <td mat-cell *matCellDef="let s">
          <a [routerLink]="['/jobs', s.job_id]" class="lineage">
            <span class="material-icons" style="font-size:12px;vertical-align:middle">link</span>
            {{ s.job_id }}
          </a>
        </td>
      </ng-container>
      <ng-container matColumnDef="node">
        <th mat-header-cell *matHeaderCellDef>NODE</th>
        <td mat-cell *matCellDef="let s">{{ s.node_id }}</td>
      </ng-container>
      <ng-container matColumnDef="upstream">
        <th mat-header-cell *matHeaderCellDef>UPSTREAM URL</th>
        <td mat-cell *matCellDef="let s" class="mono ellipsis" [title]="s.upstream_url">
          {{ s.upstream_url }}
        </td>
      </ng-container>
      <ng-container matColumnDef="port">
        <th mat-header-cell *matHeaderCellDef class="num">PORT</th>
        <td mat-cell *matCellDef="let s" class="num">{{ s.port }}</td>
      </ng-container>
      <ng-container matColumnDef="health">
        <th mat-header-cell *matHeaderCellDef>HEALTH PATH</th>
        <td mat-cell *matCellDef="let s" class="mono">{{ s.health_path }}</td>
      </ng-container>
      <ng-container matColumnDef="state">
        <th mat-header-cell *matHeaderCellDef>STATE</th>
        <td mat-cell *matCellDef="let s">
          <span class="chip" [class.chip-ready]="s.ready" [class.chip-unhealthy]="!s.ready">
            {{ s.ready ? 'READY' : 'UNHEALTHY' }}
          </span>
        </td>
      </ng-container>
      <ng-container matColumnDef="updated">
        <th mat-header-cell *matHeaderCellDef>LAST STATE CHANGE</th>
        <td mat-cell *matCellDef="let s">{{ formatDate(s.updated_at) }}</td>
      </ng-container>
      <tr mat-header-row *matHeaderRowDef="columns"></tr>
      <tr mat-row *matRowDef="let row; columns: columns;"></tr>
    </table>
  </div>
</div>
  `,
  styleUrls: ['./ml-shared.scss'],
})
export class MlServicesComponent implements OnInit, OnDestroy {
  services: ServiceEndpoint[] = [];
  loading  = true;
  error    = '';

  columns = ['job', 'node', 'upstream', 'port', 'health', 'state', 'updated'];

  private sub?: Subscription;

  constructor(private api: ApiService) {}

  ngOnInit(): void {
    // Poll every 5 s; emit once immediately via startWith so the
    // user doesn't stare at a loading state for the first tick.
    this.sub = interval(5000).pipe(
      startWith(0),
      switchMap(() => this.api.getServices().pipe(
        catchError(err => {
          this.error = err?.error?.error ?? err?.message ?? 'Failed to load services';
          this.loading = false;
          return of({ services: [], total: 0 });
        }),
      )),
    ).subscribe(resp => {
      this.services = resp.services;
      this.loading  = false;
      if (this.services.length > 0) this.error = '';
    });
  }

  ngOnDestroy(): void {
    this.sub?.unsubscribe();
  }

  reload(): void {
    this.loading = true;
    this.api.getServices().subscribe({
      next: resp => {
        this.services = resp.services;
        this.loading  = false;
        this.error    = '';
      },
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Failed to load services';
        this.loading = false;
      },
    });
  }

  formatDate(s: string): string {
    if (!s) return '—';
    const d = new Date(s);
    return isNaN(d.getTime()) ? s : d.toLocaleString();
  }
}
