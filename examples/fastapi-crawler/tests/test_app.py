"""Unit tests of the example crawler with a respx-mocked Spinneret API and target site."""

from __future__ import annotations

import json

import httpx
import pytest
import respx
from fastapi.testclient import TestClient

from app.crawler import markers_from
from app.settings import Settings

from .conftest import TARGET_URL, connect_error, sent_reports


def target_ok(router: respx.MockRouter, path: str = "/site/search") -> respx.Route:
    return router.get(f"{TARGET_URL}{path}", name="target").mock(
        return_value=httpx.Response(200, json={"ok": True, "items": [{"id": 1, "title": "item 1"}]})
    )


def test_healthz(app_client: TestClient) -> None:
    res = app_client.get("/healthz")
    assert res.status_code == 200
    assert res.json() == {"ok": True, "config_watcher_running": True}


def test_search_leases_requests_through_proxy_and_reports(router: respx.MockRouter, app_client: TestClient) -> None:
    target = target_ok(router)
    res = app_client.get("/crawl/search", params={"q": "shoes"})
    assert res.status_code == 200
    body = res.json()
    assert body["ok"] is True
    assert body["identity_id"] == "idt_1"
    assert body["proxy_id"] == "pxy_1"
    assert body["data"]["items"][0]["title"] == "item 1"

    acquire = json.loads(router.routes["acquire"].calls.last.request.content)
    assert acquire == {"site": "example", "client": "web", "uri": "/site/search", "endpoint_group": "",
                       "session_key": "", "wait_ms": 100}
    sent = target.calls.last.request
    assert sent.url.params["q"] == "shoes"
    assert sent.headers["User-Agent"] == "ExampleCrawler/01"
    assert "sessionid=example-01" in sent.headers["Cookie"]

    app_client.__exit__(None, None, None)  # shut down: flushes the reporter
    reports = sent_reports(router)
    assert len(reports) == 1
    assert reports[0]["release"] is True
    assert reports[0]["http_status"] == 200
    assert reports[0]["uri"] == "/site/search"
    assert reports[0]["markers"] == []
    assert not router.routes["release"].called, "the release rides on the last report"


def test_item_reports_markers_and_business_code(router: respx.MockRouter, app_client: TestClient) -> None:
    router.get(f"{TARGET_URL}/site/item/42").mock(
        return_value=httpx.Response(
            200, text="<html>captcha-page</html>", headers={"X-Mock-Marker": "captcha_page", "X-Mock-Business-Code": "10001"}
        )
    )
    res = app_client.get("/crawl/item/42")
    assert res.status_code == 200
    body = res.json()
    assert body["ok"] is False
    assert body["markers"] == ["captcha_page"]
    assert body["business_code"] == "10001"
    assert body["data"] is None

    app_client.__exit__(None, None, None)
    report = sent_reports(router)[0]
    assert report["markers"] == ["captcha_page"]
    assert report["business_code"] == "10001"
    assert report["uri"] == "/site/item/42"


def test_redirect_is_reported_not_followed(router: respx.MockRouter, app_client: TestClient) -> None:
    router.get(f"{TARGET_URL}/site/search").mock(
        return_value=httpx.Response(302, headers={"Location": "/login", "X-Mock-Marker": "login_redirect"})
    )
    res = app_client.get("/crawl/search", params={"q": "x"})
    assert res.json()["status"] == 302
    app_client.__exit__(None, None, None)
    assert sent_reports(router)[0]["markers"] == ["login_redirect"]


@pytest.mark.parametrize(
    ("status", "code", "reason", "expected"),
    [
        (503, "unavailable", "circuit_open", 503),
        (503, "unavailable", "site_paused", 503),
        (429, "resource_exhausted", "no_identity_available", 429),
        (429, "resource_exhausted", "no_proxy_available", 429),
        (403, "permission_denied", "scope_missing", 502),
    ],
)
def test_lease_errors_map_to_http_answers(
    router: respx.MockRouter, app_client: TestClient, status: int, code: str, reason: str, expected: int
) -> None:
    router.routes["acquire"].mock(return_value=connect_error(status, code, reason, retry_after_ms=4000))
    target = target_ok(router)
    res = app_client.get("/crawl/search", params={"q": "x"})
    assert res.status_code == expected
    assert res.json() == {"ok": False, "error": code, "reason": reason}
    if expected in (429, 503):
        assert res.headers["Retry-After"] == "4"
    assert not target.called, "no target request without a lease"


def test_target_failure_is_reported_with_error_kind(router: respx.MockRouter, app_client: TestClient) -> None:
    router.get(f"{TARGET_URL}/site/search").mock(side_effect=httpx.ConnectTimeout("timed out"))
    res = app_client.get("/crawl/search", params={"q": "x"})
    assert res.status_code == 502
    assert res.json()["error"] == "upstream_error"
    app_client.__exit__(None, None, None)
    report = sent_reports(router)[0]
    assert report["http_status"] == 0
    assert report["error_kind"] == "timeout"
    assert report["release"] is True


def test_config_is_served_from_the_watcher(app_client: TestClient) -> None:
    res = app_client.get("/config")
    assert res.status_code == 200
    assert res.json() == {
        "ok": True,
        "group": "crawler",
        "key": "example.json",
        "version": 3,
        "format": "json",
        "from_snapshot": False,
        "content": {"search_page_size": 10},
    }


def test_config_with_secret_references_is_not_echoed(router: respx.MockRouter, app_client: TestClient) -> None:
    router.routes["batch_get"].mock(
        return_value=httpx.Response(
            200,
            json={"items": [{"group": "crawler", "key": "example.json", "format": "json", "version": 1,
                             "content": '{"api_key":"sk-live"}', "has_secret_refs": True}]},
        )
    )
    with TestClient(app_client.app) as client:
        body = client.get("/config").json()
    assert body["version"] == 1
    assert body["content"] is None


def test_invalid_input_is_rejected(app_client: TestClient) -> None:
    assert app_client.get("/crawl/search").status_code == 422
    assert app_client.get("/crawl/item/bad!id").status_code == 422


def test_markers_from_headers() -> None:
    assert markers_from({}) == []
    assert markers_from({"X-Mock-Marker": "captcha_page"}) == ["captcha_page"]
    assert markers_from({"X-Mock-Marker": " empty_list , captcha_page "}) == ["empty_list", "captcha_page"]


def test_settings_from_env() -> None:
    s = Settings.from_env({"MOCK_TARGET_URL": "http://mock:9090/", "EXAMPLE_LEASE_WAIT_MS": "250"})
    assert s.mock_target_url == "http://mock:9090"
    assert s.lease_wait_ms == 250
    assert s.site == "example"
    assert Settings.from_env({}).mock_target_url == "http://mocktarget:9090"
    with pytest.raises(ValueError, match="MOCK_TARGET_URL"):
        Settings.from_env({"MOCK_TARGET_URL": "mock:9090"})
    with pytest.raises(ValueError, match="EXAMPLE_LEASE_WAIT_MS"):
        Settings.from_env({"EXAMPLE_LEASE_WAIT_MS": "9000"})
    with pytest.raises(ValueError, match="integer"):
        Settings.from_env({"EXAMPLE_REQUEST_TIMEOUT_S": "fast"})
