from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path
import shutil
import uuid

os.environ.setdefault("CHATGPT2API_AUTH_KEY", "test-auth")
TEST_TMP_DIR = Path(__file__).resolve().parents[1]
TEST_TMP_DIR.mkdir(parents=True, exist_ok=True)

from services.account_service import AccountService
from utils.helper import anonymize_token


class AccountCapabilityTests(unittest.TestCase):
    def _make_temp_dir(self) -> Path:
        path = TEST_TMP_DIR / f".tmp-account-capability-{uuid.uuid4().hex}"
        path.mkdir(parents=True, exist_ok=True)
        self.addCleanup(lambda: shutil.rmtree(path, ignore_errors=True))
        return path

    def test_unknown_quota_accounts_are_available_only_when_not_throttled(self) -> None:
        self.assertFalse(
            AccountService._is_image_account_available(
                {"status": "限流", "image_quota_unknown": True, "quota": 0}
            )
        )
        self.assertTrue(
            AccountService._is_image_account_available(
                {"status": "正常", "image_quota_unknown": True, "quota": 0}
            )
        )

    def test_prolite_variants_are_normalized(self) -> None:
        tmp_dir = self._make_temp_dir()
        service = AccountService(tmp_dir / "accounts.json")
        self.assertEqual(service._normalize_account_type("prolite"), "ProLite")
        self.assertEqual(service._normalize_account_type("pro_lite"), "ProLite")

    def test_search_account_type_ignores_unrelated_scalar_values(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = AccountService(Path(tmp_dir) / "accounts.json")
            self.assertIsNone(
                service._search_account_type(
                    {
                        "amr": ["pwd", "otp", "mfa"],
                        "chatgpt_compute_residency": "no_constraint",
                        "chatgpt_data_residency": "no_constraint",
                        "user_id": "user-I52GFfLGFM0dokFk2dBiKEBn",
                    }
                )
            )

    def test_mark_image_result_does_not_consume_unknown_quota(self) -> None:
        tmp_dir = self._make_temp_dir()
        service = AccountService(tmp_dir / "accounts.json")
        service.add_accounts(["token-1"])
        service.update_account(
            "token-1",
            {
                "status": "正常",
                "quota": 0,
                "image_quota_unknown": True,
            },
        )

        updated = service.mark_image_result("token-1", success=True)

        self.assertIsNotNone(updated)
        self.assertEqual(updated["quota"], 0)
        self.assertEqual(updated["status"], "正常")
        self.assertTrue(updated["image_quota_unknown"])


class TokenLogTests(unittest.TestCase):
    def test_anonymize_token_hides_raw_value(self) -> None:
        token = "super-secret-token"
        token_ref = anonymize_token(token)

        self.assertTrue(token_ref.startswith("token:"))
        self.assertNotIn(token, token_ref)


if __name__ == "__main__":
    unittest.main()
