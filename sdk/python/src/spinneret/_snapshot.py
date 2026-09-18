"""Local on-disk snapshots of config items, written atomically."""

from __future__ import annotations

import contextlib
import json
import logging
import os
import tempfile
from collections.abc import Callable
from pathlib import Path
from typing import Any
from urllib.parse import quote

from pydantic import ValidationError

from ._crypto import SnapshotCipher, SnapshotDecryptError
from .models import ConfigItem, format_timestamp, utc_now

__all__ = ["SNAPSHOT_FORMAT", "SecretPredicate", "SnapshotStore"]

logger = logging.getLogger("spinneret.config")

#: Version of the snapshot file layout.
SNAPSHOT_FORMAT = 1

_DEFAULT_NAMESPACE_DIR = "_token_namespace"
_PRIVATE_DIR_MODE = 0o700

#: Decides whether a config item holds secret material that must not be stored in clear text.
SecretPredicate = Callable[[ConfigItem], bool]


def _component(name: str) -> str:
    """Encode an arbitrary string as a single safe path component."""
    encoded = quote(name, safe="")
    if encoded.startswith("."):
        encoded = "%2E" + encoded[1:]
    return encoded or "%00"


def _make_private_dirs(directory: Path) -> None:
    """Create ``directory`` and its missing parents, each with mode ``0700``.

    ``Path.mkdir(parents=True, mode=...)`` applies the mode to the leaf only, which
    would leave intermediate cache directories (named after hosts, namespaces and
    groups) readable by other users.
    """
    missing: list[Path] = []
    current = directory
    while not current.exists() and current != current.parent:
        missing.append(current)
        current = current.parent
    for part in reversed(missing):
        with contextlib.suppress(FileExistsError):
            part.mkdir(mode=_PRIVATE_DIR_MODE)
    if not directory.is_dir():
        raise NotADirectoryError(f"snapshot directory {directory} is not a directory")


def _atomic_write(path: Path, data: bytes) -> None:
    _make_private_dirs(path.parent)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise
    _fsync_directory(path.parent)


def _fsync_directory(directory: Path) -> None:
    if os.name != "posix":  # pragma: no cover - directory fsync is POSIX only
        return
    with contextlib.suppress(OSError):
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


class SnapshotStore:
    """Stores the latest version of each watched config item under a cache directory.

    Layout: ``<root>/<host>/<namespace>/<group>/<key>.json`` with every
    component percent-encoded. Secret items are never written in clear text:
    they are skipped (and any older snapshot removed) unless ``cache_secrets``
    is enabled, in which case they are encrypted with a key derived from the
    token. An item is secret when the server set
    :attr:`ConfigItem.has_secret_refs` (see :attr:`ConfigItem.references_secrets`)
    or the optional ``treat_as_secret`` predicate returns true (a predicate
    that raises marks the item as secret). The predicate can only add secret
    items; it cannot exempt an item flagged by the server.
    """

    def __init__(
        self,
        root: Path,
        *,
        host: str,
        namespace: str,
        token: str,
        cache_secrets: bool = False,
        treat_as_secret: SecretPredicate | None = None,
    ) -> None:
        """Create a store.

        Raises:
            ConfigurationError: ``cache_secrets`` is set but ``cryptography`` is missing.
        """
        self._dir = Path(root) / _component(host) / _component(namespace or _DEFAULT_NAMESPACE_DIR)
        self._cipher: SnapshotCipher | None = SnapshotCipher(token) if cache_secrets else None
        self._treat_as_secret = treat_as_secret

    @property
    def directory(self) -> Path:
        """Directory holding the snapshots of this host and namespace."""
        return self._dir

    def path_for(self, group: str, key: str) -> Path:
        """Snapshot file of a config item."""
        return self._dir / _component(group) / f"{_component(key)}.json"

    def is_secret(self, item: ConfigItem) -> bool:
        """Whether ``item`` must only be persisted encrypted.

        The server flag is checked first, so ``treat_as_secret`` is not called
        for items that already are secret.
        """
        if item.references_secrets:
            return True
        if self._treat_as_secret is None:
            return False
        try:
            return bool(self._treat_as_secret(item))
        except Exception:
            logger.exception(
                "spinneret treat_as_secret predicate raised for %s/%s; treating as secret",
                item.group,
                item.key,
            )
            return True

    def _aad(self, group: str, key: str) -> bytes:
        return f"{group}\x00{key}".encode()

    def save(self, item: ConfigItem) -> bool:
        """Write a snapshot of ``item``.

        Returns:
            ``True`` when a snapshot was written, ``False`` when the item was
            skipped by the secret caching policy or the write failed.
        """
        path = self.path_for(item.group, item.key)
        document: dict[str, Any] = {
            "format": SNAPSHOT_FORMAT,
            "saved_at": format_timestamp(utc_now()),
        }
        try:
            if self.is_secret(item):
                if self._cipher is None:
                    self._remove(path)
                    return False
                plaintext = item.model_dump_json().encode("utf-8")
                document["encrypted"] = True
                document["envelope"] = self._cipher.encrypt(
                    plaintext, self._aad(item.group, item.key)
                )
            else:
                document["encrypted"] = False
                document["item"] = item.model_dump(mode="json")
            _atomic_write(path, json.dumps(document, ensure_ascii=False).encode("utf-8"))
        except OSError as exc:
            logger.warning(
                "spinneret could not write config snapshot %s/%s: %s", item.group, item.key, exc
            )
            return False
        return True

    def load(self, group: str, key: str) -> ConfigItem | None:
        """Read the snapshot of a config item, or ``None`` when unavailable."""
        path = self.path_for(group, key)
        try:
            raw = path.read_bytes()
        except FileNotFoundError:
            return None
        except OSError as exc:
            logger.warning("spinneret could not read config snapshot %s/%s: %s", group, key, exc)
            return None
        try:
            item = self._decode(raw, group, key)
        except (ValueError, ValidationError, SnapshotDecryptError) as exc:
            logger.warning(
                "spinneret ignored invalid config snapshot %s/%s: %s",
                group,
                key,
                type(exc).__name__,
            )
            return None
        if item is not None and (item.group != group or item.key != key):
            logger.warning("spinneret ignored mismatched config snapshot %s/%s", group, key)
            return None
        return item

    def _decode(self, raw: bytes, group: str, key: str) -> ConfigItem | None:
        document = json.loads(raw.decode("utf-8"))
        if not isinstance(document, dict) or document.get("format") != SNAPSHOT_FORMAT:
            raise ValueError("unsupported snapshot format")
        if document.get("encrypted"):
            if self._cipher is None:
                return None
            envelope = document.get("envelope")
            if not isinstance(envelope, dict):
                raise ValueError("encrypted snapshot without envelope")
            plaintext = self._cipher.decrypt(envelope, self._aad(group, key))
            return ConfigItem.model_validate_json(plaintext)
        return ConfigItem.model_validate(document.get("item"))

    def _remove(self, path: Path) -> None:
        with contextlib.suppress(FileNotFoundError):
            path.unlink()
