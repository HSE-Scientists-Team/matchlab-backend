"""Transparent skill-based segments, not claims about somebody's employment."""

from .features import tags

SEGMENTS = {
    "data_ml": {"python", "machine learning", "nlp", "statistics", "sql", "pandas", "pytorch"},
    "backend": {"backend", "go", "golang", "java", "postgresql", "redis", "grpc"},
    "frontend_mobile": {"frontend", "react", "typescript", "javascript", "kotlin", "android", "swift"},
    "design": {"design", "figma", "ux", "ui", "дизайн"},
    "research": {"research", "biology", "physics", "mathematics", "исследования", "биология"},
}


def classify(user):
    skills = tags(user.get("skills"))
    scored = sorted(((len(skills & words), name) for name, words in SEGMENTS.items()),
                    key=lambda value: (-value[0], value[1]))
    return scored[0][1] if scored and scored[0][0] else "unknown"
