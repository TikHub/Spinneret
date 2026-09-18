"""asyncio-based config watcher with long polling and local snapshots."""

from __future__ import annotations

import asyncio
import contextlib
import inspect
import logging
from collections.abc import Awaitable, Callable, Iterable
from os import PathLike
from pathlib import Path
from types import TracebackType
from typing import TYPE_CHECKING

from . import _calls as calls
from ._calls import ConfigKeyLike
from ._retry import NO_RETRY, is_retryable_background_error
from ._snapshot import SecretPredicate, SnapshotStore
from ._watch_core import WatchOptions, WatchState
from .errors import REASON_CLIENT_CLOSED, SpinneretError
from .models import ConfigItem

if TYPE_CHECKING:
    from .async_client import AsyncClient

__all__ = ["AsyncChangeCallback", "AsyncConfigWatcher"]

logger = logging.getLogger("spinneret.config")

#: Callback invoked with every changed item; may be a plain function or a coroutine function.
AsyncChangeCallback = Callable[[ConfigItem], Awaitable[None] | None]

_STOP_TIMEOUT = 1.0


class AsyncConfigWatcher:
    """asyncio counterpart of :class:`spinneret.ConfigWatcher`.

    The watch loop runs in an asyncio task; snapshot file I/O runs in worker
    threads. Callbacks may be regular functions or coroutine functions.

    Example::

        async with client.config_watcher(["crawler/search.json"]) as watcher:
            item = watcher.get("crawler", "search.json")
            changed = await watcher.wait_for_change(timeout=60)
    """

    def __init__(
        self,
        client: AsyncClient,
        items: Iterable[ConfigKeyLike],
        *,
        namespace: str = "",
        timeout_ms: int = 30_000,
        cache_dir: str | PathLike[str] | None = None,
        snapshots: bool = True,
        cache_secrets: bool = False,
        treat_as_secret: SecretPredicate | None = None,
        on_change: AsyncChangeCallback | None = None,
        options: WatchOptions | None = None,
    ) -> None:
        """Create a watcher (not started); see :class:`spinneret.ConfigWatcher`.

        Raises:
            ConfigurationError: ``cache_secrets`` is set but ``cryptography`` is missing.
            ValueError: No items or more than 200 items were given.
        """
        self._client = client
        self._namespace = namespace
        self._options = options or WatchOptions(timeout_ms=timeout_ms)
        self._state = WatchState(calls.to_config_keys(items))
        settings = client.settings
        self._store: SnapshotStore | None = None
        if snapshots:
            self._store = SnapshotStore(
                Path(cache_dir) if cache_dir is not None else settings.cache_dir,
                host=settings.host,
                namespace=namespace,
                token=settings.token,
                cache_secrets=cache_secrets,
                treat_as_secret=treat_as_secret,
            )
        self._listeners: list[AsyncChangeCallback] = [on_change] if on_change else []
        self._task: asyncio.Task[None] | None = None
        self._changed: asyncio.Condition | None = None
        self._stopped: asyncio.Event | None = None
        self._loaded = False
        self._from_snapshot = False

    @property
    def from_snapshot(self) -> bool:
        """Whether the current items come from local snapshots (server not confirmed yet)."""
        return self._from_snapshot

    @property
    def running(self) -> bool:
        """Whether the watch task is running."""
        return self._task is not None and not self._task.done()

    @property
    def snapshot_store(self) -> SnapshotStore | None:
        """Snapshot store, or ``None`` when snapshots are disabled."""
        return self._store

    def get(self, group: str, key: str) -> ConfigItem | None:
        """Latest known item, or ``None`` when it is unknown or missing."""
        return self._state.get(group, key)

    def items(self) -> dict[tuple[str, str], ConfigItem]:
        """Copy of all known items keyed by ``(group, key)``."""
        return self._state.items()

    def add_listener(self, callback: AsyncChangeCallback) -> None:
        """Register a callback invoked with every changed item."""
        self._listeners = [*self._listeners, callback]

    def remove_listener(self, callback: AsyncChangeCallback) -> None:
        """Unregister a callback (no-op when unknown)."""
        self._listeners = [cb for cb in self._listeners if cb is not callback]

    async def start(self, *, wait: bool = True) -> AsyncConfigWatcher:
        """Load the items and start the watch task; see :meth:`ConfigWatcher.start`."""
        self._ensure_primitives()
        if self._is_stopped():
            raise RuntimeError("a stopped AsyncConfigWatcher cannot be restarted")
        if self._task is not None:
            return self
        if wait:
            await self._initial_load()
        self._task = asyncio.get_running_loop().create_task(
            self._run(), name="spinneret-config-watcher"
        )
        return self

    async def stop(self, timeout: float = _STOP_TIMEOUT) -> None:
        """Stop the watch task, cancelling an in-flight long poll after ``timeout``."""
        self._ensure_primitives()
        if self._stopped is not None:
            self._stopped.set()
        await self._notify()
        task = self._task
        if task is None or task.done() or task is asyncio.current_task():
            return
        done, _ = await asyncio.wait({task}, timeout=timeout)
        if not done:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task

    async def wait_for_change(
        self,
        group: str | None = None,
        key: str | None = None,
        timeout: float | None = None,
    ) -> ConfigItem | None:
        """Wait until an item (optionally a specific one) changes after this call.

        Returns:
            The changed item, or ``None`` on timeout or when the watcher stops.
        """
        self._ensure_primitives()
        changed = self._changed
        stopped = self._stopped
        if changed is None or stopped is None:  # pragma: no cover - set by _ensure_primitives
            return None
        start = self._state.sequence
        found: list[ConfigItem] = []

        def ready() -> bool:
            item = self._state.changed_since(start, group, key)
            if item is not None:
                found.append(item)
                return True
            return stopped.is_set()

        with contextlib.suppress(asyncio.TimeoutError):
            async with changed:
                await asyncio.wait_for(changed.wait_for(ready), timeout=timeout)
        return found[0] if found else None

    async def __aenter__(self) -> AsyncConfigWatcher:
        return await self.start()

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        await self.stop()

    # -- internals ----------------------------------------------------------

    def _ensure_primitives(self) -> None:
        if self._changed is None:
            self._changed = asyncio.Condition()
            self._stopped = asyncio.Event()

    def _is_stopped(self) -> bool:
        return self._stopped is not None and self._stopped.is_set()

    async def _notify(self) -> None:
        if self._changed is None:
            return
        async with self._changed:
            self._changed.notify_all()

    async def _initial_load(self) -> None:
        keys = self._state.keys
        try:
            response = await self._client.batch_get_config(keys, namespace=self._namespace)
        except SpinneretError as exc:
            if not is_retryable_background_error(exc):
                raise
            logger.warning("spinneret config server unavailable, using local snapshots: %s", exc)
            loaded: list[ConfigItem] = []
            store = self._store
            if store is not None:
                for k in keys:
                    item = await asyncio.to_thread(store.load, k.group, k.key)
                    if item is not None:
                        loaded.append(item)
            await self._apply(loaded, persist=False, from_snapshot=True)
        else:
            if response.missing:
                logger.info(
                    "spinneret config items not found: %s",
                    ", ".join(f"{k.group}/{k.key}" for k in response.missing),
                )
            await self._apply(response.items, persist=True, from_snapshot=False)
        self._loaded = True

    async def _apply(
        self, items: Iterable[ConfigItem], *, persist: bool, from_snapshot: bool
    ) -> None:
        changed = self._state.apply(items)
        self._from_snapshot = from_snapshot
        await self._notify()
        store = self._store
        if persist and store is not None:
            for item in changed:
                await asyncio.to_thread(store.save, item)
        if self._is_stopped():
            return
        for item in changed:
            for callback in list(self._listeners):
                try:
                    result = callback(item)
                    if inspect.isawaitable(result):
                        await result
                except Exception:
                    logger.exception(
                        "spinneret config change callback raised for %s/%s", item.group, item.key
                    )

    async def _run(self) -> None:
        failures = 0
        stopped = self._stopped
        while not self._is_stopped():
            try:
                if not self._loaded:
                    await self._initial_load()
                    continue
                call = calls.watch_config(
                    self._namespace,
                    self._state.watch_items(),
                    self._options.timeout_ms,
                    self._client.timeouts.watch_grace,
                )
                response = await self._client._call(call, retry=NO_RETRY)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                if self._is_stopped():
                    return
                if isinstance(exc, SpinneretError) and exc.reason == REASON_CLIENT_CLOSED:
                    logger.info("spinneret config watcher stopped: the client has been closed")
                    if stopped is not None:
                        stopped.set()
                    await self._notify()
                    return
                delay = self._options.backoff.delay(failures)
                failures += 1
                if isinstance(exc, SpinneretError) and is_retryable_background_error(exc):
                    logger.warning(
                        "spinneret config watch failed, retrying in %.1fs: %s", delay, exc
                    )
                else:
                    logger.error(
                        "spinneret config watch failed, retrying in %.1fs", delay, exc_info=exc
                    )
                if stopped is not None:
                    with contextlib.suppress(asyncio.TimeoutError):
                        await asyncio.wait_for(stopped.wait(), timeout=delay)
                continue
            failures = 0
            await self._apply(response.items, persist=True, from_snapshot=False)
