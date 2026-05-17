from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

os.environ.setdefault("CHATGPT2API_AUTH_KEY", "test-auth")

from services.account_service import AccountService
from services.storage.json_storage import JSONStorageBackend


class OAuthRefreshFlowTests(unittest.TestCase):
    def _new_service(self, tmp_dir: str) -> AccountService:
        return AccountService(JSONStorageBackend(Path(tmp_dir) / "accounts.json"))

    def test_add_accounts_persists_refresh_token_from_dict_entry(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = self._new_service(tmp_dir)
            service.add_accounts(
                [{"access_token": "tok-1", "refresh_token": "refresh-1"}]
            )
            stored = service.get_account("tok-1")
            self.assertIsNotNone(stored)
            self.assertEqual(stored.get("refresh_token"), "refresh-1")

    def test_add_accounts_str_entry_keeps_existing_refresh_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = self._new_service(tmp_dir)
            service.add_accounts(
                [{"access_token": "tok-1", "refresh_token": "refresh-1"}]
            )
            service.add_accounts(["tok-1"])  # str 形式重复添加不应清掉 refresh_token
            stored = service.get_account("tok-1")
            self.assertEqual(stored.get("refresh_token"), "refresh-1")

    def test_fetch_with_oauth_refresh_rotates_token_on_401(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = self._new_service(tmp_dir)
            service.add_accounts(
                [{"access_token": "tok-old", "refresh_token": "refresh-1"}]
            )

            call_counter = {"n": 0}
            remote_info_ok = {
                "email": "u@example.com",
                "user_id": "user-x",
                "type": "Plus",
                "quota": 5,
                "image_quota_unknown": False,
                "limits_progress": [],
                "default_model_slug": "gpt-5",
                "restore_at": None,
                "status": "正常",
            }

            def fake_fetch(token: str) -> dict:
                call_counter["n"] += 1
                if token == "tok-old":
                    raise RuntimeError("/backend-api/me failed: HTTP 401")
                return dict(remote_info_ok)

            def fake_refresh(refresh_token: str) -> dict:
                self.assertEqual(refresh_token, "refresh-1")
                return {"access_token": "tok-new", "refresh_token": "refresh-2"}

            with patch.object(service, "fetch_remote_info", side_effect=fake_fetch):
                with patch.object(service, "_try_refresh_oauth_token", side_effect=fake_refresh):
                    final_token, info, error = service._fetch_with_oauth_refresh("tok-old")

            self.assertEqual(final_token, "tok-new")
            self.assertIsNone(error)
            self.assertEqual(info.get("status"), "正常")
            self.assertEqual(call_counter["n"], 2)
            self.assertIsNone(service.get_account("tok-old"))
            rotated = service.get_account("tok-new")
            self.assertEqual(rotated.get("refresh_token"), "refresh-2")

    def test_refresh_account_state_does_not_mark_abnormal_when_oauth_refresh_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = self._new_service(tmp_dir)
            service.add_accounts(
                [{"access_token": "tok-old", "refresh_token": "refresh-1"}]
            )

            remote_info_ok = {
                "email": "u@example.com",
                "user_id": "user-x",
                "type": "Plus",
                "quota": 5,
                "image_quota_unknown": False,
                "limits_progress": [],
                "default_model_slug": "gpt-5",
                "restore_at": None,
                "status": "正常",
            }

            def fake_fetch(token: str) -> dict:
                if token == "tok-old":
                    raise RuntimeError("/backend-api/me failed: HTTP 401")
                return dict(remote_info_ok)

            with patch.object(service, "fetch_remote_info", side_effect=fake_fetch):
                with patch.object(
                    service,
                    "_try_refresh_oauth_token",
                    return_value={"access_token": "tok-new", "refresh_token": "refresh-2"},
                ):
                    result = service.refresh_account_state("tok-old")

            self.assertIsNotNone(result)
            self.assertEqual(result.get("status"), "正常")
            self.assertEqual(result.get("access_token"), "tok-new")
            self.assertIsNone(service.get_account("tok-old"))

    def test_refresh_account_state_marks_abnormal_when_refresh_token_missing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            service = self._new_service(tmp_dir)
            service.add_accounts(["tok-1"])  # 无 refresh_token

            def fake_fetch(token: str) -> dict:
                raise RuntimeError("/backend-api/me failed: HTTP 401")

            # 强制走"标记异常"分支，避免依赖 config.auto_remove_invalid_accounts
            with patch.object(service, "fetch_remote_info", side_effect=fake_fetch):
                with patch.object(service, "remove_invalid_token", return_value=False):
                    with patch.object(
                        service,
                        "_try_refresh_oauth_token",
                        return_value=None,
                    ):
                        result = service.refresh_account_state("tok-1")

            self.assertIsNotNone(result)
            self.assertEqual(result.get("status"), "异常")
            self.assertEqual(result.get("quota"), 0)


if __name__ == "__main__":
    unittest.main()
