"""Integration test against a running example crawler (skipped unless EXAMPLE_URL is set).

    EXAMPLE_URL=http://localhost:18000 python -m pytest tests/test_integration.py
"""

from __future__ import annotations

import os

import httpx
import pytest

EXAMPLE_URL = os.environ.get("EXAMPLE_URL", "").rstrip("/")

pytestmark = pytest.mark.skipif(not EXAMPLE_URL, reason="EXAMPLE_URL is not set")


@pytest.fixture
def http() -> httpx.Client:
    with httpx.Client(base_url=EXAMPLE_URL, timeout=15.0) as client:
        yield client


def test_healthz(http: httpx.Client) -> None:
    res = http.get("/healthz")
    assert res.status_code == 200
    assert res.json()["ok"] is True


def test_search_and_item_through_spinneret(http: httpx.Client) -> None:
    for path, params, group in (("/crawl/search", {"q": "integration"}, "search"), ("/crawl/item/42", None, "detail")):
        res = http.get(path, params=params)
        assert res.status_code == 200, res.text
        body = res.json()
        assert body["status"] == 200
        assert body["ok"] is True
        assert body["identity_id"].startswith("idt_")
        assert body["endpoint_group"] == group
        assert body["data"]["ok"] is True


def test_config(http: httpx.Client) -> None:
    res = http.get("/config")
    assert res.status_code == 200, res.text
    body = res.json()
    assert body["version"] >= 1
    assert body["content"]["search_page_size"] == 10
