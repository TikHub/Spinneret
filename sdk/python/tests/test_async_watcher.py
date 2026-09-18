from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import AsyncIterator
from pathlib import Path

import httpx
import pytest

import spinneret as sp
from spinneret._snapshot import SnapshotStore

from ._config_server import ConfigServer, denied, unavailable
from ._helpers import (
    RESOLVED_SECRET_CONTENT,
    RESOLVED_SECRET_VALUE,
    TOKEN,
    config_item,
    secret_config_item,
)

FAST = sp.WatchOptions(timeout_ms=1000, backoff=sp.Backoff(initial=0.01, maximum=0.02))


async def _wait_until(predicate: object, timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():  # type: ignore[operator]
            return
        await asyncio.sleep(0.005)
    raise AssertionError("condition not met in time")


def _files_containing(root: Path, needle: str) -> list[Path]:
    return [p for p in root.rglob("*") if p.is_file() and needle in p.read_text()]


def _version(watcher: sp.AsyncConfigWatcher, key: str = "search.json") -> int:
    item = watcher.get("crawler", key)
    return item.version if item is not None else -1


@pytest.fixture
def server() -> ConfigServer:
    return ConfigServer([config_item(version=1), config_item(key="b.json", version=4)])


@pytest.fixture
async def client(settings: sp.Settings, server: ConfigServer) -> AsyncIterator[sp.AsyncClient]:
    http = httpx.AsyncClient(transport=httpx.MockTransport(server.async_handler))
    client = sp.AsyncClient(settings=settings, http_client=http, retry=sp.NO_RETRY)
    yield client
    await client.aclose()
    await http.aclose()


def _watcher(client: sp.AsyncClient, **kwargs: object) -> sp.AsyncConfigWatcher:
    params: dict[str, object] = {"options": FAST}
    params.update(kwargs)
    return sp.AsyncConfigWatcher(client, ["crawler/search.json", "crawler/b.json"], **params)  # type: ignore[arg-type]


async def test_async_watch_flow(
    client: sp.AsyncClient, server: ConfigServer, settings: sp.Settings
) -> None:
    sync_seen: list[int] = []
    async_seen: list[int] = []

    async def on_change_async(item: sp.ConfigItem) -> None:
        await asyncio.sleep(0)
        async_seen.append(item.version)

    watcher = client.config_watcher(
        ["crawler/search.json"], timeout_ms=1000, on_change=lambda i: sync_seen.append(i.version)
    )
    watcher.add_listener(on_change_async)
    async with watcher:
        assert watcher.running
        assert not watcher.from_snapshot
        assert _version(watcher) == 1
        assert set(watcher.items()) == {("crawler", "search.json")}
        waiter = asyncio.ensure_future(watcher.wait_for_change("crawler", "search.json", timeout=3))
        await asyncio.sleep(0.03)
        server.publish(config_item(version=2))
        changed = await waiter
        assert changed is not None
        assert changed.version == 2
        await _wait_until(lambda: async_seen == [1, 2])
        assert sync_seen == [1, 2]
        assert await watcher.start() is watcher
    store = watcher.snapshot_store
    assert store is not None
    snapshot = store.load("crawler", "search.json")
    assert snapshot is not None
    assert snapshot.version == 2
    assert store.directory.parent.parent == settings.cache_dir
    with pytest.raises(RuntimeError, match="restarted"):
        await watcher.start()
    assert not watcher.running


async def test_async_snapshot_fallback(
    client: sp.AsyncClient, server: ConfigServer, tmp_path: Path
) -> None:
    store = SnapshotStore(tmp_path, host="spinneret.test", namespace="ns", token=TOKEN)
    store.save(sp.ConfigItem.model_validate(config_item(version=1, content="cached")))
    server.batch_responses.append(unavailable())
    server.watch_responses.append(unavailable())
    watcher = _watcher(client, cache_dir=tmp_path, namespace="ns")
    async with watcher:
        cached = watcher.get("crawler", "search.json")
        assert cached is not None
        assert cached.content == "cached"
        assert watcher.from_snapshot
        server.publish(config_item(version=7))
        await _wait_until(lambda: _version(watcher) == 7)
        await _wait_until(lambda: not watcher.from_snapshot)
        assert server.watch_bodies[0]["namespace"] == "ns"
        assert server.watch_bodies[0]["items"][0]["version"] == 1


async def test_async_permanent_initial_error(client: sp.AsyncClient, server: ConfigServer) -> None:
    server.batch_responses.append(denied())
    with pytest.raises(sp.PermissionDenied):
        await _watcher(client).start()


async def test_async_background_load_and_watch_errors(
    client: sp.AsyncClient, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    server.batch_responses.extend(
        [
            denied(),
            httpx.Response(200, json={"items": [], "missing": ["crawler/search.json"]}),
        ]
    )
    server.watch_responses.extend([unavailable(), httpx.Response(200, content=b"garbage")])
    caplog.set_level(logging.INFO, logger="spinneret.config")
    watcher = _watcher(client)
    await watcher.start(wait=False)
    try:
        await _wait_until(lambda: len(server.watch_bodies) >= 3)
        server.publish(config_item(version=3))
        await _wait_until(lambda: _version(watcher) == 3)
    finally:
        await watcher.stop()
    assert "config items not found" in caplog.text
    assert "config watch failed" in caplog.text


async def test_async_secret_policy_and_disabled_snapshots(
    client: sp.AsyncClient, server: ConfigServer, tmp_path: Path
) -> None:
    server.items[("crawler", "search.json")] = secret_config_item(version=9)
    async with _watcher(
        client, cache_dir=tmp_path / "plain", treat_as_secret=lambda i: False
    ) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        item = watcher.get("crawler", "search.json")
        assert item is not None
        assert item.has_secret_refs
        assert not store.path_for("crawler", "search.json").exists()
        assert store.path_for("crawler", "b.json").exists()
        server.publish(config_item(version=10, content='{"v": 10}'))
        await _wait_until(lambda: store.path_for("crawler", "search.json").exists())
        server.publish(secret_config_item(version=11))
        await _wait_until(lambda: not store.path_for("crawler", "search.json").exists())
    async with _watcher(
        client, cache_dir=tmp_path / "pred", treat_as_secret=lambda i: i.key == "b.json"
    ) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        assert _version(watcher, "b.json") == 4
        assert not store.path_for("crawler", "b.json").exists()
    async with _watcher(client, cache_dir=tmp_path / "enc", cache_secrets=True) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        path = store.path_for("crawler", "search.json")
        assert RESOLVED_SECRET_VALUE not in path.read_text()
        loaded = store.load("crawler", "search.json")
        assert loaded is not None
        assert (loaded.content, loaded.has_secret_refs) == (RESOLVED_SECRET_CONTENT, True)
    assert _files_containing(tmp_path, RESOLVED_SECRET_VALUE) == []
    server.batch_responses.append(unavailable())
    async with _watcher(client, snapshots=False) as watcher:
        assert watcher.snapshot_store is None
        assert watcher.items() == {}


async def test_async_callbacks_errors_and_wait_semantics(
    client: sp.AsyncClient, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    def broken(item: sp.ConfigItem) -> None:
        raise RuntimeError("bug")

    def unused(item: sp.ConfigItem) -> None:
        pytest.fail("removed listener must not run")

    watcher = _watcher(client, on_change=broken)
    watcher.add_listener(unused)
    watcher.remove_listener(unused)
    with caplog.at_level(logging.ERROR, logger="spinneret.config"):
        await watcher.start()
    assert "change callback raised" in caplog.text
    assert await watcher.wait_for_change(timeout=0.05) is None
    waiter = asyncio.ensure_future(watcher.wait_for_change())
    await asyncio.sleep(0.02)
    await watcher.stop()
    assert await asyncio.wait_for(waiter, 3) is None
    await watcher.stop()


async def test_async_stop_cancels_stuck_poll(settings: sp.Settings) -> None:
    started = asyncio.Event()

    async def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path.endswith("/BatchGetConfig"):
            return httpx.Response(200, json={"items": [config_item()]})
        started.set()
        await asyncio.sleep(30)
        return httpx.Response(200, json={"items": []})

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as http:
        client = sp.AsyncClient(settings=settings, http_client=http)
        watcher = client.config_watcher(["crawler/search.json"], timeout_ms=1000)
        await watcher.start()
        await asyncio.wait_for(started.wait(), 3)
        begin = time.monotonic()
        await client.aclose()
        assert time.monotonic() - begin < 3
        assert not watcher.running


async def test_async_watcher_stops_when_its_client_is_closed(
    settings: sp.Settings, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    http = httpx.AsyncClient(transport=httpx.MockTransport(server.async_handler))
    client = sp.AsyncClient(settings=settings, http_client=http, retry=sp.NO_RETRY)
    watcher = await _watcher(client).start()
    with caplog.at_level(logging.INFO, logger="spinneret.config"):
        await http.aclose()
        await _wait_until(lambda: not watcher.running)
    assert "client has been closed" in caplog.text
    assert await watcher.wait_for_change(timeout=0.5) is None
    await client.aclose()
