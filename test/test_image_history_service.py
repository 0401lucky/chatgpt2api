import tempfile
import unittest
from pathlib import Path
import sys


ROOT_DIR = Path(__file__).resolve().parents[1]
if str(ROOT_DIR) not in sys.path:
    sys.path.insert(0, str(ROOT_DIR))

from services.image_history_service import ImageHistoryService


PNG_B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWbQAAAAASUVORK5CYII="


class ImageHistoryServiceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        base_dir = Path(self.temp_dir.name)
        self.service = ImageHistoryService(
            store_file=base_dir / "image_history.json",
            image_dir=base_dir / "image-history",
            max_records=2,
        )

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def test_save_record_persists_metadata_and_image_file(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="一只在太空里漂浮的猫",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "一只在太空里漂浮的猫"}],
            usage={
                "input_tokens": 12,
                "output_tokens": 1056,
                "total_tokens": 1068,
            },
        )

        items = self.service.list_records()
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["id"], record["id"])
        self.assertEqual(items[0]["usage"]["output_tokens"], 1056)
        self.assertEqual(items[0]["images"][0]["mime_type"], "image/png")

        image_path = self.service.get_image_path(record["id"], record["images"][0]["id"])
        self.assertIsNotNone(image_path)
        self.assertTrue(image_path.is_file())

    def test_save_record_trims_old_records_and_files(self) -> None:
        first = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="第一张",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "第一张"}],
            usage={"input_tokens": 1, "output_tokens": 1056, "total_tokens": 1057},
        )
        second = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="第二张",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "第二张"}],
            usage={"input_tokens": 2, "output_tokens": 1056, "total_tokens": 1058},
        )
        third = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="第三张",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "第三张"}],
            usage={"input_tokens": 3, "output_tokens": 1056, "total_tokens": 1059},
        )

        items = self.service.list_records()
        self.assertEqual([item["id"] for item in items], [third["id"], second["id"]])
        self.assertIsNone(self.service.get_image_path(first["id"], first["images"][0]["id"]))


if __name__ == "__main__":
    unittest.main()
