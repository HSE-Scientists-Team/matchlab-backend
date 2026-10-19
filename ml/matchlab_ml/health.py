"""Composite health check for the single-pod Airflow pilot."""
import json
import urllib.request

COMPONENTS = ("metadatabase", "scheduler", "dag_processor")

def healthy(payload):
    return all(payload.get(name, {}).get("status") == "healthy" for name in COMPONENTS)

def main():
    try:
        with urllib.request.urlopen("http://127.0.0.1:8080/api/v2/monitor/health", timeout=3) as response:
            return 0 if healthy(json.load(response)) else 1
    except (OSError, ValueError):
        return 1

if __name__ == "__main__":
    raise SystemExit(main())
