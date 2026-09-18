"""Thread-based config watcher with long polling and local snapshots."""

from __future__ import annotations

import logging
import threading
import time
from collections.abc import Callable, Iterable
from os import PathLike
from pathlib import Path
from types import TracebackType
from typing import TYPE_CHECKING, Optional, Union

from . import _calls as calls
from ._calls import ConfigKeyLike
from ._retry import NO_RETRY, is_retryable_background_error
from ._snapshot import SecretPredicate, SnapshotStore
from ._watch_core import WatchOptions, WatchState
from .errors import REASON_CLIENT_CLOSED, SpinneretError
from .models import ConfigItem, WatchItem

if TYPE_CHECKING:
    from .client import Client

__all__ = ["ChangeCallback", "ConfigWatcher"]

logger = logging.getLogger("spinneret.config")

#: Callback invoked with every changed config item.
ChangeCallback = Callable[[ConfigItem], None]

_STOP_TIMEOUT = 1.0


class ConfigWatcher:
    """Keeps a set of config items up to date with ``ConfigService/WatchConfig``.

    :meth:`start` loads the items with ``BatchGetConfig``; when the server is
    unavailable it falls back to the local snapshots. A daemon thread then
    long-polls for changes, stores the latest items, writes snapshots
    atomically and invokes the change callbacks. Callbacks run on the thread
    that applied the change: the caller of :meth:`start` for the initial
    values, the watcher thread afterwards. They must not block for long.

    Secret items are kept in memory only, unless ``cache_secrets=True``
    (requires ``cryptography``), in which case their snapshots are encrypted
    with a key derived from the API token. An item is secret when the server
    sets ``has_secret_refs`` (its published content contained resolved
    ``${secret:...}`` references) or when ``treat_as_secret`` returns true.
    Use the predicate for items whose content embeds sensitive values
    directly instead of referencing secrets.

    Example::

        with client.config_watcher(["crawler/search.json"]) as watcher:
            item = watcher.get("crawler", "search.json")
    """

    def __init__(
        self,
        client: Client,
        items: Iterable[ConfigKeyLike],
        *,
        namespace: str = "",
        timeout_ms: int = 30_000,
        cache_dir: Optional[Union[str, PathLike[str]]] = None,
        snapshots: bool = True,
        cache_secrets: bool = False,
        treat_as_secret: Optional[SecretPredicate] = None,
        on_change: Optional[ChangeCallback] = None,
        options: Optional[WatchOptions] = None,
    ) -> None:
        """Create a watcher (not started).

        Args:
            client: Client used for the calls.
            items: Items to watch: :class:`ConfigKey`, ``(group, key)`` or ``"group/key"``.
            namespace: Namespace name; empty means the token namespace.
            timeout_ms: Long-poll timeout (1..60000); ignored when ``options`` is given.
            cache_dir: Snapshot directory (default: the client's ``cache_dir``).
            snapshots: Whether to read and write local snapshots.
            cache_secrets: Encrypt and persist secret items instead of skipping them.
            treat_as_secret: Predicate marking items as secret in addition to
                those flagged with ``has_secret_refs``, e.g.
                ``lambda item: item.group == "signing"``.
            on_change: Callback registered with :meth:`add_listener`.
            options: Advanced loop options.

        Raises:
            ConfigurationError: ``cache_secrets`` is set but ``cryptography`` is missing.
            ValueError: No items or more than 200 items were given.
        """
        self._client = client
        self._namespace = namespace
        self._options = options or WatchOptions(timeout_ms=timeout_ms)
        self._state = WatchState(calls.to_config_keys(items))
        settings = client.settings
        self._store: Optional[SnapshotStore] = None
        if snapshots:
            self._store = SnapshotStore(
                Path(cache_dir) if cache_dir is not None else settings.cache_dir,
                host=settings.host,
                namespace=namespace,
                token=settings.token,
                cache_secrets=cache_secrets,
                treat_as_secret=treat_as_secret,
            )
        self._listeners: list[ChangeCallback] = [on_change] if on_change is not None else []
        self._cond = threading.Condition()
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._loaded = False
        self._from_snapshot = False

    @property
    def from_snapshot(self) -> bool:
        """Whether the current items come from local snapshots (server not confirmed yet)."""
        with self._cond:
            return self._from_snapshot

    @property
    def running(self) -> bool:
        """Whether the watch thread is running."""
        return self._thread is not None and self._thread.is_alive()

    @property
    def snapshot_store(self) -> Optional[SnapshotStore]:
        """Snapshot store, or ``None`` when snapshots are disabled."""
        return self._store

    def get(self, group: str, key: str) -> Optional[ConfigItem]:
        """Latest known item, or ``None`` when it is unknown or missing."""
        with self._cond:
            return self._state.get(group, key)

    def items(self) -> dict[tuple[str, str], ConfigItem]:
        """Copy of all known items keyed by ``(group, key)``."""
        with self._cond:
            return self._state.items()

    def add_listener(self, callback: ChangeCallback) -> None:
        """Register a callback invoked with every changed item."""
        with self._cond:
            self._listeners = [*self._listeners, callback]

    def remove_listener(self, callback: ChangeCallback) -> None:
        """Unregister a callback (no-op when unknown)."""
        with self._cond:
            self._listeners = [cb for cb in self._listeners if cb is not callback]

    def start(self, *, wait: bool = True) -> ConfigWatcher:
        """Load the items and start the watch thread.

        Args:
            wait: Perform the initial load before returning (errors other than
                server unavailability are raised). When ``False`` the load
                happens in the background thread.

        Raises:
            SpinneretError: The initial load failed for a reason other than
                server unavailability (for example ``permission_denied``).
        """
        if self._stop.is_set():
            raise RuntimeError("a stopped ConfigWatcher cannot be restarted")
        if self._thread is not None:
            return self
        if wait:
            self._initial_load()
        thread = threading.Thread(target=self._run, name="spinneret-config-watcher", daemon=True)
        self._thread = thread
        thread.start()
        return self

    def stop(self, timeout: float = _STOP_TIMEOUT) -> None:
        """Stop the watch loop.

        An in-flight long poll cannot be interrupted; the thread exits when it
        returns (callbacks are not invoked after ``stop``).
        """
        self._stop.set()
        with self._cond:
            self._cond.notify_all()
        thread = self._thread
        if thread is not None and thread is not threading.current_thread():
            thread.join(timeout)

    def wait_for_change(
        self,
        group: Optional[str] = None,
        key: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> Optional[ConfigItem]:
        """Block until an item (optionally a specific one) changes after this call.

        Returns:
            The changed item, or ``None`` on timeout or when the watcher stops.
        """
        deadline = None if timeout is None else time.monotonic() + timeout
        with self._cond:
            start = self._state.sequence
            while True:
                item = self._state.changed_since(start, group, key)
                if item is not None:
                    return item
                if self._stop.is_set():
                    return None
                remaining = None if deadline is None else deadline - time.monotonic()
                if remaining is not None and remaining <= 0:
                    return None
                self._cond.wait(remaining)

    def __enter__(self) -> ConfigWatcher:
        return self.start()

    def __exit__(
        self,
        exc_type: Optional[type[BaseException]],
        exc: Optional[BaseException],
        tb: Optional[TracebackType],
    ) -> None:
        self.stop()

    # -- internals ----------------------------------------------------------

    def _initial_load(self) -> None:
        keys = self._state.keys
        try:
            response = self._client.batch_get_config(keys, namespace=self._namespace)
        except SpinneretError as exc:
            if not is_retryable_background_error(exc):
                raise
            logger.warning("spinneret config server unavailable, using local snapshots: %s", exc)
            loaded = []
            if self._store is not None:
                loaded = [
                    item
                    for item in (self._store.load(k.group, k.key) for k in keys)
                    if item is not None
                ]
            self._apply(loaded, persist=False, from_snapshot=True)
        else:
            if response.missing:
                logger.info(
                    "spinneret config items not found: %s",
                    ", ".join(f"{k.group}/{k.key}" for k in response.missing),
                )
            self._apply(response.items, persist=True, from_snapshot=False)
        self._loaded = True

    def _apply(self, items: Iterable[ConfigItem], *, persist: bool, from_snapshot: bool) -> None:
        with self._cond:
            changed = self._state.apply(items)
            self._from_snapshot = from_snapshot
            listeners = list(self._listeners)
            self._cond.notify_all()
        if persist and self._store is not None:
            for item in changed:
                self._store.save(item)
        if self._stop.is_set():
            return
        for item in changed:
            for callback in listeners:
                try:
                    callback(item)
                except Exception:
                    logger.exception(
                        "spinneret config change callback raised for %s/%s", item.group, item.key
                    )

    def _run(self) -> None:
        failures = 0
        while not self._stop.is_set():
            try:
                if not self._loaded:
                    self._initial_load()
                    continue
                call = calls.watch_config(
                    self._namespace,
                    self._state_watch_items(),
                    self._options.timeout_ms,
                    self._client.timeouts.watch_grace,
                )
                response = self._client._call(call, retry=NO_RETRY)
            except Exception as exc:
                if self._stop.is_set():
                    return
                if isinstance(exc, SpinneretError) and exc.reason == REASON_CLIENT_CLOSED:
                    logger.info("spinneret config watcher stopped: the client has been closed")
                    self._stop.set()
                    with self._cond:
                        self._cond.notify_all()
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
                self._stop.wait(delay)
                continue
            failures = 0
            self._apply(response.items, persist=True, from_snapshot=False)

    def _state_watch_items(self) -> list[WatchItem]:
        with self._cond:
            return self._state.watch_items()
