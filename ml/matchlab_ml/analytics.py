"""Write minimized snapshots to a separate analytical database, atomically."""

import os
from pathlib import Path

from .features import tags
from .segments import classify

BI_VIEWS = ("overview", "user_segments", "universities", "skill_supply", "registrations",
            "project_status", "project_types", "role_demand", "skill_demand", "recruitment",
            "team_formation", "recommendation_quality")


def write(snapshot, run_id, model, dataset_hash, metrics, dsn=None):
    import psycopg
    from psycopg.types.json import Jsonb

    with psycopg.connect(dsn or os.environ["ANALYTICS_DATABASE_URL"], connect_timeout=10) as conn:
        conn.execute(Path(__file__).with_name("analytics.sql").read_text())
        existing = conn.execute("SELECT dataset_hash,model,state FROM analytics.runs WHERE run_id=%s", (run_id,)).fetchone()
        if existing:
            if existing[:2] != (dataset_hash, model):
                raise ValueError("run ID already belongs to a different dataset or serving model; start a new run")
            if existing[2] == "complete":
                return
        conn.execute("INSERT INTO analytics.runs(run_id,extracted_at,data_source,model,state,dataset_hash) "
                     "VALUES(%s,%s,%s,%s,'prepared',%s) ON CONFLICT(run_id) DO UPDATE SET state='prepared'",
                     (run_id, snapshot["extracted_at"], snapshot["source_mode"], model, dataset_hash))
        # Retries replace only this task-owned, unpublished run, not previous snapshots.
        for table in ("users_snapshot", "projects_snapshot", "roles_snapshot", "requests_snapshot", "model_metrics"):
            conn.execute("DELETE FROM analytics." + table + " WHERE run_id=%s", (run_id,))
        with conn.cursor() as cur:
            # Explicit columns keep SQL readable and independent of column ordering.
            cur.executemany("INSERT INTO analytics.users_snapshot "
                            "(run_id,id,status,system_role,confirmed,registered_at,last_login_at,university,study_level,open_to_collaboration,segment,skills) "
                            "VALUES(" + ",".join(["%s"]*12) + ")", [
                (run_id,u["id"],u["status"],u["role"],u["confirmed"],u["created_at"],u.get("last_login_at"),
                 u.get("university", ""),u.get("study_level", ""),u["open_to_collaboration"],classify(u),
                 Jsonb(sorted(tags(u.get("skills"))))) for u in snapshot["users"]])
            cur.executemany("INSERT INTO analytics.projects_snapshot VALUES(" + ",".join(["%s"]*7) + ")", [
                (run_id,p["id"],p.get("type", ""),p.get("field", ""),p["status"],p["moderation_hidden"],p["created_at"])
                for p in snapshot["projects"]])
            cur.executemany("INSERT INTO analytics.roles_snapshot VALUES(" + ",".join(["%s"]*7) + ")", [
                (run_id,r["id"],p["id"],r["title"],r["slots"],r["occupied"],Jsonb(sorted(tags(r.get("skills")))))
                for p in snapshot["projects"] for r in p.get("roles", [])])
            cur.executemany("INSERT INTO analytics.requests_snapshot VALUES(%s,%s,%s,%s,%s)", [
                (run_id,r["id"],r["type"],r["status"],r["created_at"]) for r in snapshot["requests"]])
            cur.executemany("INSERT INTO analytics.model_metrics VALUES(" + ",".join(["%s"]*9) + ")", [
                (run_id,name,m["generated_users"],m["recommendations"],m["fallback_fraction"],m["duration_seconds"],
                 m["evaluation_users"],m.get("recall_at_10"),m.get("ndcg_at_10")) for name,m in metrics.items()])


def complete(run_id, dsn=None):
    import psycopg
    with psycopg.connect(dsn or os.environ["ANALYTICS_DATABASE_URL"], connect_timeout=10) as conn:
        if conn.execute("UPDATE analytics.runs SET state='complete' WHERE run_id=%s RETURNING run_id", (run_id,)).fetchone() is None:
            raise ValueError("analytics run must be prepared before completion")
