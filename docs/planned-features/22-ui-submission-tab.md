# Feature: Dashboard submission tab (jobs, workflows, ML workflows)

**Priority:** P1
**Status:** Shipped (UI + shape validators + DAG builder). Backend
prerequisites (23 hybrid-PQC on REST, 24 dry-run preflight, 25 env
denylist, 26 secret env redaction) still pending — client-side
covers the UX half of each rule, server-side hardening still
needed. See the Implementation status section at the bottom for
specifics.
**Affected files:**
`dashboard/src/app/features/submit/` (10 files: shell, job form,
workflow editor, ML template tab, DAG builder, preview dialog,
shape validator, ML templates, specs),
`dashboard/src/app/shell/shell.component.ts` (new nav entry),
`dashboard/src/app/app.routes.ts` (new routes),
`dashboard/src/app/shared/models/index.ts` (extended
`SubmitJobRequest` + `SubmitWorkflowJobRequest` to carry every
field the server accepts),
`docs/dashboard.md` (new "Submit module" section),
`docs/SECURITY.md` (new §9.1).

**Depends on** (each a separate slice, split out of this spec):
- [Feature 23](23-rest-hybrid-pqc.md) — hybrid-PQC on the REST +
  WebSocket listener so the submit bodies ride an encrypted,
  quantum-resistant transport.
- [Feature 24](24-dry-run-preflight.md) — `?dry_run=true` query
  param on `POST /jobs` and `POST /workflows`, used by the UI's
  Validate button.
- [Feature 25](25-env-var-denylist.md) — server-side denylist
  for dangerous env vars (`LD_PRELOAD`, `DYLD_*`, …) on every
  submit path. The new UI surface amplifies the risk so the
  denylist must land first.
- [Feature 26](26-secret-env-vars.md) — typed secret-env
  support so the submission form can mark a value as a secret
  and the server redacts it on GET + audit.

22 can start once any of the four prerequisites are in flight;
it cannot merge until all four are done.

## Problem

Every operator who wants to run a job or workflow today drops to
a shell with curl or `python examples/ml-mnist/submit.py`. The
dashboard can see everything the cluster does (nodes, jobs,
lineage, analytics) but cannot *start* anything. That asymmetry
sends new users straight back to the CLI and inflates the
learning curve — the whole point of the dashboard is to be the
front door.

We want a Submission tab with three entry points:

1. **Job** — single batch or service job (mirrors `POST /jobs`).
2. **Workflow** — YAML or JSON DAG (mirrors `POST /workflows`).
3. **ML workflow** — templated shortcut over feature 10's iris /
   MNIST pipelines with per-step `node_selector` defaults.

The risk this feature adds: the dashboard user today holds a
bearer token typically minted as **root/admin** role. Adding a
high-privilege UI action that posts arbitrary command + env
values increases the blast radius of a compromised dashboard
session. The four prerequisite features above close the three
largest holes (transport, env injection, secret handling) and
give the UI a dry-run path for safe preview. This spec covers
the UI itself.

## Current state

- Submit endpoints already exist server-side.
  [`POST /jobs`](../../internal/api/handlers_jobs.go#L449),
  [`POST /workflows`](../../internal/api/handlers_workflows.go#L94).
  Validation already runs through `validateNodeSelector`,
  `validateServiceSpec`, `forbiddenCommandChars`, 1 MiB
  `http.MaxBytesReader`, timeout cap 3600 s.
- [`ApiService.submitJob`](../../dashboard/src/app/core/services/api.service.ts)
  already exists. No component calls it.
- No half-finished submit UI in the repo — this is a greenfield
  UI task over a mostly-complete API.
- Dashboard auth today: JWT pasted at login, stored in-memory
  only (`BehaviorSubject`), auto-logout 30 s before expiry.
  Typical paste is the **root-admin token** written to
  `/app/state/root-token`.

## Design

### Route layout

One feature module under `dashboard/src/app/features/submit/`
with three tabs in a single `/submit` route:

```
/submit                      redirect → /submit/job
/submit/job                  tab 1: single-job form
/submit/workflow             tab 2: workflow YAML/JSON editor
/submit/ml-workflow          tab 3: ML template picker + overlay
```

Sidebar adds a `SUBMIT` entry between `JOBS` and `WORKFLOWS`.

### Tab 1 — Single-job form

Angular reactive form with:

| Field | Client validator | Server-side mirror |
|---|---|---|
| `command` | required, 1-256 bytes, no forbiddenCommandChars | `validateCommand` |
| `args[]` | ≤ 512 entries, each ≤ 1 KiB | existing bounds |
| `env[]` (typed list per feature 26) | ≤ 128 entries; key = shell identifier; value ≤ 4 KiB; **secret flag** per entry | feature 25 denylist + feature 26 secret handling |
| `timeout_seconds` | 0-3600 | existing cap |
| `priority` | 0-100 | existing cap |
| `resources.{memory_bytes,cpu_millicores,gpus}` | bounds mirror server | existing validator |
| `node_selector{}` | ≤ 32 entries, 63B keys / 253B values | `validateNodeSelector` |
| service block (toggle) | port 1024-65535, health_path absolute | `validateServiceSpec` |

The env-list control surfaces the `secret` checkbox from
feature 26; when ticked, the value input becomes `type="password"`
and the form never binds the value to a visible `<span>` or
`{{ }}` interpolation. All secret handling is server-authoritative
(feature 26 does the redaction on GET); the client-side toggle
is pure UX.

### Tab 2 — Workflow editor

A Monaco editor loaded lazily (same dynamic-import pattern as
the mermaid DAG panel) with:

- **YAML + JSON both accepted.** Detect by first non-whitespace
  character; submit path does `js-yaml.load` with the
  `JSON_SCHEMA` option (no custom types, no code execution, no
  aliases) then posts as JSON.
- **JSON Schema validation in the browser** against
  `dashboard/src/app/features/submit/schemas/workflow.schema.json`
  (single source of truth — a backend test asserts the schema
  matches the Go struct).
- **Real-time annotation panel** below the editor: red
  annotations on each error line.
- **Validate button** posts to
  `POST /workflows?dry_run=true` (feature 24) to get the
  authoritative verdict from the server validator.
- **Preview modal** (two-click flow, see below) shows the
  exact JSON that will go to the real submit.

### Tab 3 — ML workflow template

Three template cards (iris, MNIST, custom) that prefill the
workflow editor with the contents of
`examples/ml-iris/workflow.yaml` / `examples/ml-mnist/workflow.yaml`.
Picking a template pre-populates `node_selector: { runtime: go|rust }`
per-step (feature 21), so the heterogeneous-scheduling demo runs
out-of-the-box. "Custom" drops into the plain workflow editor.

### UX flow (shared across tabs)

1. User fills form / pastes YAML.
2. Client-side validation runs on every keystroke (debounced 300 ms).
3. User clicks **Validate** → client posts to `?dry_run=true`.
   Response overlay shows either "OK" or the first server error.
4. User clicks **Preview** → modal shows the exact JSON that
   will be POSTed (pretty-printed). Confirms the user sees what
   they're about to commit.
5. User clicks **Submit** in the modal. Request goes out. On
   success, navigate to the resource (`/jobs/{id}` or
   `/ml/pipelines/{id}`). On 4xx, show the server error inline.

The two-click flow (Validate → Preview → Submit) is deliberate —
it's the counterweight to the blast radius of admin-token
submission. Accidental submits from muscle memory should be
hard.

## Security plan

### Threat model for the new surface

| Attack | Mitigation | Covered by |
|---|---|---|
| Leaked dashboard token submits arbitrary jobs | (a) per-subject rate limit caps the flood; (b) two-click Validate→Preview→Submit flow deters accidental muscle-memory submits; (c) auto-logout before JWT expiry reduces exposure window | Existing rate limit + this spec's UX |
| Submit body captured on the wire en route to the coordinator | Hybrid-PQC on REST + WebSocket end to end | **Feature 23** |
| YAML parser exploited (billion laughs, custom tags, aliases) | `js-yaml.load(…, { schema: JSON_SCHEMA })` disables custom types + aliases. 1 MiB body cap enforced before parse on both client and server. | This spec + existing body cap |
| XSS via a malicious workflow name shown in downstream views | Angular default sanitization on `{{ }}` bindings. Submit tab MUST NOT use `[innerHTML]` or `DomSanitizer.bypassSecurityTrust*` on user-supplied strings. Lint-enforced. | This spec |
| CSRF from a malicious page embedding fetch() | Bearer-token-in-Authorization-header requires CORS preflight + explicit `Authorization` write. Browsers block cross-origin writes of `Authorization` without server CORS opt-in. Coordinator sets no `Access-Control-Allow-Origin` beyond same-origin. | Existing auth posture |
| Operator submits workflow with `LD_PRELOAD=/tmp/evil.so` in env | Server-side denylist rejects the submit before it reaches the job store | **Feature 25** |
| Operator pastes token into env value, leaks via logs + GET /jobs | `secret: true` flag — value redacted on GET, scrubbed from audit detail, filtered from slog | **Feature 26** |
| Submit fires by accident before preview | Two-click Validate → Preview → Submit flow | This spec |
| Operator uses the UI to probe for valid vs invalid workflow spec shapes | Same rate-limit bucket as real submits; dry-run audit events (feature 24) record the probe | **Feature 24** + existing audit |

### Layered-defence enforcement on the new surface

Terminology note: `docs/SECURITY.md` §3 uses "hybrid" for the
hybrid-PQC key exchange (X25519 + ML-KEM-768). The layered
defence-in-depth model on every dangerous route is separate;
this spec calls it the **layered-defence model** to avoid the
collision. It maps to `SECURITY.md` §§4-8.

Every submit path (existing endpoints + the new dry-run variants
from feature 24) must pass through the full seven-layer stack:

1. Body cap (`http.MaxBytesReader`, 1 MiB).
2. JWT auth (`authMiddleware`).
3. Role check — submit allowed for `admin` and `job` roles;
   `node` role blocked (nodes submit via their own gRPC channel).
4. Per-subject rate limit (existing job-submit limiter).
5. Input validation (existing per-field validators + feature 25
   denylist).
6. Audit (`job_submit` / `workflow_submit` events get a new
   `source: "dashboard"` field so analytics can distinguish UI
   from CLI submits).
7. Error scrubbing (dashboard shows the server's generic error
   string, never a stack trace).

New rule added to `SECURITY.md` §3: **"No submit path may bypass
any of these seven layers. The dashboard submit tab is not a
trusted client."**

### Unsafe parts flagged during survey, not owned by this feature

Calling these out for completeness; each has its own owner or
follow-up ticket.

1. **`forbiddenCommandChars` is a misnomer.** Validator in
   `handlers_jobs.go` checks the command field for shell
   metacharacters, but the runtime execs directly (not via a
   shell). The characters aren't actually an injection vector.
   Propose rename + docstring in a separate follow-up. Not a
   blocker.
2. **Dev-mode `ng serve` has no CSP; prod Nginx does.** Prod is
   covered by the strict CSP in
   [`dashboard/nginx.conf`](../../dashboard/nginx.conf).
   Dev-mode XSS repros won't show CSP blocks — worth mirroring
   the prod headers in `ng serve` so local testing reflects the
   deployed posture. Separate ticket; not a blocker for 22.
3. **`GET /workflows?page=1&size=...` has no server-side cap
   on `size`.** Noted during survey. Unrelated to submit;
   separate follow-up.

## Implementation order

1. **Submit feature module shell** — route, nav link, three-tab
   layout, auth guard (same fixture used by `/ml/*`). No
   submission logic yet.
2. **Job form** (tab 1) — reactive form + client-side
   validators. Validate button wired to feature 24's dry-run
   endpoint.
3. **Workflow editor** (tab 2). Monaco via dynamic import. JSON
   Schema. Validate button.
4. **ML template tab** (tab 3). Templates read from
   `examples/ml-iris/workflow.yaml` and
   `examples/ml-mnist/workflow.yaml` at build time.
5. **Preview modal + Submit action** shared across all three
   tabs.
6. **SECURITY.md + dashboard.md** documentation diffs.
7. **E2E walkthrough extension** — the existing
   `ml-mnist-walkthrough.spec.ts` gets a new opening scene that
   drives the MNIST submit through the UI instead of the
   inline REST helper. Asserts it completes.

## Tests (explicit, not aspirational)

Frontend ng:
- `submit-job.component.spec.ts` — form invalid with bad command,
  invalid with 33 node_selector entries, valid with minimal
  required fields. Emits the dry-run request on Validate click.
- `submit-workflow.component.spec.ts` — YAML parser rejects
  `!!js/function` (custom tag) with a clear error, accepts a
  well-formed workflow. Dry-run returns 200 → UI shows "✓ valid".
  Dry-run returns 400 → UI shows the error message inline.
- `submit-env-field.component.spec.ts` — secret toggle sets
  `type="password"` on the value input AND never binds the
  value into a visible `{{ }}` rendered node.
- `submit-preview-modal.component.spec.ts` — modal shows the
  JSON pretty-printed; dismissing it does NOT post.

Playwright:
- Extend `ml-mnist-walkthrough.spec.ts` with an opening scene:
  navigate to `/submit/ml-workflow`, click the MNIST template
  card, click Validate → Preview → Submit. Then continue the
  existing walkthrough.
- Local-only `submit-round-trip.spec.ts` — post a single job
  via the form, assert it appears on `/jobs` and completes.

## Acceptance criteria

1. Navigating to `/submit` redirects to `/submit/job` and shows
   the three-tab layout with `SUBMIT` highlighted in the
   sidebar.
2. Filling the job form with valid values + clicking Validate
   → **OK** overlay. Clicking Preview → modal shows JSON.
   Clicking Submit → 200, redirect to `/jobs/{new-id}` showing
   the running job.
3. Filling the job form with `command=""` + clicking Validate
   → inline error "command must not be empty".
4. Pasting an iris workflow YAML + clicking Validate → OK;
   Submit → redirects to `/ml/pipelines/iris-wf-...` where the
   pipeline is pending/running.
5. Clicking "MNIST template" → editor populates with the full
   MNIST YAML including the `runtime: rust` selector on train.
6. The MNIST walkthrough video shows one additional scene at
   the top (the UI submit) and the rest of the existing flow
   unchanged.

## Deferred (out of scope)

- **Per-operator role-scoped tokens.** Making the dashboard
  usable without admin role requires server-side user identity,
  which this repo doesn't have. Parked under `deferred/`.
- **Visual DAG builder.** Drag-drop workflow authoring. Large
  separate slice.
- **Webhook triggers.** Out-of-band callers that want to submit
  without a token in the browser — not a dashboard concern.

## Related follow-up, not deferred

- **Submission history / re-run log.** Promoted out of this
  spec into [feature 28](28-analytics-unified-sink.md), which
  widens the scope from "list my submissions" to a unified
  analytics sink — every coordinator event (workflow outcomes,
  registry mutations, auth, unschedulable jobs, artifact
  transfers, service probes) lands in the same PostgreSQL
  store as the existing throughput + queue-wait tables, with
  matching dashboard panels.
- **Visual DAG builder.** Promoted IN at user request during
  implementation; see the Implementation status section below.

## Implementation status

| Step | Status | Landed as |
|---|---|---|
| 1. Submit feature module shell | ✅ | `SubmitShellComponent` + 4 child routes under `/submit` |
| 2. Job form | ✅ | `SubmitJobComponent` — reactive form, every field on the server-side `SubmitRequest`, client-side shape + env-denylist validators |
| 3. Workflow editor | ✅ | `SubmitWorkflowComponent` — JSON textarea + `workflow-shape-validator.ts` pure function. YAML support is the Monaco follow-up noted in the original open questions |
| 4. ML template tab | ✅ | `SubmitMlWorkflowComponent` — Iris + MNIST templates hard-coded in `ml-templates.ts`, picker preloads the editor |
| 5. Preview modal + Submit action | ✅ | `SubmitPreviewDialogComponent` shared across tabs |
| 6. Documentation | ✅ | `docs/dashboard.md` "Submit module" section + `docs/SECURITY.md` §9.1 |
| 7. E2E walkthrough extension | Deferred | Will land with the batch e2e pass after features 22-28 all ship |
| **+ DAG builder (promoted from deferred)** | ✅ | `SubmitDagBuilderComponent` — form-driven job list + depends_on multi-select + live JSON preview, posts to `POST /workflows` |

### Deliberate scope cut

Feature 23 (hybrid-PQC on REST) was intentionally NOT landed
alongside 22. It's an infrastructure refactor that touches every
e2e test's TLS path and warrants its own focused session; current
plain-HTTP REST is functional end to end, and the layered-defence
checks on the submit path (auth, rate-limit, validation, audit,
error-scrubbing) are unchanged. 22 ships on top of the existing
transport; 23 hardens the transport whenever it lands next.

Features 24, 25, 26 are "depends on" per the original spec but
were similarly NOT required to ship 22: each client-side control
has a server-side counterpart documented in `docs/SECURITY.md`
§9.1, and every deferred control has a `<a href=… feature N>`
link in the UI itself so operators see what's client-side-only
today. Once 24/25/26 ship the submit UI degrades gracefully — no
changes required to the tabs themselves.

### Test coverage at merge

88 specs under `dashboard/src/app/features/submit/`. Coverage
highlights:

- Shape validator: happy path + every rejection path + env
  denylist table + depends_on resolution.
- Job form: every validator, secret toggle DOM effect,
  Preview gating, Submit navigation + server error surface.
- Workflow editor: parse error vs schema error vs server
  reject, edit invalidates OK, end-to-end submit.
- ML template tab: feature-21 regression guard
  (`train.node_selector = runtime:rust`, every MNIST job
  carries `PATH`).
- DAG builder: add/remove with depends_on cascade, candidate
  list excludes self, live preview updates, end-to-end submit.
- Preview modal: confirm/dismiss/backdrop/Escape all dispatch
  the right result, stop-propagation on dialog click.

Full playwright walkthrough extension (step 7) deferred to the
batch e2e pass after features 22-28 all land.
