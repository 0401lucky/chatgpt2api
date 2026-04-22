from __future__ import annotations

import base64
import json
import uuid
from copy import deepcopy
from datetime import datetime, timezone
from pathlib import Path
from threading import Lock
from typing import Any

from services.config import config


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _detect_image_suffix(image_bytes: bytes) -> tuple[str, str]:
    if image_bytes.startswith(b"\xff\xd8\xff"):
        return ".jpg", "image/jpeg"
    if image_bytes.startswith(b"RIFF") and image_bytes[8:12] == b"WEBP":
        return ".webp", "image/webp"
    if image_bytes.startswith((b"GIF87a", b"GIF89a")):
        return ".gif", "image/gif"
    return ".png", "image/png"


def _copy_json(data: Any) -> Any:
    return json.loads(json.dumps(data, ensure_ascii=False))


class ImageHistoryService:
    def __init__(self, store_file: Path, image_dir: Path, max_records: int = 500):
        self.store_file = store_file
        self.image_dir = image_dir
        self.max_records = max(1, int(max_records or 1))
        self._lock = Lock()
        self._records = self._load_records()

    def _load_records(self) -> list[dict[str, Any]]:
        if not self.store_file.exists():
            return []
        try:
            raw = json.loads(self.store_file.read_text(encoding="utf-8"))
        except Exception:
            return []
        if not isinstance(raw, list):
            return []
        return [item for item in raw if isinstance(item, dict)]

    def _save_records(self) -> None:
        self.store_file.parent.mkdir(parents=True, exist_ok=True)
        self.store_file.write_text(
            json.dumps(self._records, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )

    def _delete_record_files(self, record: dict[str, Any]) -> None:
        for image in record.get("images") or []:
            if not isinstance(image, dict):
                continue
            file_name = str(image.get("file_name") or "").strip()
            if not file_name:
                continue
            image_path = self.image_dir / file_name
            if image_path.exists():
                image_path.unlink()

    def list_records(self) -> list[dict[str, Any]]:
        with self._lock:
            return _copy_json(self._records)

    def get_image_path(self, record_id: str, image_id: str) -> Path | None:
        with self._lock:
            for record in self._records:
                if record.get("id") != record_id:
                    continue
                for image in record.get("images") or []:
                    if not isinstance(image, dict) or image.get("id") != image_id:
                        continue
                    image_path = self.image_dir / str(image.get("file_name") or "")
                    return image_path if image_path.is_file() else None
        return None

    def get_image_entry(self, record_id: str, image_id: str) -> tuple[dict[str, Any], Path] | None:
        with self._lock:
            for record in self._records:
                if record.get("id") != record_id:
                    continue
                for image in record.get("images") or []:
                    if not isinstance(image, dict) or image.get("id") != image_id:
                        continue
                    image_path = self.image_dir / str(image.get("file_name") or "")
                    if image_path.is_file():
                        return _copy_json(image), image_path
                    return None
        return None

    def delete_images(self, items: object) -> dict[str, Any]:
        """
        批量删除图片。

        items 输入格式：[{ record_id, image_ids }]
        - 对无效输入不抛异常，返回 0 删除结果
        - deleted_images 统计的是“从历史记录中移除的图片引用条目数”（不是物理文件删除数）
        - 返回结构仅包含 items、deleted_images、deleted_records
        """

        base_dir = self.image_dir.resolve(strict=False)

        def _safe_path(file_name: str) -> Path | None:
            name = str(file_name or "").strip()
            if not name:
                return None
            candidate = (self.image_dir / name).resolve(strict=False)
            try:
                candidate.relative_to(base_dir)
            except Exception:
                return None
            if candidate == base_dir:
                return None
            return candidate

        # 规范化输入，确保后续解析、统计与行为一致
        normalized_items: list[dict[str, Any]] = []
        if isinstance(items, list):
            for raw in items:
                if not isinstance(raw, dict):
                    continue
                record_id = str(raw.get("record_id") or "").strip()
                image_ids = raw.get("image_ids")
                if not record_id or not isinstance(image_ids, list):
                    continue
                normalized_ids = [str(image_id).strip() for image_id in image_ids]
                normalized_ids = [image_id for image_id in normalized_ids if image_id]
                if not normalized_ids:
                    continue
                normalized_items.append({"record_id": record_id, "image_ids": normalized_ids})

        delete_plan: dict[str, set[str]] = {}
        for item in normalized_items:
            record_id = item["record_id"]
            image_ids = {str(image_id).strip() for image_id in item["image_ids"] if str(image_id).strip()}
            if not image_ids:
                continue
            delete_plan.setdefault(record_id, set()).update(image_ids)

        removed_images = 0
        deleted_records = 0
        changed = False
        paths_to_delete: list[Path] = []

        # 优先保证索引一致性：锁内完成记录更新、落盘与快照生成；锁外仅做 best-effort 物理删除
        latest_items: list[dict[str, Any]]
        with self._lock:
            latest_items = _copy_json(self._records)
            if not delete_plan:
                return {
                    "items": latest_items,
                    "deleted_images": 0,
                    "deleted_records": 0,
                }

            previous_records = _copy_json(self._records)

            # 统计所有 safe path 的总引用数（跨所有记录）
            total_refs: dict[Path, int] = {}
            for record in self._records:
                for image in record.get("images") or []:
                    if not isinstance(image, dict):
                        continue
                    safe = _safe_path(image.get("file_name"))
                    if safe is not None:
                        total_refs[safe] = total_refs.get(safe, 0) + 1

            # 锁内更新记录：成功语义为“移除历史引用”；不依赖物理 unlink 结果
            removed_refs: dict[Path, int] = {}
            for idx in range(len(self._records) - 1, -1, -1):
                record = self._records[idx]
                record_id = str(record.get("id") or "").strip()
                wanted_ids = delete_plan.get(record_id)
                if not wanted_ids:
                    continue

                images = record.get("images") or []
                if not isinstance(images, list):
                    continue

                kept_images: list[object] = []
                removed_here = 0
                for image in images:
                    if not isinstance(image, dict):
                        kept_images.append(image)
                        continue

                    image_id = str(image.get("id") or "").strip()
                    if not image_id or image_id not in wanted_ids:
                        kept_images.append(image)
                        continue

                    removed_here += 1
                    safe = _safe_path(image.get("file_name"))
                    if safe is not None:
                        removed_refs[safe] = removed_refs.get(safe, 0) + 1

                if removed_here == 0:
                    continue

                changed = True
                removed_images += removed_here

                # 只要还有任何未删除条目（包含非 dict 条目），就不应误删整条记录
                if kept_images:
                    record["images"] = kept_images
                    record["image_count"] = len([img for img in kept_images if isinstance(img, dict)])
                else:
                    self._records.pop(idx)
                    deleted_records += 1

            if changed:
                for path, removed_count in removed_refs.items():
                    if total_refs.get(path, 0) - removed_count == 0:
                        paths_to_delete.append(path)

                try:
                    self._save_records()
                except Exception:
                    self._records = previous_records
                    raise

            latest_items = _copy_json(self._records)

        for path in paths_to_delete:
            try:
                if path.exists() and path.is_file():
                    path.unlink()
            except Exception:
                pass

        return {
            "items": latest_items,
            "deleted_images": removed_images,
            "deleted_records": deleted_records,
        }

    def save_record(
        self,
        *,
        source_endpoint: str,
        mode: str,
        model: str,
        prompt: str,
        image_items: list[dict[str, object]],
        usage: dict[str, object],
    ) -> dict[str, Any]:
        record_id = uuid.uuid4().hex
        created_at = _now_iso()
        stored_images: list[dict[str, Any]] = []
        self.image_dir.mkdir(parents=True, exist_ok=True)

        for index, item in enumerate(image_items, start=1):
            b64_json = str(item.get("b64_json") or "").strip()
            if not b64_json:
                continue
            image_bytes = base64.b64decode(b64_json)
            suffix, mime_type = _detect_image_suffix(image_bytes)
            image_id = uuid.uuid4().hex
            file_name = f"{record_id}-{index}{suffix}"
            image_path = self.image_dir / file_name
            image_path.write_bytes(image_bytes)
            stored_images.append(
                {
                    "id": image_id,
                    "file_name": file_name,
                    "mime_type": mime_type,
                }
            )

        if not stored_images:
            raise ValueError("image_items must include at least one b64_json image")

        record = {
            "id": record_id,
            "created_at": created_at,
            "source_endpoint": str(source_endpoint or "").strip(),
            "mode": str(mode or "").strip() or "generate",
            "model": str(model or "").strip(),
            "prompt": str(prompt or "").strip(),
            "image_count": len(stored_images),
            "images": stored_images,
            "usage": {
                "input_tokens": int(usage.get("input_tokens") or 0),
                "output_tokens": int(usage.get("output_tokens") or 0),
                "total_tokens": int(usage.get("total_tokens") or 0),
            },
        }

        with self._lock:
            dropped_records = self._records[self.max_records - 1:] if len(self._records) >= self.max_records else []
            self._records = [record, *self._records[: self.max_records - 1]]
            self._save_records()

        for dropped in dropped_records:
            self._delete_record_files(dropped)

        return _copy_json(record)


image_history_service = ImageHistoryService(
    store_file=config.image_history_file,
    image_dir=config.image_history_dir,
    max_records=500,
)
