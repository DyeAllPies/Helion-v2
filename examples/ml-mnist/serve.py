"""serve.py — inference service for the MNIST-784 demo.

FastAPI app loading `model.joblib` from the staged input path and
exposing two routes:

  GET  /healthz  → {"ok": true, "model": "mnist-logreg/v1"}
                   (polled by the node-side prober)
  POST /predict  → {"predictions": [class_int, ...]}
                   body: {"features": [[784 floats], ...]}

Feature vectors are 784 floats in [0, 255] (raw pixel intensities);
the model's inference pipeline internally divides by 255.0 to match
train.py's normalisation.

Run as a Helion service job:
    POST /jobs {
      command: "uvicorn",
      args: ["serve:app", "--host", "0.0.0.0", "--port", "8000"],
      env: {"PYTHONPATH": "/app/ml-mnist"},
      inputs: [{"name": "MODEL", "uri": "...", "local_path": "model.joblib"}],
      service: {"port": 8000, "health_path": "/healthz", "health_initial_ms": 2000}
    }
"""
from __future__ import annotations

import os
import sys

import joblib
import numpy as np
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

MODEL_PATH = os.environ.get("HELION_INPUT_MODEL", "model.joblib")
MODEL_NAME_VERSION = "mnist-logreg/v1"

try:
    model = joblib.load(MODEL_PATH)
except Exception as e:  # noqa: BLE001 — fail loud at import so uvicorn logs it
    print(f"failed to load model from {MODEL_PATH}: {e}", file=sys.stderr)
    raise

app = FastAPI(title="mnist-serve", version="1.0")


class PredictRequest(BaseModel):
    features: list[list[float]]


class PredictResponse(BaseModel):
    predictions: list[int]


@app.get("/healthz")
def healthz() -> dict:
    return {"ok": True, "model": MODEL_NAME_VERSION}


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest) -> PredictResponse:
    if not req.features:
        raise HTTPException(status_code=400, detail="features must be non-empty")
    try:
        x = np.asarray(req.features, dtype=np.float32)
    except (TypeError, ValueError) as e:
        raise HTTPException(status_code=400, detail=f"bad feature matrix: {e}") from e
    if x.ndim != 2 or x.shape[1] != 784:
        raise HTTPException(
            status_code=400,
            detail=f"features must be [N, 784]; got shape {list(x.shape)}",
        )
    # Match train.py's pixel scaling.
    x = x / 255.0
    preds = model.predict(x)
    return PredictResponse(predictions=[int(p) for p in preds])
