from __future__ import annotations

import os
import unittest


RUN_INTEGRATION_TESTS = str(os.getenv("CHATGPT2API_RUN_INTEGRATION_TESTS") or "").strip().lower() in {
    "1",
    "true",
    "yes",
    "on",
}

requires_integration = unittest.skipUnless(
    RUN_INTEGRATION_TESTS,
    "联调测试默认跳过；如需运行请设置 CHATGPT2API_RUN_INTEGRATION_TESTS=1。",
)
