"""Shared fixtures of the Spinneret SDK tests (no network access)."""

from __future__ import annotations

from collections.abc import Iterator
from pathlib import Path

import pytest
import respx

from spinneret import Settings

from ._helpers import BASE_URL, NODE, TOKEN


@pytest.fixture
def settings(tmp_path: Path) -> Settings:
    """Settings pointing at the mocked server with an isolated cache directory."""
    return Settings(url=BASE_URL, token=TOKEN, node=NODE, cache_dir=tmp_path / "cache")


@pytest.fixture
def router() -> Iterator[respx.MockRouter]:
    """respx router mocking the Spinneret base URL."""
    with respx.mock(base_url=BASE_URL, assert_all_called=False) as mock:
        yield mock
