from __future__ import annotations

import hashlib
import time
from pathlib import Path

from curl_cffi import requests

from services.config import config


_DEFAULT_TIMEOUT_SECONDS = 30
_DEFAULT_FOLDER_PREFIX = "chatgpt2api"


def _build_filename(image_data: bytes) -> str:
    return f"{int(time.time())}_{hashlib.md5(image_data).hexdigest()}.png"


def _build_relative_dir() -> Path:
    return Path(time.strftime("%Y"), time.strftime("%m"), time.strftime("%d"))


def _save_locally(image_data: bytes, base_url: str | None, cleanup: bool) -> str:
    if cleanup:
        config.cleanup_old_images()
    filename = _build_filename(image_data)
    relative_dir = _build_relative_dir()
    file_path = config.images_dir / relative_dir / filename
    file_path.parent.mkdir(parents=True, exist_ok=True)
    file_path.write_bytes(image_data)
    return f"{(base_url or config.base_url)}/images/{relative_dir.as_posix()}/{filename}"


def _normalize_full_url(src: str, imgbed_base_url: str) -> str:
    cleaned_src = (src or "").strip()
    if not cleaned_src:
        raise ValueError("imgbed response missing src")
    if cleaned_src.startswith(("http://", "https://")):
        return cleaned_src
    return f"{imgbed_base_url.rstrip('/')}/{cleaned_src.lstrip('/')}"


def _upload_to_imgbed(
    image_data: bytes,
    *,
    settings: dict[str, object],
) -> str:
    imgbed_base_url = str(settings.get("base_url") or "").strip().rstrip("/")
    api_token = str(settings.get("api_token") or "").strip()
    folder_prefix = str(settings.get("folder_prefix") or _DEFAULT_FOLDER_PREFIX).strip().strip("/") or _DEFAULT_FOLDER_PREFIX
    try:
        timeout_seconds = max(1, int(settings.get("timeout_seconds") or _DEFAULT_TIMEOUT_SECONDS))
    except (TypeError, ValueError):
        timeout_seconds = _DEFAULT_TIMEOUT_SECONDS

    if not imgbed_base_url:
        raise ValueError("imgbed base_url is required")
    if not api_token:
        raise ValueError("imgbed api_token is required")

    filename = _build_filename(image_data)
    relative_dir = _build_relative_dir()
    upload_folder = f"{folder_prefix}/{relative_dir.as_posix()}"

    url = f"{imgbed_base_url}/upload"
    headers = {"Authorization": f"Bearer {api_token}"}
    params = {
        "uploadFolder": upload_folder,
        "returnFormat": "full",
        "uploadNameType": "default",
        "autoRetry": "true",
    }
    files = {"file": (filename, image_data, "image/png")}

    response = requests.post(
        url,
        headers=headers,
        params=params,
        files=files,
        timeout=timeout_seconds,
    )
    response.raise_for_status()

    payload = response.json()
    if isinstance(payload, list) and payload and isinstance(payload[0], dict):
        return _normalize_full_url(str(payload[0].get("src") or ""), imgbed_base_url)
    if isinstance(payload, dict):
        candidate = payload.get("src") or payload.get("url")
        if candidate:
            return _normalize_full_url(str(candidate), imgbed_base_url)
    raise ValueError(f"unexpected imgbed response: {payload!r}")


def save_image_with_fallback(
    image_data: bytes,
    base_url: str | None = None,
    *,
    cleanup: bool = False,
) -> str:
    settings = config.get_imgbed_settings()
    if not settings.get("enabled"):
        return _save_locally(image_data, base_url, cleanup)

    try:
        return _upload_to_imgbed(image_data, settings=settings)
    except Exception as exc:
        if not settings.get("fallback_to_local", True):
            raise
        print(f"[imgbed] upload failed, fallback to local storage: {exc}")
        return _save_locally(image_data, base_url, cleanup)


def test_imgbed_connection(settings: dict[str, object]) -> dict[str, object]:
    """主动验证图床连接：上传一张 1x1 PNG 测试图。"""
    test_png = bytes.fromhex(
        "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4"
        "890000000d49444154789c63000100000005000100"
        "0d0a2db40000000049454e44ae426082"
    )
    url = _upload_to_imgbed(test_png, settings=settings)
    return {"ok": True, "url": url}
