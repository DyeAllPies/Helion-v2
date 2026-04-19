// src/app/features/submit/submit-dag-builder.component.ts
//
// Feature 22 step 8 (promoted from the spec's deferred list) —
// visual DAG builder.
//
// Not a drag-and-drop canvas. This is the "form + live preview"
// flavour of visual authoring:
//
//   - Left pane: list of jobs. "+ add job" appends a blank job
//     row; clicking a row loads its fields into the editor on
//     the right. "×" on a row removes the job.
//   - Right pane: form for the currently-selected job
//     (name / command / args / env / depends_on / node_selector
//     / timeout). depends_on is a multi-select over the OTHER
//     job names — no free text, no typos, no references to
//     nonexistent nodes.
//   - Bottom: auto-generated workflow JSON preview, rendered
//     live as the user edits. Hitting Validate runs the same
//     shape checker the paste-JSON tab uses, and Submit posts
//     to POST /workflows via the same Preview modal.
//
// Reusing the paste-JSON editor's validator + Preview modal +
// submit path means the DAG builder is purely a UX skin — the
// wire protocol and server contract are unchanged. Swapping to
// a true drag-drop canvas in the future replaces only this
// component's template.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import {
  AbstractControl, FormArray, FormBuilder, FormGroup,
  ReactiveFormsModule, Validators,
} from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { ApiService } from '../../core/services/api.service';
import { SubmitWorkflowRequest, SubmitWorkflowJobRequest } from '../../shared/models';
import { SubmitPreviewDialogComponent, PreviewResult } from './submit-preview-dialog.component';
import { validateWorkflowShape } from './workflow-shape-validator';

@Component({
  selector: 'app-submit-dag-builder',
  standalone: true,
  imports: [CommonModule, ReactiveFormsModule, SubmitPreviewDialogComponent],
  template: `
<div class="dag-builder">

  <div class="notice">
    <span class="material-icons notice__icon">info</span>
    <span>
      Click a job on the left to edit it on the right. The
      workflow preview at the bottom refreshes live. Depends-on
      uses a multi-select over the other job names — no typos,
      no references to jobs that do not exist.
    </span>
  </div>

  <div class="dag-builder__panes">

    <!-- ── Left: job list ────────────────────────────────────── -->
    <aside class="pane pane--jobs">
      <div class="pane__header">
        <span class="pane__title">JOBS</span>
        <button type="button" class="btn-ghost" (click)="addJob()">+ add job</button>
      </div>
      <ul class="job-list" *ngIf="jobControls.length > 0; else emptyJobs">
        <li *ngFor="let ctrl of jobControls; let i = index"
            class="job-item"
            [class.job-item--active]="i === activeIdx"
            (click)="select(i)">
          <span class="job-item__index">{{ i + 1 }}</span>
          <span class="job-item__name mono">{{ ctrl.value.name || '(unnamed)' }}</span>
          <button type="button" class="btn-ghost btn-ghost--danger"
                  (click)="$event.stopPropagation(); removeJob(i)">×</button>
        </li>
      </ul>
      <ng-template #emptyJobs>
        <div class="pane__empty">No jobs yet. Click "+ add job" to start.</div>
      </ng-template>
    </aside>

    <!-- ── Right: per-job editor ─────────────────────────────── -->
    <section class="pane pane--editor">
      <ng-container *ngIf="activeJob as j; else selectHint">
        <div [formGroup]="j" class="editor-fields">
          <label>
            <span class="label">NAME</span>
            <input class="input mono" formControlName="name" placeholder="ingest" />
          </label>
          <label>
            <span class="label">COMMAND</span>
            <input class="input mono" formControlName="command" placeholder="python" />
          </label>
          <label>
            <span class="label">ARGS (one per line)</span>
            <textarea class="input mono" rows="2" formControlName="argsRaw"></textarea>
          </label>
          <label>
            <span class="label">ENV (KEY=VALUE, one per line)</span>
            <textarea class="input mono" rows="3" formControlName="envRaw"
                      placeholder="PYTHONPATH=/app"></textarea>
          </label>
          <label>
            <span class="label">DEPENDS ON (other jobs)</span>
            <div class="deps">
              <label *ngFor="let other of dependsOnCandidates(j)" class="dep-check">
                <input type="checkbox"
                       [checked]="isDependedOn(j, other)"
                       (change)="toggleDep(j, other, $any($event.target).checked)" />
                <span class="mono">{{ other }}</span>
              </label>
              <span *ngIf="dependsOnCandidates(j).length === 0" class="pane__empty">
                No other jobs to depend on yet.
              </span>
            </div>
          </label>
          <div class="row-2">
            <label>
              <span class="label">TIMEOUT (s)</span>
              <input class="input mono" type="number" min="0" max="3600"
                     formControlName="timeout_seconds" />
            </label>
            <label>
              <span class="label">NODE SELECTOR (key=value)</span>
              <input class="input mono" formControlName="selectorRaw"
                     placeholder="runtime=rust" />
            </label>
          </div>
        </div>
      </ng-container>
      <ng-template #selectHint>
        <div class="pane__empty">Pick a job on the left to edit its fields.</div>
      </ng-template>
    </section>
  </div>

  <!-- ── Metadata ─────────────────────────────────────────────── -->
  <div class="meta" [formGroup]="metaForm">
    <label>
      <span class="label">WORKFLOW ID</span>
      <input class="input mono" formControlName="id" placeholder="my-wf" />
    </label>
    <label>
      <span class="label">WORKFLOW NAME</span>
      <input class="input mono" formControlName="name" placeholder="demo" />
    </label>
  </div>

  <!-- ── Generated JSON preview ────────────────────────────────── -->
  <div class="generated">
    <div class="label-row">
      <span class="label">GENERATED JSON</span>
      <span class="hint">Live preview of the body that will be POSTed.</span>
    </div>
    <pre class="generated__body mono">{{ generatedJSON }}</pre>
  </div>

  <div *ngIf="validationErrors.length > 0" class="errors">
    <div class="errors__title">
      <span class="material-icons">warning_amber</span>
      Schema rejected {{ validationErrors.length }} issue<span *ngIf="validationErrors.length > 1">s</span>:
    </div>
    <ul><li *ngFor="let e of validationErrors">{{ e }}</li></ul>
  </div>
  <div *ngIf="validationOk" class="ok">
    <span class="material-icons">check_circle</span>
    Client-side schema passed. Server will re-validate on submit.
  </div>
  <div *ngIf="submitError" class="errors">
    <div class="errors__title">
      <span class="material-icons">error</span> Server rejected submit
    </div>
    <pre class="errors__body">{{ submitError }}</pre>
  </div>

  <div class="actions">
    <button type="button" class="btn btn--secondary" (click)="onValidate()"
            [disabled]="submitting">
      <span class="material-icons">check</span> Validate
    </button>
    <button type="button" class="btn btn--primary"
            [disabled]="!validationOk || submitting"
            (click)="openPreview()">
      <span class="material-icons">preview</span> Preview + Submit
    </button>
  </div>
</div>

<app-submit-preview-dialog *ngIf="previewOpen"
  [title]="'Submit workflow ' + (metaForm.value.id || '(unnamed)')"
  [body]="previewBody"
  (resolved)="onPreviewResolved($event)">
</app-submit-preview-dialog>
  `,
  styles: [`
    .dag-builder { display: flex; flex-direction: column; gap: 12px; }

    .notice {
      display: flex; gap: 10px; align-items: flex-start;
      padding: 10px 14px;
      background: rgba(129, 140, 248, 0.08);
      border: 1px solid rgba(129, 140, 248, 0.25);
      border-radius: var(--radius-sm);
      color: #c8d0dc; font-size: 11px; line-height: 1.5;
    }
    .notice__icon { font-size: 16px; color: var(--color-dispatching); flex-shrink: 0; margin-top: 1px; }

    .dag-builder__panes {
      display: grid;
      grid-template-columns: 260px 1fr;
      gap: 12px;
      min-height: 320px;
    }
    .pane {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      display: flex; flex-direction: column;
    }
    .pane__header {
      display: flex; justify-content: space-between; align-items: center;
      padding: 10px 12px;
      border-bottom: 1px solid var(--color-border);
      background: var(--color-surface-2);
    }
    .pane__title {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
    }
    .pane__empty {
      padding: 24px 12px;
      text-align: center;
      color: var(--color-muted);
      font-size: 11px;
    }

    .job-list { list-style: none; margin: 0; padding: 0; }
    .job-item {
      display: grid;
      grid-template-columns: 24px 1fr auto;
      gap: 8px;
      align-items: center;
      padding: 8px 12px;
      font-size: 11px;
      color: #e8edf2;
      cursor: pointer;
      border-bottom: 1px solid var(--color-border);
    }
    .job-item:hover { background: rgba(192, 132, 252, 0.05); }
    .job-item--active {
      background: rgba(192, 132, 252, 0.12);
      border-left: 2px solid var(--color-accent);
    }
    .job-item__index { color: var(--color-muted); font-size: 10px; }
    .job-item__name { }

    .editor-fields {
      display: flex; flex-direction: column; gap: 10px;
      padding: 12px;
    }
    .row-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }

    .label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.08em;
      color: var(--color-accent);
      display: block;
      margin-bottom: 2px;
    }
    .label-row { display: flex; gap: 12px; align-items: baseline; }
    .hint { font-size: 10px; color: var(--color-muted); }

    .input {
      background: var(--color-bg);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #e8edf2;
      font-size: 11px;
      padding: 6px 10px;
      width: 100%;
      font-family: var(--font-mono);
    }
    .input:focus { outline: none; border-color: var(--color-accent); }
    .mono { font-family: var(--font-mono); }

    .deps {
      display: flex; flex-wrap: wrap; gap: 8px;
      padding: 4px 0;
    }
    .dep-check {
      display: inline-flex; align-items: center; gap: 4px;
      font-size: 11px;
      color: #c8d0dc;
      padding: 2px 8px;
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      cursor: pointer;
    }

    .meta {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 10px;
    }

    .generated {
      display: flex; flex-direction: column; gap: 4px;
    }
    .generated__body {
      background: var(--color-bg);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      padding: 10px 12px;
      font-size: 10px;
      color: #c8d0dc;
      max-height: 200px;
      overflow: auto;
      white-space: pre-wrap;
    }

    .btn-ghost {
      background: transparent;
      border: 1px solid var(--color-border);
      color: var(--color-muted);
      font-size: 10px;
      padding: 2px 8px;
      border-radius: var(--radius-sm);
      cursor: pointer;
    }
    .btn-ghost:hover { color: #e8edf2; }
    .btn-ghost--danger:hover { color: var(--color-error); border-color: var(--color-error); }

    .errors, .ok {
      padding: 10px 14px;
      border-radius: var(--radius-sm);
      font-size: 11px;
    }
    .errors {
      background: rgba(255, 82, 82, 0.08);
      border: 1px solid rgba(255, 82, 82, 0.25);
      color: var(--color-error);
    }
    .errors__title { display: flex; align-items: center; gap: 8px; font-weight: 600; margin-bottom: 4px; }
    .errors ul { margin: 4px 0 0 0; padding-left: 20px; }
    .errors__body { font-family: var(--font-mono); margin: 4px 0 0 0; white-space: pre-wrap; }
    .ok {
      background: rgba(68, 181, 95, 0.08);
      border: 1px solid rgba(68, 181, 95, 0.25);
      color: var(--color-completed);
      display: flex; align-items: center; gap: 8px;
    }

    .actions { display: flex; gap: 8px; }
    .btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 8px 16px;
      font-family: var(--font-ui); font-size: 11px; letter-spacing: 0.08em;
      border-radius: var(--radius-sm);
      cursor: pointer;
      border: 1px solid transparent;
    }
    .btn--secondary {
      background: var(--color-surface);
      border-color: var(--color-border);
      color: #e8edf2;
    }
    .btn--secondary:hover:not(:disabled) { border-color: var(--color-accent-dim); }
    .btn--primary {
      background: rgba(192, 132, 252, 0.16);
      border-color: var(--color-accent);
      color: var(--color-accent);
    }
    .btn--primary:hover:not(:disabled) { background: rgba(192, 132, 252, 0.24); }
    .btn:disabled { opacity: 0.45; cursor: not-allowed; }
  `],
})
export class SubmitDagBuilderComponent implements OnInit {
  metaForm!: FormGroup;
  jobsArray!: FormArray;
  activeIdx = -1;

  validationErrors: string[] = [];
  validationOk = false;
  submitting = false;
  submitError: string | null = null;

  previewOpen = false;
  previewBody: unknown = {};

  generatedJSON = '{}';

  constructor(private fb: FormBuilder, private api: ApiService, private router: Router) {}

  ngOnInit(): void {
    this.metaForm = this.fb.group({
      id:   ['', Validators.required],
      name: [''],
    });
    this.jobsArray = this.fb.array([]);

    // Recompute the live preview on every change to any field —
    // job list, job form, or metadata.
    this.metaForm.valueChanges.subscribe(() => this.refreshGenerated());
    this.jobsArray.valueChanges.subscribe(() => this.refreshGenerated());
    this.refreshGenerated();
  }

  get jobControls(): FormGroup[] { return this.jobsArray.controls as FormGroup[]; }

  get activeJob(): FormGroup | null {
    return this.activeIdx >= 0 && this.activeIdx < this.jobControls.length
      ? this.jobControls[this.activeIdx]
      : null;
  }

  addJob(): void {
    const g = this.fb.group({
      name:            [`job-${this.jobControls.length + 1}`, Validators.required],
      command:         ['', Validators.required],
      argsRaw:         [''],
      envRaw:          [''],
      depends_on:      this.fb.control<string[]>([]),
      timeout_seconds: [0],
      selectorRaw:     [''],
    });
    this.jobsArray.push(g);
    this.activeIdx = this.jobControls.length - 1;
  }

  removeJob(i: number): void {
    // Removing a job also strips it from every remaining job's
    // depends_on — otherwise the workflow references a missing
    // name and the validator rejects.
    const removedName = (this.jobControls[i].get('name')!.value as string) ?? '';
    this.jobsArray.removeAt(i);
    for (const j of this.jobControls) {
      const deps = (j.get('depends_on')!.value as string[]) ?? [];
      const next = deps.filter(d => d !== removedName);
      if (next.length !== deps.length) j.get('depends_on')!.setValue(next);
    }
    if (this.activeIdx >= this.jobControls.length) {
      this.activeIdx = this.jobControls.length - 1;
    }
  }

  select(i: number): void { this.activeIdx = i; }

  /**
   * Candidates for the depends_on multi-select on the currently
   * edited job: names of every OTHER job in the workflow.
   */
  dependsOnCandidates(job: FormGroup): string[] {
    const selfName = (job.get('name')!.value as string) ?? '';
    return this.jobControls
      .map(c => (c.get('name')!.value as string) ?? '')
      .filter(n => n && n !== selfName);
  }

  isDependedOn(job: FormGroup, dep: string): boolean {
    const deps = (job.get('depends_on')!.value as string[]) ?? [];
    return deps.includes(dep);
  }

  toggleDep(job: FormGroup, dep: string, checked: boolean): void {
    const deps = [...((job.get('depends_on')!.value as string[]) ?? [])];
    const i = deps.indexOf(dep);
    if (checked && i < 0) deps.push(dep);
    if (!checked && i >= 0) deps.splice(i, 1);
    job.get('depends_on')!.setValue(deps);
  }

  /**
   * Re-serialise the whole form into a SubmitWorkflowRequest and
   * cache the pretty-printed JSON. Called on every value change
   * so the preview pane stays in sync with what the user sees.
   */
  private refreshGenerated(): void {
    const body = this.buildBody();
    try { this.generatedJSON = JSON.stringify(body, null, 2); }
    catch { this.generatedJSON = '{}'; }
    // Any edit invalidates the last Validate verdict.
    this.validationOk = false;
    this.submitError = null;
  }

  private buildBody(): SubmitWorkflowRequest {
    const jobs: SubmitWorkflowJobRequest[] = this.jobControls.map(c => {
      const v = c.value;
      const args   = splitLines(v.argsRaw);
      const env    = parseEnvLines(v.envRaw);
      const sel    = parseKeyValue(v.selectorRaw);
      const job: SubmitWorkflowJobRequest = {
        name:    (v.name as string) ?? '',
        command: (v.command as string) ?? '',
      };
      if (args.length > 0) job.args = args;
      if (Object.keys(env).length > 0) job.env = env;
      if ((v.depends_on as string[])?.length) job.depends_on = v.depends_on as string[];
      if (v.timeout_seconds) job.timeout_seconds = Number(v.timeout_seconds);
      if (sel) job.node_selector = sel;
      return job;
    });
    const meta = this.metaForm.value;
    return {
      id:   (meta.id as string) ?? '',
      name: (meta.name as string) ?? '',
      jobs,
    };
  }

  onValidate(): void {
    this.validationErrors = [];
    this.validationOk = false;
    const { errors } = validateWorkflowShape(this.buildBody());
    if (errors.length > 0) {
      this.validationErrors = errors;
      return;
    }
    this.validationOk = true;
    this.previewBody = this.buildBody();
  }

  openPreview(): void {
    if (!this.validationOk) return;
    this.previewBody = this.buildBody();
    this.previewOpen = true;
  }

  onPreviewResolved(result: PreviewResult): void {
    this.previewOpen = false;
    if (result !== 'confirm') return;
    this.submitting = true;
    this.submitError = null;
    const body = this.previewBody as SubmitWorkflowRequest;
    this.api.submitWorkflow(body).subscribe({
      next: wf => {
        this.submitting = false;
        this.router.navigate(['/ml/pipelines', wf.id]);
      },
      error: (err: HttpErrorResponse) => {
        this.submitting = false;
        this.submitError =
          (err?.error && typeof err.error === 'object' && err.error.error) ||
          err?.message || 'submit failed';
      },
    });
  }
}

// ── helpers ──────────────────────────────────────────────────────────

function splitLines(raw: unknown): string[] {
  return String(raw ?? '').split('\n').map(s => s.trim()).filter(s => s.length > 0);
}

function parseKeyValue(raw: unknown): Record<string, string> | undefined {
  const s = String(raw ?? '').trim();
  if (!s) return undefined;
  const eq = s.indexOf('=');
  if (eq <= 0) return undefined;
  const k = s.slice(0, eq).trim();
  const v = s.slice(eq + 1).trim();
  return k ? { [k]: v } : undefined;
}

function parseEnvLines(raw: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of splitLines(raw)) {
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    const k = line.slice(0, eq).trim();
    const v = line.slice(eq + 1).trim();
    if (k) out[k] = v;
  }
  return out;
}
