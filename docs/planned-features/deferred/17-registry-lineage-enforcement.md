# Deferred: Registry lineage enforcement

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 10 — minimal ML pipeline](../10-minimal-ml-pipeline.md)
**Audit reference:** [2026-04-14-01](../../audits/2026-04-14-01.md) findings M3/M4

## Context

Step 6 of the ML pipeline introduced a dataset + model registry where a model carries `source_job_id` and `source_dataset` fields that point back to the training job and its inputs. Today those pointers are **soft**: the registry does not validate `source_job_id` against the JobStore at register time, and deleting a dataset does not detect or cascade to models that reference it. A model can end up with a `source_dataset` pointing at a name+version that no longer resolves.

Tightening this has three shapes, each with a real cost:

1. **Reject-on-reference delete** — full model-prefix scan per dataset delete; blocks legitimate retention / GDPR deletes.
2. **Cascade delete** — silently removes downstream artifacts that may be in production serving paths. Dangerous default.
3. **Dangle detection on read** — materialise the lineage join at `GET /api/models/...` time; changes response shape + couples registry read-path to dataset store.

Similarly, validating `source_job_id` at register time would require the registry package to import the JobStore (collapsing the current clean separation) and is race-prone against job GC.

## Why deferred

The explicit step-6 design treats lineage as a historical audit trail, not a foreign-key constraint. The spec is internally consistent and the failure mode is cosmetic (broken UI link at worst). Revisit when either (a) a deployment reports that broken lineage confused an operator in practice, or (b) the ML pipeline grows a "model delete" UX that would benefit from automatic dependent-model cleanup. If (b) lands first, (1) becomes the natural fix shape; if (a) lands first, (3) is the lower-risk path.

## Revisit trigger

(a) a deployment reports that broken lineage confused an operator in practice, or (b) the ML pipeline grows a "model delete" UX that would benefit from automatic dependent-model cleanup.
