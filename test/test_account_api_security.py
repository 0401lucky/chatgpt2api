import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch
import sys


ROOT_DIR = Path(__file__).resolve().parents[1]
if str(ROOT_DIR) not in sys.path:
    sys.path.insert(0, str(ROOT_DIR))

from fastapi.testclient import TestClient

import services.api as api_module
from services.account_service import AccountService


class _FakeThread:
    def join(self, timeout: float | None = None) -> None:
        return None


class AccountApiSecurityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.service = AccountService(Path(self.temp_dir.name) / "accounts.json")
        self.service.add_accounts(
            [
                "token-alpha-1234567890",
                "token-beta-1234567890",
            ]
        )
        self.alpha_token, self.beta_token = self.service.list_tokens()
        self.service.update_account(
            self.alpha_token,
            {
                "email": "alpha@example.com",
                "quota": 3,
                "status": "正常",
                "type": "Plus",
            },
        )
        self.service.update_account(
            self.beta_token,
            {
                "email": "beta@example.com",
                "quota": 2,
                "status": "正常",
                "type": "Free",
            },
        )
        self.headers = {"Authorization": f"Bearer {api_module.config.auth_key}"}

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _build_client(self):
        return patch.object(api_module, "account_service", self.service), patch.object(
            api_module,
            "start_limited_account_watcher",
            return_value=_FakeThread(),
        )

    def test_account_list_hides_access_token(self) -> None:
        with self._build_client()[0], self._build_client()[1]:
            with TestClient(api_module.create_app()) as client:
                response = client.get("/api/accounts", headers=self.headers)

        self.assertEqual(response.status_code, 200)
        items = response.json()["items"]
        self.assertEqual(len(items), 2)
        self.assertNotIn("access_token", items[0])
        self.assertIn("token_preview", items[0])

    def test_update_account_uses_account_id(self) -> None:
        account_id = self.service.list_accounts()[0]["id"]

        with self._build_client()[0], self._build_client()[1]:
            with TestClient(api_module.create_app()) as client:
                response = client.post(
                    "/api/accounts/update",
                    headers=self.headers,
                    json={"account_id": account_id, "status": "禁用"},
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["item"]["status"], "禁用")

    def test_delete_accounts_uses_account_ids(self) -> None:
        accounts = self.service.list_accounts()
        target_id = accounts[0]["id"]

        with self._build_client()[0], self._build_client()[1]:
            with TestClient(api_module.create_app()) as client:
                response = client.request(
                    "DELETE",
                    "/api/accounts",
                    headers=self.headers,
                    json={"account_ids": [target_id]},
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.json()["items"]), 1)

    def test_refresh_accounts_uses_selected_account_ids(self) -> None:
        selected = self.service.list_accounts()[0]
        refreshed_tokens: list[str] = []

        def fake_fetch_remote_info(self, access_token: str):
            refreshed_tokens.append(access_token)
            current = self.get_account(access_token) or {}
            return {
                "email": current.get("email"),
                "user_id": current.get("user_id"),
                "type": current.get("type"),
                "quota": current.get("quota"),
                "limits_progress": current.get("limits_progress") or [],
                "default_model_slug": current.get("default_model_slug"),
                "restore_at": current.get("restore_at"),
                "status": current.get("status"),
            }

        self.service.fetch_remote_info = types.MethodType(fake_fetch_remote_info, self.service)

        with self._build_client()[0], self._build_client()[1]:
            with TestClient(api_module.create_app()) as client:
                response = client.post(
                    "/api/accounts/refresh",
                    headers=self.headers,
                    json={"account_ids": [selected["id"]]},
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(refreshed_tokens, [self.alpha_token])

    def test_refresh_errors_do_not_expose_access_token(self) -> None:
        selected = self.service.list_accounts()[0]

        def fake_fetch_remote_info(self, access_token: str):
            raise RuntimeError(f"refresh failed for {access_token}")

        self.service.fetch_remote_info = types.MethodType(fake_fetch_remote_info, self.service)

        with self._build_client()[0], self._build_client()[1]:
            with TestClient(api_module.create_app()) as client:
                response = client.post(
                    "/api/accounts/refresh",
                    headers=self.headers,
                    json={"account_ids": [selected["id"]]},
                )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(len(response.json()["errors"]), 1)
        self.assertNotIn("access_token", response.json()["errors"][0])


if __name__ == "__main__":
    unittest.main()
