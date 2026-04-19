// src/app/features/submit/submit-preview-dialog.component.ts
//
// Feature 22 step 5 — Preview modal shared across all three
// submission tabs (Job / Workflow / ML Workflow).
//
// The second click of the two-click Validate → Preview → Submit
// confirmation flow. The modal shows the exact JSON that will be
// POSTed so the operator sees what they are committing to before
// the network call fires; the Submit button inside the modal
// resolves 'confirm', Cancel resolves 'dismiss'. The parent
// component owns the actual HTTP call — the modal stays dumb so
// Job / Workflow / ML tabs can all reuse it.

import { Component, EventEmitter, HostListener, Input, Output } from '@angular/core';
import { CommonModule } from '@angular/common';

export type PreviewResult = 'confirm' | 'dismiss';

@Component({
  selector: 'app-submit-preview-dialog',
  standalone: true,
  imports: [CommonModule],
  template: `
<!--
  Backdrop swallows clicks that don't hit the dialog itself so a
  stray click on the overlay dismisses the modal (common modal
  ergonomics). Clicks INSIDE the dialog are stopped via $event
  to avoid both handlers firing.
-->
<div class="backdrop" (click)="resolved.emit('dismiss')" role="dialog"
     [attr.aria-label]="title">
  <div class="dialog" (click)="$event.stopPropagation()">
    <header class="dialog__header">
      <span class="material-icons">preview</span>
      <h2 class="dialog__title">{{ title }}</h2>
    </header>

    <p class="dialog__hint">
      This is the exact JSON body the dashboard will POST. Review
      it — the server re-validates every field. Hit Cancel to go
      back to the form, Submit to commit.
    </p>

    <pre class="dialog__body mono">{{ bodyJSON }}</pre>

    <footer class="dialog__actions">
      <button type="button" class="btn btn--ghost" (click)="resolved.emit('dismiss')">
        Cancel
      </button>
      <button type="button" class="btn btn--primary" (click)="resolved.emit('confirm')">
        <span class="material-icons">send</span> Submit
      </button>
    </footer>
  </div>
</div>
  `,
  styles: [`
    .backdrop {
      position: fixed; inset: 0;
      background: rgba(8, 12, 20, 0.75);
      display: flex; align-items: center; justify-content: center;
      z-index: 1000;
      padding: 20px;
    }
    .dialog {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      max-width: 720px; width: 100%;
      max-height: 80vh;
      display: flex; flex-direction: column;
      box-shadow: 0 16px 64px rgba(0, 0, 0, 0.6);
    }
    .dialog__header {
      display: flex; align-items: center; gap: 8px;
      padding: 14px 20px;
      border-bottom: 1px solid var(--color-border);
      background: var(--color-surface-2);
      color: var(--color-accent-dim);
    }
    .dialog__title {
      margin: 0;
      font-family: var(--font-ui);
      font-size: 13px; letter-spacing: 0.08em;
      color: #e8edf2;
    }
    .dialog__hint {
      margin: 0;
      padding: 12px 20px 0;
      font-size: 11px; color: var(--color-muted); line-height: 1.5;
    }
    .dialog__body {
      margin: 12px 20px;
      padding: 12px;
      background: var(--color-bg);
      border: 1px solid var(--color-border);
      border-radius: var(--radius-sm);
      overflow: auto;
      flex: 1;
      font-size: 11px;
      color: #c8d0dc;
      white-space: pre-wrap;
    }
    .mono { font-family: var(--font-mono); }
    .dialog__actions {
      display: flex; justify-content: flex-end; gap: 8px;
      padding: 14px 20px;
      border-top: 1px solid var(--color-border);
      background: var(--color-surface-2);
    }
    .btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 8px 16px;
      font-family: var(--font-ui); font-size: 11px; letter-spacing: 0.08em;
      border-radius: var(--radius-sm);
      cursor: pointer;
      border: 1px solid transparent;
    }
    .btn--ghost {
      background: transparent;
      border-color: var(--color-border);
      color: var(--color-muted);
    }
    .btn--ghost:hover { color: #e8edf2; border-color: var(--color-accent-dim); }
    .btn--primary {
      background: rgba(192, 132, 252, 0.16);
      border-color: var(--color-accent);
      color: var(--color-accent);
    }
    .btn--primary:hover { background: rgba(192, 132, 252, 0.24); }
  `],
})
export class SubmitPreviewDialogComponent {
  /**
   * Modal title — typically "Submit job X" or "Submit workflow
   * Y". Rendered in the dialog header.
   */
  @Input() title = 'Submit';

  /**
   * The body that WILL be POSTed. Serialised here with indent=2
   * so the preview is readable. JSON.stringify handles nested
   * objects, arrays, null — good enough for every tab's output.
   */
  @Input() body: unknown = {};

  /**
   * Parent subscribes to know whether the user confirmed.
   *   'confirm'  → parent should fire the HTTP call.
   *   'dismiss'  → parent should close without submitting.
   */
  @Output() resolved = new EventEmitter<PreviewResult>();

  get bodyJSON(): string {
    try {
      return JSON.stringify(this.body, null, 2);
    } catch {
      return '(could not serialise body)';
    }
  }

  /**
   * Escape dismisses the modal — standard modal keyboard
   * ergonomic. Enter does NOT submit to reduce accidental-enter
   * risk; user must click the Submit button explicitly.
   */
  @HostListener('document:keydown.escape')
  onEscape(): void {
    this.resolved.emit('dismiss');
  }
}
