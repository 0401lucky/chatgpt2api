import unittest
from unittest.mock import patch
from pathlib import Path
import sys


ROOT_DIR = Path(__file__).resolve().parents[1]
if str(ROOT_DIR) not in sys.path:
    sys.path.insert(0, str(ROOT_DIR))

from services.chatgpt_service import ChatGPTService
from services.usage import build_chat_usage, build_image_usage
from utils.helper import build_chat_image_completion, is_image_chat_request


PNG_B64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aWbQAAAAASUVORK5CYII="


class DummyAccountService:
    pass


class DummyHistoryService:
    def __init__(self) -> None:
        self.records: list[dict] = []

    def save_record(self, **kwargs):
        self.records.append(kwargs)
        return {"id": "history-1", **kwargs}


class ImageUsageTests(unittest.TestCase):
    def test_gpt_image_1_is_still_treated_as_image_chat_model(self) -> None:
        self.assertTrue(is_image_chat_request({"model": "gpt-image-1"}))
        self.assertTrue(is_image_chat_request({"model": "gpt-image-2"}))
        self.assertTrue(is_image_chat_request({"model": "codex-gpt-image-2"}))

    def test_build_image_usage_estimates_prompt_and_output_tokens(self) -> None:
        usage = build_image_usage("生成一只猫", image_count=2)

        self.assertGreater(usage["input_tokens"], 0)
        self.assertEqual(usage["output_tokens"], 2112)
        self.assertEqual(usage["total_tokens"], usage["input_tokens"] + usage["output_tokens"])
        self.assertEqual(usage["input_tokens_details"]["image_tokens"], 0)

    def test_build_chat_usage_maps_prompt_and_completion_tokens(self) -> None:
        usage = build_chat_usage("生成一只猫", image_count=1)

        self.assertGreater(usage["prompt_tokens"], 0)
        self.assertEqual(usage["completion_tokens"], 1056)
        self.assertEqual(usage["total_tokens"], usage["prompt_tokens"] + usage["completion_tokens"])

    def test_build_chat_image_completion_uses_usage_from_image_result(self) -> None:
        payload = build_chat_image_completion(
            "gpt-image-1",
            "生成一只猫",
            {
                "created": 123,
                "data": [{"b64_json": PNG_B64, "revised_prompt": "生成一只猫"}],
                "usage": {"input_tokens": 10, "output_tokens": 1056, "total_tokens": 1066},
            },
        )

        self.assertEqual(payload["usage"]["prompt_tokens"], 10)
        self.assertEqual(payload["usage"]["completion_tokens"], 1056)
        self.assertEqual(payload["usage"]["total_tokens"], 1066)

    def test_create_response_includes_usage_and_persists_history(self) -> None:
        history_service = DummyHistoryService()
        service = ChatGPTService(DummyAccountService(), history_service)

        with patch.object(
            ChatGPTService,
            "generate_with_pool",
            return_value={
                "created": 123,
                "data": [{"b64_json": PNG_B64, "revised_prompt": "生成一只猫"}],
            },
        ):
            payload = service.create_response(
                {
                    "model": "gpt-5",
                    "input": "生成一只猫",
                    "tools": [{"type": "image_generation"}],
                }
            )

        self.assertIn("usage", payload)
        self.assertGreater(payload["usage"]["input_tokens"], 0)
        self.assertEqual(payload["usage"]["output_tokens"], 1056)
        self.assertEqual(len(history_service.records), 1)


if __name__ == "__main__":
    unittest.main()
