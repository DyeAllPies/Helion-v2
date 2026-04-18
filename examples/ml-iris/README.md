# iris — end-to-end ML pipeline on Helion

The simplest complete ML pipeline that exercises every piece of the
Helion ML surface: artifact staging, workflow DAG with data flow,
dataset + model registries, and (via a separate submit) an
inference service with a readiness-probed port mapping.

```
           ┌──────────┐      ┌────────────┐      ┌────────┐      ┌──────────┐
workflow:  │  ingest  │ ───▶ │ preprocess │ ───▶ │ train  │ ───▶ │ register │
           └──────────┘      └────────────┘      └────────┘      └──────────┘
              RAW_CSV        TRAIN_PARQUET         MODEL            POSTs to
                             TEST_PARQUET         METRICS     /api/datasets,
                                                              /api/models

separate submit (service job, not part of DAG):
           ┌──────────┐
           │   serve  │ ──▶ /healthz (probe every 5 s)
           │  (fastapi│ ──▶ /predict  (POST {features: [[...]]})
           └──────────┘
```

The DAG and the service are deliberately separate: a service never
terminates, so a service inside a workflow would block the DAG from
reaching a terminal state. The `--serve` flag on `submit.py` submits
them in sequence for you.

> **Status: acceptance-green (2026-04-18).** The
> `docker-compose.iris.yml` + `Dockerfile.node-python` overlay runs
> this pipeline end-to-end against a clean checkout; the workflow
> reaches `completed` with zero `ml.resolve_failed` events, the
> registry shows iris-logreg/v1 with accuracy 0.9667 and correct
> lineage, the serve job becomes `ready` within one probe tick,
> and `POST /predict` returns the right class for a setosa row.

---

## Prerequisites

On your laptop:

- Go toolchain and Docker Compose (to run a local Helion cluster).
- Python 3.11 with `pip`, so `submit.py` can parse the workflow YAML.
- A Helion JWT bearer token (see [`docs/JWT-GUIDE.md`](../../docs/JWT-GUIDE.md) § Fetching a token).

On the node(s) that will run the workflow jobs:

- Python 3.11 on `$PATH`.
- The pip dependencies from [`requirements.txt`](requirements.txt)
  installed in the node's runtime environment.

For the local Docker-Compose run, this repo ships
[`Dockerfile.node-python`](../../Dockerfile.node-python) (Python +
iris deps pre-baked) and [`docker-compose.iris.yml`](../../docker-compose.iris.yml)
(overlay that swaps both nodes to that image and injects the
coordinator-side credentials). See the updated "Running it" section
below.

---

## Running it

1. **Start the cluster with the iris overlay.**

   ```bash
   rm -rf state/ logs/ && mkdir -p state logs
   COMPOSE_PROFILES=analytics,ml docker compose \
     -f docker-compose.yml \
     -f docker-compose.e2e.yml \
     -f docker-compose.iris.yml \
     up -d --build
   ```

   The three-file overlay gives you: coordinator + 2 Python-capable
   nodes + MinIO (S3 artifact store) + Postgres (analytics sink),
   with a root token written to the shared `/app/state/root-token`
   and injected into each node's env.

2. **Export environment.**

   ```bash
   export HELION_API_URL=http://127.0.0.1:8080
   # Jobs running inside the cluster reach the coordinator via the
   # internal DNS name, not 127.0.0.1 — submit.py injects this into
   # every job's env block so register.py can call back.
   export HELION_JOB_API_URL=http://coordinator:8080
   export HELION_TOKEN=$(docker exec helion-coordinator cat /app/state/root-token)
   ```

3. **Submit the workflow.**

   ```bash
   cd examples/ml-iris
   python submit.py workflow.yaml
   ```

   Watch progress on the dashboard:

   - `/workflows` — top-level status.
   - `/ml/pipelines/iris-wf-1` — DAG view with artifact edges.
   - `/events` — `ml.resolve_failed` / `dataset.registered` /
     `model.registered` / `job.unschedulable` events as the run
     progresses.

4. **After the workflow completes, check the registry.**

   ```bash
   curl -H "Authorization: Bearer $HELION_TOKEN" \
     "$HELION_API_URL/api/datasets"
   # → includes iris/v1
   curl -H "Authorization: Bearer $HELION_TOKEN" \
     "$HELION_API_URL/api/models"
   # → includes iris-logreg/v1 with source_job_id + metrics
   ```

   On the dashboard, the `iris-logreg` row in `/ml/models` should
   show:

   - `source_job_id` link into the training job's detail page.
   - `source_dataset` link into `/ml/datasets?name=iris&version=v1`.
   - metrics pills for `accuracy` and `f1_macro`.

### Serving — manual step

`submit.py --serve` is wired to submit the serve job automatically
after the workflow succeeds, but it depends on the `registry://`
input-URI scheme being resolved by the coordinator — **that scheme
is not yet implemented**. Until it lands, submit the serve job by
hand with the model's concrete URI substituted in.

The registered model URI comes back as `file://` on local Docker
backends (Stager doesn't yet expose its assigned `s3://` URI to
`register.py` — see known gap #3 below). For the serve job, grab
the authoritative `s3://` URI from the training job's lineage:

```bash
MODEL_URI=$(curl -s -H "Authorization: Bearer $HELION_TOKEN" \
  "$HELION_API_URL/workflows/iris-wf-1/lineage" \
  | python -c "import sys,json; d=json.load(sys.stdin); [print(o['uri']) for j in d['jobs'] if j['name']=='train' for o in j.get('outputs',[]) if o['name']=='MODEL']")
```

Submit the serve job. `PYTHONPATH=/app/ml-iris` makes `uvicorn`
find `serve:app` — the image bakes the iris scripts there but the
runtime doesn't inherit node env, so the job spec has to declare
the path explicitly:

```bash
cat <<JSON | curl -s -X POST \
  -H "Authorization: Bearer $HELION_TOKEN" \
  -H "Content-Type: application/json" \
  "$HELION_API_URL/jobs" -d @-
{
  "id": "iris-serve-1",
  "command": "uvicorn",
  "args": ["serve:app", "--host", "0.0.0.0", "--port", "8000"],
  "env": {"PYTHONPATH": "/app/ml-iris"},
  "inputs": [{
    "name": "MODEL",
    "uri": "$MODEL_URI",
    "local_path": "model.joblib"
  }],
  "service": {
    "port": 8000,
    "health_path": "/healthz",
    "health_initial_ms": 2000
  }
}
JSON
```

The dashboard's `/ml/services` view (and `curl $HELION_API_URL/api/services`)
shows the service go from not-listed → ready within one probe
interval (~5 s).

Test a prediction. The upstream URL uses the node's internal DNS
name, so hit it from inside the helion-net network rather than
from the host:

```bash
UPSTREAM=$(curl -s -H "Authorization: Bearer $HELION_TOKEN" \
  "$HELION_API_URL/api/services/iris-serve-1" \
  | python -c "import sys,json; print(json.load(sys.stdin)['upstream_url'].rsplit('/',1)[0])")
# UPSTREAM is now "http://<node-container-id>:8000"; exec into the
# coordinator container (or any on helion-net) to reach it:
docker exec helion-coordinator \
  wget -qO- --header='Content-Type: application/json' \
       --post-data='{"features":[[5.1,3.5,1.4,0.2]]}' \
       "$UPSTREAM/predict"
# → {"predictions":[0]}
```

---

## File reference

| File | Purpose |
|------|---------|
| [`workflow.yaml`](workflow.yaml) | 4-job DAG definition, converted to JSON by `submit.py`. |
| [`ingest.py`](ingest.py) | Downloads the UCI iris CSV → `RAW_CSV` output. |
| [`preprocess.py`](preprocess.py) | `RAW_CSV` → `TRAIN_PARQUET` + `TEST_PARQUET`. |
| [`train.py`](train.py) | `TRAIN_PARQUET` + `TEST_PARQUET` → `MODEL` + `METRICS`. |
| [`register.py`](register.py) | POSTs to `/api/datasets` + `/api/models` with lineage. |
| [`serve.py`](serve.py) | FastAPI inference app; submitted as a Service job. |
| [`submit.py`](submit.py) | YAML → JSON converter + `/workflows` client. |
| [`requirements.txt`](requirements.txt) | Pip deps for all five scripts. |

---

## Known gaps — things the author expects to fix on the first real run

1. **`registry://` input scheme is not implemented.** The serve job's
   `submit.py --serve` path writes `registry://models/iris-logreg/v1`
   as the input URI; the coordinator's dispatch-time resolver
   doesn't know this scheme and will currently either reject the
   job or dispatch it with the unresolved URI. The README's
   "Serving — manual step" section works around this by looking
   up the concrete URI via `curl` and pasting it into the submit.
   A proper fix is a small resolver addition — likely filed under
   `docs/planned-features/deferred/` after first use.
2. **Node image doesn't ship Python.** The local Docker-Compose
   `Dockerfile.node` builds a minimal Go binary; running Python
   jobs needs a different base image. This is a per-cluster
   operator choice (Python 3.11 + `pip install -r requirements.txt`
   at build time) rather than something the example can ship on
   its own.
3. **Iris CSV mirror availability.** `ingest.py` pulls from the
   `scikit-learn` GitHub raw URL. If that URL ever moves, edit
   `IRIS_URL` in `ingest.py`. A more robust alternative is to
   bake the CSV into the example folder — not done yet because
   the fresh-download path exercises more of Helion's plumbing
   (network-backed ingest = realistic).
4. **Artifact URI in `register.py` falls back to `file://` on local
   backends.** The Stager doesn't currently expose the S3 URI it
   assigned to the running job. `register.py` handles this with a
   `_resolve_uri` helper that preferentially reads
   `HELION_INPUT_<NAME>_URI` (set by the Stager on S3 backends,
   per the design in `internal/staging/`) and falls back to the
   local path. On a local Docker-Compose cluster this produces
   `file://` URIs, which is fine for the demo but will want a
   revisit once S3 is the default backend.

These are all recoverable from a first run's error messages — none
of them are structural. The scripts are written to fail loud with
clear messages so the first real run's log tells you exactly which
prereq (or missing feature) to fix.
