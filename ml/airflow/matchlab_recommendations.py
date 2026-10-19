"""One scheduled snapshot, all three models, analytics, then atomic cache promotion."""

from datetime import datetime, timedelta, timezone
import hashlib
import os

from airflow.sdk import dag, task, get_current_context
from matchlab_ml import pipeline


@dag(dag_id="matchlab_recommendations", schedule="0 */6 * * *", start_date=datetime(2026, 10, 1, tzinfo=timezone.utc),
     catchup=False, max_active_runs=1, default_args={"retries": 2, "retry_delay": timedelta(minutes=2)},
     tags=["matchlab", "recommendations", "analytics"])
def recommendations():
    @task(execution_timeout=timedelta(minutes=10))
    def extract_snapshot():
        context = get_current_context()
        run_key = hashlib.sha256(context["run_id"].encode()).hexdigest()[:24]
        directory = pipeline.run_directory(os.environ["ML_ARTIFACT_DIR"], "airflow-" + run_key)
        pipeline.extract_stage(directory, source=os.environ.get("ML_SOURCE", "postgres"))
        return str(directory)  # Only a local path in XCom, never source records.

    @task(execution_timeout=timedelta(minutes=30))
    def fit_and_compare(directory):
        from pathlib import Path
        pipeline.fit_stage(Path(directory))

    @task(execution_timeout=timedelta(minutes=10))
    def refresh_analytics(directory):
        from pathlib import Path
        pipeline.analytics_stage(Path(directory), os.environ.get("ML_MODEL", "baseline"))

    @task(execution_timeout=timedelta(minutes=10))
    def publish(directory):
        from pathlib import Path
        pipeline.publish_stage(Path(directory), os.environ.get("ML_MODEL", "baseline"))

    directory = extract_snapshot()
    fitted = fit_and_compare(directory)
    refreshed = refresh_analytics(directory)
    published = publish(directory)
    fitted >> refreshed >> published


recommendations()
