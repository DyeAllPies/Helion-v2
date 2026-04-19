// src/app/features/submit/submit-workflow.component.ts
//
// Feature 22 step 3 — Workflow submission via a text editor.
//
// This is the "paste the YAML you would hand to submit.py"
// experience, adapted for v1 as a plain <textarea>:
//
//   - JSON is the only accepted format today. Full YAML support
//     lands with a Monaco + js-yaml upgrade noted in the feature
//     22 spec's open questions. JSON covers every shape the
//     workflow submit endpoint accepts — YAML is a convenience.
//   - Client-side schema validation runs on every Validate click:
//     structural shape + required fields + per-job nested shape.
//   - The server is still the authoritative validator. The
//     Validate button will swap to POST /workflows?dry_run=true
//     when feature 24 lands (code comment marks the spot).
//
// Secret env handling is pure client-side masking for the same
// reason the Job form had to defer it: feature 26 has not
// landed, so the server still echoes values on GET. Operators
// are told so via the top-of-form notice.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import { FormBuilder, FormGroup, ReactiveFormsModule, Validators } from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { ApiService } from '../../core/services/api.service';
import { SubmitWorkflowRequest } from '../../shared/models';
import { SubmitPreviewDialogComponent, PreviewResult } from './submit-preview-dialog.component';
import { validateWorkflowShape } from './workflow-shape-validator';

@Component({
  selector: 'app-submit-workflow',
  standalone: true,
  imports: [CommonModule, ReactiveFormsModule, SubmitPreviewDialogComponent],
  template: `
<form [formGroup]="form" class="wf-form" (ngSubmit)="onValidate()">

  <div class="notice">
    <span class="material-icons notice__icon">info</span>
    <span>
      Paste the workflow as JSON. YAML support arrives with a
      Monaco upgrade (feature 22 open question). Validate runs
      client-side schema checks; server dry-run preflight lands
      with <a href="https://github.com/DyeAllPies/Helion-v2/blob/main/docs/planned-features/24-dry-run-preflight.md"
             target="_blank" rel="noopener">feature 24</a>.
    </span>
  </div>

  <div class="row">
    <div class="label-row">
      <span class="label">WORKFLOW (JSON)</span>
      <span class="hint">Body cap 1 MiB · up to 100 jobs · matches the shape at <code>internal/api/handlers_workflows.go#SubmitWorkflowRequest</code>.</span>
    </div>
    <textarea class="input mono editor" rows="18"
              formControlName="text" spellcheck="false"
              placeholder='{"id":"my-wf","name":"demo","jobs":[{"name":"hello","command":"echo","args":["hi"]}]}'></textarea>
  </div>

  <!-- Parse + validate feedback -->
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
      <span class="material-icons">error</span>
      Server rejected submit
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

<app-submit-preview-dialog *ngIf="previewOpen"
  [title]="'Submit workflow ' + (parsedId || '(unnamed)')"
  [body]="previewBody"
  (resolved)="onPreviewResolved($event)">
</app-submit-preview-dialog>
  `,
  styles: [`
    .wf-form { display: flex; flex-direction: column; gap: 12px; max-width: 960px; }

    .notice {
      display: flex; gap: 10px; align-items: flex-start;
      padding: 10px 14px;
      background: rgba(129, 140, 248, 0.08);
      border: 1px solid rgba(129, 140, 248, 0.25);
      border-radius: var(--radius-sm);
      color: #c8d0dc; font-size: 11px; line-height: 1.5;
    }
    .notice__icon { font-size: 16px; color: var(--color-dispatching); flex-shrink: 0; margin-top: 1px; }
    .notice a { color: var(--color-accent); text-decoration: none; }
    .notice a:hover { text-decoration: underline; }

    .row { display: flex; flex-direction: column; gap: 4px; }
    .label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
      text-transform: uppercase;
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
    .mono { font-family: var(--font-mono); }
    .editor {
      min-height: 300px;
      tab-size: 2;
      line-height: 1.5;
      font-size: 11px;
    }

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
export class SubmitWorkflowComponent implements OnInit {
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
      // Any edit invalidates the last Validate verdict. The user
      // must re-Validate before the Preview button re-enables.
      this.validationOk = false;
      this.submitError = null;
    });
  }

  /**
   * Parse + validate the textarea contents. Populates one of
   * parseError / validationErrors / validationOk — exactly one at
   * a time — so the template can render a single feedback block.
   */
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
    this.previewBody   = normalised;
    this.parsedId      = (normalised as SubmitWorkflowRequest).id ?? '';
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
