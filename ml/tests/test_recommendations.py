import copy
import json
import math
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from unittest.mock import MagicMock

from matchlab_ml import baseline, cache, demo, extract, tracking
from matchlab_ml.evaluate import evaluate
from matchlab_ml.features import TextModel, cosine, indexes, terms
from matchlab_ml.hybrid import HybridModel
from matchlab_ml.pipeline import extract_stage, publish_stage, run, run_directory
from matchlab_ml.rank import recommend
from matchlab_ml.segments import classify


class RecommendationTests(unittest.TestCase):
    def setUp(self):
        self.data = demo.snapshot()
        self.viewer = self.data["users"][1]

    def test_matching_skills_increase_score(self):
        project = copy.deepcopy(self.data["projects"][0])
        project["roles"][0]["skills"] = ["python", "postgresql"]
        matched = baseline.project(self.viewer, project)
        empty = dict(self.viewer, skills=[])
        self.assertGreater(matched["score"], baseline.project(empty, project)["score"])
        self.assertEqual(matched["role_id"], project["roles"][0]["id"])
        self.assertTrue(matched["factors"])

    def test_bilateral_blocks_and_private_or_inactive_people(self):
        candidates = self.data["users"][2:7]
        self.data["blocks"] = [{"blocker_user_id": candidates[0]["id"], "blocked_user_id": self.viewer["id"]}]
        candidates[1]["status"] = "blocked"
        candidates[2]["confirmed"] = False
        candidates[3]["open_to_collaboration"] = False
        candidates[4]["has_name"] = False
        excluded = {self.viewer["id"], *(u["id"] for u in candidates)}
        for model in ("baseline", "hybrid", "learned"):
            result = recommend(self.data, self.viewer["id"], "people", model)
            self.assertFalse(excluded & {item["id"] for item in result})

    def test_projects_exclude_own_memberships_hidden_closed_full_blocked_owner(self):
        projects = self.data["projects"]
        projects[0]["owner_user_id"] = self.viewer["id"]
        self.data["members"].append({"user_id": self.viewer["id"], "project_id": projects[1]["id"], "status": "active"})
        projects[2]["moderation_hidden"] = True
        projects[3]["status"] = "completed"
        projects[4]["roles"][0]["occupied"] = 3
        projects[5]["owner_user_id"] = demo.identifier(999)
        self.data["blocks"] = [{"blocker_user_id": self.viewer["id"], "blocked_user_id": demo.identifier(999)}]
        for model in ("baseline", "hybrid", "learned"):
            result = recommend(self.data, self.viewer["id"], "projects", model)
            self.assertFalse({p["id"] for p in projects[:6]} & {item["id"] for item in result})

    def test_cold_start_is_explicit_and_deterministic(self):
        cold = self.data["users"][0]
        for model in ("baseline", "hybrid", "learned"):
            for kind in ("people", "projects"):
                result = recommend(self.data, cold["id"], kind, model)
                self.assertTrue(result)
                self.assertTrue(all(item["fallback"] for item in result))
                self.assertEqual(result, recommend(self.data, cold["id"], kind, model))

    def test_scores_and_limits(self):
        for model in ("baseline", "hybrid", "learned"):
            for kind in ("people", "projects"):
                result = recommend(self.data, self.viewer["id"], kind, model, 3)
                self.assertLessEqual(len(result), 3)
                self.assertTrue(all(math.isfinite(item["score"]) and 0 <= item["score"] <= 1 for item in result))
                self.assertEqual(len(result), len({item["id"] for item in result}))
        for invalid in (0, 501, True):
            with self.assertRaises(ValueError):
                recommend(self.data, self.viewer["id"], "people", limit=invalid)
        self.viewer["confirmed"] = False
        self.assertEqual([], recommend(self.data, self.viewer["id"], "people"))

    def test_text_model_and_contact_exclusion(self):
        tokens = terms("Python https://example.org/a user@example.org")
        self.assertEqual(tokens, ["python"])
        model = TextModel(["python nlp", "python postgres", ""])
        self.assertAlmostEqual(cosine(model.vector("python nlp"), model.vector("python nlp")), 1)
        self.assertEqual(cosine({}, {}), 0)

    def test_hybrid_uses_applications_not_invitations(self):
        model = HybridModel(self.data, requests=[dict(self.data["requests"][0], type="invitation")])
        self.assertFalse(model.history)
        model = HybridModel(self.data)
        self.assertTrue(any(model.neighbours(user["id"]) for user in self.data["users"]))
        self.assertNotIn("users", model.artifact())

    def test_no_evaluation_data_is_unknown_not_perfect(self):
        self.data["requests"] = []
        metrics = evaluate(self.data, "baseline")
        self.assertEqual(metrics["evaluation_users"], 0)
        self.assertIsNone(metrics["recall_at_10"])
        self.assertIsNone(metrics["ndcg_at_10"])

    def test_exports_reject_private_fields(self):
        extract.validate(self.data)
        self.viewer["email"] = "private@example.org"
        with self.assertRaises(ValueError):
            extract.validate(self.data)

    def test_exports_reject_request_message_and_invalid_flags(self):
        self.data["requests"][0]["message"] = "private message"
        with self.assertRaises(ValueError):
            extract.validate(self.data)
        del self.data["requests"][0]["message"]
        self.data["projects"][0]["moderation_hidden"] = "false"
        with self.assertRaises(ValueError):
            extract.validate(self.data)

    def test_evaluation_training_stops_at_global_cutoff(self):
        observed = []
        def fit(train):
            observed.extend(train["requests"])
            self.assertFalse(train["members"])
            return HybridModel(train)
        with patch("matchlab_ml.evaluate.HybridModel", side_effect=fit):
            metrics = evaluate(self.data, "hybrid")
        self.assertTrue(observed)
        self.assertTrue(all(event["created_at"] < metrics["evaluation_cutoff"] for event in observed))

    def test_tracking_excludes_fitted_vocabulary_and_source_records(self):
        mlflow = MagicMock()
        with tempfile.TemporaryDirectory() as root:
            directory = Path(root)
            extract.save(directory / "hybrid-model.json", {"version": "hybrid-v1", "idf": {"sensitiveword": 1.2}})
            manifest = {"source_mode": "synthetic", "dataset_hash": "hash", "users": 24, "projects": 12}
            with patch.dict(os.environ, {"MLFLOW_TRACKING_URI": "http://local.invalid"}), patch.dict("sys.modules", {"mlflow": mlflow}):
                tracking.log("test-run", "hybrid", manifest, {"evaluation_type": "proxy", "recall_at_10": None}, directory)
            parameters = mlflow.log_dict.call_args.args[0]
            self.assertNotIn("idf", parameters)
            self.assertEqual(parameters["vocabulary_size"], 1)
            self.assertNotIn("sensitiveword", json.dumps(parameters))
            names = [Path(call.args[0]).name for call in mlflow.log_artifact.call_args_list]
            self.assertEqual(names, ["manifest.json", "hybrid-metrics.json"])

    def test_skill_segment_is_transparent_and_unknown_supported(self):
        self.assertEqual(classify({"skills": []}), "unknown")
        self.assertEqual(classify({"skills": ["python", "nlp", "machine learning"]}), "data_ml")

    def test_pipeline_offline_produces_reproducible_artifacts(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(os.environ, {"MLFLOW_TRACKING_URI": ""}):
            result = run(root, source="demo", offline=True, run_id="test-run")
            directory = Path(result["artifact_directory"])
            manifest = json.loads((directory / "manifest.json").read_text())
            self.assertEqual(manifest["source_mode"], "synthetic")
            self.assertEqual(len(manifest["dataset_hash"]), 64)
            self.assertTrue((directory / "hybrid-model.json").exists())
            self.assertEqual((directory / "snapshot.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
            with self.assertRaises(ValueError):
                run_directory(root, "../escape")

    def test_run_snapshot_is_immutable_on_retry(self):
        with tempfile.TemporaryDirectory() as root:
            directory = run_directory(root, "immutable")
            first = extract_stage(directory, "demo")
            with patch("matchlab_ml.pipeline.demo.snapshot", side_effect=AssertionError("must reuse snapshot")):
                second = extract_stage(directory, "demo")
            self.assertEqual(first, second)
            with self.assertRaises(ValueError):
                extract_stage(directory, "postgres")

    def test_synthetic_publication_is_guarded_and_analytics_only_never_touches_redis(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(os.environ, {"MLFLOW_TRACKING_URI": "", "ML_ALLOW_DEMO_PUBLISH": ""}):
            result = run(root, "demo", offline=True, run_id="synthetic")
            with patch("matchlab_ml.pipeline.cache.connect") as connect:
                with self.assertRaises(ValueError):
                    publish_stage(Path(result["artifact_directory"]), "baseline")
                connect.assert_not_called()
            with patch("matchlab_ml.pipeline.analytics_stage") as write, patch("matchlab_ml.pipeline.analytics.complete") as complete, patch("matchlab_ml.pipeline.publish_stage") as publish:
                result = run(root, "demo", run_id="preview", analytics_only=True)
                write.assert_called_once()
                complete.assert_called_once_with("preview")
                publish.assert_not_called()
                self.assertFalse(result["cache_published"])

    def test_inactive_viewers_not_counted_as_evaluation_targets(self):
        for user in self.data["users"]:
            user["confirmed"] = False
        self.assertEqual(evaluate(self.data, "baseline")["evaluation_users"], 0)


class FakeRedis:
    def __init__(self, fail=False):
        self.values = {}
        self.pending = []
        self.fail = fail

    def pipeline(self, transaction=False):
        return self

    def set(self, key, value, ex=None):
        self.pending.append((key, value))

    def execute(self):
        if self.fail:
            raise RuntimeError("redis unavailable")
        self.values.update(self.pending)
        self.pending = []

    def eval(self, script, keys, pointer, stamp, run_id, timestamp, ttl):
        if float(self.values.get(stamp, 0)) > float(timestamp):
            return 0
        self.values[pointer], self.values[stamp] = run_id, timestamp
        return 1


class CacheTests(unittest.TestCase):
    def test_failure_preserves_last_generation(self):
        redis = FakeRedis(fail=True)
        redis.values[cache.PREFIX + ":active"] = "last-good"
        with self.assertRaises(RuntimeError):
            cache.publish(redis, "new", "baseline", {"u": {"people": []}}, "2026-01-01T00:00:00+00:00")
        self.assertEqual(redis.values[cache.PREFIX + ":active"], "last-good")

    def test_promotion_and_stale_run_protection(self):
        redis = FakeRedis()
        self.assertEqual(cache.publish(redis, "new", "baseline", {"u": {"people": []}}, "2026-01-02T00:00:00+00:00"), 1)
        with self.assertRaises(ValueError):
            cache.publish(redis, "old", "baseline", {"u": {"people": []}}, "2026-01-01T00:00:00+00:00")
        self.assertEqual(redis.values[cache.PREFIX + ":active"], "new")

    def test_empty_batch_does_not_replace_live_cache(self):
        with self.assertRaises(ValueError):
            cache.publish(FakeRedis(), "run", "baseline", {}, "2026-01-01T00:00:00+00:00")


if __name__ == "__main__":
    unittest.main()
