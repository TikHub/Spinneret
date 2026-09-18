"""Sans-I/O description of every node RPC: request building, headers, decoding."""

from __future__ import annotations

import json
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Generic, Optional, TypeVar, Union

import httpx
from pydantic import ValidationError

from ._version import __version__
from .errorkind import classify_exception
from .errors import InternalError, TransportError
from .models import (
    AcquireBatchRequest,
    AcquireBatchResponse,
    AcquireRequest,
    AcquireResponse,
    BatchGetConfigRequest,
    BatchGetConfigResponse,
    ConfigKey,
    GetConfigRequest,
    GetConfigResponse,
    GetSecretRequest,
    GetSecretResponse,
    ReleaseRequest,
    ReleaseResponse,
    RenewRequest,
    RenewResponse,
    Report,
    ReportRequest,
    ReportResponse,
    SpinneretModel,
    WatchConfigRequest,
    WatchConfigResponse,
    WatchItem,
)
from .settings import Settings

__all__ = [
    "Call",
    "ConfigKeyLike",
    "Timeouts",
    "acquire",
    "acquire_batch",
    "batch_get_config",
    "build_headers",
    "decode_response",
    "get_config",
    "get_secret",
    "release",
    "renew",
    "report",
    "to_config_keys",
    "transport_error",
    "watch_config",
]

PACKAGE = "spinneret.v1"
LEASE_SERVICE = "LeaseService"
REPORT_SERVICE = "ReportService"
CONFIG_SERVICE = "ConfigService"
SECRET_SERVICE = "SecretService"  # noqa: S105 - service name

#: Server-side long-poll wait applied when ``WatchConfigRequest.timeout_ms`` is 0.
DEFAULT_WATCH_TIMEOUT_MS = 30_000

#: Accepted spellings of a config key: a model, ``(group, key)`` or ``"group/key"``.
ConfigKeyLike = Union[ConfigKey, tuple[str, str], str]

R = TypeVar("R", bound=SpinneretModel)


@dataclass(frozen=True)
class Timeouts:
    """HTTP timeouts in seconds.

    Attributes:
        connect: Time to establish a connection.
        read: Time to wait for response data of ordinary calls.
        write: Time to send the request.
        pool: Time to wait for a free pooled connection.
        watch_grace: Added to ``timeout_ms`` of ``WatchConfig`` long polls.
    """

    connect: float = 3.0
    read: float = 10.0
    write: float = 10.0
    pool: float = 10.0
    watch_grace: float = 5.0

    def for_call(self, extra_read: float = 0.0, read: Optional[float] = None) -> httpx.Timeout:
        """Build the httpx timeout of one call."""
        return httpx.Timeout(
            connect=self.connect,
            read=(self.read if read is None else read) + extra_read,
            write=self.write,
            pool=self.pool,
        )


@dataclass(frozen=True)
class Call(Generic[R]):
    """One RPC invocation.

    Attributes:
        service: Service name without package, e.g. ``"LeaseService"``.
        method: Method name, e.g. ``"Acquire"``.
        request: Request message.
        response_type: Response model class.
        idempotent: Whether repeating the call cannot duplicate side effects.
        extra_read: Seconds added to the read timeout (server-side waiting).
        read: Replaces the base read timeout when set (long polls).
    """

    service: str
    method: str
    request: SpinneretModel
    response_type: type[R]
    idempotent: bool
    extra_read: float = 0.0
    read: Optional[float] = None

    @property
    def procedure(self) -> str:
        """Connect procedure path, e.g. ``/spinneret.v1.LeaseService/Acquire``."""
        return f"/{PACKAGE}.{self.service}/{self.method}"

    def url(self, base_url: str) -> str:
        """Absolute URL of the procedure below ``base_url``."""
        return base_url + self.procedure

    def body(self) -> bytes:
        """JSON-encoded request body."""
        return json.dumps(
            self.request.to_json_dict(), separators=(",", ":"), ensure_ascii=False
        ).encode("utf-8")

    def timeout(self, timeouts: Timeouts) -> httpx.Timeout:
        """httpx timeout for this call."""
        return timeouts.for_call(self.extra_read, self.read)


def build_headers(settings: Settings) -> dict[str, str]:
    """Headers sent with every call (the token is only placed in ``Authorization``)."""
    return {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "Connect-Protocol-Version": "1",
        "Authorization": f"Bearer {settings.token}",
        "X-Spinneret-Node": settings.node,
        "User-Agent": f"spinneret-python/{__version__} httpx/{httpx.__version__}",
    }


def decode_response(call: Call[R], content: bytes) -> R:
    """Decode a successful response body into the call's response model."""
    try:
        payload: Any = json.loads(content.decode("utf-8")) if content.strip() else {}
        return call.response_type.model_validate(payload)
    except (UnicodeDecodeError, ValueError, ValidationError) as exc:
        raise InternalError(
            f"invalid response from {call.procedure}: {type(exc).__name__}",
            reason="invalid_response",
            http_status=200,
        ) from exc


def transport_error(call: Call[Any], exc: Exception) -> TransportError:
    """Wrap an httpx transport exception."""
    kind = classify_exception(exc) or "other"
    return TransportError(f"{call.procedure}: {type(exc).__name__}: {exc}", error_kind=kind)


def to_config_keys(items: Iterable[ConfigKeyLike]) -> list[ConfigKey]:
    """Normalize config key spellings into :class:`ConfigKey` models."""
    return [
        item if isinstance(item, ConfigKey) else ConfigKey.model_validate(item) for item in items
    ]


def acquire(
    site: str,
    client: str,
    uri: str,
    endpoint_group: str,
    session_key: str,
    wait_ms: int,
) -> Call[AcquireResponse]:
    """Build a ``LeaseService/Acquire`` call."""
    request = AcquireRequest(
        site=site,
        client=client,
        uri=uri,
        endpoint_group=endpoint_group,
        session_key=session_key,
        wait_ms=wait_ms,
    )
    return Call(LEASE_SERVICE, "Acquire", request, AcquireResponse, False, wait_ms / 1000.0)


def acquire_batch(
    site: str,
    client: str,
    count: int,
    uri: str,
    endpoint_group: str,
    session_key: str,
    wait_ms: int,
) -> Call[AcquireBatchResponse]:
    """Build a ``LeaseService/AcquireBatch`` call."""
    request = AcquireBatchRequest(
        site=site,
        client=client,
        count=count,
        uri=uri,
        endpoint_group=endpoint_group,
        session_key=session_key,
        wait_ms=wait_ms,
    )
    return Call(
        LEASE_SERVICE, "AcquireBatch", request, AcquireBatchResponse, False, wait_ms / 1000.0
    )


def renew(lease_id: str, extend_ms: int) -> Call[RenewResponse]:
    """Build a ``LeaseService/Renew`` call."""
    request = RenewRequest(lease_id=lease_id, extend_ms=extend_ms)
    return Call(LEASE_SERVICE, "Renew", request, RenewResponse, True)


def release(lease_id: str) -> Call[ReleaseResponse]:
    """Build a ``LeaseService/Release`` call."""
    return Call(LEASE_SERVICE, "Release", ReleaseRequest(lease_id=lease_id), ReleaseResponse, True)


def report(reports: Sequence[Report]) -> Call[ReportResponse]:
    """Build a ``ReportService/Report`` call (idempotent through ``report_id``)."""
    return Call(
        REPORT_SERVICE, "Report", ReportRequest(reports=list(reports)), ReportResponse, True
    )


def get_config(namespace: str, group: str, key: str) -> Call[GetConfigResponse]:
    """Build a ``ConfigService/GetConfig`` call."""
    request = GetConfigRequest(namespace=namespace, group=group, key=key)
    return Call(CONFIG_SERVICE, "GetConfig", request, GetConfigResponse, True)


def batch_get_config(
    namespace: str, items: Iterable[ConfigKeyLike]
) -> Call[BatchGetConfigResponse]:
    """Build a ``ConfigService/BatchGetConfig`` call."""
    request = BatchGetConfigRequest(namespace=namespace, items=to_config_keys(items))
    return Call(CONFIG_SERVICE, "BatchGetConfig", request, BatchGetConfigResponse, True)


def watch_config(
    namespace: str,
    items: Iterable[Union[WatchItem, Mapping[str, Any]]],
    timeout_ms: int,
    grace: float,
) -> Call[WatchConfigResponse]:
    """Build a ``ConfigService/WatchConfig`` long-poll call.

    The HTTP read timeout is the server-side wait plus ``grace``; ``timeout_ms=0``
    asks the server for its default wait of 30 s.
    """
    watch_items = [
        item if isinstance(item, WatchItem) else WatchItem.model_validate(item) for item in items
    ]
    request = WatchConfigRequest(namespace=namespace, items=watch_items, timeout_ms=timeout_ms)
    server_wait_ms = request.timeout_ms or DEFAULT_WATCH_TIMEOUT_MS
    return Call(
        CONFIG_SERVICE,
        "WatchConfig",
        request,
        WatchConfigResponse,
        True,
        extra_read=grace,
        read=server_wait_ms / 1000.0,
    )


def get_secret(path: str, version: int) -> Call[GetSecretResponse]:
    """Build a ``SecretService/GetSecret`` call."""
    request = GetSecretRequest(path=path, version=version)
    return Call(SECRET_SERVICE, "GetSecret", request, GetSecretResponse, True)
