from __future__ import annotations

import json
import logging
import os
import stat
from pathlib import Path

import pytest

import spinneret as sp
from spinneret import _crypto, _snapshot
from spinneret._crypto import SnapshotCipher, SnapshotDecryptError, crypto_available, hkdf_sha256
from spinneret._snapshot import SnapshotStore

from ._helpers import RESOLVED_SECRET_VALUE, TOKEN, config_item, secret_config_item


@pytest.mark.parametrize(
    ("ikm", "salt", "info", "length", "expected"),
    [
        (
            bytes([0x0B] * 22),
            bytes(range(0x0D)),
            bytes(range(0xF0, 0xFA)),
            42,
            "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865",
        ),
        (
            bytes([0x0B] * 22),
            b"",
            b"",
            42,
            "8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8",
        ),
    ],
)
def test_hkdf_rfc5869_vectors(
    ikm: bytes, salt: bytes, info: bytes, length: int, expected: str
) -> None:
    assert hkdf_sha256(ikm, salt=salt, info=info, length=length).hex() == expected


@pytest.mark.parametrize("length", [0, 255 * 32 + 1])
def test_hkdf_rejects_invalid_lengths(length: int) -> None:
    with pytest.raises(ValueError, match="length"):
        hkdf_sha256(b"k", salt=b"s", info=b"i", length=length)


def test_cipher_round_trip_and_failures() -> None:
    assert crypto_available()
    cipher = SnapshotCipher(TOKEN)
    envelope = cipher.encrypt(b"plaintext", b"aad")
    assert envelope["alg"] == _crypto.ALGORITHM
    assert b"plaintext" not in json.dumps(envelope).encode()
    assert cipher.decrypt(envelope, b"aad") == b"plaintext"
    second = cipher.encrypt(b"plaintext", b"aad")
    assert second["salt"] != envelope["salt"]
    with pytest.raises(SnapshotDecryptError):
        SnapshotCipher("spn_other").decrypt(envelope, b"aad")
    with pytest.raises(SnapshotDecryptError):
        cipher.decrypt(envelope, b"other-aad")
    for broken in (
        {**envelope, "alg": "rot13"},
        {k: v for k, v in envelope.items() if k != "salt"},
        {**envelope, "nonce": "***"},
        {**envelope, "ciphertext": 5},
    ):
        with pytest.raises(SnapshotDecryptError):
            cipher.decrypt(broken, b"aad")


def test_cipher_requires_cryptography(monkeypatch: pytest.MonkeyPatch) -> None:
    def missing(name: str) -> object:
        raise ImportError(name)

    monkeypatch.setattr(_crypto.importlib, "import_module", missing)
    assert not crypto_available()
    with pytest.raises(sp.ConfigurationError, match="cryptography") as info:
        SnapshotCipher(TOKEN)
    assert info.value.reason == "crypto_unavailable"
    with pytest.raises(sp.ConfigurationError):
        SnapshotStore(Path("/unused"), host="h", namespace="", token=TOKEN, cache_secrets=True)
    # Without cache_secrets the store works without cryptography.
    SnapshotStore(Path("/unused"), host="h", namespace="", token=TOKEN)


def _store(root: Path, **kwargs: object) -> SnapshotStore:
    params: dict[str, object] = {"host": "spinneret.test:8080", "namespace": "prod", "token": TOKEN}
    params.update(kwargs)
    return SnapshotStore(root, **params)  # type: ignore[arg-type]


def test_plain_snapshot_round_trip(tmp_path: Path) -> None:
    store = _store(tmp_path)
    item = sp.ConfigItem.model_validate(config_item(version=7, content='{"a": 1}'))
    assert store.save(item)
    path = store.path_for("crawler", "search.json")
    assert path == tmp_path / "spinneret.test%3A8080" / "prod" / "crawler" / "search.json.json"
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert store.directory == tmp_path / "spinneret.test%3A8080" / "prod"
    document = json.loads(path.read_text())
    assert document["encrypted"] is False
    assert document["item"]["version"] == 7
    assert store.load("crawler", "search.json") == item
    assert store.load("crawler", "absent.json") is None
    assert [p.name for p in path.parent.iterdir()] == ["search.json.json"]


@pytest.mark.parametrize("name", ["../escape", ".hidden", "..", "", "a/b", "ключ"])
def test_path_components_are_contained(tmp_path: Path, name: str) -> None:
    store = _store(tmp_path, namespace="")
    path = store.path_for(name, name)
    assert path.resolve().is_relative_to(store.directory.resolve())
    assert store.directory.name == "_token_namespace"
    item = sp.ConfigItem(group=name, key=name, version=1, content="x")
    assert store.save(item)
    assert store.load(name, name) == item


def test_flagged_items_are_not_written_without_cache_secrets(tmp_path: Path) -> None:
    store = _store(tmp_path)
    plain = sp.ConfigItem.model_validate(config_item(version=1, content="{}"))
    secret = sp.ConfigItem.model_validate(secret_config_item(version=2))
    assert store.save(plain)
    assert store.path_for("crawler", "search.json").exists()
    assert store.is_secret(secret)
    assert not store.save(secret)
    # The older plain snapshot of the same item is removed.
    assert not store.path_for("crawler", "search.json").exists()
    assert store.load("crawler", "search.json") is None
    assert not store.save(secret)  # nothing to remove
    for path in tmp_path.rglob("*"):
        assert not path.is_file() or RESOLVED_SECRET_VALUE not in path.read_text()


def test_unresolved_secret_markers_are_treated_as_secret(tmp_path: Path) -> None:
    store = _store(tmp_path)
    marker = sp.ConfigItem.model_validate(config_item(content='{"k": "${secret:signing/api_key}"}'))
    assert not marker.has_secret_refs
    assert not store.save(marker)
    assert not store.path_for("crawler", "search.json").exists()


def test_flagged_items_are_encrypted_with_cache_secrets(tmp_path: Path) -> None:
    store = _store(tmp_path, cache_secrets=True)
    secret = sp.ConfigItem.model_validate(secret_config_item(version=2))
    assert store.save(secret)
    raw = store.path_for("crawler", "search.json").read_text()
    assert RESOLVED_SECRET_VALUE not in raw
    assert "item" not in json.loads(raw)
    assert json.loads(raw)["encrypted"] is True
    loaded = store.load("crawler", "search.json")
    assert loaded == secret
    assert loaded is not None
    assert loaded.has_secret_refs
    # Non-secret items stay in plain text even with cache_secrets.
    plain = sp.ConfigItem.model_validate(config_item(key="plain.json"))
    assert store.save(plain)
    assert json.loads(store.path_for("crawler", "plain.json").read_text())["encrypted"] is False
    # A different token (rotation) cannot decrypt; a store without cache_secrets skips it.
    assert (
        _store(tmp_path, cache_secrets=True, token="spn_rotated").load("crawler", "search.json")
        is None
    )
    assert _store(tmp_path).load("crawler", "search.json") is None


@pytest.mark.parametrize(
    "content",
    [
        b"not json",
        b"[]",
        b'{"format": 99}',
        b'{"format": 1, "encrypted": false, "item": {"version": "x"}}',
        b'{"format": 1, "encrypted": true, "envelope": "nope"}',
        b'{"format": 1, "encrypted": false, "item": {"group": "other", "key": "k"}}',
    ],
)
def test_invalid_snapshots_are_ignored(
    tmp_path: Path, content: bytes, caplog: pytest.LogCaptureFixture
) -> None:
    store = _store(tmp_path, cache_secrets=True)
    path = store.path_for("crawler", "search.json")
    path.parent.mkdir(parents=True)
    path.write_bytes(content)
    with caplog.at_level(logging.WARNING, logger="spinneret.config"):
        assert store.load("crawler", "search.json") is None
    assert "snapshot" in caplog.text


def test_io_errors_are_reported(tmp_path: Path, caplog: pytest.LogCaptureFixture) -> None:
    blocker = tmp_path / "file"
    blocker.write_text("not a directory")
    store = _store(blocker)
    item = sp.ConfigItem.model_validate(config_item())
    with caplog.at_level(logging.WARNING, logger="spinneret.config"):
        assert not store.save(item)
    assert "could not write" in caplog.text
    readable = _store(tmp_path)
    readable.path_for("crawler", "search.json").mkdir(parents=True)
    with caplog.at_level(logging.WARNING, logger="spinneret.config"):
        assert readable.load("crawler", "search.json") is None
    assert "could not read" in caplog.text


def test_failed_atomic_write_cleans_up(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    store = _store(tmp_path)

    def broken_replace(src: str, dst: str) -> None:
        raise OSError("disk full")

    monkeypatch.setattr(_snapshot.os, "replace", broken_replace)
    assert not store.save(sp.ConfigItem.model_validate(config_item()))
    directory = store.path_for("crawler", "search.json").parent
    assert list(directory.iterdir()) == []
    assert os.path.isdir(directory)


def test_treat_as_secret_predicate(tmp_path: Path, caplog: pytest.LogCaptureFixture) -> None:
    def predicate(item: sp.ConfigItem) -> bool:
        if item.key == "boom.json":
            raise RuntimeError("predicate bug")
        return item.group == "signing"

    store = _store(tmp_path, treat_as_secret=predicate)
    signing = sp.ConfigItem.model_validate(config_item(group="signing", content="resolved-key"))
    flagged = sp.ConfigItem.model_validate(secret_config_item(key="f.json"))
    plain = sp.ConfigItem.model_validate(config_item())
    boom = sp.ConfigItem.model_validate(config_item(key="boom.json"))
    with caplog.at_level(logging.ERROR, logger="spinneret.config"):
        assert [store.save(item) for item in (signing, flagged, plain, boom)] == [
            False,
            False,
            True,
            False,
        ]
    assert "treat_as_secret predicate raised" in caplog.text
    assert not store.path_for("signing", "search.json").exists()
    assert store.is_secret(signing)
    assert not store.is_secret(plain)
    assert not _store(tmp_path).is_secret(signing)


def test_predicate_cannot_exempt_flagged_items(tmp_path: Path) -> None:
    seen: list[str] = []

    def never_secret(item: sp.ConfigItem) -> bool:
        seen.append(item.key)
        return False

    store = _store(tmp_path, treat_as_secret=never_secret)
    flagged = sp.ConfigItem.model_validate(secret_config_item(key="flagged.json"))
    plain = sp.ConfigItem.model_validate(config_item(key="plain.json"))
    assert not store.save(flagged)
    assert store.save(plain)
    assert not store.path_for("crawler", "flagged.json").exists()
    assert seen == ["plain.json"]


@pytest.mark.skipif(os.name != "posix", reason="POSIX permissions")
def test_snapshot_directories_are_private(tmp_path: Path) -> None:
    root = tmp_path / "cache"
    store = _store(root)
    assert store.save(sp.ConfigItem.model_validate(config_item()))
    for directory in (
        root,
        root / "spinneret.test%3A8080",
        store.directory,
        store.directory / "crawler",
    ):
        assert stat.S_IMODE(directory.stat().st_mode) == 0o700, directory


def test_snapshot_directory_blocked_by_file(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    store = _store(tmp_path)
    store.directory.mkdir(parents=True)
    (store.directory / "crawler").write_text("not a directory")
    with caplog.at_level(logging.WARNING, logger="spinneret.config"):
        assert not store.save(sp.ConfigItem.model_validate(config_item()))
    assert "could not write config snapshot" in caplog.text
