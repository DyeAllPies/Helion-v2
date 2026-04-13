// src/app/features/events/event-feed.component.ts
import { Component, OnInit, OnDestroy } from '@angular/core';
import { CommonModule } from '@angular/common';
import { Subscription } from 'rxjs';

import { WebSocketService } from '../../core/services/websocket.service';
import { EventFrame } from '../../shared/models';

@Component({
  selector: 'app-event-feed',
  standalone: true,
  imports: [CommonModule],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">EVENTS</h1>
      <p class="page-sub">
        <span *ngIf="connected" class="status-live">● LIVE</span>
        <span *ngIf="!connected" class="status-off">○ DISCONNECTED</span>
        &nbsp;{{ events.length }} events
      </p>
    </div>
    <button class="clear-btn" (click)="events = []">
      <span class="material-icons">delete_sweep</span> CLEAR
    </button>
  </header>

  <div class="feed">
    <div class="event-row" *ngFor="let e of events; trackBy: trackById"
         [class]="'event-row--' + eventCategory(e.type)">
      <span class="event-time">{{ e.timestamp | date:'HH:mm:ss.SSS' }}</span>
      <span class="event-type">{{ e.type }}</span>
      <span class="event-data">{{ formatData(e.data) }}</span>
    </div>
    <p class="empty-state" *ngIf="events.length === 0">
      Waiting for events...
    </p>
  </div>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      margin-bottom: 16px;
    }

    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 11px; color: var(--color-muted); margin: 0; }

    .status-live { color: var(--color-success); }
    .status-off  { color: var(--color-error); }

    .clear-btn {
      background: var(--color-surface-2);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      color: #c8d0dc;
      padding: 5px 12px;
      cursor: pointer;
      font-family: var(--font-mono);
      font-size: 11px;
      letter-spacing: 0.06em;
      display: flex;
      align-items: center;
      gap: 4px;
      .material-icons { font-size: 14px; }
      &:hover { border-color: var(--color-accent-dim); color: var(--color-accent); }
    }

    .feed {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      max-height: 70vh;
      overflow-y: auto;
      font-family: var(--font-mono);
      font-size: 11px;
    }

    .event-row {
      display: flex;
      gap: 12px;
      padding: 6px 12px;
      border-bottom: 1px solid var(--color-border);
      align-items: baseline;

      &--job   { border-left: 3px solid var(--color-info); }
      &--node  { border-left: 3px solid var(--color-warn); }
      &--workflow { border-left: 3px solid var(--color-accent); }
    }

    .event-time { color: var(--color-muted); min-width: 90px; }
    .event-type { color: var(--color-info); min-width: 140px; font-weight: 600; }
    .event-data { color: #c8d0dc; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

    .empty-state {
      text-align: center;
      color: var(--color-muted);
      padding: 40px 0;
    }
  `]
})
export class EventFeedComponent implements OnInit, OnDestroy {

  events: EventFrame[] = [];
  connected = false;
  private sub?: Subscription;

  constructor(private ws: WebSocketService) {}

  ngOnInit(): void {
    this.sub = this.ws.events(['*']).subscribe({
      next: (event) => {
        this.events.unshift(event);
        if (this.events.length > 200) {
          this.events.length = 200;
        }
        this.connected = true;
      },
      error: () => { this.connected = false; },
      complete: () => { this.connected = false; },
    });
  }

  ngOnDestroy(): void {
    this.sub?.unsubscribe();
  }

  trackById(_: number, e: EventFrame): string {
    return e.id;
  }

  eventCategory(type: string): string {
    if (type.startsWith('job.')) return 'job';
    if (type.startsWith('node.')) return 'node';
    if (type.startsWith('workflow.')) return 'workflow';
    return 'other';
  }

  formatData(data?: Record<string, unknown>): string {
    if (!data) return '';
    return Object.entries(data).map(([k, v]) => `${k}=${v}`).join(' ');
  }
}
