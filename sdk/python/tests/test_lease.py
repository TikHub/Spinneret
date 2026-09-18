from __future__ import annotations

import json
import logging
from collections.abc import Iterator
from datetime import timedelta
from typing import Any

import httpx
import pytest
import respx

import spinneret as sp
from spinneret._lease_core import build_httpx_kwargs, parse_cookie_header, report_uri

from ._helpers import LEASE_PATH, REPORT_PATH, acquire_payload

LEASE_ID = acquire_payload()["lease"]["lease_id"]


@pytest.fixture
def api(router: respx.MockRouter) -> respx.MockRouter:
    router.post(f"{LEASE_PATH}/Acquire", name="acquire").mock(
        return_value=httpx.Response(200, json=acquire_payload())
    )
    router.post(REPORT_PATH, name="report").mock(
        return_value=httpx.Response(200, json={"accepted": 1})
    )
    router.post(f"{LEASE_PATH}/Release", name="release").mock(
        return_value=httpx.Response(200, json={"released": True})
    )
    router.post(f"{LEASE_PATH}/Renew", name="renew").mock(
        return_value=httpx.Response(200, json={"expires_at": "2026-09-16T09:00:00Z"})
    )
    return router


@pytest.fixture
def client(settings: sp.Settings, router: respx.MockRouter) -> Iterator[sp.Client]:
    # Depends on the router so that the client is closed while the mock is still active.
    options = sp.ReporterOptions(flush_interval=0.01)
    client = sp.Client(settings=settings, retry=sp.NO_RETRY, reporter_options=options)
    yield client
    client.close()


def sent_reports(router: respx.MockRouter) -> list[dict[str, Any]]:
    reports: list[dict[str, Any]] = []
    for call in router.routes["report"].calls:
        reports.extend(json.loads(call.request.content)["reports"])
    return reports


def release_bodies(router: respx.MockRouter) -> list[dict[str, Any]]:
    return [json.loads(call.request.content) for call in router.routes["release"].calls]


def connect_error(code: str, reason: str = "", status: int = 400) -> httpx.Response:
    headers = {"Spinneret-Reason": reason} if reason else {}
    return httpx.Response(status, json={"code": code, "message": "release"}, headers=headers)


#: Release failures meaning the lease has already ended: ignored and logged at debug level.
LEASE_ENDED_ERRORS = [
    connect_error("failed_precondition", "lease_released"),
    connect_error("not_found", "lease_unknown", 404),
    connect_error("failed_precondition", "lease_expired"),
]


def test_lease_accessors_and_httpx_kwargs(client: sp.Client, api: respx.MockRouter) -> None:
    lease = client.lease(site="shop", client="web", uri="/api/v1/feed", session_key="s1")
    assert not lease.acquired
    assert "acquired=False" in repr(lease)
    with pytest.raises(RuntimeError):
        _ = lease.lease_id
    with lease:
        assert lease.acquired
        assert lease.lease_id == LEASE_ID
        assert lease.identity_id == "idt_1"
        assert lease.info.endpoint_group == "search"
        assert lease.hints.renew_before_ms == 30000
        assert lease.proxy is not None
        assert lease.response.credential.values["csrf_token"] == "x9y8z7"
        kwargs = lease.httpx_kwargs(headers={"user-agent": "override"}, params={"extra": "1"})
        assert kwargs == {
            "headers": {"user-agent": "override"},
            "cookies": {"sessionid": "a1b2c3"},
            "params": {"app_id": "1001", "extra": "1"},
            "proxy": "http://user:pass@203.0.113.10:8000",
        }
        assert "user:pass" not in repr(lease)
        with pytest.raises(RuntimeError, match="reentrant"):
            lease.__enter__()
    body = json.loads(api.routes["acquire"].calls.last.request.content)
    assert body["session_key"] == "s1"


@pytest.mark.parametrize(
    ("credential", "extra_cookies", "expected_headers", "expected_cookies"),
    [
        ({"cookie_header": "a=1; b=2"}, None, {"Cookie": "a=1; b=2"}, {}),
        ({"cookie_header": "a=1; b=2"}, {"c": "3"}, {}, {"a": "1", "b": "2", "c": "3"}),
        (
            {"cookie_header": "a=1", "headers": {"cookie": "x=9"}},
            None,
            {"cookie": "x=9"},
            {},
        ),
        ({"cookies": {"a": "1"}, "cookie_header": "a=1"}, None, {}, {"a": "1"}),
        ({}, None, {}, {}),
    ],
)
def test_build_httpx_kwargs_cookie_merging(
    credential: dict[str, Any],
    extra_cookies: dict[str, str] | None,
    expected_headers: dict[str, str],
    expected_cookies: dict[str, str],
) -> None:
    kwargs = build_httpx_kwargs(
        sp.Credential.model_validate(credential), None, cookies=extra_cookies
    )
    assert kwargs["headers"] == expected_headers
    assert kwargs["cookies"] == expected_cookies
    assert kwargs["proxy"] is None
    httpx.Client(**kwargs).close()


def test_parse_cookie_header_and_report_uri() -> None:
    assert parse_cookie_header("a=1; b = 2 ;flag; =x; c=") == {"a": "1", "b": "2", "c": ""}
    assert report_uri("/a?b=1") == "/a"
    assert report_uri("/a#frag") == "/a"
    assert report_uri("https://h.test/x/y?q=1#frag") == "/x/y"
    assert report_uri("http://h.test") == "/"
    assert report_uri("?only=query") == "/"
    assert report_uri("") == ""
    assert report_uri("/" + "p" * 5000) == "/" + "p" * 2047


def test_exit_without_reports_calls_release_rpc(client: sp.Client, api: respx.MockRouter) -> None:
    uri = "https://target.example.com/api/v1/feed?x=1"
    with client.lease(site="shop", client="web", uri=uri) as lease:
        pass
    assert lease.released
    assert release_bodies(api) == [{"lease_id": LEASE_ID}]
    client.close()
    assert sent_reports(api) == []


def test_reported_lease_is_released_by_last_report_not_rpc(
    client: sp.Client, api: respx.MockRouter
) -> None:
    with client.lease(site="s", client="web", uri="/a", raise_on_release_error=True) as lease:
        lease.report(200)
    client.close()
    assert [r["release"] for r in sent_reports(api)] == [True]
    assert api.routes["release"].call_count == 0


def test_last_report_carries_release(client: sp.Client, api: respx.MockRouter) -> None:
    with client.lease(site="shop", client="web", uri="/feed", flush_on_exit=True) as lease:
        first = lease.report(200, latency_ms=100, markers=["empty_list"], business_code=0)
        lease.report(429, latency_ms=50, error_kind="", uri="/feed?page=2", method="GET")
        assert first.release is False
    reports = sent_reports(api)
    assert [r["http_status"] for r in reports] == [200, 429]
    assert [r["release"] for r in reports] == [False, True]
    assert reports[0]["markers"] == ["empty_list"]
    assert reports[0]["business_code"] == "0"
    assert reports[1]["uri"] == "/feed"
    assert lease.released


def test_explicit_release_report(client: sp.Client, api: respx.MockRouter) -> None:
    with client.lease(site="shop", client="web", uri="/feed", flush_on_exit=True) as lease:
        lease.report(200)
        lease.report(200, release=True)
        with pytest.raises(sp.LeaseReleased):
            lease.report(200)
        with pytest.raises(sp.LeaseReleased):
            lease.renew()
    assert [r["release"] for r in sent_reports(api)] == [False, True]


@pytest.mark.parametrize(
    "error",
    [
        httpx.ConnectError(
            "[Errno 61] Connection refused",
            request=httpx.Request("GET", "https://target.test/api/list?page=3"),
        ),
        KeyError("parse bug"),
    ],
)
def test_exception_without_reports_releases_via_rpc(
    client: sp.Client, api: respx.MockRouter, error: Exception
) -> None:
    with pytest.raises(type(error)), client.lease(site="s", client="web", uri="/api/list"):
        raise error
    client.close()
    assert sent_reports(api) == []
    assert release_bodies(api) == [{"lease_id": LEASE_ID}]


def test_exception_after_report_releases_with_that_report(
    client: sp.Client, api: respx.MockRouter
) -> None:
    def fail_after_report(lease: sp.ManagedLease) -> None:
        lease.report(200, latency_ms=5)
        raise KeyError("parse bug")

    with (
        pytest.raises(KeyError),
        client.lease(site="s", client="web", uri="/api/list") as lease,
    ):
        fail_after_report(lease)
    client.close()
    (report,) = sent_reports(api)
    assert (report["http_status"], report["release"]) == (200, True)
    assert api.routes["release"].call_count == 0


def test_lease_without_uri_uses_release_rpc(client: sp.Client, api: respx.MockRouter) -> None:
    with client.lease(site="s", client="app", endpoint_group="feed") as lease:
        pass
    assert release_bodies(api) == [{"lease_id": LEASE_ID}]
    lease.release()  # idempotent
    assert api.routes["release"].call_count == 1


def test_explicit_release_without_reports(
    client: sp.Client, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["release"].mock(return_value=httpx.Response(200, json={"released": False}))
    with (
        caplog.at_level(logging.DEBUG, logger="spinneret.lease"),
        client.lease(site="s", client="web", uri="/a") as lease,
    ):
        lease.release()
        assert lease.released
        with pytest.raises(sp.LeaseReleased):
            lease.report(200)
    assert api.routes["release"].call_count == 1
    assert "had already ended" in caplog.text


def test_release_rpc_failure_is_logged(
    client: sp.Client, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["release"].mock(return_value=connect_error("unavailable", status=503))
    with (
        caplog.at_level(logging.WARNING, logger="spinneret.lease"),
        client.lease(site="s", client="app", endpoint_group="feed"),
    ):
        pass
    (record,) = [r for r in caplog.records if "release of lease" in r.getMessage()]
    assert record.levelno == logging.WARNING


@pytest.mark.parametrize("response", LEASE_ENDED_ERRORS)
def test_release_rpc_lease_ended_errors_are_ignored(
    client: sp.Client,
    api: respx.MockRouter,
    caplog: pytest.LogCaptureFixture,
    response: httpx.Response,
) -> None:
    api.routes["release"].mock(return_value=response)
    with caplog.at_level(logging.DEBUG, logger="spinneret.lease"):
        with client.lease(site="s", client="web", uri="/a", raise_on_release_error=True):
            pass
        lease = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
        with lease:
            lease.release()
    records = [r for r in caplog.records if r.name == "spinneret.lease"]
    assert len(records) == 2
    assert all(r.levelno == logging.DEBUG for r in records)
    assert "had already ended" in caplog.text
    assert api.routes["release"].call_count == 2


def test_raise_on_release_error(client: sp.Client, api: respx.MockRouter) -> None:
    api.routes["release"].mock(return_value=connect_error("unavailable", status=503))
    with (
        pytest.raises(sp.Unavailable),
        client.lease(site="s", client="web", uri="/a", raise_on_release_error=True) as lease,
    ):
        pass
    assert lease.released
    explicit = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
    with explicit:
        with pytest.raises(sp.Unavailable):
            explicit.release()
        explicit.release()  # already released: no second call
    assert api.routes["release"].call_count == 2
    # Without the option nothing is raised.
    with client.lease(site="s", client="web", uri="/a") as quiet:
        quiet.release()
    assert api.routes["release"].call_count == 3


def test_release_error_never_masks_block_exception(
    client: sp.Client, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["release"].mock(side_effect=httpx.ConnectError("refused"))
    with (
        caplog.at_level(logging.WARNING, logger="spinneret.lease"),
        pytest.raises(KeyError),
        client.lease(site="s", client="web", uri="/a", raise_on_release_error=True),
    ):
        raise KeyError("block failure")
    assert "release of lease" in caplog.text


def test_client_closed_release_error_is_raised_when_requested(
    settings: sp.Settings, api: respx.MockRouter
) -> None:
    client = sp.Client(settings=settings, retry=sp.NO_RETRY)
    lease = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
    with pytest.raises(sp.FailedPrecondition) as info, lease:
        client.close()
    assert info.value.reason == "client_closed"
    assert api.routes["release"].call_count == 0


def test_acquire_failure_propagates(client: sp.Client, api: respx.MockRouter) -> None:
    api.routes["acquire"].mock(
        return_value=httpx.Response(
            503,
            json={"code": "unavailable", "message": "open"},
            headers={"Spinneret-Reason": "circuit_open", "Spinneret-Retry-After-Ms": "5000"},
        )
    )
    with pytest.raises(sp.CircuitOpen), client.lease(site="s", client="web", uri="/a"):
        pytest.fail("block must not run")
    assert api.routes["report"].call_count == 0
    assert api.routes["release"].call_count == 0


def test_report_response_and_exception_helpers(client: sp.Client, api: respx.MockRouter) -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        status = 407 if request.url.path == "/proxy" else 200
        return httpx.Response(status, content=b"0123456789")

    with (
        httpx.Client(transport=httpx.MockTransport(handler)) as http,
        client.lease(site="s", client="web", uri="/items", flush_on_exit=True) as lease,
    ):
        response = http.post("https://target.test/items?cursor=abc")
        lease.report_response(response, markers=["captcha_page"], business_code="10001")
        failed = http.get("https://target.test/proxy")
        with pytest.raises(httpx.HTTPStatusError) as info:
            failed.raise_for_status()
        lease.report_exception(info.value, latency_ms=7)
        lease.report_exception(httpx.ReadTimeout("slow"), uri="/items")
        streamed = httpx.Response(
            200, headers={"content-length": "12"}, stream=httpx.ByteStream(b"x" * 12)
        )
        lease.report_response(streamed, uri="/stream")
    reports = sent_reports(api)
    assert len(reports) == 4
    ok, proxy_auth, timeout, stream = reports
    assert (ok["http_status"], ok["method"], ok["uri"]) == (200, "POST", "/items")
    assert ok["response_bytes"] == 10
    assert ok["markers"] == ["captcha_page"]
    assert ok["business_code"] == "10001"
    assert (proxy_auth["http_status"], proxy_auth["error_kind"]) == (407, "proxy_auth")
    assert proxy_auth["latency_ms"] == 7
    assert (timeout["error_kind"], timeout["http_status"], timeout["uri"]) == (
        "timeout",
        0,
        "/items",
    )
    assert (stream["http_status"], stream["response_bytes"], stream["uri"]) == (200, 12, "/stream")
    assert stream["release"] is True


def test_report_exception_with_request_and_elapsed_response(
    client: sp.Client, api: respx.MockRouter
) -> None:
    request = httpx.Request("GET", "https://target.test/api/list?page=3&sign=abc")
    timed = httpx.Response(200, request=request, content=b"{}")
    timed.elapsed = timedelta(milliseconds=25)
    with client.lease(site="s", client="web", uri="/api", flush_on_exit=True) as lease:
        lease.report_exception(httpx.ConnectError("[Errno 61] Connection refused", request=request))
        lease.report_response(timed, report_id="rpt-fixed-1")
    failed, ok = sent_reports(api)
    assert (failed["error_kind"], failed["method"], failed["uri"]) == (
        "conn_refused",
        "GET",
        "/api/list",
    )
    assert (ok["latency_ms"], ok["report_id"], ok["release"]) == (25, "rpt-fixed-1", True)
    assert api.routes["release"].call_count == 0


def test_renew_updates_expiry(client: sp.Client, api: respx.MockRouter) -> None:
    with client.lease(site="s", client="web", uri="/a") as lease:
        before = lease.expires_at
        after = lease.renew(60_000)
        assert after is not None
        assert before is not None
        assert after > before
        assert lease.expires_at == after
    body = json.loads(api.routes["renew"].calls.last.request.content)
    assert body == {"lease_id": LEASE_ID, "extend_ms": 60_000}


def test_closed_reporter_falls_back_to_direct_delivery(
    client: sp.Client, api: respx.MockRouter
) -> None:
    with client.lease(site="s", client="web", uri="/a") as lease:
        lease.report(200)
        client.reporter.close()
        with pytest.raises(sp.ReporterClosedError):
            lease.report(500)
    reports = sent_reports(api)
    assert [(r["http_status"], r["release"]) for r in reports] == [(200, True)]
    assert api.routes["release"].call_count == 0
    # Nothing reported: the release RPC does not need the reporter.
    with client.lease(site="s", client="web", uri="/a"):
        pass
    assert api.routes["release"].call_count == 1
    assert len(sent_reports(api)) == 1


def test_direct_delivery_failure_is_logged(
    client: sp.Client, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["report"].mock(
        return_value=httpx.Response(400, json={"code": "invalid_argument", "message": "bad"})
    )
    with (
        caplog.at_level(logging.WARNING, logger="spinneret.lease"),
        client.lease(site="s", client="web", uri="/a") as lease,
    ):
        lease.report(200)
        client.reporter.close()
    assert "direct report delivery failed" in caplog.text


def test_unexpected_release_errors_never_escape(
    client: sp.Client,
    api: respx.MockRouter,
    caplog: pytest.LogCaptureFixture,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def broken(*args: Any) -> Any:
        raise RuntimeError("unexpected")

    with (
        caplog.at_level(logging.ERROR, logger="spinneret.lease"),
        client.lease(site="s", client="web", uri="/a") as lease,
    ):
        lease.report(200)
        client.reporter.close()
        monkeypatch.setattr(client, "report", broken)
    assert "failed to release lease" in caplog.text
    caplog.clear()
    monkeypatch.setattr(client, "release", broken)
    with (
        caplog.at_level(logging.ERROR, logger="spinneret.lease"),
        client.lease(site="s", client="web", uri="/a", raise_on_release_error=True),
    ):
        pass
    assert "failed to release lease" in caplog.text


def test_long_request_target_is_reported_as_path(client: sp.Client, api: respx.MockRouter) -> None:
    signed = "https://target.test/api/list?signature=" + "x" * 4000
    response = httpx.Response(200, request=httpx.Request("GET", signed), content=b"{}")
    with client.lease(site="s", client="web", uri="/api/list", flush_on_exit=True) as lease:
        lease.report_response(response)
    (report,) = sent_reports(api)
    assert report["uri"] == "/api/list"
    assert "signature" not in json.dumps(report)


def test_lease_after_client_close(settings: sp.Settings, api: respx.MockRouter) -> None:
    client = sp.Client(settings=settings, retry=sp.NO_RETRY)
    client.close()
    assert client.reporter.closed
    with pytest.raises(sp.ReporterClosedError):
        client.reporter.submit(sp.Report(lease_id="l", uri="/a"))
    with pytest.raises(sp.FailedPrecondition) as info:
        client.acquire("s", "web", "/a")
    assert info.value.reason == "client_closed"
    assert api.routes["acquire"].call_count == 0


def test_status_407_response_reports_proxy_auth(client: sp.Client, api: respx.MockRouter) -> None:
    response = httpx.Response(407, request=httpx.Request("CONNECT", "https://target.test/x"))
    with client.lease(site="s", client="web", uri="/x", flush_on_exit=True) as lease:
        lease.report_response(response)
    (report,) = sent_reports(api)
    assert (report["http_status"], report["error_kind"]) == (407, "proxy_auth")


def test_flush_timeout_on_exit_is_logged(
    client: sp.Client, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["report"].mock(side_effect=httpx.ConnectError("refused"))
    options = sp.ReporterOptions(flush_interval=0.01, close_timeout=0.1)
    client._reporter = sp.Reporter(client._send_reports, options)
    with (
        caplog.at_level(logging.WARNING, logger="spinneret.lease"),
        client.lease(site="s", client="web", uri="/a", flush_on_exit=True) as lease,
    ):
        lease.report(200)
    assert "flush timed out" in caplog.text
    client.reporter.close(timeout=0)
