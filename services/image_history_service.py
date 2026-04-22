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
