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
