import tempfile
import unittest
from pathlib import Path
import sys
from unittest import mock


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

    def test_delete_images_removes_single_image_keeps_record_and_deletes_file(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="两张图",
            image_items=[
                {"b64_json": PNG_B64, "revised_prompt": "第一张"},
                {"b64_json": PNG_B64, "revised_prompt": "第二张"},
            ],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        delete_image_id = record["images"][0]["id"]
        keep_image_id = record["images"][1]["id"]

        delete_path = self.service.get_image_path(record_id, delete_image_id)
        keep_path = self.service.get_image_path(record_id, keep_image_id)
        self.assertIsNotNone(delete_path)
        self.assertIsNotNone(keep_path)
        self.assertTrue(delete_path.is_file())
        self.assertTrue(keep_path.is_file())

        result = self.service.delete_images(
            [
                {
                    "record_id": record_id,
                    "image_ids": [delete_image_id],
                }
            ]
        )
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 0)
        self.assertEqual(result["items"], self.service.list_records())

        items = self.service.list_records()
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["id"], record_id)
        self.assertEqual(items[0]["image_count"], 1)
        self.assertEqual(len(items[0]["images"]), 1)
        self.assertEqual(items[0]["images"][0]["id"], keep_image_id)

        self.assertFalse(delete_path.exists())
        self.assertTrue(keep_path.is_file())
        self.assertIsNone(self.service.get_image_path(record_id, delete_image_id))

    def test_delete_images_removes_last_image_removes_record_and_deletes_file(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="一张图",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "唯一一张"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]
        image_path = self.service.get_image_path(record_id, image_id)
        self.assertIsNotNone(image_path)
        self.assertTrue(image_path.is_file())

        result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)
        self.assertEqual(result["items"], self.service.list_records())

        items = self.service.list_records()
        self.assertEqual(len(items), 0)
        self.assertFalse(image_path.exists())
        self.assertIsNone(self.service.get_image_path(record_id, image_id))

    def test_delete_images_rejects_path_traversal_and_does_not_delete_outside_image_dir(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="路径安全",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "路径安全"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]

        # 伪造恶意 file_name，指向 image_dir 外部文件
        victim = Path(self.temp_dir.name) / "victim.txt"
        victim.write_text("do-not-delete", encoding="utf-8")
        self.service._records[0]["images"][0]["file_name"] = "..\\victim.txt"
        self.service._save_records()

        result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)
        self.assertTrue(victim.is_file())
        self.assertEqual(self.service.list_records(), [])

    def test_delete_images_does_not_claim_success_when_file_delete_fails(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="删除失败语义",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "删除失败语义"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]
        image_path = self.service.get_image_path(record_id, image_id)
        self.assertIsNotNone(image_path)
        self.assertTrue(image_path.is_file())

        orig_unlink = Path.unlink

        def fake_unlink(self: Path, *args: object, **kwargs: object) -> None:
            if self.resolve(strict=False) == image_path.resolve(strict=False):
                raise PermissionError("blocked")
            return orig_unlink(self, *args, **kwargs)

        with mock.patch("pathlib.Path.unlink", new=fake_unlink):
            result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])

        # 成功语义为“移除历史引用”，unlink 失败也不回滚
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)
        self.assertEqual(result["items"], self.service.list_records())
        self.assertTrue(image_path.is_file())
        self.assertIsNone(self.service.get_image_path(record_id, image_id))

    def test_delete_images_counts_duplicate_image_entries_and_deletes_all_files(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="重复 image_id",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "重复 image_id"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]
        orig_path = self.service.get_image_path(record_id, image_id)
        self.assertIsNotNone(orig_path)
        self.assertTrue(orig_path.is_file())

        # 手工构造重复条目，指向另一个真实文件
        dup_file_name = f"{record_id}-dup.png"
        dup_path = self.service.image_dir / dup_file_name
        dup_path.write_bytes(orig_path.read_bytes())
        self.assertTrue(dup_path.is_file())

        self.service._records[0]["images"].append(
            {
                "id": image_id,
                "file_name": dup_file_name,
                "mime_type": "image/png",
            }
        )
        self.service._records[0]["image_count"] = 2
        self.service._save_records()

        result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])
        self.assertEqual(result["deleted_images"], 2)
        self.assertEqual(result["deleted_records"], 1)
        self.assertEqual(result["items"], self.service.list_records())
        self.assertEqual(len(self.service.list_records()), 0)
        self.assertFalse(orig_path.exists())
        self.assertFalse(dup_path.exists())

    def test_delete_images_does_not_remove_record_if_non_dict_items_remain(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="残留非 dict",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "残留非 dict"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]

        # 插入一个非 dict 条目，删除 dict 图片后记录仍应保留
        self.service._records[0]["images"].append("corrupt-entry")
        self.service._save_records()

        result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 0)

        items = self.service.list_records()
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["id"], record_id)
        self.assertEqual(items[0]["image_count"], 0)
        self.assertEqual(items[0]["images"], ["corrupt-entry"])

    def test_delete_images_does_not_unlink_file_if_still_referenced_by_another_record(self) -> None:
        first = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="共享文件-1",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "共享文件-1"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )
        second = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="共享文件-2",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "共享文件-2"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        first_record_id = first["id"]
        first_image_id = first["images"][0]["id"]
        shared_path = self.service.get_image_path(first_record_id, first_image_id)
        self.assertIsNotNone(shared_path)
        self.assertTrue(shared_path.is_file())

        # 让第二条记录引用同一个 file_name（共享同一物理文件）
        shared_file_name = first["images"][0]["file_name"]
        self.service._records[0]["images"][0]["file_name"] = shared_file_name
        self.service._save_records()

        second_record_id = second["id"]
        second_image_id = second["images"][0]["id"]
        second_path = self.service.get_image_path(second_record_id, second_image_id)
        self.assertIsNotNone(second_path)
        self.assertTrue(second_path.is_file())
        self.assertEqual(second_path.resolve(strict=False), shared_path.resolve(strict=False))

        result = self.service.delete_images([{"record_id": first_record_id, "image_ids": [first_image_id]}])
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)

        # 物理文件仍被第二条记录引用，不应被 unlink
        self.assertTrue(shared_path.is_file())
        self.assertIsNotNone(self.service.get_image_path(second_record_id, second_image_id))

    def test_delete_images_removes_reference_when_file_missing(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="文件缺失仍移除引用",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "文件缺失仍移除引用"}],
            usage={"input_tokens": 1, "output_tokens": 2, "total_tokens": 3},
        )

        record_id = record["id"]
        image_id = record["images"][0]["id"]
        image_path = self.service.get_image_path(record_id, image_id)
        self.assertIsNotNone(image_path)
        self.assertTrue(image_path.is_file())

        image_path.unlink()
        self.assertFalse(image_path.exists())
        self.assertIsNone(self.service.get_image_path(record_id, image_id))

        result = self.service.delete_images([{"record_id": record_id, "image_ids": [image_id]}])
        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)
        self.assertEqual(len(self.service.list_records()), 0)


if __name__ == "__main__":
    unittest.main()
