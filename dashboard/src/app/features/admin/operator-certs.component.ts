// src/app/features/admin/operator-certs.component.ts
//
// Feature 32 — admin-only dashboard for operator certificate
// lifecycle. Stands on features 27 (issuance), 31 (revocation),
// and 37 (authz). Server is the authoritative gate for every
// action on this page; the role guard is UX-only (avoids
// surfacing a confusing 403 to non-admins who click a deep
// link).
//
// The component packages three flows into one screen:
//
//   1. Issue — admin types a new operator CN + password, the
//      server mints a P12 bundle, the UI surfaces a one-time
//      download + the password (password shown exactly once,
//      not stashed in localStorage / cookies / IndexedDB).
//
//   2. List revocations — paginated table of every revoked
//      cert + its reason.
//
//   3. Revoke a serial — admin pastes a serial + reason, the
//      UI POSTs to the feature-31 revoke endpoint.
//
// Defensive behaviour per the feature 32 spec:
//
//   - The P12 blob URL is revoked immediately after the
//     download triggers (URL.revokeObjectURL), closing the
//     browser's blob-cache window.
//   - ngOnDestroy zeros the in-memory password + p12 bytes so
//     a subsequent route/component doesn't inherit them.
//   - The password field is read-only AFTER the admin ticks
//     the "I saved the password" checkbox — you can't go back
//     and copy it again by accident (which is the scenario
//     where shoulder-surfing would kick in).
//   - Random passwords generated client-side use crypto.
//     getRandomValues, not Math.random — the latter is not
//     suitable for credential-grade entropy even in UI.

import { Component, OnDestroy } from '@angular/core';
import { CommonModule } from '@angular/common';
import {
  FormBuilder, FormGroup, FormsModule, ReactiveFormsModule, Validators,
  AbstractControl, ValidationErrors,
} from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { ApiService } from '../../core/services/api.service';
import {
  IssueOperatorCertResponse, RevocationItem,
} from '../../shared/models';

// Client-side mirror of the server's CN validator. The server
// (internal/api/operator_cert.go validateIssueOperatorCertRequest)
// is authoritative; this check just catches common typos
// before the round trip.
function commonNameValidator(ctrl: AbstractControl): ValidationErrors | null {
  const v = (ctrl.value ?? '') as string;
  if (v === '') return null;
  if (v.length > 256) return { cnTooLong: 'Common name must be ≤256 bytes.' };
  if (v.includes('\x00')) return { cnNul: 'Common name cannot contain NUL.' };
  if (v.includes('=')) return { cnEquals: 'Common name cannot contain "=".' };
  return null;
}

function serialHexValidator(ctrl: AbstractControl): ValidationErrors | null {
  const v = ((ctrl.value ?? '') as string).trim();
  if (v === '') return null;
  const stripped = v.startsWith('0x') || v.startsWith('0X') ? v.slice(2) : v;
  if (!/^[0-9a-fA-F]+$/.test(stripped)) {
    return { serialBadHex: 'Serial must be hex.' };
  }
  if (stripped.length > 64) return { serialTooLong: 'Serial too long.' };
  return null;
}

@Component({
  selector: 'app-operator-certs',
  standalone: true,
  imports: [CommonModule, FormsModule, ReactiveFormsModule],
  template: `
<div class="page">
  <header class="page-header">
    <h1>Operator Certificates</h1>
    <p class="lede">
      Mint and revoke <strong>operator client certificates</strong>. Every
      action on this page is admin-only and audited server-side.
    </p>
  </header>

  <!-- ── Issue ────────────────────────────────────────── -->
  <section class="panel">
    <h2>Issue new cert</h2>

    <form [formGroup]="issueForm" (ngSubmit)="issue()" novalidate>
      <div class="row">
        <label>
          <span class="label">COMMON NAME</span>
          <input class="input mono" formControlName="common_name"
                 autocomplete="off" placeholder="alice@ops" />
          <span class="hint">The operator's human identifier. Stamped
            on every audit event the cert is used for.</span>
        </label>
      </div>

      <div class="row row--grid">
        <label>
          <span class="label">TTL (DAYS)</span>
          <input class="input mono" type="number" min="1" max="365"
                 formControlName="ttl_days" />
          <span class="hint">Default 90. Hard cap 365.</span>
        </label>

        <label>
          <span class="label">PKCS#12 PASSWORD</span>
          <div class="input-row">
            <input class="input mono"
                   [type]="pwVisible ? 'text' : 'password'"
                   [readonly]="passwordLocked"
                   autocomplete="one-time-code"
                   formControlName="p12_password" />
            <button type="button" class="btn-ghost"
                    (click)="generatePassword()" [disabled]="passwordLocked"
                    title="Generate a 24-char random password">
              <span class="material-icons">casino</span> GEN
            </button>
            <button type="button" class="btn-ghost"
                    (click)="pwVisible = !pwVisible" [disabled]="passwordLocked">
              <span class="material-icons">
                {{ pwVisible ? 'visibility_off' : 'visibility' }}
              </span>
            </button>
          </div>
          <span class="hint">Minimum 8 chars. The operator needs this at
            browser import time — the server does NOT store it.</span>
        </label>
      </div>

      <div *ngIf="issueError" class="errors">
        <div class="errors__title">
          <span class="material-icons">error</span>
          Server rejected issuance
        </div>
        <pre class="errors__body">{{ issueError }}</pre>
      </div>

      <div class="actions">
        <button type="submit" class="btn btn--primary"
                [disabled]="issueForm.invalid || issuing || passwordLocked">
          <span class="material-icons">add_moderator</span>
          {{ issuing ? 'Issuing…' : 'Issue cert' }}
        </button>
      </div>
    </form>
  </section>

  <!-- ── Issued result (one-time download) ────────────── -->
  <section class="panel panel--result" *ngIf="issued">
    <h2>
      <span class="material-icons">verified_user</span>
      Cert issued — download once
    </h2>
    <p class="lede">
      This download is one-time. The server does not retain the
      private key. If you lose this file, revoke the serial
      below and issue a fresh cert.
    </p>

    <dl class="kv">
      <dt>Common name</dt><dd class="mono">{{ issued.common_name }}</dd>
      <dt>Serial</dt><dd class="mono">{{ issued.serial_hex }}</dd>
      <dt>Fingerprint (SHA-256)</dt><dd class="mono short">{{ issued.fingerprint_hex }}</dd>
      <dt>Valid from</dt><dd>{{ issued.not_before }}</dd>
      <dt>Valid until</dt><dd>{{ issued.not_after }}</dd>
    </dl>

    <div class="pw-display" *ngIf="!passwordLocked">
      <div class="pw-display__label">PKCS#12 PASSWORD (SHOWN ONCE)</div>
      <div class="pw-display__value mono">{{ issuedPassword }}</div>
      <button type="button" class="btn-ghost" (click)="copyPassword()">
        <span class="material-icons">content_copy</span> COPY
      </button>
    </div>

    <label class="confirm">
      <input type="checkbox" [(ngModel)]="passwordSaved" [ngModelOptions]="{standalone: true}"
             (change)="onPasswordSaved()" />
      <span>I have saved the password somewhere safe.
        Once I tick this, the password disappears.</span>
    </label>

    <div class="actions">
      <button type="button" class="btn btn--primary"
              [disabled]="!passwordSaved || !p12BlobUrl"
              (click)="downloadP12()">
        <span class="material-icons">download</span> Download P12
      </button>
      <button type="button" class="btn btn--secondary" (click)="clearIssued()">
        <span class="material-icons">close</span> Clear
      </button>
    </div>

    <p class="audit-notice" *ngIf="issued.audit_notice">
      <span class="material-icons">info</span>
      {{ issued.audit_notice }}
    </p>
  </section>

  <!-- ── Revoke a serial ─────────────────────────────── -->
  <section class="panel">
    <h2>Revoke a cert</h2>

    <form [formGroup]="revokeForm" (ngSubmit)="revoke()" novalidate>
      <div class="row">
        <label>
          <span class="label">SERIAL (HEX)</span>
          <input class="input mono" formControlName="serial"
                 autocomplete="off" placeholder="abcdef1234" />
          <span class="hint">Copy from the "Issued certs" table below or
            from the original issuance response.</span>
        </label>
      </div>
      <div class="row">
        <label>
          <span class="label">REASON (REQUIRED)</span>
          <input class="input" formControlName="reason"
                 autocomplete="off" placeholder="alice left the team" />
          <span class="hint">Stored verbatim in the audit log + revocation record.</span>
        </label>
      </div>

      <div *ngIf="revokeError" class="errors">
        <div class="errors__title">
          <span class="material-icons">error</span>
          Server rejected revoke
        </div>
        <pre class="errors__body">{{ revokeError }}</pre>
      </div>

      <div *ngIf="revokeSuccess" class="ok">
        <span class="material-icons">check_circle</span>
        Serial <span class="mono">{{ revokeSuccess.serial_hex }}</span>
        revoked<span *ngIf="revokeSuccess.idempotent"> (was already revoked)</span>.
      </div>

      <div class="actions">
        <button type="submit" class="btn btn--primary"
                [disabled]="revokeForm.invalid || revoking">
          <span class="material-icons">block</span>
          {{ revoking ? 'Revoking…' : 'Revoke' }}
        </button>
      </div>
    </form>
  </section>

  <!-- ── Revocations list ────────────────────────────── -->
  <section class="panel">
    <h2>
      Revoked certs <span class="count" *ngIf="revocations">({{ revocations.length }})</span>
    </h2>
    <p *ngIf="revocationsLoadError" class="errors__body">{{ revocationsLoadError }}</p>

    <table *ngIf="revocations && revocations.length > 0" class="rev-table">
      <thead>
        <tr>
          <th>Serial</th>
          <th>Common name</th>
          <th>Revoked at</th>
          <th>Revoked by</th>
          <th>Reason</th>
        </tr>
      </thead>
      <tbody>
        <tr *ngFor="let r of revocations">
          <td class="mono short">{{ r.serial_hex }}</td>
          <td>{{ r.common_name }}</td>
          <td>{{ r.revoked_at }}</td>
          <td class="mono">{{ r.revoked_by }}</td>
          <td>{{ r.reason }}</td>
        </tr>
      </tbody>
    </table>

    <p *ngIf="revocations && revocations.length === 0" class="env-empty">
      No revoked certs.
    </p>

    <div class="actions">
      <button type="button" class="btn btn--secondary" (click)="loadRevocations()">
        <span class="material-icons">refresh</span> Refresh
      </button>
    </div>
  </section>
</div>
  `,
  styles: [`
    .page { display: flex; flex-direction: column; gap: 16px; max-width: 960px; padding: 16px; }
    .page-header h1 { margin: 0 0 4px 0; font-family: var(--font-ui); font-size: 18px; letter-spacing: 0.05em; }
    .lede { color: var(--color-muted); font-size: 12px; margin: 0; }

    .panel {
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      background: var(--color-surface);
      padding: 16px 20px;
    }
    .panel h2 {
      margin: 0 0 12px 0; display: flex; align-items: center; gap: 6px;
      font-family: var(--font-ui); font-size: 13px; letter-spacing: 0.1em; color: var(--color-accent);
      text-transform: uppercase;
    }
    .panel h2 .count { color: var(--color-muted); font-size: 11px; font-weight: normal; }
    .panel--result {
      border-color: var(--color-accent);
      background: rgba(192, 132, 252, 0.06);
    }

    form { display: flex; flex-direction: column; gap: 12px; }
    .row { display: flex; flex-direction: column; gap: 4px; }
    .row--grid { display: grid; grid-template-columns: 140px 1fr; gap: 12px; }

    .label {
      font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em;
      color: var(--color-accent);
      text-transform: uppercase;
    }
    .hint { font-size: 10px; color: var(--color-muted); margin-top: 2px; }

    .input {
      background: var(--color-bg);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #e8edf2;
      font-size: 12px;
      padding: 8px 10px;
      width: 100%;
      font-family: var(--font-mono);
    }
    .input:focus { outline: none; border-color: var(--color-accent); }
    .input[readonly] { opacity: 0.55; cursor: not-allowed; }
    .mono { font-family: var(--font-mono); }
    .short { word-break: break-all; font-size: 11px; }

    .input-row { display: flex; gap: 6px; align-items: center; }
    .input-row .input { flex: 1; }

    .kv {
      display: grid; grid-template-columns: 160px 1fr; gap: 4px 16px;
      margin: 0 0 14px 0; font-size: 12px;
    }
    .kv dt { color: var(--color-muted); font-family: var(--font-ui); font-size: 10px; letter-spacing: 0.05em; text-transform: uppercase; }
    .kv dd { margin: 0; color: #e8edf2; }

    .pw-display {
      display: flex; align-items: center; gap: 8px;
      background: rgba(192, 132, 252, 0.08);
      border: 1px dashed var(--color-accent);
      border-radius: var(--radius-sm);
      padding: 10px 14px;
      margin: 10px 0;
    }
    .pw-display__label {
      font-family: var(--font-ui); font-size: 10px;
      letter-spacing: 0.1em; color: var(--color-accent);
      text-transform: uppercase; white-space: nowrap;
    }
    .pw-display__value { flex: 1; font-size: 14px; letter-spacing: 0.05em; color: #e8edf2; }

    .confirm {
      display: flex; gap: 8px; align-items: flex-start;
      margin: 10px 0; font-size: 12px; color: #e8edf2;
      line-height: 1.5;
    }
    .confirm input { margin-top: 2px; }

    .audit-notice {
      display: flex; gap: 8px; align-items: flex-start;
      margin-top: 12px; padding: 8px 12px;
      background: rgba(129, 140, 248, 0.08);
      border-left: 2px solid #818cf8;
      border-radius: var(--radius-sm);
      color: #c8d0dc; font-size: 11px;
    }
    .audit-notice .material-icons { font-size: 14px; color: #818cf8; }

    .actions { display: flex; gap: 8px; margin-top: 8px; }
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
      background: var(--color-surface); border-color: var(--color-border);
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
      background: transparent; border: 1px solid var(--color-border);
      color: var(--color-muted); font-size: 11px;
      padding: 6px 10px;
      border-radius: var(--radius-sm); cursor: pointer;
      display: inline-flex; align-items: center; gap: 4px;
    }
    .btn-ghost:hover:not(:disabled) { color: #e8edf2; border-color: var(--color-accent-dim); }
    .btn-ghost:disabled { opacity: 0.4; cursor: not-allowed; }

    .errors, .ok {
      padding: 10px 14px;
      border-radius: var(--radius-sm);
      font-size: 12px;
    }
    .errors { background: rgba(255, 82, 82, 0.08); border: 1px solid rgba(255, 82, 82, 0.3); color: #ff9b9b; }
    .errors__title { display: flex; align-items: center; gap: 6px; font-weight: bold; }
    .errors__body { margin: 4px 0 0 0; font-family: var(--font-mono); font-size: 11px; white-space: pre-wrap; }
    .ok { background: rgba(52, 211, 153, 0.1); border: 1px solid rgba(52, 211, 153, 0.3); color: #6ee7b7; display: flex; align-items: center; gap: 6px; }

    .env-empty {
      color: var(--color-muted); font-size: 12px;
      padding: 14px; border: 1px dashed var(--color-border);
      border-radius: var(--radius-sm); text-align: center;
    }

    .rev-table {
      width: 100%; border-collapse: collapse; font-size: 11px; margin-bottom: 12px;
    }
    .rev-table th, .rev-table td {
      text-align: left; padding: 6px 8px;
      border-bottom: 1px solid var(--color-border);
    }
    .rev-table th {
      color: var(--color-muted); font-family: var(--font-ui);
      font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase;
    }
  `],
})
export class OperatorCertsComponent implements OnDestroy {
  readonly issueForm: FormGroup;
  readonly revokeForm: FormGroup;

  issuing = false;
  issueError: string | null = null;
  issued: IssueOperatorCertResponse | null = null;
  issuedPassword = '';
  passwordSaved = false;
  passwordLocked = false;
  pwVisible = false;
  p12BlobUrl: string | null = null;

  revoking = false;
  revokeError: string | null = null;
  revokeSuccess: { serial_hex: string; idempotent: boolean } | null = null;

  revocations: RevocationItem[] | null = null;
  revocationsLoadError: string | null = null;

  constructor(private fb: FormBuilder, private api: ApiService) {
    this.issueForm = this.fb.group({
      common_name: ['', [Validators.required, Validators.minLength(1), commonNameValidator]],
      ttl_days:    [90, [Validators.required, Validators.min(1), Validators.max(365)]],
      p12_password: ['', [Validators.required, Validators.minLength(8)]],
    });
    this.revokeForm = this.fb.group({
      serial: ['', [Validators.required, serialHexValidator]],
      reason: ['', [Validators.required, Validators.minLength(1)]],
    });
    this.loadRevocations();
  }

  // ── Issue flow ───────────────────────────────────────

  generatePassword(): void {
    // crypto.getRandomValues is the browser's CSPRNG — the
    // right tool for credential-grade entropy. We map bytes
    // to a URL-safe alphabet; 24 chars of this alphabet yields
    // ~144 bits of entropy, comfortably above the ≥8-char
    // server minimum + any realistic brute-force window.
    const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
    const len = 24;
    const buf = new Uint8Array(len);
    crypto.getRandomValues(buf);
    let out = '';
    for (let i = 0; i < len; i++) {
      out += alphabet.charAt(buf[i] % alphabet.length);
    }
    this.issueForm.patchValue({ p12_password: out });
  }

  issue(): void {
    if (this.issueForm.invalid || this.issuing) return;
    this.issueError = null;
    this.issuing = true;

    const raw = this.issueForm.getRawValue() as {
      common_name: string; ttl_days: number; p12_password: string;
    };
    const cn = raw.common_name.trim();
    const password = raw.p12_password;

    this.api.issueOperatorCert({
      common_name: cn,
      ttl_days: raw.ttl_days,
      p12_password: password,
    }).subscribe({
      next: resp => {
        this.issuing = false;
        this.issued = resp;
        this.issuedPassword = password;
        this.passwordSaved = false;
        this.passwordLocked = false;
        this.p12BlobUrl = this._buildP12BlobUrl(resp.p12_base64);
        // Clear the password from the form so a refresh /
        // reissue cycle doesn't quietly reuse it.
        this.issueForm.patchValue({ p12_password: '' });
      },
      error: (err: HttpErrorResponse) => {
        this.issuing = false;
        this.issueError = this._formatError(err, 'Issuance failed.');
      },
    });
  }

  onPasswordSaved(): void {
    if (!this.passwordSaved) return;
    // The admin ticked the confirmation — clear the password
    // from view and lock the password input. The P12 download
    // button becomes enabled in its place.
    this.issuedPassword = '';
    this.passwordLocked = true;
  }

  downloadP12(): void {
    if (!this.p12BlobUrl || !this.issued) return;
    const a = document.createElement('a');
    a.href = this.p12BlobUrl;
    a.download = `${this.issued.common_name}.p12`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    // Revoke the blob URL so the browser drops the reference.
    // Safe to call even if the download is still kicking off —
    // the browser reads the blob when the click handler fires,
    // not asynchronously.
    URL.revokeObjectURL(this.p12BlobUrl);
    this.p12BlobUrl = null;
  }

  clearIssued(): void {
    if (this.p12BlobUrl) {
      URL.revokeObjectURL(this.p12BlobUrl);
      this.p12BlobUrl = null;
    }
    this.issued = null;
    this.issuedPassword = '';
    this.passwordSaved = false;
    this.passwordLocked = false;
  }

  async copyPassword(): Promise<void> {
    if (!this.issuedPassword) return;
    try {
      await navigator.clipboard.writeText(this.issuedPassword);
    } catch {
      // Clipboard may be gated (older browsers / http
      // contexts). Best-effort — the admin can still
      // select-all + copy manually from the on-screen
      // display.
    }
  }

  // ── Revoke flow ──────────────────────────────────────

  revoke(): void {
    if (this.revokeForm.invalid || this.revoking) return;
    this.revokeError = null;
    this.revokeSuccess = null;
    this.revoking = true;

    const raw = this.revokeForm.getRawValue() as { serial: string; reason: string };
    const serial = raw.serial.trim().replace(/^0x/i, '');

    this.api.revokeOperatorCert(serial, { reason: raw.reason.trim() }).subscribe({
      next: resp => {
        this.revoking = false;
        this.revokeSuccess = {
          serial_hex: resp.serial_hex,
          idempotent: resp.idempotent,
        };
        this.revokeForm.patchValue({ reason: '' });
        // Refresh the list so the just-revoked serial appears
        // without the admin having to click Refresh.
        this.loadRevocations();
      },
      error: (err: HttpErrorResponse) => {
        this.revoking = false;
        this.revokeError = this._formatError(err, 'Revoke failed.');
      },
    });
  }

  // ── List ─────────────────────────────────────────────

  loadRevocations(): void {
    this.revocationsLoadError = null;
    this.api.listRevocations().subscribe({
      next: resp => {
        this.revocations = resp.revocations ?? [];
      },
      error: (err: HttpErrorResponse) => {
        this.revocations = null;
        this.revocationsLoadError = this._formatError(err, 'Failed to load revocations.');
      },
    });
  }

  // ── Private ──────────────────────────────────────────

  private _buildP12BlobUrl(b64: string): string {
    // Decode base64 → Uint8Array → Blob. All in memory;
    // the blob URL is revoked on download or route change.
    const bin = atob(b64);
    const buf = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
      buf[i] = bin.charCodeAt(i);
    }
    const blob = new Blob([buf], { type: 'application/x-pkcs12' });
    return URL.createObjectURL(blob);
  }

  private _formatError(err: HttpErrorResponse, fallback: string): string {
    if (err.status === 0) return 'Network unreachable.';
    if (err.status === 403) return 'Admin role required. Your JWT lacks role=admin.';
    if (err.status === 401) return 'Session expired. Please sign in again.';
    const body = err.error;
    if (body && typeof body === 'object' && typeof body.error === 'string') {
      return `${err.status}: ${body.error}`;
    }
    if (typeof body === 'string' && body !== '') return `${err.status}: ${body}`;
    return `${err.status}: ${err.statusText || fallback}`;
  }

  ngOnDestroy(): void {
    // Defensive cleanup: route change after a successful
    // issuance must not leave the P12 bytes or password in
    // component state.
    if (this.p12BlobUrl) {
      URL.revokeObjectURL(this.p12BlobUrl);
      this.p12BlobUrl = null;
    }
    this.issued = null;
    this.issuedPassword = '';
    this.passwordSaved = false;
    this.passwordLocked = false;
  }
}
