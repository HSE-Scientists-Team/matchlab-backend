"""Explicit retention preview/apply and freshness checks for the private ML pilot.

Pause the DAG before applying retention. Never run against a live server without
operator authorization. Only recognized run directories and analytical snapshots
are removed; model tracking metadata and backups require their own policy.
"""

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import shutil

from . import cache

TABLES = ("users_snapshot", "projects_snapshot", "roles_snapshot", "requests_snapshot", "model_metrics")


def artifact_candidates(root, protected, now, days=7):
    if days < 1:
        raise ValueError("retention must be at least one day")
    cutoff = now - timedelta(days=days)
    result = []
    for directory in Path(root).iterdir():
        if directory.is_symlink() or not directory.is_dir() or directory.name in protected:
            continue
        manifest = directory / "manifest.json"
        snapshot = directory / "snapshot.json"
        if manifest.is_symlink() or snapshot.is_symlink() or not snapshot.is_file():
            continue
        try:
            data = json.loads(manifest.read_text())
            timestamp = datetime.fromisoformat(data["extracted_at"])
            if data["schema_version"] != 1 or timestamp.tzinfo is None:
                continue
        except (OSError, ValueError, KeyError, TypeError):
            continue
        if timestamp < cutoff:
            result.append(directory)
    return sorted(result)


def freshness_errors(latest, active, ttl, generated_at, now, max_hours=12, has_users=True):
    errors = []
    if latest is None or latest[2] != "postgres":
        errors.append("no completed PostgreSQL analytics run")
    elif now - latest[1] > timedelta(hours=max_hours):
        errors.append("analytics source is stale")
    if has_users:
        if not active or ttl <= 0:
            errors.append("recommendation cache is missing or expired")
        elif generated_at is None or now.timestamp() - generated_at > max_hours * 3600:
            errors.append("recommendation cache is stale")
    return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact-dir", default=os.environ.get("ML_ARTIFACT_DIR", "/artifacts"))
    parser.add_argument("--raw-days", type=int, default=7)
    parser.add_argument("--analytics-days", type=int, default=30)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true")
    mode.add_argument("--check-freshness", action="store_true")
    args = parser.parse_args()
    if min(args.raw_days, args.analytics_days) < 1:
        parser.error("retention must be at least one day")
    import psycopg
    now = datetime.now(timezone.utc)
    client = cache.connect()
    # If Redis/DB is unavailable, fail closed: an unknown active run is never deleted.
    active = client.get(cache.PREFIX + ":active")
    with psycopg.connect(os.environ["ANALYTICS_DATABASE_URL"], connect_timeout=10) as conn:
        latest = conn.execute("SELECT run_id,extracted_at,data_source FROM analytics.latest_run").fetchone()
        if args.check_freshness:
            has_users = bool(latest and conn.execute(
                "SELECT count(*) FROM analytics.users_snapshot WHERE run_id=%s AND status='active' AND confirmed", (latest[0],)).fetchone()[0])
            raw_time = client.get(cache.PREFIX + ":active-timestamp")
            errors = freshness_errors(latest, active, client.ttl(cache.PREFIX + ":active"),
                                      float(raw_time) if raw_time else None, now, has_users=has_users)
            print(json.dumps({"healthy": not errors, "errors": errors}))
            return int(bool(errors))
        protected = {value for value in (active, latest[0] if latest else None) if value}
        expired = [row[0] for row in conn.execute(
            "SELECT run_id FROM analytics.runs WHERE extracted_at < %s AND NOT (run_id=ANY(%s))",
            (now-timedelta(days=args.analytics_days), list(protected))).fetchall()]
        directories = artifact_candidates(Path(args.artifact_dir), protected, now, args.raw_days)
        print(json.dumps({"apply": args.apply, "artifact_runs": [p.name for p in directories],
                          "analytics_runs": expired, "protected_runs": sorted(protected)}))
        if args.apply:
            for table in TABLES:
                conn.execute("DELETE FROM analytics." + table + " WHERE run_id=ANY(%s)", (expired,))
            conn.execute("DELETE FROM analytics.runs WHERE run_id=ANY(%s)", (expired,))
    if args.apply:
        # DB transaction has committed before private files are removed. Do not
        # follow directory symlinks, even if a directory changed after preview.
        for directory in directories:
            if directory.is_dir() and not directory.is_symlink():
                shutil.rmtree(directory)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
