# Feature: DAG-builder draft persistence + Submit-tab "resume draft" button

**Priority:** P2
**Status:** Implemented (2026-04-21)
**Affected files:**
`dashboard/src/app/features/submit/submit-dag-builder.component.ts`,
`dashboard/src/app/features/submit/submit-dag-builder.component.html`,
`dashboard/src/app/features/submit/submit.component.ts` (or the landing
route under `/submit`),
`dashboard/src/app/core/services/workflow-draft.service.ts` (new —
thin wrapper over `sessionStorage` with typed getters/setters),
`dashboard/src/app/core/services/workflow-draft.service.spec.ts` (new),
`dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts` (swap the
direct `fetch()` submission for a real UI click-through),
`docs/e2e-mnist-parallel-run.mp4` (re-record after E2E rewrite).

## Problem

The MNIST walkthrough video narrates "build a workflow in the DAG
builder, then submit it from the Submit tab" — but the current E2E
spec posts the workflow body via `fetch()` inside `page.evaluate()`,
because no path exists that actually links the two pages. Today:

- The DAG builder
  ([`submit-dag-builder.component.ts:488-549`](../../../dashboard/src/app/features/submit/submit-dag-builder.component.ts#L488-L549))
  holds its form in a `FormArray`/`FormGroup` with no persistence —
  navigating away loses everything.
- Validate → Preview → Submit is a single-page flow: the "Submit"
  button only exists inside the Preview modal on the DAG-builder
  route. The Submit landing page (`/submit`) offers only job-level
  submission, not "send the workflow I just drafted."
- Consequence: the video's human-eye narrative doesn't match what the
  E2E spec actually exercises, and a real operator can't build a
  draft, tab away to check another page, and return to submit it.

## Current state

- DAG builder form state is ephemeral. Lines 380-382 construct the
  `FormArray` on component init; no `ngOnInit` hydration path.
- `buildBody()` at line 488 serialises the form to a
  `SubmitWorkflowRequest` matching `POST /workflows` — already the
  exact shape the Preview modal displays and the submit handler
  posts. This serialiser is the reuse point for the new feature.
- The Submit landing route (`/submit`) currently renders the
  job-submission form only; there is no "resume DAG-builder draft"
  affordance.
- E2E spec
  ([`ml-mnist-parallel-walkthrough.spec.ts:167-176`](../../../dashboard/e2e/specs/ml-mnist-parallel-walkthrough.spec.ts#L167-L176))
  sidesteps the UI and posts via `fetch()`; the "+ add job" clicks
  at lines 232-253 are visual filler.

## Design

### 1. `WorkflowDraftService` (new)

Single injectable service that owns read/write of one draft at a
time. `sessionStorage`-backed so drafts survive route changes within
a tab but clear on tab close — matches operator intuition and avoids
stale drafts polluting later sessions.

```typescript
// dashboard/src/app/core/services/workflow-draft.service.ts
export interface WorkflowDraft {
  savedAt: string;                 // ISO-8601, for display
  body: SubmitWorkflowRequest;     // exactly what the POST expects
}

@Injectable({ providedIn: 'root' })
export class WorkflowDraftService {
  private readonly KEY = 'helion.workflow-draft.v1';

  save(body: SubmitWorkflowRequest): void { ... }
  load(): WorkflowDraft | null       { ... }
  clear(): void                      { ... }
  snapshot$: Observable<WorkflowDraft | null>;  // BehaviorSubject
}
```

Validation on `load()`: reject drafts with a schema version mismatch
(`KEY` suffix `.v1` enables future migration without silently
corrupting state).

### 2. DAG builder auto-save

In `submit-dag-builder.component.ts` wire the form's `valueChanges`
to `WorkflowDraftService.save(this.buildBody())`, debounced by 400 ms.
Only save when `validateWorkflowShape()` returns a shape that
serialises — otherwise delete the draft (prevents a half-typed form
from masquerading as resumable).

Successful `submitWorkflow()` → `clear()` + navigate as today.

### 3. Submit landing card

Submit tab's landing component renders a "Resume draft" card **only
when `snapshot$` emits non-null**:

```
┌─ Resume draft ───────────────────────────────────────┐
│  iris-parallel-wf  ·  5 jobs  ·  saved 3m ago        │
│                                                       │
│  [ Edit in DAG builder ]   [ Submit now ]            │
└───────────────────────────────────────────────────────┘
```

- `Edit in DAG builder` → `router.navigate(['/submit/dag-builder'])`;
  the builder's `ngOnInit` hydrates from the draft.
- `Submit now` → calls `api.submitWorkflow(draft.body)` directly;
  on 2xx clears draft + navigates to `/ml/pipelines/{id}`; on 4xx
  shows the same error banner the Preview modal uses.

### 4. E2E rewrite

Replace the `fetch()` block at lines 167-176 with:

1. Navigate to `/submit/dag-builder`.
2. Fill workflow name + tags through the actual input fields.
3. For each of the 5 MNIST jobs: click "+ add job", expand the
   accordion, fill name / command / args / env / depends_on /
   node_selector through real inputs.
4. Click "Validate" → assert the checklist-green state.
5. Navigate to `/submit` (tab click — the draft card must appear).
6. Click "Submit now" on the card.
7. Assert redirect to `/ml/pipelines/{id}`.

Budget: the click-through will add ~30 s to the spec. Acceptable for
a single-run walkthrough; the non-video E2E specs stay on the
fast fetch-path.

## Security plan

- `sessionStorage` is readable by same-origin scripts only; existing
  XSS posture (strict CSP, sanitised HTML bindings, no `innerHTML`)
  is the containment boundary — no new trust boundary introduced.
- Draft body is the same shape POSTed today; all server-side
  validation (`validateWorkflowShape`, node-selector allowlist,
  env-var denylist, 1 MiB body cap) runs at submit time, unchanged.
- Do NOT serialise any server-side tokens or JWT material into the
  draft. The draft is workflow-body-only — auth lives in-memory on
  the `AuthService`, same as today.
- Add an audit event on `Submit now` button click? **No.** The real
  audit fires on the `POST /workflows` path; a UI-only event would
  double-count and break the existing `workflow_submit` counter
  invariant in the analytics sink.
- No new endpoints, no new rate-limit bucket — server surface
  unchanged.

## Implementation order

| # | Step                                                          | Depends on | Effort |
|---|---------------------------------------------------------------|-----------|--------|
| 1 | `WorkflowDraftService` + unit spec                            | —         | Small  |
| 2 | Wire auto-save in `submit-dag-builder.component.ts`           | 1         | Small  |
| 3 | Hydrate form in `ngOnInit` from `load()`                      | 2         | Small  |
| 4 | Render "Resume draft" card in Submit landing component        | 1         | Small  |
| 5 | `Submit now` click → `submitWorkflow` → clear on success      | 4         | Small  |
| 6 | Rewrite MNIST E2E to drive the full click path                | 3, 5      | Medium |
| 7 | Re-record `docs/e2e-mnist-parallel-run.mp4`                   | 6         | Small  |

## Tests

**Unit (Angular):**
- `WorkflowDraftService.save → load` round-trips a full 5-job body.
- `load()` returns null when the stored blob is missing the
  schema-version suffix.
- `clear()` removes the key.

**Component:**
- DAG builder hydrates from an existing draft on init.
- Submit landing card is hidden when `snapshot$` is null, visible
  with correct job-count + saved-at text when it's populated.
- `Submit now` click calls `api.submitWorkflow` exactly once with
  the draft body + navigates on success.

**E2E:**
- `ml-mnist-parallel-walkthrough.spec.ts` drives the end-to-end UI
  path (per Design §4). Assertions: draft card appears after tab
  switch; post-submit redirect URL matches `/ml/pipelines/*`;
  workflow reaches `completed` in Postgres as today.
- A separate short spec (`workflow-draft-resume.spec.ts`) covers the
  non-walkthrough edge cases: draft survives one navigation,
  `Edit in DAG builder` re-hydrates form, draft clears after
  successful submit. Runs in the standard (non-video) suite.

## Open questions

- Should we support **multiple** named drafts, or is one-at-a-time
  sufficient? One-at-a-time keeps the UI simple and matches the
  "build one thing at a time" mental model. Multi-draft opens the
  door to a draft list view and a server-side persistence story —
  out of scope for this slice; spin off as a follow-up if asked.
- Should the draft card show a diff against the last-submitted
  workflow? Nice-to-have; skipping for v1.

## Deferred

- Server-side draft storage (survives reload / reboot / cross-device).
  Would require a new `/workflow-drafts` CRUD endpoint, owner-scoped
  RBAC (principal model — feature 35), and storage bytes budget.
  File as `deferred/41-server-side-drafts.md` if scope expands.

## Implementation status

_Filled in as the slice lands._
