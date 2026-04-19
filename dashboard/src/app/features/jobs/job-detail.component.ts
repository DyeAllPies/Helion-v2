// src/app/features/jobs/job-detail.component.ts
//
// JobDetailComponent fetches job metadata via REST, then connects to
// GET /ws/jobs/{id}/logs to stream live log output.
// WebSocket subscription is torn down when the component is destroyed
// or when the job reaches a terminal status.

import { Component, OnInit, OnDestroy, Input, ViewChild, ElementRef, AfterViewChecked } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { Subscription } from 'rxjs';

import { ApiService } from '../../core/services/api.service';
import { WebSocketService } from '../../core/services/websocket.service';
import { Job, LogChunk } from '../../shared/models';

@Component({
  selector: 'app-job-detail',
  standalone: true,
  imports: [CommonModule, RouterLink],
  template: `
<div class="page">
  <!-- Breadcrumb -->
  <nav class="breadcrumb">
    <a routerLink="/jobs">JOBS</a>
    <span class="material-icons">chevron_right</span>
    <span class="current">{{ id }}</span>
  </nav>

  <!-- Loading -->
  <div class="loading-row" *ngIf="loading">
    <span class="material-icons spin">sync</span> Loading job...
  </div>

  <!-- Error -->
  <div class="error-banner" *ngIf="error">
    <span class="material-icons">warning_amber</span> {{ error }}
  </div>

  <div *ngIf="job" class="job-content">

    <!-- Metadata card -->
    <div class="meta-card">
      <div class="meta-card__header">
        <div>
          <span class="badge" [class]="'badge-' + job.status">{{ job.status | uppercase }}</span>
          <span class="job-id">{{ job.id }}</span>
        </div>
        <button class="refresh-btn" (click)="refreshMeta()">
          <span class="material-icons">refresh</span>
        </button>
      </div>

      <div class="meta-grid">
        <div class="meta-item">
          <span class="meta-label">COMMAND</span>
          <span class="meta-value cmd">{{ job.command }} {{ job.args.join(' ') }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">NODE</span>
          <span class="meta-value">{{ job.node_id || '—' }}</span>
        </div>
        <div class="meta-item" *ngIf="job.runtime">
          <span class="meta-label">RUNTIME</span>
          <span class="meta-value">{{ job.runtime | uppercase }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">CREATED</span>
          <span class="meta-value">{{ job.created_at | date:'yyyy-MM-dd HH:mm:ss' }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">FINISHED</span>
          <span class="meta-value">{{ job.finished_at ? (job.finished_at | date:'yyyy-MM-dd HH:mm:ss') : '—' }}</span>
        </div>
        <div class="meta-item" *ngIf="job.exit_code != null">
          <span class="meta-label">EXIT CODE</span>
          <span class="meta-value" [class.text-error]="job.exit_code !== 0">{{ job.exit_code }}</span>
        </div>
        <div class="meta-item" *ngIf="job.error">
          <span class="meta-label">ERROR</span>
          <span class="meta-value text-error">{{ job.error }}</span>
        </div>
      </div>
    </div>

    <!-- Env + Secrets card (feature 26) -->
    <div class="env-card" *ngIf="hasEnv()">
      <div class="env-card__header">
        <span class="material-icons">vpn_key</span>
        <span>ENVIRONMENT</span>
        <span class="env-card__hint"
              *ngIf="(job.secret_keys?.length || 0) > 0"
              title="Secret values are redacted on every GET. An admin may reveal via POST /admin/jobs/&#123;id&#125;/reveal-secret — every call is audited.">
          {{ job.secret_keys?.length }} secret{{ (job.secret_keys?.length || 0) === 1 ? '' : 's' }}
        </span>
      </div>
      <div class="env-list">
        <div class="env-row" *ngFor="let entry of envEntries()">
          <span class="env-row__key mono">{{ entry.key }}</span>
          <span class="env-row__eq">=</span>
          <span class="env-row__value mono"
                [class.env-row__value--secret]="entry.isSecret">{{ entry.value }}</span>
          <span class="env-row__badge" *ngIf="entry.isSecret" title="Declared secret at submit; value redacted in this response.">SECRET</span>
          <button class="btn-reveal"
                  *ngIf="entry.isSecret"
                  type="button"
                  [disabled]="entry.revealing"
                  (click)="revealSecret(entry.key)"
                  title="Admin-only. Writes an audit event carrying actor + reason.">
            <span class="material-icons">visibility</span>
            {{ entry.revealing ? 'REVEALING…' : 'REVEAL' }}
          </button>
        </div>
      </div>
      <div class="env-card__reveal" *ngIf="revealOutcome">
        <div class="env-card__reveal-title">
          <span class="material-icons">warning</span>
          {{ revealOutcome.ok ? 'Secret revealed — this action was audited' : 'Reveal failed' }}
        </div>
        <div class="env-card__reveal-body" *ngIf="revealOutcome.ok">
          <span class="env-card__reveal-kv mono">{{ revealOutcome.key }} = {{ revealOutcome.value }}</span>
          <div class="env-card__reveal-notice">{{ revealOutcome.notice }}</div>
        </div>
        <div class="env-card__reveal-body text-error" *ngIf="!revealOutcome.ok">
          {{ revealOutcome.error }}
        </div>
        <button class="btn-ghost" type="button" (click)="dismissReveal()">dismiss</button>
      </div>
    </div>

    <!-- Log viewer -->
    <div class="log-panel">
      <div class="log-panel__header">
        <span class="material-icons log-icon">terminal</span>
        <span>EXECUTION LOG</span>
        <span class="log-badge" [class.log-badge--live]="wsConnected">
          {{ wsConnected ? '● LIVE' : (wsEnded ? '○ ENDED' : '○ CONNECTING') }}
        </span>
        <span class="log-line-count">{{ logLines.length }} lines</span>
        <button class="scroll-btn" (click)="scrollToBottom()" title="Scroll to bottom">
          <span class="material-icons">arrow_downward</span>
        </button>
      </div>

      <div class="log-body" #logBody>
        <div class="log-empty" *ngIf="logLines.length === 0 && !wsConnected">
          Waiting for log output...
        </div>
        <div class="log-line" *ngFor="let line of logLines; let i = index">
          <span class="line-num">{{ (i + 1).toString().padStart(4, ' ') }}</span>
          <span class="line-text">{{ line }}</span>
        </div>
        <div class="log-cursor" *ngIf="wsConnected">▌</div>
      </div>
    </div>

  </div>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .breadcrumb {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      color: var(--color-muted);
      margin-bottom: 20px;

      a { color: var(--color-accent-dim); text-decoration: none; &:hover { color: var(--color-accent); } }
      .material-icons { font-size: 14px; }
      .current { color: #c8d0dc; }
    }

    .loading-row {
      display: flex; align-items: center; gap: 8px;
      color: var(--color-muted); font-size: 12px;
      .material-icons { font-size: 16px; }
    }

    .spin { animation: spin 0.8s linear infinite; }
    @keyframes spin { to { transform: rotate(360deg); } }

    .error-banner {
      display: flex; align-items: center; gap: 8px;
      background: rgba(255,82,82,0.08); border: 1px solid rgba(255,82,82,0.3);
      border-radius: var(--radius-sm); color: var(--color-error);
      font-size: 12px; padding: 10px 14px; margin-bottom: 16px;
    }

    .job-content { display: flex; flex-direction: column; gap: 20px; }

    /* ── Meta card ── */
    .meta-card {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
    }

    .meta-card__header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 14px 18px;
      background: var(--color-surface-2);
      border-bottom: 1px solid var(--color-border);
      gap: 12px;

      > div { display: flex; align-items: center; gap: 12px; }
    }

    .job-id {
      font-size: 12px;
      color: var(--color-info);
      letter-spacing: 0.02em;
    }

    .refresh-btn {
      background: none; border: 1px solid var(--color-border);
      border-radius: var(--radius-sm); color: var(--color-muted);
      padding: 4px 7px; cursor: pointer; display: flex; align-items: center;
      transition: color 0.15s, border-color 0.15s;
      .material-icons { font-size: 15px; }
      &:hover { color: var(--color-accent); border-color: rgba(192,132,252,0.4); }
    }

    .meta-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
      gap: 0;
      padding: 0;
    }

    .meta-item {
      padding: 12px 18px;
      border-right: 1px solid var(--color-border-soft);
      border-bottom: 1px solid var(--color-border-soft);
    }

    .meta-label {
      display: block;
      font-size: 9px;
      letter-spacing: 0.12em;
      color: var(--color-accent);
      margin-bottom: 4px;
    }

    .meta-value {
      font-size: 12px;
      color: #c8d0dc;
      &.cmd { font-family: var(--font-mono); color: var(--color-info); }
    }

    /* ── Log panel ── */
    .log-panel {
      background: #07080b;
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      overflow: hidden;
    }

    .log-panel__header {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 10px 14px;
      background: var(--color-surface);
      border-bottom: 1px solid var(--color-border);
      font-size: 11px;
      letter-spacing: 0.07em;
      color: #8896aa;

      .log-icon { font-size: 16px; color: var(--color-accent-dim); }
    }

    .log-badge {
      margin-left: auto;
      font-size: 10px;
      letter-spacing: 0.06em;
      color: var(--color-muted);

    }

    .log-badge--live {
      color: var(--color-accent);
      animation: pulse-text 1.5s infinite;
    }

    @keyframes pulse-text { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }

    .log-line-count { font-size: 10px; color: var(--color-muted); }

    .scroll-btn {
      background: none; border: none; cursor: pointer;
      color: var(--color-muted); display: flex; align-items: center;
      padding: 0; transition: color 0.12s;
      .material-icons { font-size: 15px; }
      &:hover { color: var(--color-accent); }
    }

    .log-body {
      height: 480px;
      overflow-y: auto;
      padding: 12px 0;
      font-family: var(--font-mono);
      font-size: 12px;
    }

    .log-empty {
      padding: 12px 16px;
      color: var(--color-muted);
      font-style: italic;
    }

    .log-line {
      display: flex;
      gap: 0;
      padding: 1px 0;
      line-height: 1.6;

      &:hover { background: rgba(255,255,255,0.02); }
    }

    .line-num {
      padding: 0 12px;
      color: #2a3a4a;
      user-select: none;
      white-space: pre;
      border-right: 1px solid var(--color-border-soft);
      min-width: 52px;
    }

    .line-text {
      padding: 0 14px;
      color: #b0c0d0;
      white-space: pre-wrap;
      word-break: break-all;
    }

    .log-cursor {
      padding: 2px 16px;
      color: var(--color-accent);
      animation: blink-cursor 1s step-end infinite;
    }

    @keyframes blink-cursor { 0%, 100% { opacity: 1; } 50% { opacity: 0; } }

    /* Feature 26 — env + reveal-secret panel */
    .env-card {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: 6px;
      margin-top: 16px;
      padding: 16px 20px;
    }
    .env-card__header {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 11px;
      font-weight: 600;
      letter-spacing: 0.08em;
      color: var(--color-muted);
      margin-bottom: 12px;
    }
    .env-card__header .material-icons { font-size: 16px; }
    .env-card__hint {
      margin-left: auto;
      padding: 2px 8px;
      border-radius: 10px;
      background: rgba(255, 180, 60, 0.12);
      color: #d9a649;
      font-size: 10px;
      letter-spacing: 0.06em;
    }
    .env-list { display: flex; flex-direction: column; gap: 6px; }
    .env-row {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 13px;
      padding: 4px 0;
    }
    .env-row__key { color: var(--color-accent); }
    .env-row__eq  { color: var(--color-muted); }
    .env-row__value { color: #d0dde8; word-break: break-all; }
    .env-row__value--secret {
      color: #d9a649;
      letter-spacing: 0.05em;
    }
    .env-row__badge {
      padding: 1px 6px;
      border-radius: 3px;
      background: rgba(217, 166, 73, 0.18);
      color: #d9a649;
      font-size: 9px;
      letter-spacing: 0.1em;
      font-weight: 600;
    }
    .btn-reveal {
      margin-left: auto;
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 10px;
      border: 1px solid var(--color-border);
      background: transparent;
      color: var(--color-muted);
      border-radius: 3px;
      font-size: 10px;
      letter-spacing: 0.06em;
      cursor: pointer;
    }
    .btn-reveal:hover:not([disabled]) { color: #d9a649; border-color: #d9a649; }
    .btn-reveal[disabled] { opacity: 0.5; cursor: wait; }
    .btn-reveal .material-icons { font-size: 14px; }

    .env-card__reveal {
      margin-top: 14px;
      padding: 10px 12px;
      border-left: 3px solid #d9a649;
      background: rgba(217, 166, 73, 0.06);
      border-radius: 4px;
      font-size: 12px;
    }
    .env-card__reveal-title {
      display: flex; align-items: center; gap: 6px;
      color: #d9a649;
      font-weight: 600;
      margin-bottom: 6px;
    }
    .env-card__reveal-title .material-icons { font-size: 15px; }
    .env-card__reveal-kv { color: #ecf0f3; }
    .env-card__reveal-notice {
      margin-top: 4px;
      color: var(--color-muted);
      font-style: italic;
    }
  `]
})
export class JobDetailComponent implements OnInit, OnDestroy, AfterViewChecked {

  @Input() id!: string;
  @ViewChild('logBody') logBodyRef!: ElementRef<HTMLDivElement>;

  job:         Job | null = null;
  logLines:    string[]   = [];
  loading      = true;
  error        = '';
  wsConnected  = false;
  wsEnded      = false;

  // Feature 26 — reveal-secret interaction state.
  // revealOutcome is rendered inline under the env card; populated on
  // both success (ok=true) and failure (ok=false) so the operator
  // always sees server feedback.
  revealOutcome: {
    ok: boolean;
    key?: string;
    value?: string;
    notice?: string;
    error?: string;
  } | null = null;
  private revealingKeys = new Set<string>();

  private wsSub?:     Subscription;
  private autoScroll  = true;
  private needsScroll = false;

  constructor(
    private api: ApiService,
    private ws:  WebSocketService
  ) {}

  ngOnInit(): void {
    this.loadJob();
  }

  ngAfterViewChecked(): void {
    if (this.needsScroll && this.autoScroll) {
      this.scrollToBottom();
      this.needsScroll = false;
    }
  }

  ngOnDestroy(): void {
    this.wsSub?.unsubscribe();
  }

  loadJob(): void {
    this.loading = true;
    this.api.getJob(this.id).subscribe({
      next: job => {
        this.job     = job;
        this.loading = false;
        this.connectLogs(job);
      },
      error: err => {
        this.loading = false;
        console.error('Job not found:', err);
        this.error   = 'Job not found. Please check the job ID and try again.';
      }
    });
  }

  refreshMeta(): void {
    this.api.getJob(this.id).subscribe({
      next: job => { this.job = job; },
    });
  }

  scrollToBottom(): void {
    const el = this.logBodyRef?.nativeElement;
    if (el) el.scrollTop = el.scrollHeight;
  }

  // ── Feature 26 — env display + reveal-secret action ───────────────

  hasEnv(): boolean {
    const env = this.job?.env;
    return !!env && Object.keys(env).length > 0;
  }

  envEntries(): { key: string; value: string; isSecret: boolean; revealing: boolean }[] {
    if (!this.job?.env) return [];
    const secrets = new Set(this.job.secret_keys ?? []);
    const keys = Object.keys(this.job.env).sort();
    return keys.map(k => ({
      key:       k,
      value:     this.job!.env![k],
      isSecret:  secrets.has(k),
      revealing: this.revealingKeys.has(k),
    }));
  }

  revealSecret(key: string): void {
    if (!this.job || this.revealingKeys.has(key)) return;
    // Reason is mandatory server-side; the dashboard prompts for one
    // so the operator explicitly types why they need the value. The
    // server still validates — this is belt-and-braces.
    const reason = (typeof window !== 'undefined' && typeof window.prompt === 'function')
      ? window.prompt(
          'Feature 26: reveal a secret value.\n\n' +
          'This writes a `secret_revealed` audit event carrying your ' +
          'subject + the reason you enter below. Admin token required.\n\n' +
          'Reason:'
        )
      : '';
    if (reason === null) return; // operator cancelled
    const trimmed = (reason ?? '').trim();
    if (trimmed.length === 0) {
      this.revealOutcome = {
        ok: false,
        error: 'Reason is required — every reveal is audited.',
      };
      return;
    }

    this.revealingKeys.add(key);
    this.revealOutcome = null;
    this.api.revealSecret(this.job.id, { key, reason: trimmed }).subscribe({
      next: resp => {
        this.revealingKeys.delete(key);
        this.revealOutcome = {
          ok:     true,
          key:    resp.key,
          value:  resp.value,
          notice: resp.audit_notice,
        };
      },
      error: err => {
        this.revealingKeys.delete(key);
        const status = err?.status;
        const body   = err?.error?.error ?? err?.message ?? 'unknown error';
        let message = `reveal failed (${status}): ${body}`;
        if (status === 403) {
          message = 'Forbidden: only admin-role tokens may reveal secrets.';
        } else if (status === 429) {
          message = 'Rate limit hit. Retry in a few seconds.';
        }
        this.revealOutcome = { ok: false, error: message };
      },
    });
  }

  dismissReveal(): void {
    this.revealOutcome = null;
  }

  private connectLogs(job: Job): void {
    // If terminal, just poll logs once (no WS needed)
    const terminal = ['completed','failed','timeout','lost'];
    if (terminal.includes(job.status)) {
      this.wsEnded = true;
      return;
    }

    this.wsConnected = true;
    this.wsSub = this.ws.jobLogs(this.id).subscribe({
      next: (chunk: LogChunk) => {
        this.logLines.push(...chunk.text.split('\n').filter(l => l.length > 0));
        this.needsScroll = true;
      },
      error: () => {
        this.wsConnected = false;
        this.wsEnded     = true;
        this.refreshMeta();
      },
      complete: () => {
        this.wsConnected = false;
        this.wsEnded     = true;
        this.refreshMeta();
      }
    });
  }
}
