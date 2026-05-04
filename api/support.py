from __future__ import annotations

from pathlib import Path
from threading import Event, Thread
from urllib.parse import urlsplit, urlunsplit

from fastapi import HTTPException, Request

from services.account_service import account_service
from services.auth_service import auth_service
from services.config import config

BASE_DIR = Path(__file__).resolve().parents[1]
WEB_DIST_DIR = BASE_DIR / "web_dist"


def extract_bearer_token(authorization: str | None) -> str:
    scheme, _, value = str(authorization or "").partition(" ")
    if scheme.lower() != "bearer" or not value.strip():
        return ""
    return value.strip()


def _legacy_admin_identity(token: str) -> dict[str, object] | None:
    auth_key = str(config.auth_key or "").strip()
    if auth_key and token == auth_key:
        return {"id": "admin", "name": "管理员", "role": "admin"}
    return None


def require_identity(authorization: str | None) -> dict[str, object]:
    token = extract_bearer_token(authorization)
    identity = _legacy_admin_identity(token) or auth_service.authenticate(token)
    if identity is None:
        raise HTTPException(status_code=401, detail={"error": "密钥无效或已失效，请重新登录"})
    return identity


def require_auth_key(authorization: str | None) -> None:
    require_identity(authorization)


def require_admin(authorization: str | None) -> dict[str, object]:
    identity = require_identity(authorization)
    if identity.get("role") != "admin":
        raise HTTPException(status_code=403, detail={"error": "需要管理员权限才能执行这个操作"})
    return identity


def _header_value(request: Request, name: str) -> str:
    headers = getattr(request, "headers", {}) or {}
    value = headers.get(name) or headers.get(name.lower())
    return str(value or "").strip()


def _first_header_value(request: Request, name: str) -> str:
    return _header_value(request, name).split(",", 1)[0].strip()


def _forwarded_value(request: Request, name: str) -> str:
    forwarded = _first_header_value(request, "forwarded")
    if not forwarded:
        return ""
    for part in forwarded.split(";"):
        key, _, value = part.strip().partition("=")
        if key.lower() == name:
            return value.strip().strip('"')
    return ""


def _valid_url_scheme(value: str) -> str:
    scheme = value.strip().lower()
    return scheme if scheme in {"http", "https"} else ""


def _scheme_from_url_header(request: Request, name: str) -> str:
    value = _first_header_value(request, name)
    if not value:
        return ""
    return _valid_url_scheme(urlsplit(value).scheme)


def _public_request_scheme(request: Request) -> str:
    forwarded_proto = _valid_url_scheme(_forwarded_value(request, "proto"))
    if forwarded_proto:
        return forwarded_proto

    forwarded_proto = _valid_url_scheme(_first_header_value(request, "x-forwarded-proto"))
    if forwarded_proto:
        return forwarded_proto

    forwarded_ssl = _first_header_value(request, "x-forwarded-ssl").lower()
    if forwarded_ssl in {"on", "1", "true"}:
        return "https"

    url_scheme = _valid_url_scheme(_first_header_value(request, "x-url-scheme"))
    if url_scheme:
        return url_scheme

    origin_scheme = _scheme_from_url_header(request, "origin")
    if origin_scheme:
        return origin_scheme

    referer_scheme = _scheme_from_url_header(request, "referer")
    if referer_scheme:
        return referer_scheme

    return _valid_url_scheme(str(request.url.scheme or "")) or "http"


def _public_request_host(request: Request) -> str:
    return (
        _forwarded_value(request, "host")
        or _first_header_value(request, "x-forwarded-host")
        or _first_header_value(request, "host")
        or str(request.url.netloc or "")
    ).strip()


def _upgrade_same_host_https(base_url: str, request: Request) -> str:
    if _public_request_scheme(request) != "https":
        return base_url

    parsed = urlsplit(base_url)
    public_host = _public_request_host(request).lower()
    if parsed.scheme != "http" or parsed.netloc.lower() != public_host:
        return base_url

    return urlunsplit(("https", parsed.netloc, parsed.path, parsed.query, parsed.fragment)).rstrip("/")


def resolve_image_base_url(request: Request) -> str:
    configured_base_url = str(config.base_url or "").strip().rstrip("/")
    if configured_base_url:
        return _upgrade_same_host_https(configured_base_url, request)

    return f"{_public_request_scheme(request)}://{_public_request_host(request)}"


def raise_image_quota_error(exc: Exception) -> None:
    message = str(exc)
    if "no available image quota" in message.lower():
        raise HTTPException(status_code=429, detail={"error": "no available image quota"}) from exc
    raise HTTPException(status_code=502, detail={"error": message}) from exc


def sanitize_cpa_pool(pool: dict | None) -> dict | None:
    if not isinstance(pool, dict):
        return None
    return {key: value for key, value in pool.items() if key != "secret_key"}


def sanitize_cpa_pools(pools: list[dict]) -> list[dict]:
    return [sanitized for pool in pools if (sanitized := sanitize_cpa_pool(pool)) is not None]


def sanitize_sub2api_server(server: dict | None) -> dict | None:
    if not isinstance(server, dict):
        return None
    sanitized = {key: value for key, value in server.items() if key not in {"password", "api_key"}}
    sanitized["has_api_key"] = bool(str(server.get("api_key") or "").strip())
    return sanitized


def sanitize_sub2api_servers(servers: list[dict]) -> list[dict]:
    return [sanitized for server in servers if (sanitized := sanitize_sub2api_server(server)) is not None]


def start_limited_account_watcher(stop_event: Event) -> Thread:
    interval_seconds = config.refresh_account_interval_minute * 60

    def worker() -> None:
        while not stop_event.is_set():
            try:
                limited_tokens = account_service.list_limited_tokens()
                if limited_tokens:
                    print(f"[account-limited-watcher] checking {len(limited_tokens)} limited accounts")
                    account_service.refresh_accounts(limited_tokens)
            except Exception as exc:
                print(f"[account-limited-watcher] fail {exc}")
            stop_event.wait(interval_seconds)

    thread = Thread(target=worker, name="limited-account-watcher", daemon=True)
    thread.start()
    return thread


def resolve_web_asset(requested_path: str) -> Path | None:
    if not WEB_DIST_DIR.exists():
        return None
    clean_path = requested_path.strip("/")
    base_dir = WEB_DIST_DIR.resolve()
    candidates = [base_dir / "index.html"] if not clean_path else [
        base_dir / Path(clean_path),
        base_dir / clean_path / "index.html",
        base_dir / f"{clean_path}.html",
    ]
    for candidate in candidates:
        try:
            candidate.resolve().relative_to(base_dir)
        except ValueError:
            continue
        if candidate.is_file():
            return candidate
    return None
