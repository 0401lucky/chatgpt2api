from __future__ import annotations

import base64
import binascii
from json import JSONDecodeError

from fastapi import APIRouter, Header, HTTPException, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import FileResponse
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, ConfigDict, Field
from starlette.datastructures import FormData, UploadFile

from api.support import raise_image_quota_error, require_auth_key, resolve_image_base_url
from services.account_service import account_service
from services.chatgpt_service import ChatGPTService, ImageGenerationError
from services.image_history_service import image_history_service
from utils.helper import is_image_chat_request, parse_image_count, sse_json_stream


class ImageGenerationRequest(BaseModel):
    prompt: str = Field(..., min_length=1)
    model: str = "gpt-image-2"
    n: int = Field(default=1, ge=1, le=4)
    response_format: str = "b64_json"
    history_disabled: bool = True
    stream: bool | None = None


class ChatCompletionRequest(BaseModel):
    model_config = ConfigDict(extra="allow")
    model: str | None = None
    prompt: str | None = None
    n: int | None = None
    stream: bool | None = None
    modalities: list[str] | None = None
    messages: list[dict[str, object]] | None = None


class ResponseCreateRequest(BaseModel):
    model_config = ConfigDict(extra="allow")
    model: str | None = None
    input: object | None = None
    tools: list[dict[str, object]] | None = None
    tool_choice: object | None = None
    stream: bool | None = None


class ImageHistoryDeleteItem(BaseModel):
    record_id: str = ""
    image_ids: list[str] = Field(default_factory=list)


class ImageHistoryDeleteRequest(BaseModel):
    items: list[ImageHistoryDeleteItem] = Field(default_factory=list)


def bad_request(message: str) -> HTTPException:
    return HTTPException(status_code=400, detail={"error": message})


def _image_suffix_for_mime_type(mime_type: str) -> str:
    normalized_mime_type = str(mime_type or "").strip().lower()
    if normalized_mime_type == "image/jpeg":
        return ".jpg"
    if normalized_mime_type == "image/webp":
        return ".webp"
    if normalized_mime_type == "image/gif":
        return ".gif"
    return ".png"


def _parse_stream_flag(value: object) -> bool:
    if isinstance(value, bool):
        return value
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def _decode_json_edit_image(image_url: object, index: int) -> tuple[bytes, str, str]:
    field_name = f"images[{index}].image_url"
    normalized_image_url = str(image_url or "").strip()
    if not normalized_image_url:
        raise bad_request(f"{field_name} is required")
    if not normalized_image_url.startswith("data:"):
        raise bad_request(f"{field_name} must be a data URL")

    header, separator, encoded_data = normalized_image_url.partition(",")
    if not separator:
        raise bad_request(f"{field_name} must be a valid data URL")
    if ";base64" not in header.lower():
        raise bad_request(f"{field_name} must be base64 encoded")

    mime_type = header.removeprefix("data:").split(";", 1)[0].strip().lower()
    if not mime_type.startswith("image/"):
        raise bad_request(f"{field_name} must be an image data URL")

    try:
        image_data = base64.b64decode(encoded_data, validate=True)
    except (binascii.Error, ValueError) as exc:
        raise bad_request(f"{field_name} base64 decode failed") from exc

    if not image_data:
        raise bad_request(f"{field_name} decoded image is empty")

    file_name = f"image-{index + 1}{_image_suffix_for_mime_type(mime_type)}"
    return image_data, file_name, mime_type


def _normalize_json_edit_images(raw_images: object) -> list[tuple[bytes, str, str]]:
    if raw_images is None:
        raise bad_request("images is required")
    if not isinstance(raw_images, list) or not raw_images:
        raise bad_request("images must be a non-empty array")

    images: list[tuple[bytes, str, str]] = []
    for index, item in enumerate(raw_images):
        if not isinstance(item, dict):
            raise bad_request(f"images[{index}] must be an object")
        images.append(_decode_json_edit_image(item.get("image_url"), index))
    return images


async def _normalize_multipart_edit_images(form: FormData) -> list[tuple[bytes, str, str]]:
    uploads = [
        *[item for item in form.getlist("image") if isinstance(item, UploadFile)],
        *[item for item in form.getlist("image[]") if isinstance(item, UploadFile)],
    ]
    if not uploads:
        raise bad_request("image file is required")

    images: list[tuple[bytes, str, str]] = []
    for upload in uploads:
        image_data = await upload.read()
        if not image_data:
            raise bad_request("image file is empty")
        images.append((image_data, upload.filename or "image.png", upload.content_type or "image/png"))
    return images


async def _parse_image_edit_request(request: Request) -> tuple[str, str, int, str, bool, list[tuple[bytes, str, str]]]:
    content_type = str(request.headers.get("content-type") or "").lower()
    if content_type.startswith("application/json"):
        try:
            body = await request.json()
        except (JSONDecodeError, UnicodeDecodeError) as exc:
            raise bad_request("request body must be valid JSON") from exc
        if not isinstance(body, dict):
            raise bad_request("request body must be a JSON object")

        prompt = str(body.get("prompt") or "").strip()
        if not prompt:
            raise bad_request("prompt is required")

        model = str(body.get("model") or "gpt-image-2").strip() or "gpt-image-2"
        n = parse_image_count(body.get("n"))
        response_format = str(body.get("response_format") or "b64_json").strip() or "b64_json"
        stream = _parse_stream_flag(body.get("stream"))
        images = _normalize_json_edit_images(body.get("images"))
        return prompt, model, n, response_format, stream, images

    form = await request.form()
    prompt = str(form.get("prompt") or "").strip()
    if not prompt:
        raise bad_request("prompt is required")

    model = str(form.get("model") or "gpt-image-2").strip() or "gpt-image-2"
    n = parse_image_count(form.get("n"))
    response_format = str(form.get("response_format") or "b64_json").strip() or "b64_json"
    stream = _parse_stream_flag(form.get("stream"))
    images = await _normalize_multipart_edit_images(form)
    return prompt, model, n, response_format, stream, images


def create_router(chatgpt_service: ChatGPTService) -> APIRouter:
    router = APIRouter()

    @router.get("/v1/models")
    async def list_models(authorization: str | None = Header(default=None)):
        require_auth_key(authorization)
        try:
            return await run_in_threadpool(chatgpt_service.list_models)
        except Exception as exc:
            raise HTTPException(status_code=502, detail={"error": str(exc)}) from exc

    @router.post("/v1/images/generations")
    async def generate_images(
            body: ImageGenerationRequest,
            request: Request,
            authorization: str | None = Header(default=None),
    ):
        require_auth_key(authorization)
        base_url = resolve_image_base_url(request)
        if body.stream:
            try:
                await run_in_threadpool(account_service.get_available_access_token)
            except RuntimeError as exc:
                raise_image_quota_error(exc)
            return StreamingResponse(
                sse_json_stream(
                    chatgpt_service.stream_image_generation(
                        body.prompt, body.model, body.n, body.response_format, base_url
                    )
                ),
                media_type="text/event-stream",
            )
        try:
            return await run_in_threadpool(
                chatgpt_service.generate_api_images,
                body.prompt,
                body.model,
                body.n,
                "/v1/images/generations",
                body.response_format,
                base_url,
            )
        except ImageGenerationError as exc:
            raise_image_quota_error(exc)

    @router.post("/v1/images/edits")
    async def edit_images(
            request: Request,
            authorization: str | None = Header(default=None),
    ):
        require_auth_key(authorization)
        prompt, model, n, response_format, stream, images = await _parse_image_edit_request(request)
        base_url = resolve_image_base_url(request)
        if stream:
            if not account_service.has_available_account():
                raise_image_quota_error(RuntimeError("no available image quota"))
            return StreamingResponse(
                sse_json_stream(chatgpt_service.stream_image_edit(prompt, images, model, n, response_format, base_url)),
                media_type="text/event-stream",
            )
        try:
            return await run_in_threadpool(
                chatgpt_service.edit_api_images,
                prompt,
                images,
                model,
                n,
                "/v1/images/edits",
                response_format,
                base_url,
            )
        except ImageGenerationError as exc:
            raise_image_quota_error(exc)

    @router.post("/v1/chat/completions")
    async def create_chat_completion(body: ChatCompletionRequest, authorization: str | None = Header(default=None)):
        require_auth_key(authorization)
        payload = body.model_dump(mode="python")
        if bool(payload.get("stream")):
            if is_image_chat_request(payload):
                try:
                    await run_in_threadpool(account_service.get_available_access_token)
                except RuntimeError as exc:
                    raise_image_quota_error(exc)
            return StreamingResponse(
                sse_json_stream(chatgpt_service.stream_chat_completion(payload)),
                media_type="text/event-stream",
            )
        return await run_in_threadpool(chatgpt_service.create_chat_completion, payload)

    @router.post("/v1/responses")
    async def create_response(body: ResponseCreateRequest, authorization: str | None = Header(default=None)):
        require_auth_key(authorization)
        payload = body.model_dump(mode="python")
        if bool(payload.get("stream")):
            return StreamingResponse(
                sse_json_stream(chatgpt_service.stream_response(payload)),
                media_type="text/event-stream",
            )
        return await run_in_threadpool(chatgpt_service.create_response, payload)

    @router.get("/api/image-history")
    async def get_image_history(authorization: str | None = Header(default=None)):
        require_auth_key(authorization)
        return {"items": image_history_service.list_records()}

    @router.get("/api/image-history/{record_id}/images/{image_id}")
    async def get_image_history_image(
        record_id: str,
        image_id: str,
        authorization: str | None = Header(default=None),
    ):
        require_auth_key(authorization)
        image_entry = image_history_service.get_image_entry(record_id, image_id)
        if image_entry is None:
            raise HTTPException(status_code=404, detail={"error": "image not found"})

        image_meta, image_path = image_entry
        return FileResponse(
            image_path,
            media_type=str(image_meta.get("mime_type") or "image/png"),
            filename=image_path.name,
        )

    @router.post("/api/image-history/delete")
    async def delete_image_history_images(
        body: ImageHistoryDeleteRequest,
        authorization: str | None = Header(default=None),
    ):
        require_auth_key(authorization)
        delete_items = body.model_dump(mode="python").get("items") or []
        if not delete_items:
            raise HTTPException(status_code=404, detail={"error": "images not found"})

        result = image_history_service.delete_images(delete_items)
        if int(result.get("deleted_images") or 0) <= 0:
            raise HTTPException(status_code=404, detail={"error": "images not found"})

        return result

    return router
