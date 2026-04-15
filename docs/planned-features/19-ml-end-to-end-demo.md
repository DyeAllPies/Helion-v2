# Feature: ML End-to-End Demo

**Priority:** P1
**Status:** Implementation written; **acceptance run pending**. The
scripts + workflow YAML + submitter are in `examples/ml-iris/`, but
no one has yet observed the pipeline transition `pending → completed`
against a live Docker-Compose cluster — the author's environment did
not have Docker running at the time of the commit that landed this
slice. Do not flip to Done and do not move to `implemented/` until a
clean-checkout run succeeds end-to-end (see the README's "Running
it" section for the exact steps).
**Affected files:** `examples/ml-iris/` (new — seven files: scripts, workflow, submitter, requirements, README).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## End-to-end demo workflow

Ship a worked example under `examples/ml-iris/`:

```
examples/ml-iris/
├── workflow.yaml          # 4-job DAG: ingest → preprocess → train → register
├── ingest.py              # downloads iris CSV → RAW_CSV output
├── preprocess.py          # RAW_CSV → TRAIN_PARQUET + TEST_PARQUET
├── train.py               # parquet splits → model.joblib + metrics.json
├── register.py            # POSTs to /api/datasets + /api/models with lineage
├── serve.py               # FastAPI app loading model.joblib, exposed via Service
├── submit.py              # YAML-to-JSON converter + /workflows client
├── requirements.txt       # pandas, scikit-learn, fastapi, uvicorn, pyyaml…
└── README.md              # step-by-step run guide + known gaps
```

The example is the acceptance test for "can a normal person run an
ML pipeline on Helion." If this works on a clean checkout with one
`docker compose up` + `python examples/ml-iris/submit.py
examples/ml-iris/workflow.yaml`, the feature is done.

### Deviations from the original one-line spec

- **sklearn, not PyTorch.** Iris is a 150-row toy dataset. Swapping
  `torch` for `scikit-learn + joblib` cuts the runtime dep chain
  from ~800 MiB to ~60 MiB without changing what the pipeline
  exercises on the Helion side. Model artifact is `model.joblib`.
- **Serve submitted separately.** `serve.py` is a Helion service
  job — never terminates, would block the workflow DAG from
  completing. `submit.py --serve` submits it in a second call
  after the workflow reaches `completed`. The README walks through
  both the automated `--serve` flow and a manual `curl` fallback.
- **`submit.py` ships in the example.** The coordinator takes
  workflows as JSON over `POST /workflows`; the spec's
  `workflow.yaml` is the human-authored source. `submit.py`
  converts (one dep: PyYAML) rather than adding a workflow
  subcommand to `helion-run`, which is out of scope for this
  slice.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None (read-only artifact reads; registry POSTs ride the standard JWT-authenticated API) | — | — |

The iris pipeline introduces no new trust boundary. Every RPC the
example scripts issue (`/workflows`, `/api/datasets`, `/api/models`,
`/api/services`, `/jobs`) rides the existing JWT bearer auth + rate
limiters. `register.py` reads `HELION_TOKEN` from the job's env;
the operator is responsible for not leaking that into logs — the
Go runtime already redacts `HELION_TOKEN` from emitted audit
records (see `docs/SECURITY.md` § 6).

## Known gaps — blocking-ish, documented in the README

The scripts were authored against the Helion REST and env contracts
without the ability to execute end-to-end against a live cluster.
These are the items the author expects to need attention on the
first real run, reproduced here from `examples/ml-iris/README.md`
§ "Known gaps":

1. **`registry://` input-URI scheme is not implemented.** The serve
   job's `submit.py --serve` path uses `registry://models/iris-logreg/v1`
   as the model input URI; the coordinator's dispatch-time resolver
   doesn't know this scheme. Workaround: manual submit with the
   concrete URI (README shows the copy-paste). Proper fix is a small
   resolver addition — candidate for a numbered deferred item after
   the first real run.
2. **Node image doesn't ship Python.** `Dockerfile.node` builds a
   minimal Go binary; Python jobs need a different base image.
   Per-cluster operator choice, not something the example ships.
3. **Iris CSV mirror availability.** `ingest.py` pulls from the
   scikit-learn GitHub raw URL. Baking the CSV into the example
   folder would be more robust but loses the network-backed
   ingest path's realism.
4. **Artifact URIs in `register.py` fall back to `file://` on local
   backends.** The Stager doesn't expose the assigned S3 URI to the
   running job today. `_resolve_uri` prefers a `HELION_INPUT_<NAME>_URI`
   env var that the Stager can start setting; falls back to
   `file://<local-path>` for now.

## Acceptance run checklist

On the first real run, verify each of these in order:

1. `docker compose up --build` succeeds; coordinator prints a root
   token; nodes show as `healthy` on `/nodes`.
2. `python submit.py workflow.yaml` returns 200 with the workflow ID.
3. `/workflows/iris-wf-1` reaches `completed` (dashboard or
   `curl /workflows/iris-wf-1`). No `ml.resolve_failed` events in
   `/events`.
4. `/api/datasets` returns an `iris` entry; `/api/models` returns an
   `iris-logreg` entry with non-zero `accuracy` and a non-empty
   `source_job_id`.
5. `/ml/pipelines/iris-wf-1` renders the DAG with artifact edges.
6. Manual serve submit (README copy-paste) succeeds; `/ml/services`
   shows `iris-serve-1` as `ready`; `POST /predict` returns a
   prediction.

When all six green-check, flip `**Status:**` to **Done**,
`git mv` this file to `planned-features/implemented/`, strike the
row on `planned-features/README.md`, and fix the relative paths
inside the moved file (sibling-feature links gain a `../` prefix;
`../SECURITY.md` becomes `../../SECURITY.md`).
