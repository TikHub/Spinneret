from __future__ import annotations

import json
from typing import Any

import httpx
import pytest
import respx

import spinneret as sp

from ._helpers import (
    BASE_URL,
    CONFIG_PATH,
    FAST_RETRY,
    LEASE_PATH,
    NODE,
    REPORT_PATH,
    SECRET_PATH,
    TOKEN,
    acquire_payload,
    config_item,
)


def _json(request: httpx.Request) -> Any:
    return json.loads(request.content)


def _error(status: int, code: str, reason: str = "", retry_after: str = "") -> httpx.Response:
    headers = {"Spinneret-Reason": reason} if reason else {}
    if retry_after:
        headers["Spinneret-Retry-After-Ms"] = retry_after
    return httpx.Response(status, json={"code": code, "message": "m"}, headers=headers)


@pytest.fixture
def client(settings: sp.Settings) -> Any:
    with sp.Client(settings=settings, retry=FAST_RETRY) as c:
        yield c


def test_client_resolves_settings_from_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SPINNERET_URL", BASE_URL)
    monkeypatch.setenv("SPINNERET_TOKEN", TOKEN)
    with sp.Client(node="explicit") as c:
        assert c.settings.url == BASE_URL
        assert c.settings.node == "explicit"
        assert TOKEN not in repr(c)


def test_client_requires_configuration(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("SPINNERET_URL", raising=False)
    monkeypatch.delenv("SPINNERET_TOKEN", raising=False)
    with pytest.raises(sp.ConfigurationError):
        sp.Client()


def test_acquire_request_and_headers(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(
        return_value=httpx.Response(200, json=acquire_payload())
    )
    response = client.acquire("shop", "web", "/api/v1/search", session_key="task-1", wait_ms=1500)
    assert response.lease.identity_type == "web_cookie"
    request = route.calls.last.request
    assert request.url == f"{BASE_URL}{LEASE_PATH}/Acquire"
    assert request.headers["content-type"] == "application/json"
    assert request.headers["connect-protocol-version"] == "1"
    assert request.headers["authorization"] == f"Bearer {TOKEN}"
    assert request.headers["x-spinneret-node"] == NODE
    assert request.headers["user-agent"].startswith("spinneret-python/")
    assert _json(request) == {
        "site": "shop",
        "client": "web",
        "uri": "/api/v1/search",
        "endpoint_group": "",
        "session_key": "task-1",
        "wait_ms": 1500,
    }
    timeout = request.extensions["timeout"]
    assert timeout["connect"] == pytest.approx(3.0)
    assert timeout["read"] == pytest.approx(11.5)


def test_acquire_batch(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/AcquireBatch").mock(
        return_value=httpx.Response(
            200, json={"leases": [acquire_payload(), acquire_payload()], "requested": 3}
        )
    )
    response = client.acquire_batch("shop", "app", 3, endpoint_group="feed")
    assert len(response.leases) == 2
    assert response.requested == 3
    body = _json(route.calls.last.request)
    assert body["count"] == 3
    assert body["endpoint_group"] == "feed"


def test_renew_release_report_secret(client: sp.Client, router: respx.MockRouter) -> None:
    renew = router.post(f"{LEASE_PATH}/Renew").mock(
        return_value=httpx.Response(200, json={"expires_at": "2026-09-16T09:00:00.5Z"})
    )
    release = router.post(f"{LEASE_PATH}/Release").mock(
        return_value=httpx.Response(200, json={"released": True})
    )
    report = router.post(REPORT_PATH).mock(
        return_value=httpx.Response(
            200,
            json={
                "accepted": "1",
                "duplicated": 1,
                "rejected": [{"report_id": "r3", "reason": "lease_unknown", "message": "gone"}],
            },
        )
    )
    secret = router.post(SECRET_PATH).mock(
        return_value=httpx.Response(
            200,
            json={"path": "signing/api_key", "version": "3", "value": "s3cr3t", "expires_at": None},
        )
    )
    assert client.renew("lse_1", 60_000).expires_at is not None
    assert _json(renew.calls.last.request) == {"lease_id": "lse_1", "extend_ms": 60_000}
    assert client.release("lse_1").released is True
    assert _json(release.calls.last.request) == {"lease_id": "lse_1"}
    reports = [sp.Report(report_id=f"r{i}", lease_id="lse_1", uri="/a") for i in range(3)]
    result = client.report(reports)
    assert (result.accepted, result.duplicated, result.rejected[0].reason) == (
        1,
        1,
        "lease_unknown",
    )
    assert [r["report_id"] for r in _json(report.calls.last.request)["reports"]] == [
        "r0",
        "r1",
        "r2",
    ]
    value = client.get_secret("signing/api_key", version=3)
    assert (value.value, value.version) == ("s3cr3t", 3)
    assert _json(secret.calls.last.request) == {"path": "signing/api_key", "version": 3}


def test_config_calls(client: sp.Client, router: respx.MockRouter) -> None:
    get = router.post(f"{CONFIG_PATH}/GetConfig").mock(
        return_value=httpx.Response(200, json={"item": config_item(version=12)})
    )
    batch = router.post(f"{CONFIG_PATH}/BatchGetConfig").mock(
        return_value=httpx.Response(
            200, json={"items": [config_item()], "missing": [{"group": "g", "key": "k"}]}
        )
    )
    watch = router.post(f"{CONFIG_PATH}/WatchConfig").mock(
        return_value=httpx.Response(200, json={"items": []})
    )
    item = client.get_config("crawler", "search.json", namespace="prod")
    assert item.version == 12
    assert _json(get.calls.last.request) == {
        "namespace": "prod",
        "group": "crawler",
        "key": "search.json",
    }
    result = client.batch_get_config(["crawler/search.json", ("g", "k")])
    assert result.missing[0].key == "k"
    assert _json(batch.calls.last.request)["items"] == [
        {"group": "crawler", "key": "search.json"},
        {"group": "g", "key": "k"},
    ]
    changed = client.watch_config(
        [sp.WatchItem(group="crawler", key="search.json", version=12), {"group": "g", "key": "k"}],
        timeout_ms=20_000,
    )
    assert changed.items == []
    request = watch.calls.last.request
    assert _json(request) == {
        "namespace": "",
        "items": [
            {"group": "crawler", "key": "search.json", "version": 12},
            {"group": "g", "key": "k", "version": 0},
        ],
        "timeout_ms": 20_000,
    }
    assert request.extensions["timeout"]["read"] == pytest.approx(25.0)


@pytest.mark.parametrize(
    ("response", "expected"),
    [
        (
            _error(429, "resource_exhausted", "no_identity_available", "1200"),
            sp.NoIdentityAvailable,
        ),
        (_error(503, "unavailable", "circuit_open", "30000"), sp.CircuitOpen),
        (_error(503, "unavailable", "site_paused"), sp.SitePaused),
        (_error(401, "unauthenticated", "token_invalid"), sp.Unauthenticated),
        (_error(403, "permission_denied", "scope_missing"), sp.PermissionDenied),
        (_error(400, "invalid_argument", "site_unknown"), sp.InvalidArgument),
    ],
)
def test_acquire_errors_are_not_retried(
    client: sp.Client, router: respx.MockRouter, response: httpx.Response, expected: type
) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(return_value=response)
    with pytest.raises(expected) as info:
        client.acquire("shop", "web", "/a")
    assert route.call_count == 1
    assert info.value.http_status == response.status_code


def test_lease_unknown_on_renew(client: sp.Client, router: respx.MockRouter) -> None:
    router.post(f"{LEASE_PATH}/Renew").mock(return_value=_error(404, "not_found", "lease_unknown"))
    with pytest.raises(sp.LeaseUnknown):
        client.renew("lse_gone")


def test_acquire_retries_connect_errors(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(
        side_effect=[
            httpx.ConnectError("[Errno 61] Connection refused"),
            httpx.ConnectTimeout("t"),
            httpx.Response(200, json=acquire_payload()),
        ]
    )
    assert client.acquire("shop", "web", "/a").lease.lease_id
    assert route.call_count == 3


def test_acquire_does_not_retry_ambiguous_transport_errors(
    client: sp.Client, router: respx.MockRouter
) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(side_effect=httpx.ReadTimeout("slow"))
    with pytest.raises(sp.TransportError) as info:
        client.acquire("shop", "web", "/a")
    assert route.call_count == 1
    assert info.value.error_kind == "timeout"
    assert isinstance(info.value.__cause__, httpx.ReadTimeout)


def test_acquire_retries_server_declared_unavailable(
    client: sp.Client, router: respx.MockRouter
) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(
        side_effect=[
            _error(503, "unavailable", "rebuilding", "1"),
            httpx.Response(200, json=acquire_payload()),
        ]
    )
    client.acquire("shop", "web", "/a")
    assert route.call_count == 2


def test_acquire_does_not_retry_bare_gateway_errors(
    client: sp.Client, router: respx.MockRouter
) -> None:
    route = router.post(f"{LEASE_PATH}/Acquire").mock(
        return_value=httpx.Response(503, text="upstream reset")
    )
    with pytest.raises(sp.Unavailable):
        client.acquire("shop", "web", "/a")
    assert route.call_count == 1


def test_idempotent_calls_retry_until_exhausted(
    client: sp.Client, router: respx.MockRouter
) -> None:
    route = router.post(f"{LEASE_PATH}/Release").mock(side_effect=httpx.ReadError("reset"))
    with pytest.raises(sp.TransportError) as info:
        client.release("lse_1")
    assert route.call_count == 3
    assert info.value.error_kind == "conn_reset"


def test_idempotent_calls_retry_bare_503(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{CONFIG_PATH}/GetConfig").mock(
        side_effect=[
            httpx.Response(502, text="bad gateway"),
            httpx.Response(200, json={"item": config_item()}),
        ]
    )
    client.get_config("crawler", "search.json")
    assert route.call_count == 2


def test_long_retry_after_is_not_waited(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/Renew").mock(
        return_value=_error(503, "unavailable", "rebuilding", "60000")
    )
    with pytest.raises(sp.Unavailable) as info:
        client.renew("lse_1")
    assert route.call_count == 1
    assert info.value.retry_after == 60.0


def test_retry_disabled(settings: sp.Settings, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/Release").mock(side_effect=httpx.ConnectError("refused"))
    with sp.Client(settings=settings, retry=sp.NO_RETRY) as c, pytest.raises(sp.TransportError):
        c.release("lse_1")
    assert route.call_count == 1


def test_proxy_errors_are_not_retried(client: sp.Client, router: respx.MockRouter) -> None:
    route = router.post(f"{LEASE_PATH}/Release").mock(side_effect=httpx.ProxyError("407"))
    with pytest.raises(sp.TransportError):
        client.release("lse_1")
    assert route.call_count == 1


@pytest.mark.parametrize(
    "response",
    [
        httpx.Response(200, content=b"not json"),
        httpx.Response(200, json={"leases": "nope"}),
        httpx.Response(200, content=b"\xff"),
    ],
)
def test_invalid_success_payloads(
    client: sp.Client, router: respx.MockRouter, response: httpx.Response
) -> None:
    router.post(f"{LEASE_PATH}/AcquireBatch").mock(return_value=response)
    with pytest.raises(sp.InternalError) as info:
        client.acquire_batch("s", "c", 2)
    assert info.value.reason == "invalid_response"


def test_empty_success_body_uses_defaults(client: sp.Client, router: respx.MockRouter) -> None:
    router.post(f"{LEASE_PATH}/Release").mock(return_value=httpx.Response(200, content=b""))
    assert client.release("lse_1").released is False


def test_injected_http_client_is_not_closed(
    settings: sp.Settings, router: respx.MockRouter
) -> None:
    router.post(f"{LEASE_PATH}/Release").mock(return_value=httpx.Response(200, json={}))
    http = httpx.Client()
    client = sp.Client(settings=settings, http_client=http)
    client.release("lse_1")
    client.close()
    client.close()
    assert client.closed
    assert not http.is_closed
    http.close()


def test_close_flushes_reporter(settings: sp.Settings, router: respx.MockRouter) -> None:
    route = router.post(REPORT_PATH).mock(return_value=httpx.Response(200, json={"accepted": 1}))
    client = sp.Client(settings=settings)
    client.reporter.submit(sp.Report(lease_id="lse_1", uri="/a"))
    assert client.reporter is client.reporter
    client.close()
    assert route.call_count == 1
    assert client.reporter.closed
    assert client.timeouts.read == 10.0


@pytest.mark.parametrize(("timeout_ms", "expected_read"), [(0, 35.0), (1_000, 6.0), (60_000, 65.0)])
def test_watch_read_timeout_covers_server_wait(
    client: sp.Client, router: respx.MockRouter, timeout_ms: int, expected_read: float
) -> None:
    route = router.post(f"{CONFIG_PATH}/WatchConfig").mock(
        return_value=httpx.Response(200, json={"items": []})
    )
    client.watch_config([{"group": "g", "key": "k"}], timeout_ms=timeout_ms)
    request = route.calls.last.request
    assert _json(request)["timeout_ms"] == timeout_ms
    assert request.extensions["timeout"]["read"] == pytest.approx(expected_read)
