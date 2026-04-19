// src/app/features/submit/submit-job.component.ts
//
// Feature 22 step 2 — Job submission form.
//
// Reactive form that mirrors the server-side SubmitRequest shape
// (see internal/api/handlers_jobs.go#handleSubmitJob). Every
// client-side validator is an advisory UX check — the server is
// the authoritative validator and its error messages are surfaced
// verbatim on a failed submit.
//
// Integration with the prerequisite features:
//
//   - Feature 24 (dry-run preflight) is NOT yet shipped, so the
//     Validate button runs the CLIENT-SIDE validator suite only.
//     A comment on the Validate handler names feature 24 as the
//     backend endpoint we will swap in once it lands.
//
//   - Feature 25 (env denylist) is NOT yet shipped. The form
//     rejects known-dangerous env keys (LD_PRELOAD, DYLD_*, ...)
//     at input time as an advisory check. Server-side rejection
//     is the load-bearing control; this is UX.
//
//   - Feature 26 (secret env vars) is NOT yet shipped. The
//     "secret" toggle flips the env-value <input> to
//     type="password" so casual bystanders don't read a pasted
//     token off the screen. Until 26 lands, the server still
//     echoes the value on a subsequent GET /jobs/{id} — every
//     secret-toggle control carries an explanatory tooltip to
//     avoid misleading the operator.
//
// The two-click Validate → Preview → Submit flow is deliberate
// (see feature 22 spec §UX flow). Accidental submits from muscle
// memory should be hard.

import { Component, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Router } from '@angular/router';
import {
  FormArray, FormBuilder, FormGroup, ReactiveFormsModule, Validators,
  AbstractControl, ValidationErrors,
} from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { ApiService } from '../../core/services/api.service';
import { SubmitJobRequest } from '../../shared/models';
import { SubmitPreviewDialogComponent, PreviewResult } from './submit-preview-dialog.component';

/**
 * Env keys the form blocks client-side because they are known
 * dynamic-loader injection vectors. This mirrors the list that
 * will ship server-side with feature 25. Keep the two lists in
 * sync: if you add a new prefix here, add it to the Go validator
 * AND to the feature 25 spec's test table. Client-side enforcement
 * alone is NOT a security boundary (any attacker can post JSON
 * directly to /jobs) — this is a UX nudge, not a sandbox.
 */
const CLIENT_ENV_DENYLIST_PREFIXES = ['LD_', 'DYLD_'];
const CLIENT_ENV_DENYLIST_EXACT = new Set([
  'GCONV_PATH', 'GIO_EXTRA_MODULES', 'HOSTALIASES', 'NLSPATH', 'RES_OPTIONS',
]);

function envKeyDenied(key: string): string | null {
  for (const p of CLIENT_ENV_DENYLIST_PREFIXES) {
    if (key.startsWith(p)) return `"${key}" is a dynamic-loader injection vector (${p}* prefix)`;
  }
  if (CLIENT_ENV_DENYLIST_EXACT.has(key)) {
    return `"${key}" is a known module-loading / resolver env var`;
  }
  return null;
}

/**
 * Key-shape validator: shell-identifier style. Anything else is
 * very unlikely to work as an env var anyway and hints at paste
 * errors.
 */
function envKeyShapeValidator(control: AbstractControl): ValidationErrors | null {
  const v = control.value as string;
  if (!v) return { required: true };
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(v)) {
    return { shape: 'must match ^[A-Za-z_][A-Za-z0-9_]*$' };
  }
  const deny = envKeyDenied(v);
  if (deny) return { denied: deny };
  return null;
}

function commandValidator(control: AbstractControl): ValidationErrors | null {
  const v = (control.value as string) ?? '';
  if (!v.trim()) return { required: true };
  if (v.length > 256) return { tooLong: 'command must be ≤ 256 bytes' };
  return null;
}

function commaListToArray(raw: string): string[] {
  return raw.split('\n').map(s => s.trim()).filter(s => s.length > 0);
}

@Component({
  selector: 'app-submit-job',
  standalone: true,
  imports: [CommonModule, ReactiveFormsModule, SubmitPreviewDialogComponent],
  template: `
<form [formGroup]="form" class="job-form" (ngSubmit)="onValidate()">

  <!-- ── Top notice: tell the user what is/isn't live yet ── -->
  <div class="notice">
    <span class="material-icons notice__icon">info</span>
    <span>
      Submitting goes through the live coordinator API. The
      <strong>Validate</strong> button runs client-side checks
      only; server dry-run preflight ships with
      <a href="https://github.com/DyeAllPies/Helion-v2/blob/main/docs/planned-features/24-dry-run-preflight.md"
         target="_blank" rel="noopener">feature 24</a>.
      Secret env vars are masked in this form AND redacted server-
      side on every GET path
      (<a href="https://github.com/DyeAllPies/Helion-v2/blob/main/docs/planned-features/implemented/26-secret-env-vars.md"
         target="_blank" rel="noopener">feature 26</a>).
      An admin can read a value back via POST /admin/jobs/&#123;id&#125;/reveal-secret;
      every reveal is audited.
    </span>
  </div>

  <!-- ── Required identity + command ─────────────────────── -->
  <div class="row">
    <label>
      <span class="label">JOB ID</span>
      <input class="input mono" formControlName="id" placeholder="hello-world" />
      <span class="hint">ULID or slug. Must be unique across all jobs.</span>
    </label>
  </div>

  <div class="row">
    <label>
      <span class="label">COMMAND</span>
      <input class="input mono" formControlName="command" placeholder="python" />
      <span class="hint">The subprocess to exec on the target node.</span>
    </label>
  </div>

  <div class="row">
    <label>
      <span class="label">ARGS (one per line)</span>
      <textarea class="input mono" rows="3" formControlName="argsRaw"
                placeholder="/app/ml-mnist/train.py"></textarea>
      <span class="hint">≤ 512 entries. Each line is passed as-is; no shell expansion.</span>
    </label>
  </div>

  <!-- ── Env vars ─────────────────────────────────────────── -->
  <div class="row">
    <div class="label-row">
      <span class="label">ENVIRONMENT</span>
      <button type="button" class="btn-ghost" (click)="addEnvEntry()">+ add var</button>
    </div>
    <div class="env-list" formArrayName="env">
      <div *ngFor="let entry of envControls; let i = index" [formGroupName]="i" class="env-entry">
        <input class="input mono env-entry__key" formControlName="key" placeholder="KEY" />
        <input class="input mono env-entry__value"
               [type]="entry.value.secret ? 'password' : 'text'"
               formControlName="value" placeholder="value" />
        <label class="env-entry__secret" title="Mask in UI AND send as a secret_keys entry. Server redacts values on every GET path.">
          <input type="checkbox" formControlName="secret" />
          <span>secret</span>
        </label>
        <button type="button" class="btn-ghost btn-ghost--danger" (click)="removeEnvEntry(i)">×</button>
      </div>
      <div *ngIf="envControls.length === 0" class="env-empty">
        No environment variables. Click "+ add var" to define one.
      </div>
    </div>
  </div>

  <!-- ── Resource bounds ──────────────────────────────────── -->
  <div class="row row--grid">
    <label>
      <span class="label">TIMEOUT (s)</span>
      <input class="input mono" type="number" min="0" max="3600"
             formControlName="timeout_seconds" />
      <span class="hint">0 = inherit cluster default. Hard cap 3600.</span>
    </label>
    <label>
      <span class="label">PRIORITY</span>
      <input class="input mono" type="number" min="0" max="100"
             formControlName="priority" />
      <span class="hint">Higher = scheduled sooner. Range 0-100.</span>
    </label>
    <label>
      <span class="label">GPUs</span>
      <input class="input mono" type="number" min="0" max="16"
             formControlName="gpus" />
      <span class="hint">Reservation count. 0 = no GPU.</span>
    </label>
  </div>

  <!-- ── Node selector ────────────────────────────────────── -->
  <div class="row">
    <label>
      <span class="label">NODE SELECTOR (one per line, <code>key=value</code>)</span>
      <textarea class="input mono" rows="2" formControlName="nodeSelectorRaw"
                placeholder="runtime=rust"></textarea>
      <span class="hint">Scheduler only picks nodes whose labels match every entry. Max 32 entries.</span>
    </label>
  </div>

  <!-- ── Form-wide errors ─────────────────────────────────── -->
  <div *ngIf="validationErrors.length > 0" class="errors">
    <div class="errors__title">
      <span class="material-icons">warning_amber</span>
      Fix {{ validationErrors.length }} issue<span *ngIf="validationErrors.length > 1">s</span>:
    </div>
    <ul>
      <li *ngFor="let e of validationErrors">{{ e }}</li>
    </ul>
  </div>

  <div *ngIf="validationOk" class="ok">
    <span class="material-icons">check_circle</span>
    Client-side checks passed. Server will re-validate on submit.
  </div>

  <div *ngIf="submitError" class="errors">
    <div class="errors__title">
      <span class="material-icons">error</span>
      Server rejected submit
    </div>
    <pre class="errors__body">{{ submitError }}</pre>
  </div>

  <!-- ── Actions ──────────────────────────────────────────── -->
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

<!--
  Preview dialog is rendered at the form level so a single shared
  component can back every tab (workflow + ml-workflow forms will
  reuse it when they land). Visibility toggled by *ngIf so the
  DOM is clean when no preview is open.
-->
<app-submit-preview-dialog *ngIf="previewOpen"
  [title]="'Submit job ' + form.value.id"
  [body]="previewBody"
  (resolved)="onPreviewResolved($event)">
</app-submit-preview-dialog>
  `,
  styles: [`
    .job-form { display: flex; flex-direction: column; gap: 16px; max-width: 720px; }

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
    .row--grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }

    .label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
      text-transform: uppercase;
    }
    .label-row { display: flex; justify-content: space-between; align-items: baseline; }
    .hint { font-size: 10px; color: var(--color-muted); margin-top: 2px; }

    .input {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #e8edf2;
      font-size: 12px;
      padding: 8px 10px;
      width: 100%;
      font-family: var(--font-mono);
    }
    .input:focus { outline: none; border-color: var(--color-accent); }
    .mono { font-family: var(--font-mono); }

    /* env list */
    .env-list { display: flex; flex-direction: column; gap: 6px; }
    .env-entry {
      display: grid;
      grid-template-columns: 140px 1fr auto auto;
      gap: 8px;
      align-items: center;
    }
    .env-entry__key   { font-size: 11px; }
    .env-entry__value { font-size: 11px; }
    .env-entry__secret {
      display: inline-flex; align-items: center; gap: 4px;
      font-size: 10px; color: var(--color-muted);
      cursor: pointer;
    }
    .env-empty {
      color: var(--color-muted);
      font-size: 11px;
      padding: 8px;
      border: 1px dashed var(--color-border);
      border-radius: var(--radius-sm);
      text-align: center;
    }

    /* buttons */
    .actions { display: flex; gap: 8px; margin-top: 12px; }
    .btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 8px 16px;
      font-family: var(--font-ui); font-size: 11px; letter-spacing: 0.08em;
      border-radius: var(--radius-sm);
      cursor: pointer;
      transition: background 0.12s, border-color 0.12s;
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

    .btn-ghost {
      background: transparent;
      border: 1px solid var(--color-border);
      color: var(--color-muted);
      font-size: 10px;
      padding: 3px 8px;
      border-radius: var(--radius-sm);
      cursor: pointer;
    }
    .btn-ghost:hover { color: #e8edf2; }
    .btn-ghost--danger:hover { color: var(--color-error); border-color: var(--color-error); }

    /* feedback */
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
  `],
})
export class SubmitJobComponent implements OnInit {
  form!: FormGroup;
  validationErrors: string[] = [];
  validationOk = false;
  submitting = false;
  submitError: string | null = null;
  previewOpen = false;
  previewBody: unknown = {};

  constructor(private fb: FormBuilder, private api: ApiService, private router: Router) {}

  ngOnInit(): void {
    this.form = this.fb.group({
      id:               ['', [Validators.required, Validators.maxLength(128)]],
      command:          ['', commandValidator],
      argsRaw:          [''],
      env:              this.fb.array([]),
      timeout_seconds:  [0, [Validators.min(0), Validators.max(3600)]],
      priority:         [50, [Validators.min(0), Validators.max(100)]],
      gpus:             [0, [Validators.min(0), Validators.max(16)]],
      nodeSelectorRaw:  [''],
    });

    // Clear the "client checks passed" indicator the moment the
    // user starts editing again — the verdict refers to the state
    // at Validate-click time, not the current form value.
    this.form.valueChanges.subscribe(() => {
      this.validationOk = false;
      this.submitError  = null;
    });
  }

  get envArray(): FormArray { return this.form.get('env') as FormArray; }
  get envControls(): FormGroup[] { return this.envArray.controls as FormGroup[]; }

  addEnvEntry(): void {
    this.envArray.push(this.fb.group({
      key:    ['', envKeyShapeValidator],
      value:  [''],
      secret: [false],
    }));
  }

  removeEnvEntry(idx: number): void { this.envArray.removeAt(idx); }

  /**
   * Client-side Validate. Runs Angular's reactive-form
   * validators + the denylist check + bounds that aren't
   * expressible via built-in Validators (e.g. duplicate env
   * keys). Server-side dry-run preflight replaces this once
   * feature 24 lands.
   */
  onValidate(): void {
    const errs: string[] = [];

    // Built-in validator rollup.
    this.form.markAllAsTouched();
    if (!this.form.get('id')!.valid) errs.push('JOB ID is required (≤ 128 chars).');
    if (!this.form.get('command')!.valid) errs.push('COMMAND is required (≤ 256 bytes).');
    if (!this.form.get('timeout_seconds')!.valid) errs.push('TIMEOUT must be between 0 and 3600.');
    if (!this.form.get('priority')!.valid) errs.push('PRIORITY must be between 0 and 100.');
    if (!this.form.get('gpus')!.valid) errs.push('GPUs must be between 0 and 16.');

    // Per-env-entry checks.
    const keys = new Set<string>();
    this.envControls.forEach((ctrl, i) => {
      const k = (ctrl.get('key')!.value as string) ?? '';
      const v = (ctrl.get('value')!.value as string) ?? '';
      if (!k) {
        errs.push(`env row ${i + 1}: key is required`);
        return;
      }
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k)) {
        errs.push(`env row ${i + 1}: key "${k}" must match ^[A-Za-z_][A-Za-z0-9_]*$`);
      }
      const denied = envKeyDenied(k);
      if (denied) errs.push(`env row ${i + 1}: ${denied}`);
      if (keys.has(k)) errs.push(`env row ${i + 1}: duplicate key "${k}"`);
      keys.add(k);
      if (v.length > 4096) errs.push(`env row ${i + 1}: value exceeds 4 KiB`);
    });

    // Node selector shape.
    const ns = this.parseNodeSelector();
    if (ns === null) {
      errs.push('NODE SELECTOR: each line must be key=value');
    } else if (Object.keys(ns).length > 32) {
      errs.push('NODE SELECTOR: more than 32 entries (max 32)');
    }

    // Args count cap.
    const args = commaListToArray(this.form.value.argsRaw || '');
    if (args.length > 512) errs.push('ARGS: more than 512 entries (max 512)');

    this.validationErrors = errs;
    this.validationOk = errs.length === 0;
  }

  /**
   * Parse the newline-delimited node-selector textarea into a
   * map. Returns null if any non-empty line fails to match
   * `key=value` — the caller treats that as a validation error.
   */
  private parseNodeSelector(): Record<string, string> | null {
    const raw = (this.form.value.nodeSelectorRaw as string) ?? '';
    const out: Record<string, string> = {};
    for (const line of raw.split('\n').map(l => l.trim()).filter(l => l.length > 0)) {
      const eq = line.indexOf('=');
      if (eq <= 0) return null;
      const key = line.slice(0, eq).trim();
      const val = line.slice(eq + 1).trim();
      if (!key) return null;
      out[key] = val;
    }
    return out;
  }

  /**
   * Open the Preview modal. Preview is the second click of the
   * two-click confirmation flow — it shows the exact JSON that
   * will be POSTed so the operator sees what they are committing
   * to before the actual network call.
   */
  openPreview(): void {
    if (!this.validationOk) return;
    this.previewBody = this.buildBody();
    this.previewOpen = true;
  }

  /**
   * Submit only fires from inside the Preview modal (via the
   * modal's resolved event). This component owns the HTTP call
   * so the modal can stay dumb / reusable across all three tabs.
   */
  onPreviewResolved(result: PreviewResult): void {
    this.previewOpen = false;
    if (result !== 'confirm') return;

    this.submitting = true;
    this.submitError = null;
    const body = this.buildBody();
    this.api.submitJob(body).subscribe({
      next: job => {
        this.submitting = false;
        this.router.navigate(['/jobs', job.id]);
      },
      error: (err: HttpErrorResponse) => {
        this.submitting = false;
        // Server response shape: `{ error: "...reason..." }`.
        // Fall back to the raw HTTP status message if absent.
        this.submitError =
          (err?.error && typeof err.error === 'object' && err.error.error) ||
          err?.message || 'submit failed';
      },
    });
  }

  /**
   * Build the exact SubmitJobRequest the API will receive.
   * Factored out so Preview + Submit see the same payload; if
   * they drift the modal becomes a lie.
   */
  private buildBody(): SubmitJobRequest {
    const v = this.form.value;
    const env: Record<string, string> = {};
    // Feature 26 — split the form's (key,value,secret) rows into the
    // flat `env` map + sibling `secret_keys` list the API accepts.
    // The secret flag on a row is only meaningful when that row has
    // a key; rows without a key are skipped for env AND for secret_keys.
    const secretKeys: string[] = [];
    for (const row of this.envControls) {
      const k = (row.get('key')!.value as string) ?? '';
      if (!k) continue;
      env[k] = (row.get('value')!.value as string) ?? '';
      if (row.get('secret')!.value === true) {
        secretKeys.push(k);
      }
    }
    const ns = this.parseNodeSelector() ?? {};
    const body: SubmitJobRequest = {
      id:              v.id,
      command:         v.command,
      args:            commaListToArray(v.argsRaw || ''),
      env:             Object.keys(env).length > 0 ? env : undefined,
      secret_keys:     secretKeys.length > 0 ? secretKeys : undefined,
      timeout_seconds: v.timeout_seconds ? Number(v.timeout_seconds) : undefined,
      priority:        v.priority != null ? Number(v.priority) : undefined,
      node_selector:   Object.keys(ns).length > 0 ? ns : undefined,
    };
    if (v.gpus && Number(v.gpus) > 0) {
      body.resources = { gpus: Number(v.gpus) };
    }
    return body;
  }
}
