"""Read one consistent snapshot from owner-maintained views, using a reader role."""

import json
import os
from datetime import date, datetime, timezone
from pathlib import Path
import tempfile
from uuid import UUID


VIEWS = {
    "users": "users.ml_profiles_v1", "blocks": "users.ml_blocks_v1",
    "projects": "projects.ml_projects_v1", "roles": "projects.ml_roles_v1",
    "members": "projects.ml_members_v1", "requests": "projects.ml_requests_v1",
}
ORDER = {"users": "id", "projects": "id", "roles": "id", "requests": "id",
         "members": "project_id,user_id", "blocks": "blocker_user_id,blocked_user_id"}


def json_value(value):
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    raise TypeError(type(value).__name__)


def validate(snapshot):
    if type(snapshot.get("schema_version")) is not int or snapshot.get("schema_version") != 1 or snapshot.get("source_mode") not in {"postgres", "synthetic"}:
        raise ValueError("unsupported snapshot or missing explicit source mode")
    timestamp = datetime.fromisoformat(snapshot["extracted_at"])
    if timestamp.tzinfo is None:
        raise ValueError("snapshot timestamp must include a timezone")
    for name in ("users", "projects", "members", "requests", "blocks"):
        if not isinstance(snapshot.get(name), list):
            raise ValueError("snapshot is missing " + name)
    for kind in ("users", "projects", "requests"):
        ids = [item["id"] for item in snapshot[kind]]
        if len(set(ids)) != len(ids):
            raise ValueError("duplicate " + kind + " IDs")
    # Do not accept arbitrary profile dictionaries that may contain credentials.
    allowed = {"id", "status", "role", "confirmed", "created_at", "last_login_at", "updated_at",
               "has_name", "university", "study_level", "specialization", "about",
               "open_to_collaboration", "skills", "interests", "project_types"}
    for user in snapshot["users"]:
        if set(user) - allowed:
            raise ValueError("unknown or disallowed user export fields")
        if any(type(user.get(flag)) is not bool for flag in ("confirmed", "open_to_collaboration", "has_name")):
            raise ValueError("profile flags must be booleans")
        for key in ("skills", "interests", "project_types"):
            if not isinstance(user.get(key, []), list) or any(not isinstance(value, str) for value in user.get(key, [])):
                raise ValueError(key + " must be an array")
    # Each contract is an explicit allowlist: new fields require conscious review.
    contracts = {
        "projects": {"id", "owner_user_id", "status", "moderation_hidden", "created_at", "updated_at",
                     "title", "type", "field", "problem", "goal", "tags", "planned_start", "planned_end", "roles"},
        "members": {"project_id", "user_id", "role_id", "status"},
        "requests": {"id", "project_id", "role_id", "candidate_user_id", "type", "status", "created_at"},
        "blocks": {"blocker_user_id", "blocked_user_id"},
    }
    for kind, allowed_fields in contracts.items():
        if any(set(item) - allowed_fields for item in snapshot[kind]):
            raise ValueError("unknown or disallowed " + kind + " export fields")
    for project in snapshot["projects"]:
        if type(project.get("moderation_hidden")) is not bool:
            raise ValueError("project visibility must be boolean")
        for role in project.get("roles", []):
            if set(role) - {"id", "project_id", "title", "description", "skills", "slots", "occupied"}:
                raise ValueError("unknown role export fields")
            if any(type(role.get(key)) is not int or role[key] < 0 for key in ("slots", "occupied")):
                raise ValueError("role capacity must be nonnegative integer")
    for kind in ("users", "projects", "requests", "members", "blocks"):
        for item in snapshot[kind]:
            for key, value in item.items():
                if key == "id" or key.endswith("_id"):
                    if value is not None and (str(UUID(value)) != value or UUID(value).int == 0):
                        raise ValueError("export IDs must be nonzero canonical UUIDs")
    return snapshot


def from_postgres(dsn=None):
    import psycopg
    from psycopg.rows import dict_row

    dsn = dsn or os.environ["SOURCE_DATABASE_URL"]
    result = {"schema_version": 1, "source_mode": "postgres"}
    with psycopg.connect(dsn, row_factory=dict_row, connect_timeout=10,
                         options="-c default_transaction_read_only=on -c statement_timeout=60000") as conn:
        conn.execute("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        result["extracted_at"] = conn.execute("SELECT transaction_timestamp() AS ts").fetchone()["ts"].isoformat()
        for name, view in VIEWS.items():
            # Identifiers come only from this source-code allowlist.
            result[name] = list(conn.execute("SELECT * FROM " + view + " ORDER BY " + ORDER[name]).fetchall())
    projects = {p["id"]: p for p in result["projects"]}
    for project in projects.values():
        project["roles"] = []
    for role in result.pop("roles"):
        projects[role["project_id"]]["roles"].append(role)
    # The snapshot on disk uses portable ISO times, never Python-specific objects.
    return validate(json.loads(json.dumps(result, default=json_value)))


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    # The temporary file is private from creation, and cannot be redirected by
    # a pre-existing symlink. Atomic rename never exposes a half-written export.
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent,
                                         prefix="." + path.name, suffix=".tmp", delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(value, stream, default=json_value, ensure_ascii=False, sort_keys=True, allow_nan=False)
        temporary.replace(path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)



def load(path):
    with Path(path).open(encoding="utf-8") as stream:
        return validate(json.load(stream))
