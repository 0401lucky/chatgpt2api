import unittest
from types import SimpleNamespace
from unittest import mock

from api import support as support_module


class ImageBaseUrlApiTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fake_config = SimpleNamespace(base_url="https://public.example.com")
        patcher = mock.patch.object(support_module, "config", self.fake_config)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_prefers_configured_base_url(self) -> None:
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={"host": "127.0.0.1:8000"},
        )

        self.assertEqual(support_module.resolve_image_base_url(request), "https://public.example.com")

    def test_falls_back_to_request_host(self) -> None:
        self.fake_config.base_url = ""
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={"host": "internal.example:9000"},
        )

        self.assertEqual(support_module.resolve_image_base_url(request), "http://internal.example:9000")

    def test_falls_back_to_request_netloc_when_host_missing(self) -> None:
        self.fake_config.base_url = ""
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="https", netloc="public.example.com"),
            headers={},
        )

        self.assertEqual(support_module.resolve_image_base_url(request), "https://public.example.com")


if __name__ == "__main__":
    unittest.main()
