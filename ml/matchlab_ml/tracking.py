"""Artifact logging is optional offline and required when a tracking URI is set."""

import hashlib
import json
import os


def log(run_id, model, manifest, metrics, directory):
    if not os.environ.get("MLFLOW_TRACKING_URI"):
        return
    import mlflow

    mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
    mlflow.set_experiment("MatchLab recommendations")
    with mlflow.start_run(run_name=run_id + "-" + model):
        mlflow.set_tags({"matchlab_run_id": run_id, "data_source": manifest["source_mode"],
                         "evaluation_type": metrics["evaluation_type"]})
        mlflow.log_params({"algorithm": model, "dataset_sha256": manifest["dataset_hash"],
                           "code_version": manifest.get("code_version", "working-tree"),
                           "schema_version": 1, "users": manifest["users"], "projects": manifest["projects"]})
        mlflow.log_metrics({key: value for key, value in metrics.items()
                            if isinstance(value, (int, float)) and not isinstance(value, bool)})
        # Free-text vocabulary can itself contain personal information. Keep the
        # fitted vocabulary locally; upload its fingerprint and safe parameters.
        fitted = json.loads((directory / (model + "-model.json")).read_text())
        vocabulary = fitted.pop("idf", None)
        if vocabulary is not None:
            fitted["vocabulary_size"] = len(vocabulary)
            fitted["vocabulary_sha256"] = hashlib.sha256(json.dumps(vocabulary, sort_keys=True).encode()).hexdigest()
        history = fitted.pop("history", [])
        if model == "learned":
            mlflow.set_tag("training_status", fitted["training_status"])
            people = fitted.get("people_ranker", {})
            people_history = people.pop("history", [])
            if people:
                mlflow.set_tag("people_training_status", people["training_status"])
                mlflow.set_tag("people_label_signal", people["label_signal"])
                mlflow.log_params({"people_"+key: people[key] for key in
                                   ("training_pairs", "validation_pairs", "validation_cutoff")})
            for row in people_history:
                mlflow.log_metrics({"people_train_loss": row["train_loss"],
                                    "people_validation_loss": row["validation_loss"]}, step=row["epoch"])
            if people_history:
                from .learned import curve_svg
                mlflow.log_dict(people_history, "people-loss-history.json")
                mlflow.log_text(curve_svg(people_history), "people-loss-curve.svg")
            mlflow.log_params({key: fitted[key] for key in
                               ("training_pairs", "validation_pairs", "negative_sampling", "people_model")})
            for row in history:
                mlflow.log_metrics({"train_loss": row["train_loss"],
                                    "validation_loss": row["validation_loss"]}, step=row["epoch"])
            if history:
                from .learned import curve_svg
                mlflow.log_dict(history, "loss-history.json")
                mlflow.log_text(curve_svg(history), "loss-curve.svg")
        mlflow.log_dict(fitted, model + "-parameters.json")
        for file in ("manifest.json", model + "-metrics.json"):
            mlflow.log_artifact(str(directory / file))
