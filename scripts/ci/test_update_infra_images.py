import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("images", Path(__file__).with_name("update-infra-images.py"))
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class ImageUpdateTest(unittest.TestCase):
    def setUp(self):
        self.content = "data:\n  GATEWAY_IMAGE: old-gateway\n  AUTH_IMAGE: old-auth\n"
        self.record = {"service": "gateway", "image": "ghcr.io/org/backend/gateway@sha256:" + "a" * 64}

    def test_partial_release_preserves_other_services(self):
        result = images.update(self.content, [self.record], "Org/Backend")
        self.assertIn(self.record["image"], result)
        self.assertIn("AUTH_IMAGE: old-auth", result)

    def test_rejects_invalid_artifacts(self):
        for records in ([], [self.record, self.record], [{"service": "oops", "image": "x"}], [{"service": "gateway", "image": "ghcr.io/other/gateway@sha256:" + "a" * 64}], [{"service": "gateway", "image": "ghcr.io/org/backend/gateway:latest"}]):
            with self.subTest(records=records), self.assertRaises(ValueError):
                images.update(self.content, records, "org/backend")

    def test_requires_exactly_one_target(self):
        for content in ("data:\n", self.content + "  GATEWAY_IMAGE: duplicate\n"):
            with self.subTest(content=content), self.assertRaises(ValueError):
                images.update(content, [self.record], "org/backend")

    def test_private_package_path_rejects_public_artifacts(self):
        record = dict(self.record, image="ghcr.io/org/backend/private/gateway@sha256:" + "a" * 64)
        self.assertIn(record["image"], images.update(self.content, [record], "org/backend/private"))
        with self.assertRaises(ValueError):
            images.update(self.content, [self.record], "org/backend/private")
