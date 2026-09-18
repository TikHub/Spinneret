from __future__ import annotations

import logging
import threading
import time
from collections.abc import Iterator
from pathlib import Path

import httpx
import pytest

import spinneret as sp
from spinneret._snapshot import SnapshotStore

from ._config_server import ConfigServer, denied, unavailable
from ._helpers import (
    BASE_URL,
    RESOLVED_SECRET_CONTENT,
    RESOLVED_SECRET_VALUE,
    TOKEN,
    config_item,
    secret_config_item,
)

FAST = sp.WatchOptions(timeout_ms=1000, backoff=sp.Backoff(initial=0.01, maximum=0.02))


def _wait_until(predicate: object, timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():  # type: ignore[operator]
            return
        time.sleep(0.005)
    raise AssertionError("condition not met in time")


def _version(watcher: sp.ConfigWatcher, key: str) -> int:
    item = watcher.get("crawler", key)
    return item.version if item is not None else -1


@pytest.fixture
def server() -> ConfigServer:
    return ConfigServer(
        [config_item(version=1, content='{"v": 1}'), config_item(key="b.json", version=4)]
    )


@pytest.fixture
def client(settings: sp.Settings, server: ConfigServer) -> Iterator[sp.Client]:
    http = httpx.Client(transport=httpx.MockTransport(server.handler))
    client = sp.Client(settings=settings, http_client=http, retry=sp.NO_RETRY)
    yield client
    client.close()
    http.close()


def _watcher(client: sp.Client, **kwargs: object) -> sp.ConfigWatcher:
    params: dict[str, object] = {"options": FAST}
    params.update(kwargs)
    return sp.ConfigWatcher(client, ["crawler/search.json", ("crawler", "b.json")], **params)  # type: ignore[arg-type]


def test_start_loads_items_and_watches_changes(
    client: sp.Client, server: ConfigServer, settings: sp.Settings
) -> None:
    seen: list[tuple[str, int]] = []
    watcher = client.config_watcher(
        ["crawler/search.json", "crawler/b.json", "crawler/search.json"],
        timeout_ms=1000,
        on_change=lambda item: seen.append((item.key, item.version)),
    )
    with watcher:
        assert watcher.running
        assert not watcher.from_snapshot
        item = watcher.get("crawler", "search.json")
        assert item is not None
        assert item.version == 1
        assert set(watcher.items()) == {("crawler", "search.json"), ("crawler", "b.json")}
        assert sorted(seen) == [("b.json", 4), ("search.json", 1)]

        waiter_result: list[sp.ConfigItem | None] = []
        waiter = threading.Thread(
            target=lambda: waiter_result.append(
                watcher.wait_for_change("crawler", "search.json", timeout=3)
            )
        )
        waiter.start()
        time.sleep(0.05)
        server.publish(config_item(version=2, content='{"v": 2}'))
        waiter.join(5)
        assert waiter_result[0] is not None
        assert waiter_result[0].version == 2
        _wait_until(lambda: ("search.json", 2) in seen)
        body = server.watch_bodies[-1]
        assert body["timeout_ms"] == 1000
        assert {"group": "crawler", "key": "b.json", "version": 4} in body["items"]
        assert watcher.start() is watcher

    store = watcher.snapshot_store
    assert store is not None
    snapshot = store.load("crawler", "search.json")
    assert snapshot is not None
    assert snapshot.version == 2
    assert store.directory.parent.parent == settings.cache_dir
    with pytest.raises(RuntimeError, match="restarted"):
        watcher.start()


def test_falls_back_to_snapshots_when_server_is_unavailable(
    client: sp.Client, server: ConfigServer, tmp_path: Path
) -> None:
    store = SnapshotStore(tmp_path, host="spinneret.test", namespace="", token=TOKEN)
    store.save(sp.ConfigItem.model_validate(config_item(version=1, content='{"cached": true}')))
    server.batch_responses.append(unavailable())
    server.watch_responses.append(unavailable())
    watcher = _watcher(client, cache_dir=tmp_path)
    watcher.start()
    try:
        cached = watcher.get("crawler", "search.json")
        assert cached is not None
        assert cached.content == '{"cached": true}'
        assert watcher.get("crawler", "b.json") is None
        assert watcher.from_snapshot
        server.publish(config_item(version=5, content='{"fresh": true}'))
        _wait_until(lambda: _version(watcher, "search.json") == 5)
        _wait_until(lambda: not watcher.from_snapshot)
        # The snapshot version was sent so the server only returned newer content.
        assert server.watch_bodies[0]["items"][0]["version"] == 1
    finally:
        watcher.stop()


def test_permanent_initial_error_is_raised(client: sp.Client, server: ConfigServer) -> None:
    server.batch_responses.append(denied())
    watcher = _watcher(client)
    with pytest.raises(sp.PermissionDenied):
        watcher.start()
    assert not watcher.running


def test_background_initial_load_retries(
    client: sp.Client, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    server.batch_responses.extend(
        [
            denied(),
            httpx.Response(
                200, json={"items": [], "missing": [{"group": "crawler", "key": "search.json"}]}
            ),
        ]
    )
    caplog.set_level(logging.INFO, logger="spinneret.config")
    watcher = _watcher(client)
    watcher.start(wait=False)
    try:
        _wait_until(lambda: len(server.watch_bodies) >= 1)
        assert server.batch_calls == 2
        assert "config items not found" in caplog.text
        assert "config watch failed" in caplog.text
    finally:
        watcher.stop()


def test_watch_errors_back_off_and_recover(
    client: sp.Client, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.WARNING, logger="spinneret.config")
    with _watcher(client) as watcher:
        with server.lock:
            server.watch_responses.extend(
                [unavailable(), httpx.Response(200, content=b"garbage"), denied()]
            )
        server.publish(config_item(version=3))
        _wait_until(lambda: _version(watcher, "search.json") == 3)
    assert "retrying" in caplog.text


def _files_containing(root: Path, needle: str) -> list[Path]:
    return [p for p in root.rglob("*") if p.is_file() and needle in p.read_text()]


def test_secret_caching_policy(client: sp.Client, server: ConfigServer, tmp_path: Path) -> None:
    server.items[("crawler", "search.json")] = secret_config_item(version=9)
    with _watcher(client, cache_dir=tmp_path / "plain") as watcher:
        store = watcher.snapshot_store
        assert store is not None
        item = watcher.get("crawler", "search.json")
        assert item is not None
        assert item.has_secret_refs
        assert item.content == RESOLVED_SECRET_CONTENT
        assert not store.path_for("crawler", "search.json").exists()
        assert store.path_for("crawler", "b.json").exists()
    assert _files_containing(tmp_path / "plain", RESOLVED_SECRET_VALUE) == []
    with _watcher(
        client, cache_dir=tmp_path / "pred", treat_as_secret=lambda i: i.key == "b.json"
    ) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        assert not store.path_for("crawler", "b.json").exists()
        assert not store.path_for("crawler", "search.json").exists()
    with _watcher(
        client, cache_dir=tmp_path / "exempt", treat_as_secret=lambda i: False
    ) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        assert not store.path_for("crawler", "search.json").exists()
    with _watcher(client, cache_dir=tmp_path / "enc", cache_secrets=True) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        path = store.path_for("crawler", "search.json")
        assert path.exists()
        assert RESOLVED_SECRET_VALUE not in path.read_text()
        loaded = store.load("crawler", "search.json")
        assert loaded is not None
        assert loaded.has_secret_refs
        assert loaded.content == RESOLVED_SECRET_CONTENT
    assert _files_containing(tmp_path / "enc", RESOLVED_SECRET_VALUE) == []


def test_item_gaining_secret_refs_removes_plain_snapshot(
    client: sp.Client, server: ConfigServer, tmp_path: Path
) -> None:
    with _watcher(client, cache_dir=tmp_path) as watcher:
        store = watcher.snapshot_store
        assert store is not None
        path = store.path_for("crawler", "search.json")
        assert path.exists()
        server.publish(secret_config_item(version=2))
        _wait_until(lambda: _version(watcher, "search.json") == 2)
        _wait_until(lambda: not path.exists())
        server.publish(config_item(version=3, content='{"v": 3}'))
        _wait_until(lambda: path.exists())
        loaded = store.load("crawler", "search.json")
        assert loaded is not None
        assert (loaded.version, loaded.has_secret_refs) == (3, False)
    assert _files_containing(tmp_path, RESOLVED_SECRET_VALUE) == []


def test_encrypted_secret_snapshot_is_used_when_server_is_unavailable(
    client: sp.Client, server: ConfigServer, tmp_path: Path
) -> None:
    item = sp.ConfigItem.model_validate(secret_config_item(version=3))
    encrypted = SnapshotStore(
        tmp_path, host="spinneret.test", namespace="", token=TOKEN, cache_secrets=True
    )
    assert encrypted.save(item)
    server.batch_responses.extend([unavailable(), unavailable()])
    with _watcher(client, cache_dir=tmp_path, cache_secrets=True) as watcher:
        assert watcher.get("crawler", "search.json") == item
    # Without cache_secrets the encrypted snapshot is not readable.
    with _watcher(client, cache_dir=tmp_path) as watcher:
        assert watcher.get("crawler", "search.json") is None
    assert server.batch_calls == 2


def test_snapshots_can_be_disabled(client: sp.Client, server: ConfigServer) -> None:
    server.batch_responses.append(unavailable())
    with _watcher(client, snapshots=False) as watcher:
        assert watcher.snapshot_store is None
        assert watcher.items() == {}
        assert watcher.from_snapshot


def test_listeners_and_callback_errors(
    client: sp.Client, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    calls: list[str] = []

    def broken(item: sp.ConfigItem) -> None:
        raise RuntimeError("bug")

    def recorder(item: sp.ConfigItem) -> None:
        calls.append(item.key)

    watcher = _watcher(client)
    watcher.add_listener(broken)
    watcher.add_listener(recorder)
    watcher.remove_listener(recorder)
    watcher.remove_listener(recorder)
    with caplog.at_level(logging.ERROR, logger="spinneret.config"), watcher:
        pass
    assert calls == []
    assert "change callback raised" in caplog.text


def test_wait_for_change_timeout_and_stop(client: sp.Client, server: ConfigServer) -> None:
    watcher = _watcher(client).start()
    assert watcher.wait_for_change(timeout=0.05) is None
    result: list[sp.ConfigItem | None] = [sp.ConfigItem()]
    waiter = threading.Thread(target=lambda: result.__setitem__(0, watcher.wait_for_change()))
    waiter.start()
    time.sleep(0.05)
    watcher.stop()
    waiter.join(3)
    assert result == [None]
    assert watcher.wait_for_change(timeout=1) is None


def test_client_close_stops_watchers(settings: sp.Settings, server: ConfigServer) -> None:
    http = httpx.Client(transport=httpx.MockTransport(server.handler))
    client = sp.Client(settings=settings, http_client=http)
    watcher = client.config_watcher(["crawler/search.json"], timeout_ms=1000).start()
    client.close()
    _wait_until(lambda: not watcher.running)
    http.close()


def test_watcher_requires_items(client: sp.Client) -> None:
    with pytest.raises(ValueError, match="at least one"):
        sp.ConfigWatcher(client, [])
    with pytest.raises(ValueError, match="at most 200"):
        sp.ConfigWatcher(client, [f"g/k{i}" for i in range(201)])
    with pytest.raises(ValueError, match="timeout_ms"):
        sp.WatchOptions(timeout_ms=0)
    assert BASE_URL.endswith(client.settings.host)


def test_watcher_stops_when_its_client_is_closed(
    settings: sp.Settings, server: ConfigServer, caplog: pytest.LogCaptureFixture
) -> None:
    http = httpx.Client(transport=httpx.MockTransport(server.handler))
    client = sp.Client(settings=settings, http_client=http, retry=sp.NO_RETRY)
    watcher = _watcher(client).start()
    with caplog.at_level(logging.INFO, logger="spinneret.config"):
        http.close()
        _wait_until(lambda: not watcher.running)
    assert "client has been closed" in caplog.text
    assert watcher.wait_for_change(timeout=0.5) is None
    client.close()
