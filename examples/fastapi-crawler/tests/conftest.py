"""Fixtures: the app wired to a respx-mocked Spinneret API and target site (no network access)."""

from __future__ import annotations

import asyncio
import json
from pathlib import Path
from typing import Any, Dict, Iterator, List

import httpx
import pytest
import respx
import spinneret
from fastapi.testclient import TestClient

from app.main import create_app
from app.settings import Settings

SPINNERET_URL = "http://spinneret.test"
TARGET_URL = "http://target.test"
LEASE = "/spinneret.v1.LeaseService"
CONFIG = "/spinneret.v1.ConfigService"
REPORT = "/spinneret.v1.ReportService/Report"


def acquire_payload(identity_id: str = "idt_1", proxy: bool = True) -> Dict[str, Any]:
    return {
        "lease": {
            "lease_id": "lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f",
            "identity_id": identity_id,
            "identity_type": "example_web_cookie",
            "endpoint_group": "search",
            "expires_at": "2030-01-01T00:00:00Z",
            "sticky": False,
            "probe": False,
        },
        "credential": {
            "cookies": {"sessionid": "example-01"},
            "cookie_header": "",
            "headers": {"User-Agent": "ExampleCrawler/01"},
            "query": {},
            "json": None,
            "values": {},
        },
        "proxy": {"proxy_id": "pxy_1", "url": "http://expx1:secret@proxy.test:9091", "kind": "datacenter", "region": ""}
        if proxy
        else None,
        "hints": {"renew_before_ms": 15000},
    }


def connect_error(status: int, code: str, reason: str, retry_after_ms: int = 0) -> httpx.Response:
    headers = {"Spinneret-Reason": reason}
    if retry_after_ms:
        headers["Spinneret-Retry-After-Ms"] = str(retry_after_ms)
    return httpx.Response(status, json={"code": code, "message": reason}, headers=headers)


async def _slow_empty_watch(request: httpx.Request) -> httpx.Response:
    # A long poll that times out without changes (kept short for the tests).
    await asyncio.sleep(0.05)
    return httpx.Response(200, json={"items": []})


@pytest.fixture
def router() -> Iterator[respx.MockRouter]:
    with respx.mock(assert_all_mocked=True, assert_all_called=False) as mock:
        mock.post(f"{SPINNERET_URL}{CONFIG}/BatchGetConfig", name="batch_get").mock(
            return_value=httpx.Response(
                200,
                json={
                    "items": [
                        {
                            "namespace": "default",
                            "group": "crawler",
                            "key": "example.json",
                            "format": "json",
                            "version": 3,
                            "content": json.dumps({"search_page_size": 10}),
                            "updated_at": "2026-09-17T00:00:00Z",
                            "has_secret_refs": False,
                        }
                    ],
                    "missing": [],
                },
            )
        )
        mock.post(f"{SPINNERET_URL}{CONFIG}/WatchConfig", name="watch").mock(side_effect=_slow_empty_watch)
        mock.post(f"{SPINNERET_URL}{LEASE}/Acquire", name="acquire").mock(
            return_value=httpx.Response(200, json=acquire_payload())
        )
        mock.post(f"{SPINNERET_URL}{LEASE}/Release", name="release").mock(
            return_value=httpx.Response(200, json={"released": True})
        )
        mock.post(f"{SPINNERET_URL}{REPORT}", name="report").mock(return_value=httpx.Response(200, json={"accepted": 1}))
        yield mock


@pytest.fixture
def app_client(router: respx.MockRouter, tmp_path: Path) -> Iterator[TestClient]:
    settings = Settings(mock_target_url=TARGET_URL, lease_wait_ms=100, request_timeout=5.0)

    def factory() -> spinneret.AsyncClient:
        return spinneret.AsyncClient(
            SPINNERET_URL,
            "spn_test_token",
            node="example-test",
            cache_dir=tmp_path,
            retry=spinneret.NO_RETRY,
            reporter_options=spinneret.ReporterOptions(flush_interval=0.01),
        )

    with TestClient(create_app(settings, factory)) as client:
        yield client


def sent_reports(router: respx.MockRouter) -> List[Dict[str, Any]]:
    """Reports delivered to the mocked ReportService (read after the app shut down)."""
    reports: List[Dict[str, Any]] = []
    for call in router.routes["report"].calls:
        reports.extend(json.loads(call.request.content)["reports"])
    return reports
