"""Run after stack.py up and batch. Requires live local Docker services."""

import os
import psycopg
from psycopg.conninfo import make_conninfo

from matchlab_ml.analytics import BI_VIEWS
from matchlab_ml.cache import connect, PREFIX


def main():
    with psycopg.connect(os.environ["SOURCE_DATABASE_URL"]) as source:
        assert source.execute("SHOW default_transaction_read_only").fetchone()[0] == "on"
        assert source.execute("SELECT has_table_privilege(current_user,'users.user_account','SELECT')").fetchone()[0] is False
        assert source.execute("SELECT has_table_privilege(current_user,'users.ml_profiles_v1','SELECT')").fetchone()[0] is True
    with psycopg.connect(os.environ["ANALYTICS_DATABASE_URL"]) as analytics:
        latest = analytics.execute("SELECT run_id,data_source FROM analytics.latest_run").fetchone()
        assert latest, "Run one successful batch first"
        for name in BI_VIEWS:
            analytics.execute("SELECT * FROM analytics." + name + " LIMIT 1").fetchall()
        assert analytics.execute("SELECT has_table_privilege('analytics_reader','analytics.users_snapshot','SELECT')").fetchone()[0] is False
        assert analytics.execute("SELECT has_table_privilege('analytics_reader','analytics.overview','SELECT')").fetchone()[0] is True
    client = connect()
    active = client.get(PREFIX + ":active")
    if active:
        assert client.ttl(PREFIX + ":active") > 0
    print("Export grants, aggregate views, latest analytics run and Redis access checked.")
    print("Also exercise authenticated /api/v1/recommendations/people and /projects with real local accounts.")


if __name__ == "__main__":
    main()
