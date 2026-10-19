"""Update only published images; reject unexpected registries/services/digests."""
import argparse
import json
import re
from pathlib import Path

SERVICES = {name: name.upper().replace("-", "_") + "_IMAGE" for name in ("gateway", "auth", "user", "mail", "media", "support", "project", "search", "recommendation", "admin", "chat", "api-docs", "ml", "airflow", "mlflow")}


def update(content, records, repository):
    if not records:
        raise ValueError("No published images")
    seen = set()
    for record in records:
        service = record["service"]
        if service not in SERVICES or service in seen:
            raise ValueError("Unknown or duplicated service: " + service)
        seen.add(service)
        image = record["image"]
        expected = "ghcr.io/" + repository.lower() + "/" + service + "@sha256:"
        if not image.startswith(expected) or not re.fullmatch(r"[a-f0-9]{64}", image[len(expected):]):
            raise ValueError("Unexpected image for " + service)
        pattern = re.compile(r"^  " + SERVICES[service] + r": [^\n]+$", re.MULTILINE)
        if not pattern.findall(content) and service in {"project", "search", "recommendation", "admin", "chat", "ml", "airflow", "mlflow"}:
            # First release introduces a digest without requiring a fake placeholder.
            match = re.search(r"^data:\n(?:[ \t].*\n|#.*\n|\n)*", content, re.MULTILINE)
            if not match:
                raise ValueError("Expected ConfigMap data block")
            pos = match.end()
            content = content[:pos] + "  " + SERVICES[service] + ": " + image + "\n" + content[pos:]
            continue
        if len(pattern.findall(content)) != 1:
            raise ValueError("Expected one image entry for " + service)
        content = pattern.sub("  " + SERVICES[service] + ": " + image, content)
    return content


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("manifest", type=Path)
    parser.add_argument("artifacts", type=Path)
    parser.add_argument("--repository", required=True)
    args = parser.parse_args()
    records = [json.loads(p.read_text()) for p in sorted(args.artifacts.glob("*.json"))]
    content = update(args.manifest.read_text(), records, args.repository)
    args.manifest.write_text(content)
