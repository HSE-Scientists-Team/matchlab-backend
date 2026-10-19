"""The same small functions run in CLI and Airflow; no hidden framework state."""

import hashlib
import json
import os
import time
from datetime import datetime, timezone
from pathlib import Path
from uuid import uuid4

from . import analytics, cache, demo, extract, tracking
from .evaluate import evaluate
from .rank import score_snapshot


def run_directory(root, run_id):
    if not run_id or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_" for c in run_id):
        raise ValueError("unsafe run ID")
    directory = Path(root) / run_id
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    return directory


def extract_stage(directory, source="postgres"):
    if source not in {"postgres", "demo"}:
        raise ValueError("source must be postgres or explicitly demo")
    snapshot_path = directory / "snapshot.json"
    # A task retry/clear reuses the same input. Start a new DAG run for new data.
    snapshot = extract.load(snapshot_path) if snapshot_path.exists() else (
        extract.from_postgres() if source == "postgres" else demo.snapshot())
    if snapshot["source_mode"] != ("postgres" if source == "postgres" else "synthetic"):
        raise ValueError("cannot change the source of an existing run")
    extract.validate(snapshot)
    extract.save(directory / "snapshot.json", snapshot)
    digest = hashlib.sha256(json.dumps(snapshot, sort_keys=True).encode()).hexdigest()
    manifest_path = directory / "manifest.json"
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text())
        if manifest["dataset_hash"] != digest:
            raise ValueError("run snapshot does not match its immutable manifest")
        return manifest
    manifest = {"schema_version": 1, "dataset_hash": digest, "source_mode": snapshot["source_mode"],
                "code_version": os.environ.get("ML_CODE_VERSION", "working-tree"),
                "extracted_at": snapshot["extracted_at"], "users": len(snapshot["users"]),
                "projects": len(snapshot["projects"]), "requests": len(snapshot["requests"])}
    extract.save(directory / "manifest.json", manifest)
    return manifest


def fit_stage(directory):
    snapshot = extract.load(directory / "snapshot.json")
    manifest = json.loads((directory / "manifest.json").read_text())
    all_metrics = {}
    for model in ("baseline", "hybrid", "learned"):
        outputs = [directory / (model + suffix) for suffix in ("-recommendations.json", "-model.json", "-metrics.json")]
        if all(path.exists() for path in outputs):
            metrics = json.loads(outputs[2].read_text())
            tracking.log(directory.name, model, manifest, metrics, directory)
            all_metrics[model] = metrics
            continue
        started = time.monotonic()
        results, fitted = score_snapshot(snapshot, model, limit=int(os.environ.get("ML_CACHE_ITEMS", "100")))
        items = [item for kinds in results.values() for rows in kinds.values() for item in rows]
        metrics = {"generated_users": len(results), "recommendations": len(items),
                   "fallback_fraction": sum(item["fallback"] for item in items)/len(items) if items else 0.0,
                   "duration_seconds": time.monotonic()-started, **evaluate(snapshot, model),
                   **(fitted["ranking_metrics"] if model == "learned" else {})}
        extract.save(directory / (model + "-recommendations.json"), results)
        extract.save(directory / (model + "-model.json"), fitted)
        extract.save(directory / (model + "-metrics.json"), metrics)
        tracking.log(directory.name, model, manifest, metrics, directory)
        all_metrics[model] = metrics
    return all_metrics


def analytics_stage(directory, model):
    snapshot = extract.load(directory / "snapshot.json")
    manifest = json.loads((directory / "manifest.json").read_text())
    metrics = {name: json.loads((directory / (name + "-metrics.json")).read_text())
               for name in ("baseline", "hybrid", "learned")}
    analytics.write(snapshot, directory.name, model, manifest["dataset_hash"], metrics)


def publish_stage(directory, model):
    if model not in {"baseline", "hybrid", "learned"}:
        raise ValueError("serving model must be baseline, hybrid or learned")
    manifest = json.loads((directory / "manifest.json").read_text())
    results = json.loads((directory / (model + "-recommendations.json")).read_text())
    if manifest["source_mode"] == "synthetic" and os.environ.get("ML_ALLOW_DEMO_PUBLISH") != "true":
        raise ValueError("synthetic cache publication requires ML_ALLOW_DEMO_PUBLISH=true")
    if not results:
        # An empty new installation still needs a truthful zero-count dashboard.
        # Do not clear any previous cache generation as a side effect.
        analytics.complete(directory.name)
        return False
    client = cache.connect()
    with client.lock(cache.PREFIX + ":publish-lock", timeout=60, blocking_timeout=1):
        cache.publish(client, directory.name, model, results, manifest["extracted_at"],
                      ttl=int(os.environ.get("ML_CACHE_TTL_SECONDS", "86400")))
        analytics.complete(directory.name)
    return True


def run(root, source="postgres", model="baseline", offline=False, run_id=None, analytics_only=False):
    if offline and analytics_only:
        raise ValueError("offline and analytics_only are mutually exclusive")
    run_id = run_id or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S") + "-" + uuid4().hex[:8]
    directory = run_directory(root, run_id)
    extract_stage(directory, source)
    metrics = fit_stage(directory)
    published = False
    if not offline:
        analytics_stage(directory, model)
        if analytics_only:
            analytics.complete(directory.name)
        else:
            published = publish_stage(directory, model)
    return {"run_id": run_id, "artifact_directory": str(directory), "model": model,
            "offline": offline, "analytics_only": analytics_only, "cache_published": published, "metrics": metrics}
