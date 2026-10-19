"""Hybrid baseline + fitted TF-IDF + implicit nearest-neighbour collaboration.

Applications are weak positive intent. Invitations/rejections are not treated as
candidate dislikes or consent. Sparse dictionaries keep the model inspectable.
"""

import math
from collections import defaultdict

from . import baseline
from .features import TextModel, cosine, project_text, user_text

VERSION = "hybrid-v1"


class HybridModel:
    def __init__(self, snapshot, requests=None):
        documents = [user_text(u) for u in snapshot["users"]] + [project_text(p) for p in snapshot["projects"]]
        self.text = TextModel(documents)
        self.users = {u["id"]: self.text.vector(user_text(u)) for u in snapshot["users"]}
        self.projects = {p["id"]: self.text.vector(project_text(p)) for p in snapshot["projects"]}
        self.history = defaultdict(set)
        self.applicants = defaultdict(set)
        for event in snapshot.get("requests", []) if requests is None else requests:
            if event["type"] == "application":
                self.history[event["candidate_user_id"]].add(event["project_id"])
                self.applicants[event["project_id"]].add(event["candidate_user_id"])
        self._neighbours = {}

    def neighbours(self, user_id):
        if user_id not in self._neighbours:
            history = self.history[user_id]
            candidates = set().union(*(self.applicants[p] for p in history)) if history else set()
            scores = {}
            for other in candidates - {user_id}:
                other_history = self.history[other]
                scores[other] = len(history & other_history) / math.sqrt(len(history) * len(other_history))
            self._neighbours[user_id] = dict(sorted(scores.items(), key=lambda p: (-p[1], p[0]))[:200])
        return self._neighbours[user_id]

    def people(self, viewer, candidate):
        result = baseline.people(viewer, candidate)
        content = cosine(self.users.get(viewer["id"], {}), self.users.get(candidate["id"], {}))
        behaviour = self.neighbours(viewer["id"]).get(candidate["id"], 0.0)
        result["score"] = 0.60 * result["score"] + 0.30 * content + 0.10 * behaviour
        if content > 0:
            result["factors"].append("profile_text_similarity")
        if behaviour > 0:
            result["factors"].append("similar_project_interests")
        return result

    def project(self, viewer, candidate):
        result = baseline.project(viewer, candidate)
        content = cosine(self.users.get(viewer["id"], {}), self.projects.get(candidate["id"], {}))
        neighbours = self.neighbours(viewer["id"])
        total = sum(neighbours.values())
        behaviour = (sum(value for user, value in neighbours.items()
                         if candidate["id"] in self.history[user]) / total) if total else 0.0
        # No interaction history: retain content/rule quality instead of shrinking everything.
        if total:
            result["score"] = 0.55 * result["score"] + 0.25 * content + 0.20 * behaviour
        else:
            result["score"] = 0.70 * result["score"] + 0.30 * content
        if content > 0:
            result["factors"].append("project_text_similarity")
        if behaviour > 0:
            result["factors"].append("similar_users_applied")
        return result

    def artifact(self):
        # Parameters and learned vocabulary only. Never upload the raw source snapshot.
        return {"version": VERSION, "idf": self.text.idf, "neighbour_limit": 200,
                "interaction": "application_creation", "text_model": "tf-idf",
                "people_weights": [0.60, 0.30, 0.10], "project_weights": [0.55, 0.25, 0.20],
                "project_no_history_weights": [0.70, 0.30]}
