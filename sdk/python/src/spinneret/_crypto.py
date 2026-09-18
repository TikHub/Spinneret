"""Snapshot encryption: HKDF-SHA256 key derivation and AES-256-GCM.

HKDF (RFC 5869) is implemented with :mod:`hmac`/:mod:`hashlib`. The standard
library has no AES-GCM, so encryption requires the optional ``cryptography``
package (``pip install 'spinneret[crypto]'``).
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import importlib
import os
from collections.abc import Mapping
from typing import Any

from .errors import ConfigurationError

__all__ = [
    "ALGORITHM",
    "SnapshotCipher",
    "SnapshotDecryptError",
    "crypto_available",
    "hkdf_sha256",
]

#: Identifier of the snapshot encryption scheme stored with every envelope.
ALGORITHM = "HKDF-SHA256/AES-256-GCM"

_INFO = b"spinneret-config-snapshot-v1"
_SALT_BYTES = 16
_NONCE_BYTES = 12
_KEY_BYTES = 32
_HASH_LEN = hashlib.sha256().digest_size


class SnapshotDecryptError(ValueError):
    """An encrypted snapshot could not be decrypted (wrong token or tampered file)."""


def hkdf_sha256(ikm: bytes, *, salt: bytes, info: bytes, length: int = _KEY_BYTES) -> bytes:
    """Derive ``length`` bytes with HKDF-SHA256 (RFC 5869)."""
    if not 0 < length <= 255 * _HASH_LEN:
        raise ValueError("invalid HKDF output length")
    prk = hmac.new(salt or bytes(_HASH_LEN), ikm, hashlib.sha256).digest()
    output = b""
    block = b""
    counter = 1
    while len(output) < length:
        block = hmac.new(prk, block + info + bytes([counter]), hashlib.sha256).digest()
        output += block
        counter += 1
    return output[:length]


def _load_aesgcm() -> Any:
    try:
        module = importlib.import_module("cryptography.hazmat.primitives.ciphers.aead")
    except ImportError:
        return None
    return getattr(module, "AESGCM", None)


def crypto_available() -> bool:
    """Whether AES-GCM is available (the ``cryptography`` package is importable)."""
    return _load_aesgcm() is not None


def _b64(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


def _unb64(envelope: Mapping[str, Any], field: str) -> bytes:
    value = envelope.get(field)
    if not isinstance(value, str):
        raise SnapshotDecryptError(f"snapshot envelope is missing {field!r}")
    try:
        return base64.b64decode(value, validate=True)
    except ValueError as exc:
        raise SnapshotDecryptError(f"snapshot envelope field {field!r} is not base64") from exc


class SnapshotCipher:
    """Encrypts snapshots with keys derived from the API token.

    Each envelope uses a fresh random salt (HKDF) and nonce (AES-GCM); the
    snapshot location is bound as additional authenticated data.
    """

    def __init__(self, token: str) -> None:
        """Create a cipher.

        Raises:
            ConfigurationError: When ``cryptography`` is not installed.
        """
        aesgcm = _load_aesgcm()
        if aesgcm is None:
            raise ConfigurationError(
                "cache_secrets=True requires the 'cryptography' package "
                "(pip install 'spinneret[crypto]')",
                reason="crypto_unavailable",
            )
        self._aesgcm = aesgcm
        self._secret = token.encode("utf-8")

    def _key(self, salt: bytes) -> bytes:
        return hkdf_sha256(self._secret, salt=salt, info=_INFO)

    def encrypt(self, plaintext: bytes, aad: bytes) -> dict[str, str]:
        """Encrypt ``plaintext`` into a JSON-serializable envelope."""
        salt = os.urandom(_SALT_BYTES)
        nonce = os.urandom(_NONCE_BYTES)
        ciphertext = self._aesgcm(self._key(salt)).encrypt(nonce, plaintext, aad)
        return {
            "alg": ALGORITHM,
            "salt": _b64(salt),
            "nonce": _b64(nonce),
            "ciphertext": _b64(ciphertext),
        }

    def decrypt(self, envelope: Mapping[str, Any], aad: bytes) -> bytes:
        """Decrypt an envelope produced by :meth:`encrypt`.

        Raises:
            SnapshotDecryptError: When the envelope is malformed or authentication fails.
        """
        if envelope.get("alg") != ALGORITHM:
            raise SnapshotDecryptError("unsupported snapshot encryption algorithm")
        salt = _unb64(envelope, "salt")
        nonce = _unb64(envelope, "nonce")
        ciphertext = _unb64(envelope, "ciphertext")
        try:
            plaintext: bytes = self._aesgcm(self._key(salt)).decrypt(nonce, ciphertext, aad)
        except Exception as exc:  # cryptography raises InvalidTag
            raise SnapshotDecryptError("snapshot authentication failed") from exc
        return plaintext
