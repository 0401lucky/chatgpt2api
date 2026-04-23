from __future__ import annotations

from typing import Iterable

from fastapi import HTTPException

from services.account_service import AccountService
from services.image_history_service import ImageHistoryService, image_history_service
from services.image_service import ImageGenerationError, edit_image_result, generate_image_result, is_token_invalid_error
from services.usage import build_image_usage
from services.utils import (
    build_chat_image_completion,
    extract_chat_image,
    extract_chat_prompt,
    extract_image_from_message_content,
    extract_response_prompt,
    has_response_image_generation_tool,
    is_image_chat_request,
    parse_image_count,
)


def _extract_response_image(input_value: object) -> tuple[bytes, str] | None:
    if isinstance(input_value, dict):
        return extract_image_from_message_content(input_value.get("content"))
    if not isinstance(input_value, list):
        return None
    for item in reversed(input_value):
        if isinstance(item, dict):
            if str(item.get("type") or "").strip() == "input_image":
                import base64 as b64
                image_url = str(item.get("image_url") or "")
                if image_url.startswith("data:"):
                    header, _, data = image_url.partition(",")
                    mime = header.split(";")[0].removeprefix("data:")
                    return b64.b64decode(data), mime or "image/png"
            content = item.get("content")
            if content:
                result = extract_image_from_message_content(content)
                if result:
                    return result
    return None


class ChatGPTService:
    def __init__(
        self,
        account_service: AccountService,
        history_service: ImageHistoryService | None = None,
    ):
        self.account_service = account_service
        self.history_service = history_service or image_history_service

    @staticmethod
    def _attach_usage(image_result: dict[str, object], prompt: str) -> dict[str, object]:
        image_items = image_result.get("data") if isinstance(image_result.get("data"), list) else []
        return {
            **image_result,
            "usage": build_image_usage(prompt, len(image_items)),
        }

    def _persist_history(
        self,
        *,
        source_endpoint: str,
        mode: str,
        model: str,
        prompt: str,
        image_result: dict[str, object],
    ) -> None:
        image_items = image_result.get("data") if isinstance(image_result.get("data"), list) else []
        usage = image_result.get("usage") if isinstance(image_result.get("usage"), dict) else {}
        if not image_items:
            return
        self.history_service.save_record(
            source_endpoint=source_endpoint,
            mode=mode,
            model=model,
            prompt=prompt,
            image_items=image_items,
            usage=usage,
        )

    def generate_api_images(
        self,
        prompt: str,
        model: str,
        n: int,
        source_endpoint: str,
        response_format: str = "b64_json",
        base_url: str | None = None,
    ) -> dict[str, object]:
        image_result = self._attach_usage(
            self.generate_with_pool(prompt, model, n, response_format=response_format, base_url=base_url),
            prompt,
        )
        self._persist_history(
            source_endpoint=source_endpoint,
            mode="generate",
            model=model,
            prompt=prompt,
            image_result=image_result,
        )
        return image_result

    def generate_with_pool(
        self,
        prompt: str,
        model: str,
        n: int,
        response_format: str = "b64_json",
        base_url: str | None = None,
    ) -> dict[str, object]:
        created = None
        image_items: list[dict[str, object]] = []

        for index in range(1, n + 1):
            while True:
                try:
                    request_token = self.account_service.get_available_access_token()
                except RuntimeError as exc:
                    print(f"[image-generate] stop index={index}/{n} error={exc}")
                    break

                print(f"[image-generate] start pooled token={request_token[:12]}... model={model} index={index}/{n}")
                try:
                    result = generate_image_result(request_token, prompt, model, response_format, base_url)
                    account = self.account_service.mark_image_result(request_token, success=True)
                    if created is None:
                        created = result.get("created")
                    data = result.get("data")
                    if isinstance(data, list):
                        image_items.extend(item for item in data if isinstance(item, dict))
                    print(
                        f"[image-generate] success pooled token={request_token[:12]}... "
                        f"quota={account.get('quota') if account else 'unknown'} status={account.get('status') if account else 'unknown'}"
                    )
                    break
                except ImageGenerationError as exc:
                    account = self.account_service.mark_image_result(request_token, success=False)
                    message = str(exc)
                    print(
                        f"[image-generate] fail pooled token={request_token[:12]}... "
                        f"error={message} quota={account.get('quota') if account else 'unknown'} status={account.get('status') if account else 'unknown'}"
                    )
                    if is_token_invalid_error(message):
                        self.account_service.remove_token(request_token)
                        print(f"[image-generate] remove invalid token={request_token[:12]}...")
                        continue
                    break

        if not image_items:
            raise ImageGenerationError("image generation failed")

        return {
            "created": created,
            "data": image_items,
        }

    def edit_api_images(
        self,
        prompt: str,
        images: Iterable[tuple[bytes, str, str]],
        model: str,
        n: int,
        source_endpoint: str,
        response_format: str = "b64_json",
        base_url: str | None = None,
    ) -> dict[str, object]:
        image_result = self._attach_usage(
            self.edit_with_pool(
                prompt,
                images,
                model,
                n,
                response_format=response_format,
                base_url=base_url,
            ),
            prompt,
        )
        self._persist_history(
            source_endpoint=source_endpoint,
            mode="edit",
            model=model,
            prompt=prompt,
            image_result=image_result,
        )
        return image_result

    def edit_with_pool(
        self,
        prompt: str,
        images: Iterable[tuple[bytes, str, str]],
        model: str,
        n: int,
        response_format: str = "b64_json",
        base_url: str | None = None,
    ) -> dict[str, object]:
        created = None
        image_items: list[dict[str, object]] = []
        normalized_images = list(images)
        if not normalized_images:
            raise ImageGenerationError("image is required")

        for index in range(1, n + 1):
            while True:
                try:
                    request_token = self.account_service.get_available_access_token()
                except RuntimeError as exc:
                    print(f"[image-edit] stop index={index}/{n} error={exc}")
                    break

                print(
                    f"[image-edit] start pooled token={request_token[:12]}... "
                    f"model={model} index={index}/{n} images={len(normalized_images)}"
                )
                try:
                    result = edit_image_result(request_token, prompt, normalized_images, model, response_format, base_url)
                    account = self.account_service.mark_image_result(request_token, success=True)
                    if created is None:
                        created = result.get("created")
                    data = result.get("data")
                    if isinstance(data, list):
                        image_items.extend(item for item in data if isinstance(item, dict))
                    print(
                        f"[image-edit] success pooled token={request_token[:12]}... "
                        f"quota={account.get('quota') if account else 'unknown'} status={account.get('status') if account else 'unknown'}"
                    )
                    break
                except ImageGenerationError as exc:
                    account = self.account_service.mark_image_result(request_token, success=False)
                    message = str(exc)
                    print(
                        f"[image-edit] fail pooled token={request_token[:12]}... "
                        f"error={message} quota={account.get('quota') if account else 'unknown'} status={account.get('status') if account else 'unknown'}"
                    )
                    if is_token_invalid_error(message):
                        self.account_service.remove_token(request_token)
                        print(f"[image-edit] remove invalid token={request_token[:12]}...")
                        continue
                    break

        if not image_items:
            raise ImageGenerationError("image edit failed")

        return {
            "created": created,
            "data": image_items,
        }

    def create_image_completion(self, body: dict[str, object]) -> dict[str, object]:
        if not is_image_chat_request(body):
            raise HTTPException(
                status_code=400,
                detail={"error": "only image generation requests are supported on this endpoint"},
            )

        if bool(body.get("stream")):
            raise HTTPException(status_code=400, detail={"error": "stream is not supported for image generation"})

        model = str(body.get("model") or "gpt-image-1").strip() or "gpt-image-1"
        n = parse_image_count(body.get("n"))
        prompt = extract_chat_prompt(body)
        if not prompt:
            raise HTTPException(status_code=400, detail={"error": "prompt is required"})

        image_info = extract_chat_image(body)
        try:
            if image_info:
                image_data, mime_type = image_info
                image_result = self.edit_api_images(
                    prompt,
                    [(image_data, "image.png", mime_type)],
                    model,
                    n,
                    "/v1/chat/completions",
                )
            else:
                image_result = self.generate_api_images(prompt, model, n, "/v1/chat/completions")
        except ImageGenerationError as exc:
            raise HTTPException(status_code=502, detail={"error": str(exc)}) from exc

        return build_chat_image_completion(model, prompt, image_result)

    def create_response(self, body: dict[str, object]) -> dict[str, object]:
        if bool(body.get("stream")):
            raise HTTPException(status_code=400, detail={"error": "stream is not supported"})

        if not has_response_image_generation_tool(body):
            raise HTTPException(
                status_code=400,
                detail={"error": "only image_generation tool requests are supported on this endpoint"},
            )

        prompt = extract_response_prompt(body.get("input"))
        if not prompt:
            raise HTTPException(status_code=400, detail={"error": "input text is required"})

        image_info = _extract_response_image(body.get("input"))
        model = str(body.get("model") or "gpt-5").strip() or "gpt-5"
        try:
            if image_info:
                image_data, mime_type = image_info
                image_result = self.edit_api_images(
                    prompt,
                    [(image_data, "image.png", mime_type)],
                    model,
                    1,
                    "/v1/responses",
                )
            else:
                image_result = self.generate_api_images(prompt, model, 1, "/v1/responses")
        except ImageGenerationError as exc:
            raise HTTPException(status_code=502, detail={"error": str(exc)}) from exc

        image_items = image_result.get("data") if isinstance(image_result.get("data"), list) else []
        output = []
        for item in image_items:
            if not isinstance(item, dict):
                continue
            b64_json = str(item.get("b64_json") or "").strip()
            if not b64_json:
                continue
            output.append(
                {
                    "id": f"ig_{len(output) + 1}",
                    "type": "image_generation_call",
                    "status": "completed",
                    "result": b64_json,
                    "revised_prompt": str(item.get("revised_prompt") or prompt).strip(),
                }
            )

        if not output:
            raise HTTPException(status_code=502, detail={"error": "image generation failed"})

        created = int(image_result.get("created") or 0)
        usage = image_result.get("usage") if isinstance(image_result.get("usage"), dict) else {}
        return {
            "id": f"resp_{created}",
            "object": "response",
            "created_at": created,
            "status": "completed",
            "error": None,
            "incomplete_details": None,
            "model": model,
            "output": output,
            "parallel_tool_calls": False,
            "usage": {
                "input_tokens": int(usage.get("input_tokens") or 0),
                "output_tokens": int(usage.get("output_tokens") or 0),
                "total_tokens": int(usage.get("total_tokens") or 0),
                "input_tokens_details": usage.get("input_tokens_details") or {
                    "text_tokens": 0,
                    "image_tokens": 0,
                },
                "output_tokens_details": usage.get("output_tokens_details") or {
                    "text_tokens": 0,
                    "image_tokens": 0,
                },
            },
        }
