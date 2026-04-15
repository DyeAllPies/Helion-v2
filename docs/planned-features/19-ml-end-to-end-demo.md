# Feature: ML End-to-End Demo

**Priority:** P1
**Status:** Pending
**Affected files:** `examples/ml-iris/` (new).
**Parent slice:** [feature 10 — ML pipeline](10-minimal-ml-pipeline.md)

## End-to-end demo workflow

Ship a worked example under `examples/ml-iris/`:

```
examples/ml-iris/
├── workflow.yaml          # 4-job DAG: ingest → preprocess → train → register
├── ingest.py              # downloads iris CSV → outputs raw.csv
├── preprocess.py          # raw.csv → train.parquet + test.parquet
├── train.py               # train.parquet → model.pt + metrics.json
├── register.py            # POSTs to /api/models with metrics + lineage
└── serve.py               # FastAPI app loading model.pt, exposed via Service
```

The example is the acceptance test for "can a normal person run an ML
pipeline on Helion." If this works on a clean checkout with one
`docker compose up` + `helion-cli submit examples/ml-iris/workflow.yaml`,
the feature is done.

## Security plan (this step)

| New attack surface | Controls landing this step | SECURITY.md doctrine used |
|-------------------|---------------------------|---------------------------|
| None (read-only artifact reads) | — | — |
