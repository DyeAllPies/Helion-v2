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
import { LineageJob, WorkflowLineage } from '../../shared/models';
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

    <!--
      Workflow-level progress bar — X of N jobs done plus a visual
      fill. Uses the client-side count so it updates the moment the
      next poll tick comes in. Live-polls cover the "moves in time"
      aspect; this bar compresses that motion into a single glanceable
      widget at the top of the page.
    -->
    <div class="wf-progress">
      <div class="wf-progress__header">
        <span class="wf-progress__label">WORKFLOW PROGRESS</span>
        <span class="wf-progress__value mono">
          {{ completedCount() }}/{{ totalCount() }} jobs
          <span class="wf-progress__hint" *ngIf="runningCount() > 0">
            · {{ runningCount() }} running
          </span>
        </span>
      </div>
      <div class="wf-progress__bar">
        <div class="wf-progress__fill wf-progress__fill--done"
             [style.width.%]="percentCompleted()"></div>
        <div class="wf-progress__fill wf-progress__fill--running"
             [style.width.%]="percentRunning()"
             [style.left.%]="percentCompleted()"></div>
      </div>
    </div>

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
          <!--
            Elapsed / total-duration indicator. For in-flight jobs
            we show elapsed-since-dispatch recomputed against the
            component nowMs value on every poll tick; completed
            jobs show their final wall clock. Node id next to it
            attributes each job to the node that executed it.
          -->
          <div class="job-progress" *ngIf="j.dispatched_at">
            <div class="job-progress__row">
              <span class="job-label">elapsed</span>
              <span class="mono job-progress__time">{{ elapsedLabel(j) }}</span>
              <span class="job-progress__node mono" *ngIf="j.node_id">
                · on <strong>{{ j.node_id }}</strong>
              </span>
            </div>
          </div>
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

    /* Workflow-level progress bar — rendered once at the top of the
       detail page. Two stacked fills: "done" (completed + skipped)
       in the completed-green palette and "running" in the running-
       cyan palette, so the viewer sees not just "how far through"
       but also "how much is in flight right now". */
    .wf-progress {
      margin-bottom: 16px;
      padding: 10px 14px;
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
    }
    .wf-progress__header {
      display: flex; justify-content: space-between; align-items: center;
      margin-bottom: 6px;
      font-size: 11px; letter-spacing: 0.08em;
    }
    .wf-progress__label { color: #8896aa; }
    .wf-progress__value { color: #e8edf2; font-size: 12px; }
    .wf-progress__hint  { color: var(--color-dispatching); margin-left: 4px; }
    .wf-progress__bar {
      position: relative;
      height: 6px;
      background: var(--color-surface-2);
      border-radius: 3px;
      overflow: hidden;
    }
    .wf-progress__fill {
      position: absolute;
      top: 0; bottom: 0;
      transition: width 0.3s ease;
    }
    .wf-progress__fill--done    { left: 0; background: var(--color-completed); }
    .wf-progress__fill--running {
      background: #68b4d4;
      opacity: 0.7;
      /* animate a subtle pulse so the running slice reads as
         "in motion" rather than a static segment. */
      animation: wf-progress-pulse 1.4s ease-in-out infinite;
    }
    @keyframes wf-progress-pulse {
      0%, 100% { opacity: 0.45; }
      50%      { opacity: 0.9;  }
    }

    /* Per-job elapsed / duration indicator on each card. Lives above
       the standard job-line rows so the "live" metric is the first
       thing a reader's eye lands on. */
    .job-progress { margin-top: 4px; }
    .job-progress__row {
      display: flex; gap: 8px; align-items: baseline;
      font-size: 11px;
    }
    .job-progress__time { color: #c7f0d2; font-weight: 600; }
    .job-progress__node { color: #8896aa; font-size: 10px; }
    .job-progress__node strong { color: var(--color-accent); font-weight: 600; }
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
        // Refresh the cached "now" each tick so the elapsed labels
        // advance without a separate setInterval; see `nowMs` /
        // `elapsedLabel` below.
        this.nowMs = Date.now();
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

  // ── Workflow-progress helpers (rendered in the template) ────────

  /**
   * Lowercases a job status for the filters below. Split out so the
   * helpers stay short and readable.
   */
  private statusOf(j: LineageJob): string { return (j.status ?? '').toLowerCase(); }

  totalCount(): number { return this.lineage?.jobs.length ?? 0; }

  completedCount(): number {
    if (!this.lineage) return 0;
    return this.lineage.jobs.filter(j => {
      const s = this.statusOf(j);
      return s === 'completed' || s === 'skipped';
    }).length;
  }

  runningCount(): number {
    if (!this.lineage) return 0;
    return this.lineage.jobs.filter(j => {
      const s = this.statusOf(j);
      return s === 'running' || s === 'dispatching';
    }).length;
  }

  percentCompleted(): number {
    const total = this.totalCount();
    return total === 0 ? 0 : (this.completedCount() / total) * 100;
  }

  /**
   * Width for the in-flight slice of the progress bar. Sits to the
   * right of the completed fill (see template — `left.%` is set to
   * percentCompleted) so the two segments stack without overlap.
   */
  percentRunning(): number {
    const total = this.totalCount();
    return total === 0 ? 0 : (this.runningCount() / total) * 100;
  }

  /**
   * Human-friendly elapsed-since-dispatch for a single job.
   *
   *   - completed: `12.4 s` (dispatched → finished wall clock)
   *   - in-flight: `4.1 s` (dispatched → now; updates on next poll)
   *   - pending:   empty (template hides the row when
   *                dispatched_at is missing)
   *
   * Uses the cached `nowMs` rather than `Date.now()` directly so
   * that every card in one render references the same "now" and
   * the bars don't skew against each other across paint ticks.
   */
  elapsedLabel(j: LineageJob): string {
    if (!j.dispatched_at) return '';
    const start = Date.parse(j.dispatched_at);
    if (Number.isNaN(start)) return '';
    const end = j.finished_at ? Date.parse(j.finished_at) : this.nowMs;
    const ms = Math.max(0, end - start);
    return formatElapsed(ms);
  }

  /**
   * Cached wall clock. Refreshed on every poll tick (and therefore
   * on every change-detection pass the poll triggers) so elapsed
   * labels advance without a separate per-second timer spinning in
   * the background. The cost is lower-resolution ticking (one update
   * per tokenRefreshMs), which is fine for the "this job took
   * roughly 12 seconds" read the card provides.
   */
  nowMs = Date.now();
}

/**
 * Format a duration in ms to a short human-readable string:
 *   650   → "0.6 s"
 *   4120  → "4.1 s"
 *   67000 → "1 min 7 s"
 *
 * Exported for unit testing — the component method delegates here
 * so we can pin boundary cases without rebuilding the whole view.
 */
export function formatElapsed(ms: number): string {
  if (ms < 0) return '0 s';
  const seconds = ms / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)} s`;
  const mins = Math.floor(seconds / 60);
  const rem  = Math.round(seconds - mins * 60);
  return `${mins} min ${rem} s`;
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

  // Bucket a status into one of our mermaid classDef names. Kept
  // aligned with MlPipelineDetailComponent.statusChipClass so the
  // cards below the DAG and the nodes inside it use the same
  // palette (feature 21 walkthrough pass).
  const classFor = (status: string): string => {
    const s = (status || '').toLowerCase();
    if (s === 'running')     return 'st-running';
    if (s === 'completed')   return 'st-completed';
    if (s === 'failed' || s === 'cancelled' || s === 'timeout' || s === 'lost') {
      return 'st-failed';
    }
    if (s === 'dispatching') return 'st-dispatching';
    if (s === 'scheduled' || s === 'pending') return 'st-pending';
    return 'st-default';
  };

  // Nodes — show job name + status. Assign each a status class so
  // the classDef block below colours it distinctly.
  const perNodeClass: { id: string; cls: string }[] = [];
  for (const j of lineage.jobs) {
    const id = sanitize(j.name);
    const label = `"${j.name}<br/><small>${j.status}</small>"`;
    lines.push(`  ${id}[${label}]`);
    perNodeClass.push({ id, cls: classFor(j.status) });
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

  // classDef block — mermaid picks these up and applies the fill
  // and stroke to any node carrying the class. Colours mirror the
  // job-card chip palette; we emit 8-digit hex with an alpha
  // channel (last two hex digits) instead of `rgba(r,g,b,a)`
  // because mermaid's flowchart parser treats the comma inside
  // the rgba() call as a classDef property separator and bails
  // with "Expecting SEMI".
  //   0x2E ≈ 0.18 alpha, 0x38 ≈ 0.22 alpha, 0x26 ≈ 0.15 alpha.
  lines.push('  classDef st-pending     fill:#ffab402E,stroke:#ffab40,color:#ffab40');
  lines.push('  classDef st-dispatching fill:#818cf82E,stroke:#818cf8,color:#818cf8');
  lines.push('  classDef st-running     fill:#68b4d438,stroke:#68b4d4,color:#68b4d4');
  lines.push('  classDef st-completed   fill:#44b55f38,stroke:#44b55f,color:#c7f0d2');
  lines.push('  classDef st-failed      fill:#ff525238,stroke:#ff5252,color:#ff5252');
  lines.push('  classDef st-default     fill:#78787826,stroke:#4a5568,color:#a0aec0');
  // class assignments: one line per bucket so we send the shortest
  // possible spec to mermaid (bucketing by class collapses the
  // common case of "all four cards completed" into one line).
  const byClass = new Map<string, string[]>();
  for (const { id, cls } of perNodeClass) {
    const list = byClass.get(cls) ?? [];
    list.push(id);
    byClass.set(cls, list);
  }
  for (const [cls, ids] of byClass) {
    lines.push(`  class ${ids.join(',')} ${cls}`);
  }

  return lines.join('\n');
}
