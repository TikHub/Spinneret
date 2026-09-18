"""State of a config watch shared by the thread and asyncio watchers."""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from typing import Optional

from ._retry import Backoff
from .models import MAX_CONFIG_ITEMS, ConfigItem, ConfigKey, WatchItem

__all__ = ["WatchOptions", "WatchState"]

_Key = tuple[str, str]
_DEFAULT_BACKOFF = Backoff(initial=1.0, maximum=30.0)


@dataclass(frozen=True)
class WatchOptions:
    """Tuning of a config watcher loop.

    Attributes:
        timeout_ms: Long-poll timeout sent to the server (max 60000).
        backoff: Backoff between failed polls.
    """

    timeout_ms: int = 30_000
    backoff: Backoff = _DEFAULT_BACKOFF

    def __post_init__(self) -> None:
        if not 0 < self.timeout_ms <= 60_000:
            raise ValueError("timeout_ms must be within 1..60000")


class WatchState:
    """Latest known version of every watched item plus a change sequence.

    Not synchronized: the thread watcher guards it with a lock and the asyncio
    watcher only touches it from the event loop.
    """

    def __init__(self, keys: Sequence[ConfigKey]) -> None:
        unique: dict[_Key, ConfigKey] = {}
        for key in keys:
            unique.setdefault((key.group, key.key), key)
        if not unique:
            raise ValueError("at least one config item must be watched")
        if len(unique) > MAX_CONFIG_ITEMS:
            raise ValueError(f"at most {MAX_CONFIG_ITEMS} config items can be watched")
        self._keys = list(unique.values())
        self._items: dict[_Key, ConfigItem] = {}
        self._changed_seq: dict[_Key, int] = {}
        self._seq = 0

    @property
    def keys(self) -> list[ConfigKey]:
        """Watched keys in declaration order."""
        return list(self._keys)

    @property
    def sequence(self) -> int:
        """Number of changes applied so far."""
        return self._seq

    def get(self, group: str, key: str) -> Optional[ConfigItem]:
        """Latest item, or ``None`` when unknown."""
        return self._items.get((group, key))

    def items(self) -> dict[_Key, ConfigItem]:
        """Copy of all known items keyed by ``(group, key)``."""
        return dict(self._items)

    def watch_items(self) -> list[WatchItem]:
        """Watch request items carrying the known versions."""
        result = []
        for key in self._keys:
            current = self._items.get((key.group, key.key))
            version = current.version if current is not None else 0
            result.append(WatchItem(group=key.group, key=key.key, version=max(0, version)))
        return result

    def apply(self, items: Iterable[ConfigItem]) -> list[ConfigItem]:
        """Store items that differ from the known ones and return them."""
        changed: list[ConfigItem] = []
        watched = {(key.group, key.key) for key in self._keys}
        for item in items:
            ident = (item.group, item.key)
            if ident not in watched:
                continue
            current = self._items.get(ident)
            if current is not None and (current.version, current.content) == (
                item.version,
                item.content,
            ):
                continue
            self._items[ident] = item
            self._seq += 1
            self._changed_seq[ident] = self._seq
            changed.append(item)
        return changed

    def changed_since(
        self, sequence: int, group: Optional[str] = None, key: Optional[str] = None
    ) -> Optional[ConfigItem]:
        """First item changed after ``sequence`` that matches the optional filter."""
        candidates = [
            (seq, ident)
            for ident, seq in self._changed_seq.items()
            if seq > sequence
            and (group is None or ident[0] == group)
            and (key is None or ident[1] == key)
        ]
        if not candidates:
            return None
        _, ident = min(candidates)
        return self._items[ident]
