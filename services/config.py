from __future__ import annotations

from dataclasses import dataclass
import json
import os
import sys
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parents[1]
DATA_DIR = BASE_DIR / "data"
CONFIG_FILE = BASE_DIR / "config.json"
PLACEHOLDER_AUTH_KEYS = {
    "your_real_auth_key",
    "replace-me",
    "chatgpt2api",
}


@dataclass(frozen=True)
class LoadedSettings:
    auth_key: str
    accounts_file: Path
    image_history_file: Path
    image_history_dir: Path
    refresh_account_interval_minute: int


def _read_json_object(path: Path, *, name: str) -> dict[str, object]:
    if not path.exists():
        return {}
    if path.is_dir():
        print(
            f"Warning: {name} at '{path}' is a directory, ignoring it and falling back to other configuration sources.",
            file=sys.stderr,
        )
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return {}
    return data if isinstance(data, dict) else {}


def _normalize_auth_key(value: object) -> str:
    auth_key = str(value or "").strip()
    return "" if auth_key.lower() in PLACEHOLDER_AUTH_KEYS else auth_key


def _missing_auth_key_message() -> str:
    return (
        "❌ auth-key 未设置！\n"
        "请按以下任意一种方式解决：\n"
        "1. 在环境变量中添加：\n"
        "   CHATGPT2API_AUTH_KEY = your_real_auth_key\n"
        "2. 或者在 config.json 中填写：\n"
        '   "auth-key": "your_real_auth_key"\n'
        "注意：占位值 your_real_auth_key / replace-me / chatgpt2api 不会被接受。"
    )


def _load_settings() -> LoadedSettings:
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    raw_config = _read_json_object(CONFIG_FILE, name="config.json")
    auth_key = _normalize_auth_key(os.getenv("CHATGPT2API_AUTH_KEY") or raw_config.get("auth-key") or "")
    if not auth_key:
        raise ValueError(_missing_auth_key_message())

    try:
        refresh_interval = int(raw_config.get("refresh_account_interval_minute", 60))
    except (TypeError, ValueError):
        refresh_interval = 60

    return LoadedSettings(
        auth_key=auth_key,
        accounts_file=DATA_DIR / "accounts.json",
        image_history_file=DATA_DIR / "image_history.json",
        image_history_dir=DATA_DIR / "image-history",
        refresh_account_interval_minute=refresh_interval,
    )


class ConfigStore:
    def __init__(self, path: Path):
        self.path = path
        DATA_DIR.mkdir(parents=True, exist_ok=True)
        self.data = self._load()
        if not self.auth_key:
            raise ValueError(_missing_auth_key_message())

    def _load(self) -> dict[str, object]:
        return _read_json_object(self.path, name="config.json")

    def _save(self) -> None:
        self.path.write_text(json.dumps(self.data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    @property
    def auth_key(self) -> str:
        return _normalize_auth_key(os.getenv("CHATGPT2API_AUTH_KEY") or self.data.get("auth-key") or "")

    @property
    def accounts_file(self) -> Path:
        return DATA_DIR / "accounts.json"

    @property
    def image_history_file(self) -> Path:
        return DATA_DIR / "image_history.json"

    @property
    def image_history_dir(self) -> Path:
        path = DATA_DIR / "image-history"
        path.mkdir(parents=True, exist_ok=True)
        return path

    @property
    def refresh_account_interval_minute(self) -> int:
        try:
            return int(self.data.get("refresh_account_interval_minute", 60))
        except (TypeError, ValueError):
            return 60

    @property
    def images_dir(self) -> Path:
        path = DATA_DIR / "images"
        path.mkdir(parents=True, exist_ok=True)
        return path

    @property
    def base_url(self) -> str:
        return str(
            os.getenv("CHATGPT2API_BASE_URL")
            or self.data.get("base_url")
            or ""
        ).strip().rstrip("/")

    def get(self) -> dict[str, object]:
        return dict(self.data)

    def get_proxy_settings(self) -> str:
        return str(self.data.get("proxy") or "").strip()

    def update(self, data: dict[str, object]) -> dict[str, object]:
        self.data = dict(data or {})
        self._save()
        return self.get()


config = ConfigStore(CONFIG_FILE)
