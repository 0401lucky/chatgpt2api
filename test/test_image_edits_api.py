import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from fastapi.testclient import TestClient

from api import app as app_module
from api import support as support_module


class _FakeThread:
    def join(self, timeout: float | None = None) -> None:
        return None


class _FakeChatGPTService:
    last_call: dict[str, object] | None = None

    def __init__(self, _account_service) -> None:
        return None

    def edit_api_images(
        self,
        prompt: str,
        images,
        model: str,
        n: int,
        source_endpoint: str,
        response_format: str = "b64_json",
        base_url: str | None = None,
    ):
        normalized_images = list(images)
        type(self).last_call = {
            "prompt": prompt,
            "images": normalized_images,
            "model": model,
            "n": n,
            "source_endpoint": source_endpoint,
            "response_format": response_format,
            "base_url": base_url,
        }
        return {
            "created": 123,
            "data": [{"b64_json": "ZmFrZQ==", "revised_prompt": prompt}],
        }


class ImageEditsApiTests(unittest.TestCase):
    def setUp(self) -> None:
        _FakeChatGPTService.last_call = None
        self.auth_header = {"Authorization": "Bearer test-auth"}
        self.fake_config = SimpleNamespace(
            auth_key="test-auth",
            app_version="test-version",
            images_dir=Path.cwd(),
            refresh_account_interval_minute=60,
            base_url="",
        )
        self.patches = [
            mock.patch.object(app_module, "ChatGPTService", _FakeChatGPTService),
            mock.patch.object(app_module, "config", self.fake_config),
            mock.patch.object(support_module, "config", self.fake_config),
            mock.patch.object(app_module, "start_limited_account_watcher", lambda _stop_event: _FakeThread()),
        ]
        for patcher in self.patches:
            patcher.start()
        self.addCleanup(self._cleanup_patches)
        self.client = TestClient(app_module.create_app())
        self.addCleanup(self.client.close)

    def _cleanup_patches(self) -> None:
        for patcher in reversed(self.patches):
            patcher.stop()

    def test_accepts_repeated_image_field(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            data={"prompt": "test prompt", "model": "gpt-image-1", "n": "1"},
            files=[
                ("image", ("first.png", b"first", "image/png")),
                ("image", ("second.png", b"second", "image/png")),
            ],
        )

        self.assertEqual(response.status_code, 200)
        self.assertIsNotNone(_FakeChatGPTService.last_call)
        self.assertEqual(len(_FakeChatGPTService.last_call["images"]), 2)
        self.assertEqual(
            [item[1] for item in _FakeChatGPTService.last_call["images"]],
            ["first.png", "second.png"],
        )

    def test_accepts_repeated_image_bracket_field(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            data={"prompt": "test prompt", "model": "gpt-image-1", "n": "1"},
            files=[
                ("image[]", ("first.png", b"first", "image/png")),
                ("image[]", ("second.png", b"second", "image/png")),
            ],
        )

        self.assertEqual(response.status_code, 200)
        self.assertIsNotNone(_FakeChatGPTService.last_call)
        self.assertEqual(len(_FakeChatGPTService.last_call["images"]), 2)
        self.assertEqual(
            [item[1] for item in _FakeChatGPTService.last_call["images"]],
            ["first.png", "second.png"],
        )

    def test_accepts_json_data_url_image(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            json={
                "prompt": "把这张图改成复古海报",
                "model": "gpt-image-1",
                "n": 1,
                "response_format": "b64_json",
                "images": [
                    {
                        "image_url": "data:image/png;base64,ZmFrZQ==",
                    }
                ],
            },
        )

        self.assertEqual(response.status_code, 200)
        self.assertIsNotNone(_FakeChatGPTService.last_call)
        self.assertEqual(_FakeChatGPTService.last_call["prompt"], "把这张图改成复古海报")
        self.assertEqual(
            _FakeChatGPTService.last_call["images"],
            [(b"fake", "image-1.png", "image/png")],
        )

    def test_rejects_json_without_images(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            json={
                "prompt": "把这张图改成复古海报",
                "model": "gpt-image-1",
                "n": 1,
                "response_format": "b64_json",
            },
        )

        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json(), {"detail": {"error": "images is required"}})

    def test_rejects_json_with_invalid_image_url(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            json={
                "prompt": "把这张图改成复古海报",
                "model": "gpt-image-1",
                "n": 1,
                "response_format": "b64_json",
                "images": [
                    {
                        "image_url": "not-a-data-url",
                    }
                ],
            },
        )

        self.assertEqual(response.status_code, 400)
        self.assertEqual(
            response.json(),
            {"detail": {"error": "images[0].image_url must be a data URL"}},
        )

    def test_rejects_json_with_invalid_base64_image(self) -> None:
        response = self.client.post(
            "/v1/images/edits",
            headers=self.auth_header,
            json={
                "prompt": "把这张图改成复古海报",
                "model": "gpt-image-1",
                "n": 1,
                "response_format": "b64_json",
                "images": [
                    {
                        "image_url": "data:image/png;base64,***",
                    }
                ],
            },
        )

        self.assertEqual(response.status_code, 400)
        self.assertEqual(
            response.json(),
            {"detail": {"error": "images[0].image_url base64 decode failed"}},
        )


if __name__ == "__main__":
    unittest.main()
