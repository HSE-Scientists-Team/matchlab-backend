"""Update only published images; reject unexpected registries/services/digests."""
import argparse
import json
import re
from pathlib import Path

SERVICES = {name: name.upper().replace("-", "_") + "_IMAGE" for name in ("gateway", "auth", "user", "mail", "api-docs")}


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
