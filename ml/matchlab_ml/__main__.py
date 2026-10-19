import argparse
import json
import os

from .pipeline import run


def main():
    parser = argparse.ArgumentParser(description="MatchLab batch recommendations and analytics")
    parser.add_argument("--source", choices=("postgres", "demo"), default="postgres")
    parser.add_argument("--model", choices=("baseline", "hybrid", "learned"), default=os.environ.get("ML_MODEL", "baseline"))
    parser.add_argument("--artifact-dir", default=os.environ.get("ML_ARTIFACT_DIR", "./artifacts"))
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--offline", action="store_true", help="Write local artifacts without analytics DB or Redis publication")
    mode.add_argument("--analytics-only", action="store_true", help="Update analytics but never publish recommendation IDs")
    args = parser.parse_args()
    result = run(args.artifact_dir, args.source, args.model, args.offline, analytics_only=args.analytics_only)
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
