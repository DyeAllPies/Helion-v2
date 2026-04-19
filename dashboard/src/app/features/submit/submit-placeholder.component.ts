// src/app/features/submit/submit-placeholder.component.ts
//
// Feature 22 step 1 — empty-tab placeholder.
//
// The three tab routes (/submit/job, /submit/workflow,
// /submit/ml-workflow) all render this single placeholder until
// their real forms / editors land in later steps. Doing it this way
// means:
//
//   1. The nav entry and routes are live from day one — the URL is
//      stable, deep-linking works, and dashboard users aren't
//      surprised by a tab that "suddenly appears" later.
//   2. Each later step replaces this placeholder under a specific
//      route without touching routing or shell code. Smaller diffs,
//      easier review.
//
// The placeholder is deliberately explicit about which prerequisite
// feature each tab is waiting on so a reader of the code (or the
// dashboard) understands why the tab is empty.

import { Component, Input } from '@angular/core';
import { CommonModule } from '@angular/common';
import { ActivatedRoute } from '@angular/router';

interface TabInfo {
  title:    string;
  blurb:    string;
  waiting:  string;  // human-readable list of blocking features
}

const TAB_INFO: Record<string, TabInfo> = {
  job: {
    title:   'Single job',
    blurb:   'Submit one batch or service job — the coordinator runs it on the first matching node.',
    waiting: 'feature 24 (dry-run preflight) + feature 25 (env denylist) + feature 26 (secret env vars)',
  },
  workflow: {
    title:   'Workflow DAG',
    blurb:   'Paste a YAML or JSON workflow. Multiple jobs with depends_on edges and from: artifact references.',
    waiting: 'feature 24 (dry-run preflight) + feature 26 (secret env vars)',
  },
  'ml-workflow': {
    title:   'ML workflow',
    blurb:   'Start from the iris or MNIST template, or drop straight into the raw workflow editor.',
    waiting: 'feature 24 (dry-run preflight) + feature 26 (secret env vars) — also reuses the workflow editor',
  },
};

@Component({
  selector: 'app-submit-placeholder',
  standalone: true,
  imports: [CommonModule],
  template: `
<div class="placeholder">
  <span class="material-icons placeholder__icon">construction</span>
  <h2 class="placeholder__title">{{ info.title }}</h2>
  <p class="placeholder__blurb">{{ info.blurb }}</p>
  <div class="placeholder__waiting">
    <span class="placeholder__waiting-label">waiting on</span>
    <span class="mono">{{ info.waiting }}</span>
  </div>
  <p class="placeholder__hint">
    The form for this tab lands in a later step of
    <a href="https://github.com/DyeAllPies/Helion-v2/blob/main/docs/planned-features/22-ui-submission-tab.md"
       target="_blank" rel="noopener">feature 22</a>. Until then this
    tab is a placeholder — the nav entry, the route, and the auth
    guard are all in place, so deep-linking works.
  </p>
</div>
  `,
  styles: [`
    .placeholder {
      text-align: center;
      padding: 40px 20px;
      color: var(--color-muted);
    }
    .placeholder__icon {
      font-size: 48px;
      color: var(--color-accent-dim);
      opacity: 0.6;
      margin-bottom: 12px;
    }
    .placeholder__title {
      font-family: var(--font-ui);
      font-size: 16px;
      letter-spacing: 0.08em;
      color: #e8edf2;
      margin: 0 0 10px;
    }
    .placeholder__blurb {
      font-size: 12px;
      color: #8896aa;
      margin: 0 auto 20px;
      max-width: 480px;
      line-height: 1.5;
    }
    .placeholder__waiting {
      display: inline-flex;
      align-items: center; gap: 8px;
      padding: 6px 12px;
      background: rgba(255, 171, 64, 0.08);
      border: 1px solid rgba(255, 171, 64, 0.25);
      border-radius: var(--radius-sm);
      font-size: 11px;
      color: var(--color-pending);
      margin-bottom: 16px;
    }
    .placeholder__waiting-label {
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }
    .placeholder__hint {
      font-size: 11px;
      color: var(--color-muted);
      max-width: 480px;
      margin: 0 auto;
    }
    .placeholder__hint a {
      color: var(--color-accent);
      text-decoration: none;
    }
    .placeholder__hint a:hover { text-decoration: underline; }
    .mono { font-family: var(--font-mono); }
  `],
})
export class SubmitPlaceholderComponent {
  /**
   * Which tab this placeholder is standing in for. Set via the
   * `tab` route data (see app.routes.ts) so each tab route can
   * point at its own copy of this same component without needing
   * its own class.
   */
  @Input() tabKey: 'job' | 'workflow' | 'ml-workflow' = 'job';

  info: TabInfo = TAB_INFO['job'];

  constructor(private route: ActivatedRoute) {
    // Pick up the `tab` key from the route data. Falls back to the
    // @Input when used programmatically from a test.
    const routeTab = this.route.snapshot.data?.['tab'] as string | undefined;
    if (routeTab === 'job' || routeTab === 'workflow' || routeTab === 'ml-workflow') {
      this.tabKey = routeTab;
    }
    this.info = TAB_INFO[this.tabKey];
  }
}
