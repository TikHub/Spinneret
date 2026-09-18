from __future__ import annotations

import json
import logging
from collections.abc import AsyncIterator
from typing import Any

import httpx
import pytest
import respx

import spinneret as sp

from ._helpers import LEASE_PATH, REPORT_PATH, acquire_payload

LEASE_ID = acquire_payload()["lease"]["lease_id"]


@pytest.fixture
def api(router: respx.MockRouter) -> respx.MockRouter:
    router.post(f"{LEASE_PATH}/Acquire", name="acquire").mock(
        return_value=httpx.Response(200, json=acquire_payload(probe=True))
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
async def client(settings: sp.Settings, router: respx.MockRouter) -> AsyncIterator[sp.AsyncClient]:
    options = sp.ReporterOptions(flush_interval=0.01)
    client = sp.AsyncClient(settings=settings, retry=sp.NO_RETRY, reporter_options=options)
    yield client
    await client.aclose()


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


async def test_async_lease_reports_and_release(
    client: sp.AsyncClient, api: respx.MockRouter
) -> None:
    lease = client.lease(site="shop", client="web", uri="/feed", wait_ms=200, flush_on_exit=True)
    async with lease:
        assert lease.info.probe is True
        assert lease.httpx_kwargs()["proxy"].startswith("http://")
        lease.report(200, latency_ms=10)
        lease.report(200, latency_ms=20, markers=["empty_list"])
        with pytest.raises(RuntimeError, match="reentrant"):
            await lease.__aenter__()
    reports = sent_reports(api)
    assert [r["release"] for r in reports] == [False, True]
    assert reports[1]["markers"] == ["empty_list"]
    assert json.loads(api.routes["acquire"].calls.last.request.content)["wait_ms"] == 200


async def test_async_exit_without_reports(client: sp.AsyncClient, api: respx.MockRouter) -> None:
    async with client.lease(site="s", client="web", uri="/a") as lease:
        pass
    assert lease.released
    assert release_bodies(api) == [{"lease_id": LEASE_ID}]
    with pytest.raises(httpx.ReadTimeout):
        async with client.lease(site="s", client="web", uri="/a"):
            raise httpx.ReadTimeout("slow")
    assert api.routes["release"].call_count == 2
    await client.aclose()
    assert sent_reports(api) == []


async def test_async_reported_lease_is_released_by_last_report(
    client: sp.AsyncClient, api: respx.MockRouter
) -> None:
    def fail_after_report(lease: sp.AsyncManagedLease) -> None:
        lease.report(503)
        raise KeyError("parse bug")

    with pytest.raises(KeyError):
        async with client.lease(
            site="s", client="web", uri="/a", raise_on_release_error=True
        ) as lease:
            fail_after_report(lease)
    await client.aclose()
    assert [(r["http_status"], r["release"]) for r in sent_reports(api)] == [(503, True)]
    assert api.routes["release"].call_count == 0


async def test_async_explicit_release_and_renew(
    client: sp.AsyncClient, api: respx.MockRouter
) -> None:
    async with client.lease(site="s", client="web", uri="/a", flush_on_exit=True) as lease:
        expiry = await lease.renew(1000)
        assert expiry is not None
        lease.report_response(
            httpx.Response(200, content=b"abc", request=httpx.Request("GET", "https://t.test/a"))
        )
        lease.report_exception(httpx.ConnectError("refused"), uri="/a", release=True)
        assert lease.released
        with pytest.raises(sp.LeaseReleased):
            lease.report(200)
        with pytest.raises(sp.LeaseReleased):
            await lease.renew()
        await lease.release()
    reports = sent_reports(api)
    assert [(r["http_status"], r["error_kind"], r["release"]) for r in reports] == [
        (200, "", False),
        (0, "conn_refused", True),
    ]
    assert reports[0]["response_bytes"] == 3


async def test_async_release_rpc_without_uri(
    client: sp.AsyncClient, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    async with client.lease(site="s", client="app", endpoint_group="feed"):
        pass
    assert api.routes["release"].call_count == 1
    api.routes["release"].mock(return_value=connect_error("not_found", status=404))
    with caplog.at_level(logging.WARNING, logger="spinneret.lease"):
        async with client.lease(site="s", client="app", endpoint_group="feed"):
            pass
    (record,) = [r for r in caplog.records if "release of lease" in r.getMessage()]
    assert record.levelno == logging.WARNING


@pytest.mark.parametrize(
    ("code", "reason", "status"),
    [
        ("failed_precondition", "lease_released", 400),
        ("not_found", "lease_unknown", 404),
        ("failed_precondition", "lease_expired", 400),
    ],
)
async def test_async_lease_ended_release_errors_are_ignored(
    client: sp.AsyncClient,
    api: respx.MockRouter,
    caplog: pytest.LogCaptureFixture,
    code: str,
    reason: str,
    status: int,
) -> None:
    api.routes["release"].mock(return_value=connect_error(code, reason, status))
    with caplog.at_level(logging.DEBUG, logger="spinneret.lease"):
        async with client.lease(site="s", client="web", uri="/a", raise_on_release_error=True):
            pass
        lease = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
        async with lease:
            await lease.release()
    records = [r for r in caplog.records if r.name == "spinneret.lease"]
    assert len(records) == 2
    assert all(r.levelno == logging.DEBUG for r in records)
    assert api.routes["release"].call_count == 2


async def test_async_raise_on_release_error(
    client: sp.AsyncClient, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["release"].mock(return_value=connect_error("unavailable", status=503))
    lease = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
    with pytest.raises(sp.Unavailable):
        async with lease:
            pass
    assert lease.released
    explicit = client.lease(site="s", client="web", uri="/a", raise_on_release_error=True)
    async with explicit:
        with pytest.raises(sp.Unavailable):
            await explicit.release()
        await explicit.release()  # already released: no second call
    # The block's own exception is never replaced by a release error.
    with caplog.at_level(logging.WARNING, logger="spinneret.lease"), pytest.raises(KeyError):
        async with client.lease(site="s", client="web", uri="/a", raise_on_release_error=True):
            raise KeyError("block failure")
    assert "release of lease" in caplog.text
    # Without the option nothing is raised.
    async with client.lease(site="s", client="web", uri="/a") as quiet:
        await quiet.release()
    assert api.routes["release"].call_count == 4


async def test_async_closed_reporter(client: sp.AsyncClient, api: respx.MockRouter) -> None:
    async with client.lease(site="s", client="web", uri="/a") as lease:
        lease.report(200)
        await client.reporter.close()
        with pytest.raises(sp.ReporterClosedError):
            lease.report(500)
    assert [(r["http_status"], r["release"]) for r in sent_reports(api)] == [(200, True)]
    async with client.lease(site="s", client="web", uri="/a"):
        pass
    assert api.routes["release"].call_count == 1


async def test_async_direct_delivery_failure_is_logged(
    client: sp.AsyncClient, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["report"].mock(
        return_value=httpx.Response(400, json={"code": "invalid_argument", "message": "bad"})
    )
    with caplog.at_level(logging.WARNING, logger="spinneret.lease"):
        async with client.lease(site="s", client="web", uri="/a") as lease:
            lease.report(200)
            await client.reporter.close()
    assert "direct report delivery failed" in caplog.text


async def test_async_unexpected_errors_are_logged(
    client: sp.AsyncClient,
    api: respx.MockRouter,
    caplog: pytest.LogCaptureFixture,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    async def broken(*args: Any) -> Any:
        raise RuntimeError("unexpected")

    with caplog.at_level(logging.ERROR, logger="spinneret.lease"):
        async with client.lease(site="s", client="web", uri="/a") as lease:
            lease.report(200)
            await client.reporter.close()
            monkeypatch.setattr(client, "report", broken)
    assert "failed to release lease" in caplog.text
    caplog.clear()
    monkeypatch.setattr(client, "release", broken)
    with caplog.at_level(logging.ERROR, logger="spinneret.lease"):
        async with client.lease(site="s", client="web", uri="/a", raise_on_release_error=True):
            pass
    assert "failed to release lease" in caplog.text


async def test_async_lease_after_client_close(
    settings: sp.Settings, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    client = sp.AsyncClient(settings=settings, retry=sp.NO_RETRY)
    await client.aclose()
    assert client.reporter.closed
    with pytest.raises(sp.FailedPrecondition) as info:
        await client.release("lse_1")
    assert info.value.reason == "client_closed"
    with pytest.raises(sp.FailedPrecondition):
        async with client.lease(site="s", client="web", uri="/a"):
            pytest.fail("block must not run")
    assert api.routes["acquire"].call_count == 0


async def test_async_flush_timeout_on_exit(
    client: sp.AsyncClient, api: respx.MockRouter, caplog: pytest.LogCaptureFixture
) -> None:
    api.routes["report"].mock(side_effect=httpx.ConnectError("refused"))
    options = sp.ReporterOptions(flush_interval=0.01, close_timeout=0.1)
    client._reporter = sp.AsyncReporter(client._send_reports, options)
    with caplog.at_level(logging.WARNING, logger="spinneret.lease"):
        async with client.lease(site="s", client="web", uri="/a", flush_on_exit=True) as lease:
            lease.report(200)
    assert "flush timed out" in caplog.text
    await client.reporter.close(timeout=0)


async def test_async_acquire_failure(client: sp.AsyncClient, api: respx.MockRouter) -> None:
    api.routes["acquire"].mock(
        return_value=httpx.Response(
            429,
            json={"code": "resource_exhausted", "message": "none"},
            headers={"Spinneret-Reason": "no_identity_available", "Spinneret-Retry-After-Ms": "50"},
        )
    )
    lease = client.lease(site="s", client="web", uri="/a")
    with pytest.raises(sp.NoIdentityAvailable) as info:
        async with lease:
            pytest.fail("block must not run")
    assert info.value.retry_after == 0.05
    assert not lease.acquired
    assert api.routes["release"].call_count == 0
