from __future__ import annotations

import socket
from pathlib import Path

import pytest

from spinneret import ConfigurationError, Settings
from spinneret.settings import default_cache_dir, default_node_name, sanitize_node_name


def test_from_env_reads_all_variables(tmp_path: Path) -> None:
    env = {
        "SPINNERET_URL": "https://spinneret.internal:8443/",
        "SPINNERET_TOKEN": " spn_abc ",
        "SPINNERET_NODE": "crawler-hk-03",
        "SPINNERET_CACHE_DIR": str(tmp_path / "snap"),
    }
    settings = Settings.from_env(env)
    assert settings.url == "https://spinneret.internal:8443"
    assert settings.token == "spn_abc"
    assert settings.node == "crawler-hk-03"
    assert settings.cache_dir == tmp_path / "snap"
    assert settings.host == "spinneret.internal:8443"


def test_from_env_defaults() -> None:
    settings = Settings.from_env({"SPINNERET_URL": "http://x", "SPINNERET_TOKEN": "t"})
    assert settings.node == default_node_name()
    assert settings.cache_dir == default_cache_dir()
    assert settings.cache_dir == Path("~/.spinneret/cache").expanduser()


def test_explicit_values_override_env() -> None:
    env = {"SPINNERET_URL": "http://env", "SPINNERET_TOKEN": "env", "SPINNERET_NODE": "env"}
    settings = Settings.from_env(env, url="http://arg", token="arg", node="arg", cache_dir="/c")
    assert (settings.url, settings.token, settings.node) == ("http://arg", "arg", "arg")
    assert settings.cache_dir == Path("/c")


def test_from_env_uses_os_environ(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SPINNERET_URL", "http://os-env")
    monkeypatch.setenv("SPINNERET_TOKEN", "spn_os")
    monkeypatch.delenv("SPINNERET_NODE", raising=False)
    settings = Settings.from_env()
    assert settings.url == "http://os-env"


@pytest.mark.parametrize(
    ("env", "message"),
    [
        ({"SPINNERET_TOKEN": "t"}, "SPINNERET_URL"),
        ({"SPINNERET_URL": "http://x"}, "SPINNERET_TOKEN"),
        ({"SPINNERET_URL": "  ", "SPINNERET_TOKEN": "t"}, "SPINNERET_URL"),
    ],
)
def test_from_env_missing_values(env: dict[str, str], message: str) -> None:
    with pytest.raises(ConfigurationError, match=message):
        Settings.from_env(env)


@pytest.mark.parametrize(
    "url",
    ["ftp://x", "spinneret.internal", "http://", "http://x/?a=1", "http://x/#f", "http://u:p@x"],
)
def test_invalid_urls(url: str) -> None:
    with pytest.raises(ConfigurationError):
        Settings(url=url, token="t")


@pytest.mark.parametrize("token", ["", "   ", "spn bad"])
def test_invalid_tokens(token: str) -> None:
    with pytest.raises(ConfigurationError):
        Settings(url="http://x", token=token)


def test_repr_hides_token() -> None:
    settings = Settings(url="http://x", token="spn_super_secret")
    assert "spn_super_secret" not in repr(settings)


def test_configuration_error_is_value_error() -> None:
    with pytest.raises(ValueError, match="URL"):
        Settings.from_env({})


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("crawler-hk-03", "crawler-hk-03"),
        ("pod name/with spaces", "pod-name-with-spaces"),
        ("  ", "unknown"),
        ("ノード", "unknown"),
        ("a" * 200, "a" * 128),
    ],
)
def test_sanitize_node_name(raw: str, expected: str) -> None:
    assert sanitize_node_name(raw) == expected


def test_default_node_name_handles_errors(monkeypatch: pytest.MonkeyPatch) -> None:
    def fail() -> str:
        raise OSError("no hostname")

    monkeypatch.setattr(socket, "gethostname", fail)
    assert default_node_name() == "unknown"
