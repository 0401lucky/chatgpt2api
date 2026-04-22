import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
import sys


ROOT_DIR = Path(__file__).resolve().parents[1]
if str(ROOT_DIR) not in sys.path:
    sys.path.insert(0, str(ROOT_DIR))

from fastapi.testclient import TestClient

import services.api as api_module
from services.image_history_service import ImageHistoryService


PNG_B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWbQAAAAASUVORK5CYII="


class _FakeThread:
    def join(self, timeout: float | None = None) -> None:
        return None


class ImageHistoryApiTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        base_dir = Path(self.temp_dir.name)
        self.history_service = ImageHistoryService(
            store_file=base_dir / "image_history.json",
            image_dir=base_dir / "image-history",
            max_records=10,
        )
        self.history_service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="一只猫",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "一只猫"}],
            usage={"input_tokens": 5, "output_tokens": 1056, "total_tokens": 1061},
        )

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def test_image_history_requires_authentication(self) -> None:
        with patch.object(api_module, "image_history_service", self.history_service), patch.object(
            api_module,
            "start_limited_account_watcher",
            return_value=_FakeThread(),
        ):
            with TestClient(api_module.create_app()) as client:
                response = client.get("/api/image-history")

        self.assertEqual(response.status_code, 401)

    def test_image_history_list_and_image_download(self) -> None:
        record = self.history_service.list_records()[0]
        image = record["images"][0]
        headers = {"Authorization": f"Bearer {api_module.config.auth_key}"}

        with patch.object(api_module, "image_history_service", self.history_service), patch.object(
            api_module,
            "start_limited_account_watcher",
            return_value=_FakeThread(),
        ):
            with TestClient(api_module.create_app()) as client:
                list_response = client.get("/api/image-history", headers=headers)
                image_response = client.get(
                    f"/api/image-history/{record['id']}/images/{image['id']}",
                    headers=headers,
                )
                missing_response = client.get(
                    "/api/image-history/not-found/images/not-found",
                    headers=headers,
                )

        self.assertEqual(list_response.status_code, 200)
        self.assertEqual(list_response.json()["items"][0]["id"], record["id"])
        self.assertEqual(image_response.status_code, 200)
        self.assertEqual(image_response.headers["content-type"], "image/png")
        self.assertTrue(image_response.content)
        self.assertEqual(missing_response.status_code, 404)


if __name__ == "__main__":
    unittest.main()
