"""Feature preparation and eligibility shared by both offline models."""

import hashlib
import math
import re
from collections import Counter, defaultdict


ALIASES = {
    "машинное обучение": "machine learning", "ml": "machine learning",
    "питон": "python", "postgres": "postgresql", "постгрес": "postgresql",
    "обработка естественного языка": "nlp", "бэкенд": "backend", "бекенд": "backend",
    "фронтенд": "frontend", "фронтэнд": "frontend", "реакт": "react",
}


def tags(values):
    return {ALIASES.get(" ".join(str(v).casefold().split()), " ".join(str(v).casefold().split()))
            for v in values or [] if str(v).strip()}


def overlap(left, right):
    a, b = tags(left), tags(right)
    return len(a & b) / len(a | b) if a | b else 0.0


def coverage(skills, required):
    required = tags(required)
    return len(tags(skills) & required) / len(required) if required else 0.0


def terms(text):
    # Do not turn pasted contact addresses or URLs into model vocabulary.
    text = re.sub(r"https?://\S+|\b[^\s@]+@[^\s@]+\b", " ", text.casefold())
    return re.findall(r"[^\W_][\w+#.-]{1,}", text, re.UNICODE)


def user_text(user):
    return " ".join([user.get("about", ""), user.get("specialization", ""),
                     *sorted(tags(user.get("skills"))), *sorted(tags(user.get("interests")))])


def project_text(project):
    return " ".join([project.get(k, "") for k in ("title", "field", "problem", "goal")] +
                    sorted(tags(project.get("tags"))) +
                    [" ".join([r.get("title", ""), r.get("description", ""),
                                *sorted(tags(r.get("skills")))]) for r in project.get("roles", [])])


def active_user(user):
    return user.get("status") == "active" and user.get("confirmed") is True


def open_roles(project):
    return [r for r in project.get("roles", [])
            if not r.get("deleted_at") and r.get("slots", 0) > r.get("occupied", 0)]


def indexes(snapshot):
    users = {u["id"]: u for u in snapshot["users"]}
    projects = {p["id"]: p for p in snapshot["projects"]}
    blocked = set()
    for block in snapshot.get("blocks", []):
        a, b = block["blocker_user_id"], block["blocked_user_id"]
        blocked.update(((a, b), (b, a)))
    memberships = {(m["user_id"], m["project_id"]) for m in snapshot.get("members", [])
                   if m["status"] == "active"}
    people_by_tag, projects_by_tag = defaultdict(set), defaultdict(set)
    for user in users.values():
        for tag in tags(user.get("skills")) | tags(user.get("interests")):
            people_by_tag[tag].add(user["id"])
    for project in projects.values():
        keys = tags(project.get("tags")) | tags([project.get("field", "")])
        for role in open_roles(project):
            keys |= tags(role.get("skills"))
        for tag in keys:
            projects_by_tag[tag].add(project["id"])
    day = snapshot["extracted_at"][:10]
    pools = {kind: sorted(items, key=lambda key: hashlib.sha256(f"{day}:{key}".encode()).digest())
             for kind, items in (("people", users), ("projects", projects))}
    return {"users": users, "projects": projects, "blocked": blocked, "pools": pools,
            "memberships": memberships, "people_by_tag": people_by_tag,
            "projects_by_tag": projects_by_tag}


def eligible_person(viewer, candidate, data):
    return (candidate["id"] != viewer["id"] and active_user(candidate)
            and candidate.get("has_name", False) and candidate.get("open_to_collaboration", False)
            and (viewer["id"], candidate["id"]) not in data["blocked"])


def eligible_project(viewer, project, data):
    return (project.get("status") == "recruiting" and not project.get("moderation_hidden", False)
            and project["owner_user_id"] != viewer["id"] and bool(open_roles(project))
            and (viewer["id"], project["id"]) not in data["memberships"]
            and (viewer["id"], project["owner_user_id"]) not in data["blocked"])


class TextModel:
    """Sparse TF-IDF: a fitted vocabulary/IDF table, no neural model download."""

    def __init__(self, documents):
        counts = Counter(word for text in documents for word in set(terms(text)))
        self.idf = {word: math.log((1 + len(documents)) / (1 + count)) + 1
                    for word, count in sorted(counts.items(), key=lambda item: (-item[1], item[0]))[:20000]}

    def vector(self, text):
        values = {word: (1 + math.log(count)) * self.idf[word]
                  for word, count in Counter(terms(text)).items() if word in self.idf}
        length = math.sqrt(sum(v * v for v in values.values()))
        return {word: value / length for word, value in values.items()} if length else {}


def cosine(left, right):
    return min(1.0, max(0.0, sum(value * right.get(word, 0.0) for word, value in left.items())))
