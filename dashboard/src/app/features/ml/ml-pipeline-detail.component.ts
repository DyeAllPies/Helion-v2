// src/app/features/ml/ml-pipeline-detail.component.ts
//
// Detail view for a single workflow's lineage — renders the DAG
// using mermaid. The coordinator's /workflows/{id}/lineage endpoint
// does the expensive join (workflow jobs ↔ live JobStore status ↔
// registered models) so the dashboard only needs one round-trip.
//
// Mermaid is loaded lazily: the mermaid bundle is ~200 KiB and
// the Pipelines detail page is the only place using it, so we
// import it dynamically inside ngAfterViewInit so the main bundle
// stays lean.

import {
  AfterViewInit, ChangeDetectorRef, Component, OnInit,
  ViewChild, ElementRef, OnDestroy,
} from '@angular/core';
import { CommonModule } from '@angular/common';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { Subscription, interval, startWith, switchMap } from 'rxjs';

import { ApiService } from '../../core/services/api.service';
import { WorkflowLineage } from '../../shared/models';
import { environment } from '../../../environments/environment';

@Component({
  selector: 'app-ml-pipeline-detail',
  standalone: true,
  imports: [CommonModule, RouterLink],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <a routerLink="/ml/pipelines" class="back-link">
        <span class="material-icons" style="font-size:14px">arrow_back</span>
        All pipelines
      </a>
      <h1 class="page-title" style="margin-top:8px">
        PIPELINE · {{ lineage?.workflow_id || workflowId }}
      </h1>
      <p class="page-sub" *ngIf="lineage">
        {{ lineage.name || 'unnamed workflow' }} · status
        <strong>{{ lineage.status }}</strong> ·
        {{ lineage.jobs.length }} job<span *ngIf="lineage.jobs.length !== 1">s</span>
      </p>
    </div>
  </header>

  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div class="waiting" *ngIf="loading && !error">
    <span class="material-icons spin">sync</span>
    Loading lineage…
  </div>

  <div *ngIf="lineage && !loading && !error">
    <div class="dag-panel">
      <div class="dag-panel__header">
        <span class="material-icons" style="font-size:16px;color:var(--color-accent-dim)">account_tree</span>
        DAG · DEPENDENCY ARROWS + ARTIFACT FLOW
      </div>
      <div class="dag-wrap" #dagHost></div>
      <div class="dag-legend">
        <span class="legend-item"><span class="legend-arrow solid"></span> dependency</span>
        <span class="legend-item"><span class="legend-arrow dashed"></span> artifact (upstream output → downstream input)</span>
      </div>
    </div>

    <div class="job-grid">
      <div class="job-card" *ngFor="let j of lineage.jobs">
        <div class="job-card__header">
          <span class="mono">{{ j.name }}</span>
          <span class="chip" [class]="statusChipClass(j.status)">{{ j.status }}</span>
        </div>
        <div class="job-card__body">
          <div class="job-line" *ngIf="j.command">
            <span class="job-label">cmd</span>
            <span class="mono job-value">{{ j.command }}</span>
          </div>
          <div class="job-line" *ngIf="j.depends_on?.length">
            <span class="job-label">deps</span>
            <span class="mono job-value">{{ j.depends_on?.join(', ') }}</span>
          </div>
          <div class="job-line" *ngIf="j.outputs?.length">
            <span class="job-label">outputs</span>
            <span class="job-value">
              <span class="out-pill" *ngFor="let o of j.outputs" [title]="o.uri">
                {{ o.name }} · {{ formatBytes(o.size) }}
              </span>
            </span>
          </div>
          <div class="job-line" *ngIf="j.models_produced?.length">
            <span class="job-label">models</span>
            <span class="job-value">
              <a *ngFor="let m of j.models_produced"
                 routerLink="/ml/models"
                 class="model-link">
                {{ m.name }}&nbsp;{{ m.version }}
              </a>
            </span>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>
  `,
  styleUrls: ['./ml-shared.scss'],
  styles: [`
    .back-link {
      color: var(--color-muted);
      font-size: 11px;
      text-decoration: none;
      display: inline-flex;
      align-items: center;
      gap: 4px;
    }
    .back-link:hover { color: var(--color-accent); }

    .dag-panel {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
      margin-bottom: 24px;
    }
    .dag-panel__header {
      display: flex; align-items: center; gap: 8px;
      padding: 12px 16px;
      background: var(--color-surface-2);
      border-bottom: 1px solid var(--color-border);
      font-size: 11px; letter-spacing: 0.07em; color: #8896aa;
    }
    .dag-wrap {
      padding: 24px 16px;
      min-height: 180px;
      display: flex;
      justify-content: center;
      align-items: center;
      overflow-x: auto;
    }
    .dag-wrap :global(svg) { max-width: 100%; height: auto; }

    .dag-legend {
      display: flex; gap: 16px;
      padding: 8px 16px 12px;
      font-size: 10px;
      color: var(--color-muted);
    }
    .legend-item { display: inline-flex; align-items: center; gap: 6px; }
    .legend-arrow {
      width: 20px; height: 2px;
      background: var(--color-muted);
    }
    .legend-arrow.dashed {
      background: repeating-linear-gradient(to right, var(--color-accent) 0 4px, transparent 4px 8px);
    }

    .job-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
      gap: 12px;
    }
    .job-card {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
    }
    .job-card__header {
      display: flex; justify-content: space-between; align-items: center;
      padding: 10px 14px;
      border-bottom: 1px solid var(--color-border);
      font-size: 12px;
    }
    .job-card__body { padding: 10px 14px; display: flex; flex-direction: column; gap: 6px; }
    .job-line { display: flex; gap: 10px; font-size: 11px; }
    .job-label {
      flex-shrink: 0; width: 64px;
      color: #8896aa; font-size: 10px;
      text-transform: uppercase; letter-spacing: 0.06em;
      padding-top: 2px;
    }
    .job-value { flex: 1; word-break: break-all; }

    .out-pill {
      display: inline-block;
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      padding: 2px 8px;
      margin-right: 4px;
      font-size: 10px;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    }
    .model-link {
      color: var(--color-accent);
      text-decoration: none;
      margin-right: 8px;
      font-size: 11px;
    }
    .model-link:hover { text-decoration: underline; }

    /* Status-chip palette. Each transitional state gets its own
       distinctive colour so a viewer can see the DAG progress at
       a glance (pending -> dispatching -> running -> completed).
       Vars come from styles.scss; matches the workflow-detail
       border-left palette (feature 4 job state machine). */
    .chip.chip-pending     { background: rgba(255, 171, 64, 0.12); color: var(--color-pending); }
    .chip.chip-scheduled   { background: rgba(255, 171, 64, 0.12); color: var(--color-pending); }
    .chip.chip-dispatching { background: rgba(129, 140, 248, 0.15); color: var(--color-dispatching); }
    .chip.chip-running     { background: rgba(68, 160, 200, 0.18); color: #68b4d4; }
    .chip.chip-completed   { background: rgba(68, 181, 95, 0.15); color: var(--color-completed); }
    .chip.chip-failed      { background: rgba(244, 67, 54, 0.15); color: var(--color-error); }
    .chip.chip-default     { background: var(--color-surface-2); color: var(--color-muted); }
  `],
})
export class MlPipelineDetailComponent implements OnInit, AfterViewInit, OnDestroy {
  @ViewChild('dagHost') dagHost?: ElementRef<HTMLDivElement>;

  workflowId = '';
  lineage: WorkflowLineage | null = null;
  loading = true;
  error   = '';

  // Guard against re-rendering the DAG if lineage changes after the
  // view is already torn down (component destroyed during in-flight
  // mermaid render).
  private destroyed = false;

  // Live-polling bookkeeping.
  private sub?: Subscription;
  // Signature of the last-rendered lineage — `name:status` per job,
  // ordered. If the next poll returns the same signature we skip
  // the mermaid render entirely (spec unchanged → output unchanged;
  // re-rendering would flash the SVG for no reason).
  private lastRenderSignature = '';
  // Terminal statuses for the workflow itself. Once we see one we
  // stop polling — the lineage is frozen and further ticks would
  // just burn coordinator CPU.
  private static readonly TERMINAL = new Set(['completed', 'failed', 'cancelled']);

  constructor(
    private route: ActivatedRoute,
    private api: ApiService,
    private cdr: ChangeDetectorRef,
  ) {}

  /**
   * Poll `GET /workflows/{id}/lineage` on the same cadence as the
   * nodes list (environment.tokenRefreshMs). Three safeguards:
   *
   *   1. switchMap cancels an in-flight request if the next tick
   *      fires first — a slow coordinator won't stack requests.
   *   2. The mermaid render only runs when the job-status signature
   *      actually changes, so steady-state ticks are free.
   *   3. When the workflow reaches a terminal status we unsubscribe
   *      (pausePolling) — no point polling a completed pipeline.
   */
  ngOnInit(): void {
    this.workflowId = this.route.snapshot.paramMap.get('id') ?? '';
    if (!this.workflowId) {
      this.error = 'missing workflow id';
      this.loading = false;
      return;
    }
    this.sub = interval(environment.tokenRefreshMs).pipe(
      startWith(0),
      switchMap(() => this.api.getWorkflowLineage(this.workflowId)),
    ).subscribe({
      next: l => {
        this.lineage = l;
        this.loading = false;
        this.error   = '';
        // Render after the *ngIf branch materialises the #dagHost
        // element — ngAfterViewInit fires before lineage arrives
        // for async fetches, so we drive the render from here too.
        this.cdr.detectChanges();
        const sig = this.lineageSignature(l);
        if (sig !== this.lastRenderSignature) {
          this.lastRenderSignature = sig;
          void this.renderDag();
        }
        if (MlPipelineDetailComponent.TERMINAL.has((l.status ?? '').toLowerCase())) {
          this.pausePolling();
        }
      },
      error: err => {
        this.error   = err?.error?.error ?? err?.message ?? 'Failed to load lineage';
        this.loading = false;
      },
    });
  }

  ngAfterViewInit(): void {
    // If lineage arrived synchronously (unlikely but fine) its
    // render was queued before dagHost was ready. Re-run here.
    if (this.lineage && this.dagHost) {
      void this.renderDag();
    }
  }

  ngOnDestroy(): void {
    this.destroyed = true;
    this.pausePolling();
  }

  private pausePolling(): void {
    this.sub?.unsubscribe();
    this.sub = undefined;
  }

  /**
   * Stable string key for a lineage snapshot: per-job `name:status`
   * joined, plus the workflow-level status. Two polls that produce
   * the same signature represent the same state machine moment,
   * so we can skip the expensive mermaid re-render.
   */
  private lineageSignature(l: WorkflowLineage): string {
    const perJob = (l.jobs ?? [])
      .map(j => `${j.name}:${j.status ?? ''}`)
      .join('|');
    return `${l.status ?? ''}#${perJob}`;
  }

  private async renderDag(): Promise<void> {
    if (!this.lineage || !this.dagHost) return;
    const spec = buildMermaidSpec(this.lineage);
    try {
      const mermaid = (await import('mermaid')).default;
      mermaid.initialize({
        startOnLoad: false,
        theme: 'dark',
        securityLevel: 'strict',
        flowchart: { htmlLabels: false, curve: 'basis' },
      });
      const id = `dag-${this.workflowId.replace(/[^a-zA-Z0-9]/g, '-')}`;
      const { svg } = await mermaid.render(id, spec);
      if (!this.destroyed && this.dagHost) {
        this.dagHost.nativeElement.innerHTML = svg;
      }
    } catch (e) {
      // Mermaid parse errors typically mean the spec we built is
      // malformed — surface as an inline error rather than silently
      // showing a blank DAG.
      if (!this.destroyed) {
        this.error = 'Failed to render DAG: ' + (e as Error).message;
      }
    }
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

  formatBytes(n?: number): string {
    if (!n) return '—';
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
    return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
  }
}

/**
 * Build a mermaid flowchart spec from a WorkflowLineage. Dependency
 * edges (depends_on) render as solid arrows; artifact edges (From:
 * upstream.output) render as dashed with the output → input labels.
 * Exported for unit testing — the render path itself is dynamic so
 * not easily unit-testable in jsdom.
 */
export function buildMermaidSpec(lineage: WorkflowLineage): string {
  const lines: string[] = ['flowchart LR'];
  const sanitize = (s: string) => s.replace(/[^a-zA-Z0-9_]/g, '_');

  // Nodes — show job name + status. Mermaid auto-styles nodes by
  // class name; we attach a status class so the dark theme colours
  // completed/failed/etc. distinctly.
  for (const j of lineage.jobs) {
    const id = sanitize(j.name);
    const label = `"${j.name}<br/><small>${j.status}</small>"`;
    lines.push(`  ${id}[${label}]`);
  }

  // Dependency edges (solid).
  for (const j of lineage.jobs) {
    if (!j.depends_on) continue;
    for (const dep of j.depends_on) {
      lines.push(`  ${sanitize(dep)} --> ${sanitize(j.name)}`);
    }
  }

  // Artifact edges (dashed with label).
  for (const e of lineage.artifact_edges || []) {
    lines.push(`  ${sanitize(e.from_job)} -. "${e.from_output} → ${e.to_input}" .-> ${sanitize(e.to_job)}`);
  }

  return lines.join('\n');
}
