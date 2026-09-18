from __future__ import annotations

import json
from collections.abc import AsyncIterator
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


@pytest.fixture
async def client(settings: sp.Settings) -> AsyncIterator[sp.AsyncClient]:
    async with sp.AsyncClient(settings=settings, retry=FAST_RETRY) as c:
        yield c


async def test_all_calls_serialize_requests(
    client: sp.AsyncClient, router: respx.MockRouter
) -> None:
    acquire = router.post(f"{LEASE_PATH}/Acquire").mock(
        return_value=httpx.Response(200, json=acquire_payload())
    )
    batch = router.post(f"{LEASE_PATH}/AcquireBatch").mock(
        return_value=httpx.Response(200, json={"leases": [acquire_payload()], "requested": 2})
    )
    renew = router.post(f"{LEASE_PATH}/Renew").mock(
        return_value=httpx.Response(200, json={"expires_at": "2026-09-16T09:00:00Z"})
    )
    release = router.post(f"{LEASE_PATH}/Release").mock(
        return_value=httpx.Response(200, json={"released": True})
    )
    report = router.post(REPORT_PATH).mock(
        return_value=httpx.Response(200, json={"accepted": 1, "duplicated": 0, "rejected": []})
    )
    get = router.post(f"{CONFIG_PATH}/GetConfig").mock(
        return_value=httpx.Response(200, json={"item": config_item()})
    )
    batch_get = router.post(f"{CONFIG_PATH}/BatchGetConfig").mock(
        return_value=httpx.Response(200, json={"items": [config_item()], "missing": []})
    )
    watch = router.post(f"{CONFIG_PATH}/WatchConfig").mock(
        return_value=httpx.Response(200, json={"items": [config_item(version=2)]})
    )
    secret = router.post(SECRET_PATH).mock(
        return_value=httpx.Response(200, json={"path": "p", "version": 1, "value": "v"})
    )

    lease = await client.acquire("shop", "web", "/a", wait_ms=100)
    assert lease.proxy is not None
    request = acquire.calls.last.request
    assert request.headers["authorization"] == f"Bearer {TOKEN}"
    assert request.headers["x-spinneret-node"] == NODE
    assert request.extensions["timeout"]["read"] == pytest.approx(10.1)
    assert (await client.acquire_batch("shop", "web", 2, "/a")).requested == 2
    assert _json(batch.calls.last.request)["count"] == 2
    assert (await client.renew("lse_1")).expires_at is not None
    assert _json(renew.calls.last.request) == {"lease_id": "lse_1", "extend_ms": 0}
    assert (await client.release("lse_1")).released
    assert _json(release.calls.last.request) == {"lease_id": "lse_1"}
    result = await client.report([sp.Report(lease_id="lse_1", uri="/a")])
    assert result.accepted == 1
    assert len(_json(report.calls.last.request)["reports"]) == 1
    assert (await client.get_config("crawler", "search.json")).key == "search.json"
    assert _json(get.calls.last.request)["group"] == "crawler"
    assert len((await client.batch_get_config(["crawler/search.json"])).items) == 1
    assert _json(batch_get.calls.last.request)["items"][0]["key"] == "search.json"
    changed = await client.watch_config([{"group": "crawler", "key": "search.json", "version": 1}])
    assert changed.items[0].version == 2
    assert _json(watch.calls.last.request)["timeout_ms"] == 30_000
    assert (await client.get_secret("p")).value == "v"
    assert _json(secret.calls.last.request) == {"path": "p", "version": 0}
    assert BASE_URL in repr(client)
    assert client.timeouts.connect == 3.0


async def test_errors_and_retries(client: sp.AsyncClient, router: respx.MockRouter) -> None:
    acquire = router.post(f"{LEASE_PATH}/Acquire").mock(
        side_effect=[
            httpx.ConnectError("refused"),
            httpx.Response(
                429,
                json={"code": "resource_exhausted", "message": "none"},
                headers={"Spinneret-Reason": "no_identity_available"},
            ),
        ]
    )
    with pytest.raises(sp.NoIdentityAvailable):
        await client.acquire("shop", "web", "/a")
    assert acquire.call_count == 2

    read_timeout = router.post(f"{LEASE_PATH}/AcquireBatch").mock(
        side_effect=httpx.ReadTimeout("slow")
    )
    with pytest.raises(sp.TransportError):
        await client.acquire_batch("shop", "web", 2)
    assert read_timeout.call_count == 1

    release = router.post(f"{LEASE_PATH}/Release").mock(
        side_effect=[
            httpx.Response(
                503,
                json={"code": "unavailable", "message": "x"},
                headers={"Spinneret-Reason": "rebuilding", "Spinneret-Retry-After-Ms": "1"},
            ),
            httpx.ReadError("reset"),
            httpx.Response(200, json={"released": True}),
        ]
    )
    assert (await client.release("lse_1")).released
    assert release.call_count == 3

    renew = router.post(f"{LEASE_PATH}/Renew").mock(
        return_value=httpx.Response(
            503,
            json={"code": "unavailable", "message": "x"},
            headers={"Spinneret-Reason": "rebuilding", "Spinneret-Retry-After-Ms": "600000"},
        )
    )
    with pytest.raises(sp.Unavailable):
        await client.renew("lse_1")
    assert renew.call_count == 1


async def test_aclose_is_idempotent_and_flushes(
    settings: sp.Settings, router: respx.MockRouter
) -> None:
    route = router.post(REPORT_PATH).mock(return_value=httpx.Response(200, json={"accepted": 1}))
    http = httpx.AsyncClient()
    client = sp.AsyncClient(settings=settings, http_client=http)
    client.reporter.submit(sp.Report(lease_id="lse_1", uri="/a"))
    await client.aclose()
    await client.aclose()
    assert client.closed
    assert route.call_count == 1
    assert not http.is_closed
    await http.aclose()


async def test_env_configuration(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SPINNERET_URL", BASE_URL)
    monkeypatch.setenv("SPINNERET_TOKEN", TOKEN)
    async with sp.AsyncClient() as c:
        assert c.settings.token == TOKEN
        assert TOKEN not in repr(c)
