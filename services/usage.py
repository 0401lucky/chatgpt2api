from __future__ import annotations

import math
from functools import lru_cache


IMAGE_OUTPUT_TOKENS_PER_IMAGE = 1056


@lru_cache(maxsize=1)
def _get_encoder():
    import tiktoken

    return tiktoken.get_encoding("o200k_base")


def estimate_text_tokens(text: str) -> int:
    normalized = str(text or "").strip()
    if not normalized:
        return 0
    try:
        return len(_get_encoder().encode(normalized))
    except Exception:
        return max(1, math.ceil(len(normalized) / 4))


def estimate_image_usage(prompt: str, image_count: int) -> dict[str, object]:
    normalized_count = max(0, int(image_count or 0))
    input_tokens = estimate_text_tokens(prompt)
    output_tokens = normalized_count * IMAGE_OUTPUT_TOKENS_PER_IMAGE
    total_tokens = input_tokens + output_tokens
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "total_tokens": total_tokens,
        "input_tokens_details": {
            "text_tokens": input_tokens,
            "image_tokens": 0,
        },
        "output_tokens_details": {
            "text_tokens": 0,
            "image_tokens": output_tokens,
        },
        "prompt_tokens": input_tokens,
        "completion_tokens": output_tokens,
    }


def build_image_usage(prompt: str, image_count: int) -> dict[str, object]:
    usage = estimate_image_usage(prompt, image_count)
    return {
        "input_tokens": usage["input_tokens"],
        "output_tokens": usage["output_tokens"],
        "total_tokens": usage["total_tokens"],
        "input_tokens_details": usage["input_tokens_details"],
        "output_tokens_details": usage["output_tokens_details"],
    }


def build_chat_usage(prompt: str, image_count: int) -> dict[str, int]:
    usage = estimate_image_usage(prompt, image_count)
    return {
        "prompt_tokens": int(usage["prompt_tokens"]),
        "completion_tokens": int(usage["completion_tokens"]),
        "total_tokens": int(usage["total_tokens"]),
    }
