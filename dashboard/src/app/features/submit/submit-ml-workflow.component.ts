// src/app/features/submit/submit-ml-workflow.component.ts
//
// Feature 22 step 4 — ML workflow tab.
//
// Thin layer over the same workflow-submit path as
// submit-workflow.component.ts, with a template picker on top:
// clicking a card pre-fills the textarea with a JSON template
// from ml-templates.ts so an operator can hit "Validate → Submit"
// without typing anything. "Custom" opens the textarea empty.
//
// The picker is a UX convenience, not a separate code path —
// a submitted ML workflow is just a workflow. We duplicate the
// textarea + Validate/Preview/Submit flow (rather than sharing
// the SubmitWorkflowComponent) because the two tabs otherwise
// need subtly different wrapping chrome, and route-level
// composition in Angular is more friction than duplicating a
// hundred lines of template. When Monaco lands the editor logic
// factors out; until then, straight-line code.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { FormBuilder, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { ApiService } from '../../core/services/api.service';
import { SubmitWorkflowRequest } from '../../shared/models';
import { SubmitPreviewDialogComponent, PreviewResult } from './submit-preview-dialog.component';
import { validateWorkflowShape } from './workflow-shape-validator';
import { TEMPLATE_CARDS, TemplateCard } from './ml-templates';

@Component({
  selector: 'app-submit-ml-workflow',
  standalone: true,
  imports: [CommonModule, ReactiveFormsModule, SubmitPreviewDialogComponent],
  template: `
<div class="ml-tab">

  <!-- ── Template picker ───────────────────────────────────── -->
  <div class="picker">
    <span class="picker__label">START FROM TEMPLATE</span>
    <div class="picker__cards" role="group">
      <button *ngFor="let card of cards"
              type="button"
              class="card"
              [class.card--active]="activeKey === card.key"
              (click)="pickTemplate(card)">
        <span class="material-icons card__icon">{{ card.icon }}</span>
        <span class="card__title">{{ card.title }}</span>
        <span class="card__blurb">{{ card.blurb }}</span>
      </button>
    </div>
  </div>

  <form [formGroup]="form" class="wf-form" (ngSubmit)="onValidate()">

    <div class="notice">
      <span class="material-icons notice__icon">info</span>
      <span>
        Templates are hard-coded to match
        <code class="mono">examples/ml-iris/workflow.yaml</code> and
        <code class="mono">examples/ml-mnist/workflow.yaml</code>.
        Edit the JSON freely before submitting — the picker is just
        a starting point.
      </span>
    </div>

    <div class="row">
      <div class="label-row">
        <span class="label">WORKFLOW (JSON)</span>
        <span class="hint">Body cap 1 MiB · up to 100 jobs.</span>
      </div>
      <textarea class="input mono editor" rows="18"
                formControlName="text" spellcheck="false"
                placeholder="Pick a template above, or paste your own JSON here."></textarea>
    </div>

    <div *ngIf="parseError" class="errors">
      <div class="errors__title">
        <span class="material-icons">error</span> JSON parse error
      </div>
      <pre class="errors__body">{{ parseError }}</pre>
    </div>

    <div *ngIf="validationErrors.length > 0" class="errors">
      <div class="errors__title">
        <span class="material-icons">warning_amber</span>
        Schema rejected {{ validationErrors.length }} issue<span *ngIf="validationErrors.length > 1">s</span>:
      </div>
      <ul>
        <li *ngFor="let e of validationErrors">{{ e }}</li>
      </ul>
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
      <button type="submit" class="btn btn--secondary" [disabled]="submitting">
        <span class="material-icons">check</span> Validate
      </button>
      <button type="button" class="btn btn--primary"
              [disabled]="!validationOk || submitting"
              (click)="openPreview()">
        <span class="material-icons">preview</span> Preview + Submit
      </button>
    </div>
  </form>
</div>

<app-submit-preview-dialog *ngIf="previewOpen"
  [title]="'Submit ML workflow ' + (parsedId || '(unnamed)')"
  [body]="previewBody"
  (resolved)="onPreviewResolved($event)">
</app-submit-preview-dialog>
  `,
  styles: [`
    .ml-tab { display: flex; flex-direction: column; gap: 16px; max-width: 960px; }

    /* Picker */
    .picker { display: flex; flex-direction: column; gap: 8px; }
    .picker__label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
    }
    .picker__cards {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
      gap: 10px;
    }
    .card {
      display: flex; flex-direction: column; gap: 4px;
      padding: 12px 14px;
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      cursor: pointer;
      text-align: left;
      transition: border-color 0.12s, background 0.12s;
    }
    .card:hover { border-color: var(--color-accent-dim); }
    .card--active {
      border-color: var(--color-accent);
      background: rgba(192, 132, 252, 0.08);
    }
    .card__icon { color: var(--color-accent-dim); font-size: 20px; }
    .card__title { font-family: var(--font-ui); font-size: 12px; color: #e8edf2; }
    .card__blurb { font-size: 10px; color: var(--color-muted); line-height: 1.4; }

    /* Editor */
    .wf-form { display: flex; flex-direction: column; gap: 12px; }
    .notice {
      display: flex; gap: 10px; align-items: flex-start;
      padding: 10px 14px;
      background: rgba(129, 140, 248, 0.08);
      border: 1px solid rgba(129, 140, 248, 0.25);
      border-radius: var(--radius-sm);
      color: #c8d0dc; font-size: 11px; line-height: 1.5;
    }
    .notice__icon { font-size: 16px; color: var(--color-dispatching); flex-shrink: 0; margin-top: 1px; }
    .notice code { font-size: 10px; padding: 1px 4px; background: rgba(255,255,255,0.05); border-radius: 2px; }
    .mono { font-family: var(--font-mono); }

    .row { display: flex; flex-direction: column; gap: 4px; }
    .label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
    }
    .label-row { display: flex; gap: 12px; align-items: baseline; justify-content: space-between; }
    .hint { font-size: 10px; color: var(--color-muted); }

    .input {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #e8edf2;
      font-size: 12px;
      padding: 10px 12px;
      width: 100%;
      font-family: var(--font-mono);
    }
    .input:focus { outline: none; border-color: var(--color-accent); }
    .editor { min-height: 300px; tab-size: 2; line-height: 1.5; font-size: 11px; }

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

    .actions { display: flex; gap: 8px; margin-top: 4px; }
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
export class SubmitMlWorkflowComponent implements OnInit {
  readonly cards: readonly TemplateCard[] = TEMPLATE_CARDS;
  activeKey: TemplateCard['key'] | null = null;

  form!: FormGroup;
  parseError: string | null = null;
  validationErrors: string[] = [];
  validationOk = false;
  submitting = false;
  submitError: string | null = null;
  previewOpen = false;
  previewBody: unknown = {};
  parsedId = '';

  constructor(private fb: FormBuilder, private api: ApiService, private router: Router) {}

  ngOnInit(): void {
    this.form = this.fb.group({
      text: ['', Validators.required],
    });
    this.form.valueChanges.subscribe(() => {
      this.validationOk = false;
      this.submitError = null;
    });
  }

  /**
   * Clicking a card pre-fills the editor. "Custom" clears the
   * editor so the operator starts from an empty textarea.
   */
  pickTemplate(card: TemplateCard): void {
    this.activeKey = card.key;
    if (card.template) {
      this.form.patchValue({ text: JSON.stringify(card.template, null, 2) });
    } else {
      this.form.patchValue({ text: '' });
    }
  }

  onValidate(): void {
    this.parseError = null;
    this.validationErrors = [];
    this.validationOk = false;

    const text = (this.form.value.text as string) ?? '';
    if (!text.trim()) {
      this.parseError = 'workflow body is empty';
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (err) {
      this.parseError = (err as Error).message;
      return;
    }
    const { errors, normalised } = validateWorkflowShape(parsed);
    if (errors.length > 0) {
      this.validationErrors = errors;
      return;
    }
    this.validationOk = true;
    this.previewBody = normalised;
    this.parsedId = (normalised as SubmitWorkflowRequest).id ?? '';
  }

  openPreview(): void {
    if (!this.validationOk) return;
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
