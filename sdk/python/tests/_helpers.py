"""Constants and payload builders shared by the tests."""

from __future__ import annotations

import copy
from typing import Any

from spinneret import Backoff, RetryPolicy

BASE_URL = "http://spinneret.test"
TOKEN = "spn_test_token_value"
NODE = "node-1"

LEASE_PATH = "/spinneret.v1.LeaseService"
REPORT_PATH = "/spinneret.v1.ReportService/Report"
CONFIG_PATH = "/spinneret.v1.ConfigService"
SECRET_PATH = "/spinneret.v1.SecretService/GetSecret"

FAST_RETRY = RetryPolicy(max_retries=2, backoff=Backoff(initial=0.001, maximum=0.002))

_ACQUIRE_RESPONSE: dict[str, Any] = {
    "lease": {
        "lease_id": "lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f",
        "identity_id": "idt_1",
        "identity_type": "web_cookie",
        "endpoint_group": "search",
        "expires_at": "2026-09-16T08:32:10Z",
        "sticky": False,
        "probe": False,
    },
    "credential": {
        "cookies": {"sessionid": "a1b2c3"},
        "cookie_header": "sessionid=a1b2c3",
        "headers": {"User-Agent": "Mozilla/5.0"},
        "query": {"app_id": "1001"},
        "json": None,
        "values": {"csrf_token": "x9y8z7"},
    },
    "proxy": {
        "proxy_id": "pxy_1",
        "url": "http://user:pass@203.0.113.10:8000",
        "kind": "residential",
        "region": "US",
    },
    "hints": {"renew_before_ms": 30000},
}


def acquire_payload(**lease_overrides: Any) -> dict[str, Any]:
    """Return a fresh acquire response JSON with optional lease field overrides."""
    payload = copy.deepcopy(_ACQUIRE_RESPONSE)
    payload["lease"].update(lease_overrides)
    return payload


def config_item(
    group: str = "crawler",
    key: str = "search.json",
    version: int = 1,
    content: str = "{}",
    *,
    has_secret_refs: bool = False,
) -> dict[str, Any]:
    """Return a config item JSON as the server emits it (unpopulated fields included)."""
    return {
        "namespace": "prod",
        "group": group,
        "key": key,
        "format": "json",
        "version": str(version),
        "content": content,
        "updated_at": "2026-09-16T08:30:00Z",
        "has_secret_refs": has_secret_refs,
    }


#: Content of an item whose ``${secret:signing/api_key}`` reference the server resolved.
RESOLVED_SECRET_CONTENT = '{"api_key": "sk-resolved-signer-value"}'
RESOLVED_SECRET_VALUE = "sk-resolved-signer-value"


def secret_config_item(
    group: str = "crawler", key: str = "search.json", version: int = 1
) -> dict[str, Any]:
    """Return a config item JSON whose secret references were resolved by the server."""
    return config_item(group, key, version, RESOLVED_SECRET_CONTENT, has_secret_refs=True)
