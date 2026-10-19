"""Explainable baseline. Weights are hypotheses, scores are not probabilities."""

from .features import coverage, open_roles, overlap, tags

VERSION = "baseline-v1"


def people(viewer, candidate):
    skills = sorted(tags(viewer.get("skills")) & tags(candidate.get("skills")))
    interests = sorted(tags(viewer.get("interests")) & tags(candidate.get("interests")))
    same_field = bool(viewer.get("specialization")) and (
        viewer["specialization"].casefold() == candidate.get("specialization", "").casefold())
    score = (0.50 * overlap(viewer.get("interests"), candidate.get("interests"))
             + 0.35 * overlap(viewer.get("skills"), candidate.get("skills")) + 0.15 * same_field)
    # Factor codes do not retain text a user may later remove from their profile.
    factors = (["shared_skills"] if skills else [])
    factors += (["shared_interests"] if interests else [])
    if same_field:
        factors.append("shared_specialization")
    return {"id": candidate["id"], "score": score, "factors": factors, "role_id": "", "fallback": False}


def project(viewer, candidate):
    roles = sorted(open_roles(candidate), key=lambda r: (-coverage(viewer.get("skills"), r.get("skills")), r["id"]))
    role = roles[0] if roles else None
    skills = sorted(tags(viewer.get("skills")) & tags(role.get("skills") if role else []))
    interests = sorted(tags(viewer.get("interests")) & (tags(candidate.get("tags")) | tags([candidate.get("field", "")])))
    topic = overlap(viewer.get("interests"), (candidate.get("tags") or []) + [candidate.get("field", "")])
    type_match = candidate.get("type") in (viewer.get("project_types") or [])
    score = 0.65 * coverage(viewer.get("skills"), role.get("skills") if role else []) + 0.25 * topic + 0.10 * type_match
    factors = (["role_skills"] if skills else [])
    factors += (["shared_interests"] if interests else [])
    if type_match:
        factors.append("preferred_project_type")
    return {"id": candidate["id"], "score": score, "factors": factors,
            "role_id": role["id"] if role else "", "fallback": False}
