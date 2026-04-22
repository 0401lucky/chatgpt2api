from __future__ import annotations
import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import cast

BASE_DIR = Path(__file__).resolve().parents[1]
DATA_DIR = BASE_DIR / "data"
CONFIG_FILE = BASE_DIR / "config.json"
PLACEHOLDER_AUTH_KEYS = {
    "your_real_auth_key",
    "replace-me",
    "chatgpt2api",
}


@dataclass(frozen=True)
class AppSettings:
    auth_key: str
    host: str
    port: int
    accounts_file: Path
    image_history_file: Path
    image_history_dir: Path
    refresh_account_interval_minute: int


def _readable_json_file(path: Path, *, name: str) -> Path | None:
    if not path.exists():
        return None
    if path.is_dir():
        print(
            f"Warning: {name} at '{path}' is a directory, ignoring it and falling back to other configuration sources.",
            file=sys.stderr,
        )
        return None
    return path


def _load_json_object(path: Path, *, name: str) -> dict[str, object]:
    text = path.read_text(encoding="utf-8").strip()
    if not text:
        return {}
    loaded = json.loads(text)
    if not isinstance(loaded, dict):
        raise ValueError(f"{name} must be a JSON object")
    return loaded


def _load_settings() -> AppSettings:
    DATA_DIR.mkdir(parents=True, exist_ok=True)

    # 优先使用环境变量，文件配置仅作为本地/自托管回退
    raw_config: dict[str, object] = {}
    config_file = _readable_json_file(CONFIG_FILE, name="config.json")
    if config_file is not None:
        raw_config.update(_load_json_object(config_file, name="config.json"))

    raw_auth_key = str(
        os.getenv("CHATGPT2API_AUTH_KEY")
        or raw_config.get("auth-key")
        or ""
    ).strip()
    auth_key = "" if raw_auth_key.lower() in PLACEHOLDER_AUTH_KEYS else raw_auth_key

    if not auth_key:
        raise ValueError(
            "❌ auth-key 未设置！\n"
            "请按以下任意一种方式解决：\n"
            "1. 在 Render 的 Environment 变量中添加：\n"
            "   CHATGPT2API_AUTH_KEY = your_real_auth_key\n"
            "2. 或者在 config.json 中填写：\n"
            '   "auth-key": "your_real_auth_key"\n'
            "注意：占位值 your_real_auth_key / replace-me / chatgpt2api 不会被接受。"
        )

    refresh_account_interval_minute = cast(
        int, raw_config.get("refresh_account_interval_minute", 60)
    )

    return AppSettings(
        auth_key=auth_key,
        host="0.0.0.0",
        port=8000,
        accounts_file=DATA_DIR / "accounts.json",
        image_history_file=DATA_DIR / "image_history.json",
        image_history_dir=DATA_DIR / "image-history",
        refresh_account_interval_minute=refresh_account_interval_minute,
    )


config = _load_settings()
