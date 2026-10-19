"""Candidate generation, current-snapshot filtering, ranking, and cold start."""

import hashlib
import math
from collections import Counter

from . import baseline
from .features import active_user, eligible_person, eligible_project, indexes, tags
from .hybrid import HybridModel
from .learned import LearnedModel


def fallback(viewer, candidate, kind, day):
    # Stable daily exploration, not a fake personalised match or popularity claim.
    seed = f'{day}:{viewer["id"]}:{kind}:{candidate["id"]}'
    rotation = int(hashlib.sha256(seed.encode()).hexdigest()[:8], 16) / 0xFFFFFFFF
    return {"id": candidate["id"], "score": 0.001 * rotation,
            "factors": ["cold_start_available_catalogue"], "role_id": "", "fallback": True}


def recommend(snapshot, user_id, kind, model="baseline", limit=100, fitted=None, data=None):
    if kind not in {"people", "projects"} or model not in {"baseline", "hybrid", "learned"}:
        raise ValueError("kind must be people/projects and model baseline/hybrid/learned")
    if type(limit) is not int or not 1 <= limit <= 500:
        raise ValueError("limit must be from 1 to 500")
    data = data or indexes(snapshot)
    viewer = data["users"].get(user_id)
    if viewer is None or not active_user(viewer):
        return []
    fitted = fitted or (HybridModel(snapshot) if model == "hybrid" else LearnedModel(snapshot) if model == "learned" else baseline)
    catalogue = data["users"] if kind == "people" else data["projects"]
    by_tag = data["people_by_tag"] if kind == "people" else data["projects_by_tag"]
    keys = tags(viewer.get("skills")) | tags(viewer.get("interests"))
    votes = Counter(candidate for tag in keys for candidate in by_tag.get(tag, ()))
    candidate_ids = {key for key, _ in sorted(votes.items(), key=lambda p: (-p[1], p[0]))[:500]}
    if model == "hybrid":
        neighbours = fitted.neighbours(user_id)
        collaborative = Counter(neighbours) if kind == "people" else Counter(
            p for other in neighbours for p in fitted.history[other])
        candidate_ids.update(key for key, _ in sorted(collaborative.items(), key=lambda item: (-item[1], item[0]))[:500])
    # A deterministic reservoir also supplies cold start and text-only candidates.
    day = snapshot["extracted_at"][:10]
    pool = data["pools"][kind]
    offset = int(hashlib.sha256(user_id.encode()).hexdigest()[:8], 16) % max(len(pool), 1)
    extra = [pool[(offset + i) % len(pool)] for i in range(min(500, len(pool)))]
    candidate_ids.update(extra)
    eligible = eligible_person if kind == "people" else eligible_project
    results = []
    for key in sorted(candidate_ids):
        candidate = catalogue.get(key)
        if candidate is None or not eligible(viewer, candidate, data):
            continue
        item = fitted.people(viewer, candidate) if kind == "people" else fitted.project(viewer, candidate)
        if not math.isfinite(item["score"]) or not 0 <= item["score"] <= 1:
            raise ValueError("model returned an invalid score")
        if item["score"] <= 0:
            item = fallback(viewer, candidate, kind, day)
        item["model"] = model + "-v1"
        results.append(item)
    return sorted(results, key=lambda item: (item["fallback"], -item["score"], item["id"]))[:limit]


def score_snapshot(snapshot, model, limit=100):
    data = indexes(snapshot)
    fitted = HybridModel(snapshot) if model == "hybrid" else LearnedModel(snapshot) if model == "learned" else None
    results = {}
    for user in snapshot["users"]:
        if active_user(user):
            results[user["id"]] = {kind: recommend(snapshot, user["id"], kind, model, limit, fitted, data)
                                   for kind in ("people", "projects")}
    return results, (fitted.artifact() if fitted else {"version": baseline.VERSION,
                    "project_weights": [0.65, 0.25, 0.10], "people_weights": [0.50, 0.35, 0.15]})
