"""Versioned Redis ID lists. A failed batch never clears the active generation."""

import json
import os
from datetime import datetime

PREFIX = "matchlab:recommendations:v1"

PROMOTE = """
local current = redis.call('GET', KEYS[2])
if current and tonumber(current) > tonumber(ARGV[2]) then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'EX', ARGV[3])
return 1
"""


def connect(url=None):
    import redis
    return redis.Redis.from_url(url or os.environ["RECOMMENDATION_REDIS_URL"],
                                decode_responses=True, socket_connect_timeout=3, socket_timeout=5)


def publish(client, run_id, model, results, generated_at, ttl=86400):
    if type(ttl) is not int or ttl < 600:
        raise ValueError("recommendation TTL must be at least ten minutes")
    if not results:
        raise ValueError("empty dataset must not replace a working recommendation generation")
    timestamp = datetime.fromisoformat(generated_at)
    if timestamp.tzinfo is None:
        raise ValueError("generated_at must include a timezone")
    if model not in {"baseline", "hybrid", "learned"}:
        raise ValueError("unknown serving model")
    pipeline = client.pipeline(transaction=False)
    count = 0
    for user_id, kinds in results.items():
        for kind, items in kinds.items():
            payload = {"schema_version": 1, "run_id": run_id, "model": model + "-v1",
                       "generated_at": generated_at, "items": items}
            pipeline.set(f"{PREFIX}:{run_id}:{kind}:{user_id}", json.dumps(payload, allow_nan=False), ex=ttl + 300)
            count += 1
            if count % 200 == 0:
                pipeline.execute()
    pipeline.execute()
    # Publishing the pointer last is the atomic promotion; old generations expire.
    promoted = client.eval(PROMOTE, 2, PREFIX + ":active", PREFIX + ":active-timestamp",
                           run_id, timestamp.timestamp(), ttl)
    if not promoted:
        raise ValueError("a newer generation is already active; refusing stale promotion")
    return count
