"""serve.py — inference service for the iris end-to-end demo.

Loads the model produced by train.py and exposes two endpoints:

  GET  /healthz      — Helion's service prober hits this every 5s.
                       Returns 200 once the model is loaded in
                       memory; 503 during the brief startup window.
  POST /predict      — takes a JSON body of feature arrays, returns
                       predicted class indices.

This is submitted as a *separate* Helion job (not part of the
workflow DAG) because a service never terminates — a service node
inside a DAG would block the workflow from completing. The README
walks through the two-submit shape.

Environment
-----------
HELION_INPUT_MODEL   Path to the model pickle, staged by Helion's
                     Stager from `from: train.MODEL`. Required.
PORT                 Port to bind. Defaults to 8000 so it matches
                     the ServiceSpec.port in the sample submit.

The serve job's ServiceSpec in the Helion submit JSON should set:
  {"port": 8000, "health_path": "/healthz", "health_initial_ms": 2000}
so the node-side prober waits 2s after process start before the
first probe — sklearn model load is fast, but giving it a 2s
grace avoids a spurious `service.unhealthy` on the very first tick.
"""
from __future__ import annotations

import os
import sys
from typing import List

import joblib
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel


# Global-ish model handle: loaded once at process start, served
# from memory on every request. FastAPI's startup event is an
# acceptable alternative but the error-handling shape is simpler
# when the load is unconditional and failure means the process
# exits non-zero (Helion restarts it under the retry policy).
_model = None


class PredictRequest(BaseModel):
    """Body for POST /predict. features is a 2-D array:
    outer list = rows to score, inner list = the 4 feature columns
    in the same order train.py used:
    [sepal_length, sepal_width, petal_length, petal_width]."""
    features: List[List[float]]


class PredictResponse(BaseModel):
    predictions: List[int]


def _load_model() -> object:
    """Load at import time so /healthz can answer 200 as soon as
    uvicorn accepts its first request. Exits the process non-zero
    if the model file is missing or corrupt — the node agent's
    retry policy (if one is set on the serve job) handles restart."""
    path = os.environ.get("HELION_INPUT_MODEL")
    if not path:
        print("HELION_INPUT_MODEL not set", file=sys.stderr)
        sys.exit(1)
    if not os.path.isfile(path):
        print(f"model file missing: {path}", file=sys.stderr)
        sys.exit(1)
    return joblib.load(path)


_model = _load_model()
app = FastAPI(title="iris-logreg")


@app.get("/healthz")
def healthz() -> dict:
    # _model is set at import time; if we got here, the load
    # succeeded. Returning a JSON body (rather than an empty 200)
    # so operators curling the endpoint see a useful signal.
    return {"status": "ok", "model_loaded": _model is not None}


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest) -> PredictResponse:
    if _model is None:
        # Should not be reachable — _load_model exits on failure —
        # but belt-and-braces for a future refactor where the load
        # becomes lazy.
        raise HTTPException(status_code=503, detail="model not loaded")
    if not req.features:
        raise HTTPException(status_code=400, detail="features must not be empty")
    # sklearn's predict takes a 2-D array; pydantic already
    # validated the inner shape via List[List[float]].
    preds = _model.predict(req.features)
    return PredictResponse(predictions=[int(p) for p in preds])


if __name__ == "__main__":
    # Let the user run `python serve.py` locally for quick smoke-
    # tests. In production the node runs `uvicorn serve:app
    # --host 0.0.0.0 --port 8000` via the service job's command.
    import uvicorn  # local import so the workflow jobs that don't
                    # serve don't pay the uvicorn import cost.

    port = int(os.environ.get("PORT", "8000"))
    uvicorn.run(app, host="0.0.0.0", port=port)  # noqa: S104 — bind-
    # to-all is required so the node's probe at 127.0.0.1:<port>
    # reaches us from the same container/host.
