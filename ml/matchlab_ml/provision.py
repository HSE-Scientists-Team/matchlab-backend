"""Explicit provisioning: database roles, export permissions, BI schema.

This script is never run by an online backend service. It uses administrator
credentials only in the short-lived provisioning container.
"""

import os
import time

import psycopg
from psycopg import sql
from psycopg.conninfo import make_conninfo

from matchlab_ml.analytics import BI_VIEWS
from matchlab_ml.extract import VIEWS
import matchlab_ml.analytics
from pathlib import Path


def role(conn, name, password):
    if not conn.execute("SELECT 1 FROM pg_roles WHERE rolname=%s", (name,)).fetchone():
        conn.execute(sql.SQL("CREATE ROLE {} LOGIN PASSWORD {} NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION").format(
            sql.Identifier(name), sql.Literal(password)))
    # Reruns never silently rotate a stored credential.


def wait_database(dsn, attempts=120):
    for attempt in range(attempts):
        try:
            with psycopg.connect(dsn, connect_timeout=5) as conn:
                conn.execute("SELECT 1")
            return
        except psycopg.OperationalError:
            if attempt + 1 == attempts:
                raise RuntimeError("Database did not become ready; credentials omitted") from None
            time.sleep(5)


def main():
    # Only this named Docker volume, never a source or host directory.
    if os.environ.get("ML_PREPARE_ARTIFACTS") == "true":
        os.chown("/artifacts", 50000, 0)
        os.chmod("/artifacts", 0o750)
    source_admin = os.environ.get("SOURCE_ADMIN_URL") or make_conninfo(
        host=os.environ.get("SOURCE_ADMIN_HOST", "postgres"), dbname="matchlab", user="matchlab",
        password=os.environ["SOURCE_ADMIN_PASSWORD"], connect_timeout=5)
    # Backend services apply the complete migration set before opening their API.
    for attempt in range(60):
        try:
            with psycopg.connect(source_admin, connect_timeout=5) as conn:
                ready = all(conn.execute("SELECT to_regclass(%s)", (view,)).fetchone()[0] for view in VIEWS.values())
        except psycopg.OperationalError:
            ready = False
        if ready:
            break
        time.sleep(2)
    else:
        raise RuntimeError("Versioned ML exports missing: rebuild all backend images with migration 00009")
    with psycopg.connect(source_admin) as conn:
        role(conn, "ml_reader", os.environ["ML_SOURCE_PASSWORD"])
        conn.execute("ALTER ROLE ml_reader SET default_transaction_read_only=on")
        conn.execute("GRANT CONNECT ON DATABASE matchlab TO ml_reader")
        conn.execute("GRANT USAGE ON SCHEMA users, projects TO ml_reader")
        for view in VIEWS.values():
            conn.execute(sql.SQL("GRANT SELECT ON {} TO ml_reader").format(sql.Identifier(*view.split("."))))

    admin_dsn = os.environ["ML_ADMIN_URL"]
    wait_database(admin_dsn)
    accounts = [("analytics_writer", "analytics", "ML_ANALYTICS_PASSWORD"),
                ("airflow", "airflow", "ML_AIRFLOW_PASSWORD"),
                ("superset", "superset", "ML_SUPERSET_DB_PASSWORD")]
    with psycopg.connect(admin_dsn, autocommit=True) as conn:
        for user, database, password in accounts:
            role(conn, user, os.environ[password])
            if not conn.execute("SELECT 1 FROM pg_database WHERE datname=%s", (database,)).fetchone():
                conn.execute(sql.SQL("CREATE DATABASE {} OWNER {}").format(sql.Identifier(database), sql.Identifier(user)))
            conn.execute(sql.SQL("REVOKE CONNECT ON DATABASE {} FROM PUBLIC").format(sql.Identifier(database)))
        role(conn, "analytics_reader", os.environ["ML_BI_PASSWORD"])
        conn.execute("GRANT CONNECT ON DATABASE analytics TO analytics_reader")
        conn.execute("ALTER ROLE analytics_reader SET default_transaction_read_only=on")
    analytics_dsn = make_conninfo(admin_dsn, dbname="analytics", user="analytics_writer", password=os.environ["ML_ANALYTICS_PASSWORD"])
    with psycopg.connect(analytics_dsn) as conn:
        conn.execute(Path(matchlab_ml.analytics.__file__).with_name("analytics.sql").read_text())
        conn.execute("GRANT USAGE ON SCHEMA analytics TO analytics_reader")
        for view in BI_VIEWS:
            conn.execute(sql.SQL("GRANT SELECT ON analytics.{} TO analytics_reader").format(sql.Identifier(view)))
    print("Local export reader and separate analytics/Airflow/Superset databases ready; no source rows changed.")


if __name__ == "__main__":
    main()
