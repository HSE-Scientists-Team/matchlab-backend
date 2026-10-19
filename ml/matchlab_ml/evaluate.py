"""Retrospective next-application check, not a causal online quality estimate."""

import math
from collections import defaultdict
from copy import deepcopy

from .features import active_user, indexes
from .hybrid import HybridModel
from .learned import LearnedModel
from .rank import recommend


def evaluate(snapshot, model, k=10):
    events = defaultdict(list)
    for event in snapshot.get("requests", []):
        if event["type"] == "application":
            events[event["candidate_user_id"]].append(event)
    holdout = {}
    for user, history in events.items():
        history = sorted(history, key=lambda e: (e["created_at"], e["id"]))
        if len({e["project_id"] for e in history}) >= 3:
            holdout[user] = history[-1]
    if not holdout:
        return {"evaluation_users": 0, "recall_at_10": None, "ndcg_at_10": None,
                "evaluation_type": "retrospective_current_profile_proxy"}
    # A single global cutoff prevents training on another user's later actions.
    cutoff = min(e["created_at"] for e in holdout.values())
    train = deepcopy(snapshot)
    train["requests"] = [e for e in snapshot["requests"] if e["created_at"] < cutoff]
    # Membership has no joining timestamp: omit it rather than use a future state.
    train["members"] = []
    data = indexes(train)
    fitted = HybridModel(train) if model == "hybrid" else LearnedModel(train) if model == "learned" else None
    hits, discounted, count = 0, 0.0, 0
    for user, target in holdout.items():
        if user not in data["users"] or not active_user(data["users"][user]):
            continue
        # Only evaluate targets currently eligible for the viewer. Historical profile
        # and visibility snapshots are not available, so this remains a proxy.
        from .features import eligible_project
        project = data["projects"].get(target["project_id"])
        if not project or not eligible_project(data["users"][user], project, data):
            continue
        ranked = recommend(train, user, "projects", model, k, fitted, data)
        ids = [item["id"] for item in ranked]
        count += 1
        if target["project_id"] in ids:
            hits += 1
            discounted += 1 / math.log2(ids.index(target["project_id"]) + 2)
    return {"evaluation_users": count, "recall_at_10": hits / count if count else None,
            "ndcg_at_10": discounted / count if count else None,
            "evaluation_cutoff": cutoff, "evaluation_type": "retrospective_current_profile_proxy"}
