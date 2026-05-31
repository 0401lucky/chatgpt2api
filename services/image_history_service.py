from __future__ import annotations

import base64
import json
import shutil
import uuid
from copy import deepcopy
from datetime import datetime, timezone
from pathlib import Path
from threading import Lock
from typing import Any
from urllib.parse import unquote, urlsplit

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


def _mime_type_for_suffix(suffix: str) -> str:
    normalized = str(suffix or "").strip().lower()
    if normalized in {".jpg", ".jpeg"}:
        return "image/jpeg"
    if normalized == ".webp":
        return "image/webp"
    if normalized == ".gif":
        return "image/gif"
    return "image/png"


def _safe_relative_path(value: object) -> str:
    normalized = str(value or "").strip().replace("\\", "/").lstrip("/")
    if not normalized:
        return ""
    parts = Path(normalized).parts
    if any(part in {"", ".", ".."} for part in parts):
        return ""
    return Path(*parts).as_posix()


def _record_day_parts(created_at: str) -> tuple[str, str, str]:
    try:
        parsed = datetime.fromisoformat(str(created_at or "").replace("Z", "+00:00"))
    except Exception:
        parsed = datetime.now(timezone.utc)
    return f"{parsed.year:04d}", f"{parsed.month:02d}", f"{parsed.day:02d}"


def _copy_json(data: Any) -> Any:
    return json.loads(json.dumps(data, ensure_ascii=False))


class ImageHistoryService:
    def __init__(
        self,
        store_file: Path,
        image_dir: Path,
        max_records: int = 500,
        managed_image_dir: Path | None = None,
    ):
        self.store_file = store_file
        self.image_dir = image_dir
        self.managed_image_dir = managed_image_dir
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

    def _safe_managed_path(self, rel_path: object) -> Path | None:
        if self.managed_image_dir is None:
            return None
        rel = _safe_relative_path(rel_path)
        if not rel:
            return None
        base_dir = self.managed_image_dir.resolve(strict=False)
        candidate = (self.managed_image_dir / rel).resolve(strict=False)
        try:
            candidate.relative_to(base_dir)
        except Exception:
            return None
        if candidate == base_dir:
            return None
        return candidate

    def _safe_history_path(self, file_name: object) -> Path | None:
        rel = _safe_relative_path(file_name)
        if not rel:
            return None
        base_dir = self.image_dir.resolve(strict=False)
        candidate = (self.image_dir / rel).resolve(strict=False)
        try:
            candidate.relative_to(base_dir)
        except Exception:
            return None
        if candidate == base_dir:
            return None
        return candidate

    def _safe_path_for_entry(self, image: dict[str, Any]) -> Path | None:
        paths = self._safe_paths_for_entry(image)
        return paths[0] if paths else None

    def _safe_paths_for_entry(self, image: dict[str, Any]) -> list[Path]:
        paths: list[Path] = []
        managed_path = self._safe_managed_path(image.get("rel_path"))
        if managed_path is not None:
            paths.append(managed_path)
        history_path = self._safe_history_path(image.get("file_name"))
        if history_path is not None and history_path not in paths:
            paths.append(history_path)
        return paths

    def _image_path_for_entry(self, image: dict[str, Any]) -> Path | None:
        for candidate in self._safe_paths_for_entry(image):
            if candidate.is_file():
                return candidate
        return None

    def _managed_rel_from_url(self, url: object) -> str:
        if self.managed_image_dir is None:
            return ""
        parsed = urlsplit(str(url or "").strip())
        path = unquote(parsed.path or "")
        marker = "/images/"
        if marker not in path:
            return ""
        rel = _safe_relative_path(path.split(marker, 1)[1])
        if not rel:
            return ""
        candidate = self._safe_managed_path(rel)
        return rel if candidate is not None and candidate.is_file() else ""

    def _managed_rel_for_record(self, created_at: str, record_id: str, index: int, suffix: str) -> str:
        year, month, day = _record_day_parts(created_at)
        clean_suffix = suffix if str(suffix or "").startswith(".") else ".png"
        return f"api-history/{year}/{month}/{day}/{record_id}-{index}{clean_suffix}"

    def _delete_record_files(self, record: dict[str, Any]) -> None:
        for image in record.get("images") or []:
            if not isinstance(image, dict):
                continue
            for image_path in self._safe_paths_for_entry(image):
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
                    return self._image_path_for_entry(image)
        return None

    def get_image_entry(self, record_id: str, image_id: str) -> tuple[dict[str, Any], Path] | None:
        with self._lock:
            for record in self._records:
                if record.get("id") != record_id:
                    continue
                for image in record.get("images") or []:
                    if not isinstance(image, dict) or image.get("id") != image_id:
                        continue
                    image_path = self._image_path_for_entry(image)
                    if image_path is not None:
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
                    for safe in self._safe_paths_for_entry(image):
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
                    for safe in self._safe_paths_for_entry(image):
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

    def delete_by_relative_paths(self, paths: list[str]) -> dict[str, Any]:
        wanted_paths = {_safe_relative_path(path) for path in paths}
        wanted_paths.discard("")
        if not wanted_paths:
            return {
                "items": self.list_records(),
                "deleted_images": 0,
                "deleted_records": 0,
            }

        delete_map: dict[str, list[str]] = {}
        with self._lock:
            for record in self._records:
                record_id = str(record.get("id") or "").strip()
                if not record_id:
                    continue
                for image in record.get("images") or []:
                    if not isinstance(image, dict):
                        continue
                    rel_path = _safe_relative_path(image.get("rel_path"))
                    image_id = str(image.get("id") or "").strip()
                    if rel_path in wanted_paths and image_id:
                        delete_map.setdefault(record_id, []).append(image_id)

        if not delete_map:
            return {
                "items": self.list_records(),
                "deleted_images": 0,
                "deleted_records": 0,
            }

        return self.delete_images(
            [{"record_id": record_id, "image_ids": image_ids} for record_id, image_ids in delete_map.items()]
        )

    def ensure_managed_images(self) -> int:
        if self.managed_image_dir is None:
            return 0

        migrated = 0
        changed = False
        with self._lock:
            for record in self._records:
                record_id = str(record.get("id") or "").strip()
                created_at = str(record.get("created_at") or "")
                if not record_id:
                    continue
                for index, image in enumerate(record.get("images") or [], start=1):
                    if not isinstance(image, dict):
                        continue
                    existing_rel = _safe_relative_path(image.get("rel_path"))
                    existing_path = self._safe_managed_path(existing_rel)
                    if existing_rel and existing_path is not None and existing_path.is_file():
                        continue

                    source_path = self._safe_history_path(image.get("file_name"))
                    if source_path is None or not source_path.is_file():
                        continue

                    suffix = source_path.suffix or ".png"
                    rel_path = existing_rel or self._managed_rel_for_record(created_at, record_id, index, suffix)
                    target_path = self._safe_managed_path(rel_path)
                    if target_path is None:
                        continue
                    target_path.parent.mkdir(parents=True, exist_ok=True)
                    if not target_path.exists():
                        shutil.copyfile(source_path, target_path)
                        migrated += 1
                    image["rel_path"] = rel_path
                    image["file_name"] = str(image.get("file_name") or source_path.name)
                    image["mime_type"] = str(image.get("mime_type") or _mime_type_for_suffix(suffix))
                    changed = True

            if changed:
                self._save_records()

        return migrated

    def managed_metadata_by_path(self) -> dict[str, dict[str, Any]]:
        metadata: dict[str, dict[str, Any]] = {}
        with self._lock:
            for record in self._records:
                record_meta = {
                    "record_id": str(record.get("id") or ""),
                    "created_at": str(record.get("created_at") or ""),
                    "source_endpoint": str(record.get("source_endpoint") or ""),
                    "mode": str(record.get("mode") or "generate"),
                    "model": str(record.get("model") or ""),
                    "prompt": str(record.get("prompt") or ""),
                    "usage": _copy_json(record.get("usage") or {}),
                }
                for image in record.get("images") or []:
                    if not isinstance(image, dict):
                        continue
                    rel_path = _safe_relative_path(image.get("rel_path"))
                    image_id = str(image.get("id") or "").strip()
                    if not rel_path or not image_id:
                        continue
                    metadata[rel_path] = {
                        **record_meta,
                        "image_id": image_id,
                    }
        return metadata

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
        if self.managed_image_dir is not None:
            self.managed_image_dir.mkdir(parents=True, exist_ok=True)

        for index, item in enumerate(image_items, start=1):
            b64_json = str(item.get("b64_json") or "").strip()
            image_id = uuid.uuid4().hex
            managed_rel_path = self._managed_rel_from_url(item.get("url"))
            if managed_rel_path:
                managed_path = self._safe_managed_path(managed_rel_path)
                suffix = managed_path.suffix if managed_path is not None else Path(managed_rel_path).suffix
                mime_type = _mime_type_for_suffix(suffix)
                file_name = Path(managed_rel_path).name
                stored_images.append(
                    {
                        "id": image_id,
                        "file_name": file_name,
                        "rel_path": managed_rel_path,
                        "mime_type": mime_type,
                    }
                )
                continue

            if not b64_json:
                continue
            image_bytes = base64.b64decode(b64_json)
            suffix, mime_type = _detect_image_suffix(image_bytes)
            file_name = f"{record_id}-{index}{suffix}"
            rel_path = ""
            if self.managed_image_dir is not None:
                rel_path = self._managed_rel_for_record(created_at, record_id, index, suffix)
                image_path = self._safe_managed_path(rel_path)
                if image_path is None:
                    continue
            else:
                image_path = self.image_dir / file_name
            image_path.parent.mkdir(parents=True, exist_ok=True)
            image_path.write_bytes(image_bytes)
            image_record = {
                "id": image_id,
                "file_name": file_name,
                "mime_type": mime_type,
            }
            if rel_path:
                image_record["rel_path"] = rel_path
            stored_images.append(
                image_record
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
    managed_image_dir=config.images_dir,
)
