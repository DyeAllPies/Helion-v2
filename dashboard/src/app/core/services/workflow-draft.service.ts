// src/app/core/services/workflow-draft.service.ts
//
// Feature 41 — DAG-builder draft persistence.
//
// A single sessionStorage-backed slot holding one in-progress
// workflow draft, so an operator who builds a workflow in the DAG
// builder can navigate to the Submit landing tab and dispatch it
// with one click, without losing state. Session-scoped (not
// localStorage) is deliberate:
//   - Drafts are short-lived work-in-progress, not long-term
//     artefacts — server-side drafts would be the right home for
//     anything the user expects to survive reloads or cross-device.
//   - Closing the tab clears the slot automatically, so stale
//     drafts from earlier sessions cannot masquerade as resumable.
//
// Schema: a JSON envelope `{ schema: 'v1', savedAt, body }` keyed
// by `WorkflowDraftService.KEY`. A version suffix on the key (.v1)
// lets a future breaking shape change land without silently
// hydrating a broken body.
//
// Security plan: sessionStorage is same-origin only; the dashboard's
// existing CSP + sanitised template bindings are the trust boundary.
// No tokens or JWT material ever ride the draft — the body is the
// same shape the DAG builder's Preview modal already POSTs, so all
// server-side validation (handleSubmitWorkflow's validators, the
// 1 MiB body cap, the env-var denylist) runs unchanged at submit
// time.

import { Injectable } from '@angular/core';
import { BehaviorSubject, Observable } from 'rxjs';

import { SubmitWorkflowRequest } from '../../shared/models';

const STORAGE_KEY = 'helion.workflow-draft.v1';
const SCHEMA      = 'v1';

/**
 * Draft envelope stored under `STORAGE_KEY`.
 *
 * `savedAt` is the ISO-8601 timestamp of the most recent auto-save
 * and is surfaced by the Resume-draft card (e.g. "saved 3m ago").
 * `body` matches exactly what `ApiService.submitWorkflow` POSTs.
 */
export interface WorkflowDraft {
  schema:  typeof SCHEMA;
  savedAt: string;
  body:    SubmitWorkflowRequest;
}

/**
 * Minimal SubmitWorkflowRequest signal — used for the "is there
 * anything worth saving here" heuristic so we don't persist a
 * blank form on every keystroke.
 */
function isDraftWorthPersisting(body: SubmitWorkflowRequest): boolean {
  if ((body.id ?? '').trim() !== '')   return true;
  if ((body.name ?? '').trim() !== '') return true;
  if ((body.jobs ?? []).length > 0) {
    for (const j of body.jobs) {
      if ((j.name ?? '').trim() !== '' && j.name !== 'job-1') return true;
      if ((j.command ?? '').trim() !== '')                     return true;
    }
  }
  return false;
}

@Injectable({ providedIn: 'root' })
export class WorkflowDraftService {
  /** Exposed for the landing card and any future consumer. */
  static readonly STORAGE_KEY = STORAGE_KEY;

  private readonly subject$ = new BehaviorSubject<WorkflowDraft | null>(this.loadInitial());

  /**
   * Hot observable of the current draft. Emits `null` when no
   * draft is saved or after `clear()`. The landing card subscribes
   * to this to toggle its visibility.
   */
  readonly snapshot$: Observable<WorkflowDraft | null> = this.subject$.asObservable();

  /**
   * Persist `body` as the single live draft. A body that reads as
   * effectively empty (no id/name/commands typed) is cleared
   * instead of saved so the landing card doesn't show a meaningless
   * "Resume draft" on a freshly-loaded builder.
   */
  save(body: SubmitWorkflowRequest): void {
    if (!isDraftWorthPersisting(body)) {
      this.clear();
      return;
    }
    const draft: WorkflowDraft = {
      schema:  SCHEMA,
      savedAt: new Date().toISOString(),
      body,
    };
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(draft));
      this.subject$.next(draft);
    } catch {
      // Quota / private-mode / storage-disabled — degrade silently.
      // The DAG builder's normal submit path still works; we just
      // don't offer a resume card.
      this.subject$.next(null);
    }
  }

  /**
   * Return the currently-saved draft, or null if the slot is empty
   * or holds a schema-mismatched body. Does NOT mutate storage —
   * callers choose when to clear.
   */
  load(): WorkflowDraft | null {
    return this.readStorage();
  }

  /** Remove the draft from both storage and the hot observable. */
  clear(): void {
    try { sessionStorage.removeItem(STORAGE_KEY); } catch { /* ignored */ }
    this.subject$.next(null);
  }

  // ── internals ─────────────────────────────────────────────────

  private loadInitial(): WorkflowDraft | null {
    return this.readStorage();
  }

  private readStorage(): WorkflowDraft | null {
    let raw: string | null = null;
    try { raw = sessionStorage.getItem(STORAGE_KEY); }
    catch { return null; }
    if (!raw) return null;

    let parsed: unknown;
    try { parsed = JSON.parse(raw); }
    catch {
      // Corrupt blob — wipe it so it doesn't poison future reads.
      try { sessionStorage.removeItem(STORAGE_KEY); } catch { /* ignored */ }
      return null;
    }

    if (!isWorkflowDraft(parsed)) {
      try { sessionStorage.removeItem(STORAGE_KEY); } catch { /* ignored */ }
      return null;
    }
    return parsed;
  }
}

/**
 * Type guard for a trusted WorkflowDraft envelope. Rejects:
 *   - anything whose schema !== 'v1'
 *   - missing / wrong-typed savedAt
 *   - body not shaped like SubmitWorkflowRequest (id string, jobs array)
 *
 * The guard is intentionally conservative — the draft feeds directly
 * into a form patch + later POST, so we want sessionStorage tampering
 * to fail closed.
 */
function isWorkflowDraft(v: unknown): v is WorkflowDraft {
  if (!v || typeof v !== 'object') return false;
  const o = v as Record<string, unknown>;
  if (o['schema'] !== SCHEMA) return false;
  if (typeof o['savedAt'] !== 'string' || o['savedAt'] === '') return false;
  const body = o['body'];
  if (!body || typeof body !== 'object') return false;
  const b = body as Record<string, unknown>;
  if (typeof b['id'] !== 'string') return false;
  if (typeof b['name'] !== 'string') return false;
  if (!Array.isArray(b['jobs'])) return false;
  for (const j of b['jobs']) {
    if (!j || typeof j !== 'object') return false;
    const jr = j as Record<string, unknown>;
    if (typeof jr['name'] !== 'string')    return false;
    if (typeof jr['command'] !== 'string') return false;
  }
  return true;
}
