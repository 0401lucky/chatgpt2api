import unittest
from types import SimpleNamespace
from unittest import mock

import api.support as api_support


class ImageBaseUrlApiTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fake_config = SimpleNamespace(base_url="https://public.example.com")
        patcher = mock.patch.object(api_support, "config", self.fake_config)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_prefers_configured_base_url(self) -> None:
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={"host": "127.0.0.1:8000"},
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "https://public.example.com")

    def test_falls_back_to_request_host(self) -> None:
        self.fake_config.base_url = ""
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={"host": "internal.example:9000"},
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "http://internal.example:9000")

    def test_uses_forwarded_proto_and_host(self) -> None:
        self.fake_config.base_url = ""
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={
                "x-forwarded-proto": "https",
                "x-forwarded-host": "public.example.com",
            },
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "https://public.example.com")

    def test_upgrades_configured_same_host_url_when_request_is_https(self) -> None:
        self.fake_config.base_url = "http://public.example.com"
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={
                "host": "public.example.com",
                "origin": "https://public.example.com",
            },
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "https://public.example.com")

    def test_keeps_configured_different_host_url(self) -> None:
        self.fake_config.base_url = "http://assets.example.com"
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="http", netloc="127.0.0.1:8000"),
            headers={
                "host": "public.example.com",
                "x-forwarded-proto": "https",
            },
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "http://assets.example.com")

    def test_falls_back_to_request_netloc_when_host_missing(self) -> None:
        self.fake_config.base_url = ""
        request = SimpleNamespace(
            url=SimpleNamespace(scheme="https", netloc="public.example.com"),
            headers={},
        )

        self.assertEqual(api_support.resolve_image_base_url(request), "https://public.example.com")


if __name__ == "__main__":
    unittest.main()
