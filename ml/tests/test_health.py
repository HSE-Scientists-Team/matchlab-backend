import unittest
from matchlab_ml.health import healthy

class HealthTests(unittest.TestCase):
    def test_http_success_does_not_mask_dead_scheduler(self):
        status = {name: {"status": "healthy"} for name in ("metadatabase", "scheduler", "dag_processor")}
        self.assertTrue(healthy(status))
        for name in status:
            bad = {**status, name: {"status": "unhealthy"}}
            self.assertFalse(healthy(bad))
        self.assertFalse(healthy({}))
