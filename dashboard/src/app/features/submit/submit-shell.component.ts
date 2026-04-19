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

import { Component } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';

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
  `],
})
export class SubmitShellComponent {
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
