"""Example crawler settings, read from environment variables.

The Spinneret connection itself (``SPINNERET_URL``, ``SPINNERET_TOKEN``, ``SPINNERET_NODE``,
``SPINNERET_CACHE_DIR``) is read by the SDK.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Mapping, Optional


def _int(env: Mapping[str, str], name: str, default: int, minimum: int, maximum: int) -> int:
    raw = env.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer, got {raw!r}") from exc
    if not minimum <= value <= maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}, got {value}")
    return value


@dataclass(frozen=True)
class Settings:
    """Crawler settings.

    Attributes:
        mock_target_url: Base URL of the crawled site (the Spinneret mock target in the demo).
        site: Spinneret site name the leases are taken for.
        client: Client type of the site.
        config_group: Config center group watched by the crawler.
        config_key: Config center key watched by the crawler.
        lease_wait_ms: How long Acquire may wait for a free identity (0..5000 ms).
        request_timeout: Timeout of one target request in seconds.
    """

    mock_target_url: str = "http://mocktarget:9090"
    site: str = "example"
    client: str = "web"
    config_group: str = "crawler"
    config_key: str = "example.json"
    lease_wait_ms: int = 2000
    request_timeout: float = 10.0

    @classmethod
    def from_env(cls, env: Optional[Mapping[str, str]] = None) -> "Settings":
        """Build settings from ``env`` (default: ``os.environ``); invalid values raise ``ValueError``."""
        env = os.environ if env is None else env
        url = env.get("MOCK_TARGET_URL", "").strip() or cls.mock_target_url
        if not url.startswith(("http://", "https://")):
            raise ValueError(f"MOCK_TARGET_URL must be an http(s) URL, got {url!r}")
        return cls(
            mock_target_url=url.rstrip("/"),
            site=env.get("EXAMPLE_SITE", "").strip() or cls.site,
            client=env.get("EXAMPLE_CLIENT", "").strip() or cls.client,
            config_group=env.get("EXAMPLE_CONFIG_GROUP", "").strip() or cls.config_group,
            config_key=env.get("EXAMPLE_CONFIG_KEY", "").strip() or cls.config_key,
            lease_wait_ms=_int(env, "EXAMPLE_LEASE_WAIT_MS", cls.lease_wait_ms, 0, 5000),
            request_timeout=float(_int(env, "EXAMPLE_REQUEST_TIMEOUT_S", int(cls.request_timeout), 1, 120)),
        )
