"""Synthetic data shaped exactly like the minimized owner export views."""

from copy import deepcopy
from datetime import datetime, timedelta, timezone
from uuid import UUID


def identifier(value):
    return str(UUID(int=value))


def snapshot():
    now = datetime.now(timezone.utc)
    users = []
    groups = [(["python", "nlp"], ["medicine", "nlp"], "Data / ML"),
              (["backend", "postgresql", "python"], ["medicine", "education"], "Backend"),
              (["react", "typescript", "design"], ["education", "interfaces"], "Frontend"),
              (["biology", "statistics"], ["medicine", "research"], "Research")]
    for i in range(1, 25):
        skills, interests, specialization = groups[(i - 1) % len(groups)]
        users.append({"id": identifier(i), "status": "active", "role": "client", "confirmed": True,
                      "has_name": True, "skills": list(skills), "interests": list(interests),
                      "specialization": specialization, "about": " ".join(interests + skills),
                      "university": "Demo University " + str(i % 3 + 1), "study_level": "bachelor",
                      "project_types": ["scientific", "practical"], "open_to_collaboration": True,
                      "created_at": (now - timedelta(days=40-i)).isoformat(),
                      "last_login_at": (now - timedelta(days=i % 5)).isoformat()})
    # Explicit cold-start profile; still an active confirmed account.
    users[0].update(skills=[], interests=[], about="", specialization="", project_types=[])
    projects, members, requests = [], [], []
    for j in range(12):
        skills, interests, _ = groups[j % 4]
        pid, rid, owner = identifier(101+j), identifier(201+j), identifier(20 + j % 4)
        created = now - timedelta(days=30-j)
        projects.append({"id": pid, "owner_user_id": owner,
                         "type": "scientific" if j % 2 else "practical", "field": interests[0],
                         "title": "Demo project " + str(j+1), "problem": " ".join(interests),
                         "goal": " ".join(skills), "tags": list(interests),
                         "status": "recruiting" if j < 10 else "completed", "moderation_hidden": False,
                         "created_at": created.isoformat(), "planned_start": created.date().isoformat(),
                         "planned_end": (created+timedelta(days=90)).date().isoformat(),
                         "roles": [{"id": rid, "title": " / ".join(skills), "description": " ".join(interests),
                                    "skills": list(skills), "slots": 3, "occupied": 1}]})
        members.append({"project_id": pid, "user_id": owner, "role_id": None, "status": "active"})
        members.append({"project_id": pid, "user_id": identifier(j+2), "role_id": rid, "status": "active"})
        for k in range(1, 8):
            requests.append({"id": identifier(1000+j*10+k), "project_id": pid, "role_id": rid,
                             "candidate_user_id": identifier(2 + (j+k*4) % 18), "type": "application",
                             "status": "accepted" if k == 1 else "pending",
                             "created_at": (created+timedelta(days=k)).isoformat()})
    return deepcopy({"schema_version": 1, "source_mode": "synthetic", "extracted_at": now.isoformat(),
                     "users": users, "projects": projects, "members": members,
                     "requests": requests, "blocks": []})
