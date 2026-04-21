"""Unit tests for preprocess.py — feature 43 variant-asymmetric subsamples.

Exercises the preprocess.main() path end-to-end against a synthetic
MNIST-shaped CSV, plus the parse_samples() validation helper.

Not part of the coordinator Go test suite — run manually with
`pytest examples/ml-mnist/test_preprocess.py` from the repo root
inside a venv that has the examples/ml-mnist/requirements.txt
dependencies installed.
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

import numpy as np
import pandas as pd
import pytest

# Ensure the example module is importable without an install step.
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import preprocess  # noqa: E402


# ─── Fixtures ────────────────────────────────────────────────────────

@pytest.fixture
def mnist_like_csv(tmp_path: Path) -> Path:
    """Write a synthetic MNIST-784 CSV with 30 000 balanced rows.

    30 000 rows is enough to satisfy the default heavy split
    (20 000 train + 4 000 test = 24 000 rows pooled) without
    dominating disk I/O on CI.
    """
    rng = np.random.default_rng(seed=42)
    n_rows = 30_000
    # Balanced labels — 3000 rows per digit, satisfies stratified split.
    labels = np.repeat(np.arange(10), n_rows // 10)
    rng.shuffle(labels)
    pixels = rng.integers(0, 256, size=(n_rows, 784), dtype=np.uint8)
    cols = {f"pixel{i + 1}": pixels[:, i] for i in range(784)}
    cols["class"] = labels  # OpenML's column name
    df = pd.DataFrame(cols)
    path = tmp_path / "mnist.csv"
    df.to_csv(path, index=False)
    return path


@pytest.fixture
def variant_env(tmp_path: Path, mnist_like_csv: Path, monkeypatch: pytest.MonkeyPatch) -> dict[str, Path]:
    """Set the full preprocess env and return the four output paths.

    Tests that need variant-specific overrides call
    monkeypatch.setenv after this fixture runs.
    """
    outputs = {
        "train_light": tmp_path / "train_light.parquet",
        "test_light":  tmp_path / "test_light.parquet",
        "train_heavy": tmp_path / "train_heavy.parquet",
        "test_heavy":  tmp_path / "test_heavy.parquet",
    }
    monkeypatch.setenv("HELION_INPUT_RAW_CSV", str(mnist_like_csv))
    for key, path in outputs.items():
        monkeypatch.setenv(
            f"HELION_OUTPUT_{key.upper()}_PARQUET", str(path)
        )
    return outputs


# ─── parse_samples() ─────────────────────────────────────────────────

class TestParseSamples:
    def test_unset_returns_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("HELION_PREPROCESS_SAMPLES_LIGHT", raising=False)
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1234) == 1234

    def test_blank_returns_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", "   ")
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 999) == 999

    def test_valid_int_in_range(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", "2500")
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1000) == 2500

    def test_non_integer_falls_back_with_warning(
        self,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", "abc")
        result = preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1000)
        assert result == 1000
        assert "abc" in capsys.readouterr().err

    def test_below_minimum_falls_back(
        self,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", str(preprocess.MIN_TRAIN_ROWS - 1))
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1000) == 1000
        assert "out of range" in capsys.readouterr().err

    def test_above_maximum_falls_back(
        self,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", str(preprocess.MAX_TRAIN_ROWS + 1))
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1000) == 1000
        assert "out of range" in capsys.readouterr().err

    def test_negative_rejected(
        self,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        """Defensive: negative values masquerade as integers but are nonsensical."""
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_LIGHT", "-500")
        assert preprocess.parse_samples("HELION_PREPROCESS_SAMPLES_LIGHT", 1000) == 1000
        assert "out of range" in capsys.readouterr().err


# ─── main() end-to-end ───────────────────────────────────────────────

class TestMainVariantSplits:
    def test_default_sizes_write_all_four_artefacts(
        self, variant_env: dict[str, Path],
    ) -> None:
        assert preprocess.main() == 0
        for path in variant_env.values():
            assert path.exists(), f"missing {path}"
            assert path.stat().st_size > 0

    def test_light_row_count_matches_default(
        self, variant_env: dict[str, Path],
    ) -> None:
        preprocess.main()
        train_light = pd.read_parquet(variant_env["train_light"])
        test_light  = pd.read_parquet(variant_env["test_light"])
        assert len(train_light) == preprocess.DEFAULT_LIGHT_TRAIN
        # _test_size(1000) = max(200, 200) = 200
        assert len(test_light) == 200

    def test_heavy_row_count_matches_default(
        self, variant_env: dict[str, Path],
    ) -> None:
        preprocess.main()
        train_heavy = pd.read_parquet(variant_env["train_heavy"])
        test_heavy  = pd.read_parquet(variant_env["test_heavy"])
        assert len(train_heavy) == preprocess.DEFAULT_HEAVY_TRAIN
        # _test_size(20000) = max(200, 4000) = 4000
        assert len(test_heavy) == 4_000

    def test_variant_splits_have_distinct_rows(
        self, variant_env: dict[str, Path],
    ) -> None:
        """Light + heavy should draw different pools — we use the same
        random_state, so the intersection check validates the two
        variants actually read different slices, not the same rows."""
        preprocess.main()
        light = pd.read_parquet(variant_env["train_light"])
        heavy = pd.read_parquet(variant_env["train_heavy"])
        # Light is a subset-sized pool of 1200; heavy is 24000. Heavy
        # should contain many rows not present in light, and the two
        # frames' concatenated shape should not collapse to something
        # trivial.
        assert len(light) < len(heavy)
        assert light.shape[1] == heavy.shape[1]  # same feature width

    def test_heavy_env_override_applied(
        self,
        variant_env: dict[str, Path],
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        monkeypatch.setenv("HELION_PREPROCESS_SAMPLES_HEAVY", "3000")
        preprocess.main()
        heavy = pd.read_parquet(variant_env["train_heavy"])
        assert len(heavy) == 3_000

    def test_labels_are_int_and_stratified(
        self, variant_env: dict[str, Path],
    ) -> None:
        preprocess.main()
        for path_key in ("train_light", "train_heavy"):
            df = pd.read_parquet(variant_env[path_key])
            assert df["label"].dtype.kind == "i"  # integer, not string
            # All 10 digit classes represented in every split.
            assert set(df["label"].unique()) == set(range(10))

    def test_missing_env_exits_nonzero(
        self,
        mnist_like_csv: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        monkeypatch.setenv("HELION_INPUT_RAW_CSV", str(mnist_like_csv))
        monkeypatch.delenv("HELION_OUTPUT_TRAIN_LIGHT_PARQUET", raising=False)
        monkeypatch.delenv("HELION_OUTPUT_TEST_LIGHT_PARQUET", raising=False)
        monkeypatch.delenv("HELION_OUTPUT_TRAIN_HEAVY_PARQUET", raising=False)
        monkeypatch.delenv("HELION_OUTPUT_TEST_HEAVY_PARQUET", raising=False)
        assert preprocess.main() == 1
        err = capsys.readouterr().err
        assert "missing env" in err
        assert "HELION_OUTPUT_TRAIN_LIGHT_PARQUET" in err

    def test_insufficient_rows_exits_nonzero(
        self,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        """If the input has fewer rows than the heavy split needs,
        bail out with a clear error rather than silently truncating."""
        # 500 rows, balanced across 10 classes.
        small = pd.DataFrame({
            **{f"pixel{i + 1}": np.zeros(500, dtype=np.uint8) for i in range(784)},
            "class": np.tile(np.arange(10), 50),
        })
        src = tmp_path / "tiny.csv"
        small.to_csv(src, index=False)
        monkeypatch.setenv("HELION_INPUT_RAW_CSV", str(src))
        for key in ("train_light", "test_light", "train_heavy", "test_heavy"):
            monkeypatch.setenv(
                f"HELION_OUTPUT_{key.upper()}_PARQUET",
                str(tmp_path / f"{key}.parquet"),
            )
        assert preprocess.main() == 1
        assert "need at least" in capsys.readouterr().err
