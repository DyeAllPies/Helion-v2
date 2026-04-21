// src/app/features/submit/submit-shell.component.ts
//
// Feature 22 step 1 — Submission tab shell.
//
// The route `/submit` lets operators start jobs / workflows / ML
// workflows without dropping to the CLI. Three sibling routes under
// /submit/job|workflow|ml-workflow each own their own form or
// editor (lands in later steps of feature 22). This component is
// the purely structural wrapper: it renders the tab bar + the
// router outlet.
//
// This step ships with EMPTY tab bodies on purpose:
//   - the job form depends on feature 24 (dry-run preflight) for
//     its Validate button to have a server endpoint to hit;
//   - the secret-env-var UI depends on feature 26;
//   - the env denylist enforcement is feature 25 (server-side;
//     client-side is advisory UX only).
// Shipping the shell first makes the nav entry visible + the URL
// deep-linkable while the backend prereqs land on their own slice.

import { Component, OnDestroy } from '@angular/core';
import { CommonModule } from '@angular/common';
import { HttpErrorResponse } from '@angular/common/http';
import { NavigationEnd, Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { Subject } from 'rxjs';
import { takeUntil, startWith, filter } from 'rxjs/operators';

import { ApiService } from '../../core/services/api.service';
import {
  WorkflowDraft,
  WorkflowDraftService,
} from '../../core/services/workflow-draft.service';
import { SubmitWorkflowRequest } from '../../shared/models';

interface SubmitTab {
  path:  string;         // route segment under /submit
  label: string;         // tab label
  icon:  string;         // material icon name
  hint:  string;         // tooltip explaining what the tab submits
}

@Component({
  selector: 'app-submit-shell',
  standalone: true,
  imports: [CommonModule, RouterLink, RouterLinkActive, RouterOutlet],
  template: `
<div class="page">
  <header class="page-header">
    <div>
      <h1 class="page-title">SUBMIT</h1>
      <p class="page-sub">
        Start a new run from the dashboard. All submissions go through
        the same coordinator validators as CLI submits.
      </p>
    </div>
  </header>

  <!--
    Feature 41 — Resume draft card. Visible on every submit sub-route
    EXCEPT /submit/dag-builder itself (redundant to show the card on
    the page you'd click Edit to return to). One-click Submit posts
    the draft as-is; Edit re-enters the DAG builder with the form
    hydrated; Discard clears the slot. The card is the bridge
    between "I built a workflow" and "now send it to the coordinator."
  -->
  <section class="resume-card"
           *ngIf="draft && !hideCard"
           data-testid="resume-draft-card">
    <div class="resume-card__head">
      <span class="material-icons resume-card__icon">drafts</span>
      <div class="resume-card__title-block">
        <div class="resume-card__title">RESUME DRAFT</div>
        <div class="resume-card__meta mono">
          {{ draft.body.name || draft.body.id || '(unnamed)' }}
          &middot;
          {{ draft.body.jobs.length }} job{{ draft.body.jobs.length === 1 ? '' : 's' }}
          &middot;
          saved {{ draft.savedAt | date:'HH:mm:ss' }}
        </div>
      </div>
    </div>
    <div class="resume-card__actions">
      <button type="button" class="btn btn--ghost"
              [disabled]="submitting"
              (click)="onDiscard()">DISCARD</button>
      <button type="button" class="btn btn--secondary"
              [disabled]="submitting"
              [routerLink]="['/submit','dag-builder']">EDIT IN DAG BUILDER</button>
      <button type="button" class="btn btn--primary"
              data-testid="resume-draft-submit"
              [disabled]="submitting"
              (click)="onSubmitDraft()">{{ submitting ? 'SUBMITTING…' : 'SUBMIT NOW' }}</button>
    </div>
    <div class="resume-card__error" *ngIf="submitError">
      <span class="material-icons resume-card__error-icon">error_outline</span>
      {{ submitError }}
    </div>
  </section>

  <!--
    Tab bar — one entry per submission kind. routerLinkActive lights
    the active tab; the child outlet below renders whichever tab's
    component matches the current URL. Clicking a tab is a plain
    Angular navigation (no state-in-parent) so deep-linking works.
  -->
  <nav class="submit-tabs" role="tablist">
    <a *ngFor="let tab of tabs"
       [routerLink]="['/submit', tab.path]"
       routerLinkActive="submit-tab--active"
       class="submit-tab"
       role="tab"
       [attr.aria-label]="tab.hint"
       [title]="tab.hint">
      <span class="material-icons submit-tab__icon">{{ tab.icon }}</span>
      <span class="submit-tab__label">{{ tab.label }}</span>
    </a>
  </nav>

  <div class="submit-body">
    <router-outlet></router-outlet>
  </div>
</div>
  `,
  styles: [`
    .page { padding: 28px 32px; }

    .page-header {
      display: flex; align-items: flex-start; justify-content: space-between;
      margin-bottom: 20px; gap: 16px; flex-wrap: wrap;
    }
    .page-title { font-family: var(--font-ui); font-size: 20px; letter-spacing: 0.1em; color: #e8edf2; margin: 0 0 4px; }
    .page-sub   { font-size: 12px; color: var(--color-muted); margin: 0; max-width: 640px; }

    /* Tab bar — visually distinct from the sidebar nav so users
       don't confuse them. Mirrors the underline-active pattern used
       elsewhere in Material design rather than the sidebar's pill. */
    .submit-tabs {
      display: flex; gap: 2px;
      border-bottom: 1px solid var(--color-border);
      margin-bottom: 20px;
    }
    .submit-tab {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 10px 16px;
      color: var(--color-muted);
      font-family: var(--font-ui); font-size: 11px; letter-spacing: 0.08em;
      text-decoration: none;
      border-bottom: 2px solid transparent;
      transition: color 0.12s, border-color 0.12s, background 0.12s;
    }
    .submit-tab:hover {
      color: #e8edf2;
      background: rgba(192, 132, 252, 0.06);
    }
    .submit-tab--active {
      color: var(--color-accent);
      border-bottom-color: var(--color-accent);
    }
    .submit-tab__icon { font-size: 16px; }
    .submit-tab__label { }

    .submit-body {
      background: var(--color-surface);
      border: 1px solid var(--color-border);
      border-radius: var(--radius);
      padding: 20px;
      min-height: 240px;
    }

    /* Feature 41 — Resume draft card. Visually distinct from the
       form bodies below so the "this is a thing you built earlier"
       affordance reads immediately. Accent-coloured left rail +
       subdued neutral background so it doesn't compete with the
       active tab's content. */
    .resume-card {
      display: flex; flex-direction: column; gap: 12px;
      padding: 14px 18px;
      border: 1px solid var(--color-border);
      border-left: 3px solid var(--color-accent);
      border-radius: var(--radius);
      background: rgba(192, 132, 252, 0.04);
      margin-bottom: 18px;
    }
    .resume-card__head {
      display: flex; align-items: center; gap: 12px;
    }
    .resume-card__icon {
      color: var(--color-accent); font-size: 22px;
    }
    .resume-card__title-block { display: flex; flex-direction: column; gap: 2px; }
    .resume-card__title {
      font-family: var(--font-ui); font-size: 12px; letter-spacing: 0.12em;
      color: var(--color-accent);
    }
    .resume-card__meta { font-size: 12px; color: var(--color-muted); }
    .resume-card__actions {
      display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end;
    }
    .resume-card__error {
      display: flex; align-items: center; gap: 6px;
      color: #ff7a8a; font-size: 12px;
    }
    .resume-card__error-icon { font-size: 16px; }

    /* Shared pill buttons for the card. Match the DAG builder's
       button system so the two surfaces read as one UI. */
    .btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 8px 14px;
      font-family: var(--font-ui); font-size: 11px; letter-spacing: 0.08em;
      border: 1px solid var(--color-border); border-radius: 6px;
      background: transparent; color: #e8edf2;
      cursor: pointer;
      transition: background 0.12s, border-color 0.12s, color 0.12s;
    }
    .btn:hover:not(:disabled) { background: rgba(255,255,255,0.04); }
    .btn:disabled { opacity: 0.45; cursor: not-allowed; }
    .btn--primary {
      background: rgba(192, 132, 252, 0.16);
      border-color: var(--color-accent);
      color: var(--color-accent);
    }
    .btn--primary:hover:not(:disabled) { background: rgba(192, 132, 252, 0.24); }
    .btn--secondary { border-color: var(--color-accent-dim, var(--color-border)); }
    .btn--ghost { border-color: transparent; }
  `],
})
export class SubmitShellComponent implements OnDestroy {
  // Feature 41 — resume-draft card state. Bound to the template
  // with *ngIf; toggled by the snapshot$ subscription in ngOnInit.
  draft: WorkflowDraft | null = null;
  hideCard = false;   // true while the active route is /submit/dag-builder
  submitting = false;
  submitError: string | null = null;

  private readonly destroy$ = new Subject<void>();

  constructor(
    private drafts: WorkflowDraftService,
    private api: ApiService,
    private router: Router,
  ) {
    this.drafts.snapshot$
      .pipe(takeUntil(this.destroy$))
      .subscribe(d => { this.draft = d; });
    // Hide the card on the DAG-builder sub-route so the user isn't
    // staring at a "Resume draft" banner while editing the draft.
    this.router.events
      .pipe(
        filter((e): e is NavigationEnd => e instanceof NavigationEnd),
        startWith(null),
        takeUntil(this.destroy$),
      )
      .subscribe(() => {
        this.hideCard = this.router.url.startsWith('/submit/dag-builder');
      });
  }

  ngOnDestroy(): void {
    this.destroy$.next();
    this.destroy$.complete();
  }

  onSubmitDraft(): void {
    if (!this.draft || this.submitting) return;
    this.submitting = true;
    this.submitError = null;
    const body: SubmitWorkflowRequest = this.draft.body;
    this.api.submitWorkflow(body).subscribe({
      next: wf => {
        this.submitting = false;
        this.drafts.clear();
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

  onDiscard(): void {
    this.drafts.clear();
    this.submitError = null;
  }

  /**
   * Tab definitions. Kept as a class field (not a template
   * constant) so unit tests can assert the three expected tabs
   * without re-parsing the template.
   */
  readonly tabs: SubmitTab[] = [
    {
      path:  'job',
      label: 'JOB',
      icon:  'work_outline',
      hint:  'Single batch or service job (mirrors POST /jobs)',
    },
    {
      path:  'workflow',
      label: 'WORKFLOW',
      icon:  'account_tree',
      hint:  'Multi-job DAG defined as YAML or JSON (mirrors POST /workflows)',
    },
    {
      path:  'ml-workflow',
      label: 'ML WORKFLOW',
      icon:  'hub',
      hint:  'Templated shortcut over the iris / MNIST pipelines',
    },
    // Promoted from the feature 22 "Deferred" list into v1 on
    // user request. Builds a SubmitWorkflowRequest via form
    // controls + a live JSON preview; posts through the same
    // /workflows endpoint as the other two workflow tabs.
    {
      path:  'dag-builder',
      label: 'DAG BUILDER',
      icon:  'schema',
      hint:  'Compose a workflow visually; depends_on edges picked from a list of jobs',
    },
  ];
}
