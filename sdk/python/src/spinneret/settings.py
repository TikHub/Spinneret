"""SDK settings resolved from explicit arguments and ``SPINNERET_*`` environment variables."""

from __future__ import annotations

import os
import re
import socket
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

from .errors import ConfigurationError

__all__ = [
    "ENV_CACHE_DIR",
    "ENV_NODE",
    "ENV_TOKEN",
    "ENV_URL",
    "MAX_NODE_NAME_LENGTH",
    "Settings",
    "default_cache_dir",
    "default_node_name",
    "sanitize_node_name",
]

ENV_URL = "SPINNERET_URL"
ENV_TOKEN = "SPINNERET_TOKEN"  # noqa: S105 - environment variable name
ENV_NODE = "SPINNERET_NODE"
ENV_CACHE_DIR = "SPINNERET_CACHE_DIR"

#: Maximum length of the ``X-Spinneret-Node`` header accepted by the server.
MAX_NODE_NAME_LENGTH = 128

_NODE_INVALID_CHARS = re.compile(r"[^A-Za-z0-9._:@-]+")


def sanitize_node_name(name: str) -> str:
    """Normalize a node name to a header-safe value of at most 128 characters.

    Characters outside ``[A-Za-z0-9._:@-]`` are replaced by ``-``. An empty
    result becomes ``"unknown"``.
    """
    cleaned = _NODE_INVALID_CHARS.sub("-", name.strip()).strip("-")
    return cleaned[:MAX_NODE_NAME_LENGTH] or "unknown"


def default_node_name() -> str:
    """Return the host name, used when ``SPINNERET_NODE`` is not set."""
    try:
        return sanitize_node_name(socket.gethostname())
    except OSError:
        return "unknown"


def default_cache_dir() -> Path:
    """Return the default snapshot cache directory ``~/.spinneret/cache``."""
    return Path("~/.spinneret/cache").expanduser()


def _normalize_url(url: str) -> str:
    candidate = url.strip().rstrip("/")
    parts = urlsplit(candidate)
    if parts.scheme not in ("http", "https") or not parts.netloc:
        raise ConfigurationError(
            f"invalid Spinneret URL {candidate!r}: expected http(s)://host[:port][/prefix]"
        )
    if parts.query or parts.fragment:
        raise ConfigurationError("invalid Spinneret URL: query and fragment are not allowed")
    if parts.username or parts.password:
        raise ConfigurationError("invalid Spinneret URL: credentials must not be embedded")
    return candidate


@dataclass(frozen=True)
class Settings:
    """Connection settings of a Spinneret node.

    Attributes:
        url: Base URL of the Spinneret server, e.g. ``https://spinneret.internal``.
        token: API token (``spn_...``). Never included in ``repr``.
        node: Node name sent as ``X-Spinneret-Node`` (defaults to the host name).
        cache_dir: Directory for local config snapshots.
    """

    url: str
    token: str = field(repr=False)
    node: str = field(default_factory=default_node_name)
    cache_dir: Path = field(default_factory=default_cache_dir)

    def __post_init__(self) -> None:
        object.__setattr__(self, "url", _normalize_url(self.url))
        token = self.token.strip()
        if not token:
            raise ConfigurationError("Spinneret token is empty")
        if any(ch.isspace() for ch in token):
            raise ConfigurationError("Spinneret token must not contain whitespace")
        object.__setattr__(self, "token", token)
        object.__setattr__(self, "node", sanitize_node_name(self.node))
        object.__setattr__(self, "cache_dir", Path(self.cache_dir).expanduser())

    @property
    def host(self) -> str:
        """Host (and port) of :attr:`url`, used to scope local snapshots."""
        return urlsplit(self.url).netloc

    @classmethod
    def from_env(
        cls,
        environ: Mapping[str, str] | None = None,
        *,
        url: str | None = None,
        token: str | None = None,
        node: str | None = None,
        cache_dir: str | os.PathLike[str] | None = None,
    ) -> Settings:
        """Build settings from explicit values, falling back to environment variables.

        Args:
            environ: Environment mapping; defaults to :data:`os.environ`.
            url: Overrides ``SPINNERET_URL``.
            token: Overrides ``SPINNERET_TOKEN``.
            node: Overrides ``SPINNERET_NODE`` (default: host name).
            cache_dir: Overrides ``SPINNERET_CACHE_DIR`` (default: ``~/.spinneret/cache``).

        Raises:
            ConfigurationError: When the URL or token is missing or invalid.
        """
        env = os.environ if environ is None else environ
        resolved_url = url if url is not None else env.get(ENV_URL, "")
        resolved_token = token if token is not None else env.get(ENV_TOKEN, "")
        if not resolved_url.strip():
            raise ConfigurationError(f"Spinneret URL is not configured (set {ENV_URL})")
        if not resolved_token.strip():
            raise ConfigurationError(f"Spinneret token is not configured (set {ENV_TOKEN})")
        resolved_node = node if node is not None else env.get(ENV_NODE, "")
        resolved_cache = cache_dir if cache_dir is not None else env.get(ENV_CACHE_DIR, "")
        return cls(
            url=resolved_url,
            token=resolved_token,
            node=resolved_node if resolved_node.strip() else default_node_name(),
            cache_dir=Path(resolved_cache) if resolved_cache else default_cache_dir(),
        )
