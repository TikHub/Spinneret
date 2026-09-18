"""Pydantic v2 models of the Spinneret node API messages.

The models mirror the protobuf messages of ``spinneret.v1`` in their JSON form
(snake_case field names). Parsing is tolerant: unknown fields are ignored,
``null`` values fall back to field defaults and 64-bit integers encoded as JSON
strings are coerced. All models are immutable; use ``model_copy(update=...)``
to derive a modified instance.

Annotations intentionally use ``typing.Optional`` (not ``X | None``) so that the
models evaluate on Python 3.9.
"""

import uuid
from datetime import datetime, timedelta, timezone
from typing import Annotated, Any, Optional

from pydantic import (
    AfterValidator,
    BaseModel,
    BeforeValidator,
    ConfigDict,
    Field,
    PlainSerializer,
    model_validator,
)

__all__ = [
    "ERROR_KINDS",
    "SECRET_REF_MARKER",
    "AcquireBatchRequest",
    "AcquireBatchResponse",
    "AcquireRequest",
    "AcquireResponse",
    "BatchGetConfigRequest",
    "BatchGetConfigResponse",
    "ConfigItem",
    "ConfigKey",
    "ConfigRef",
    "Credential",
    "ErrorKind",
    "GetConfigRequest",
    "GetConfigResponse",
    "GetSecretRequest",
    "GetSecretResponse",
    "Hints",
    "Lease",
    "Outcome",
    "Proxy",
    "ProxyAssignment",
    "RejectedReport",
    "ReleaseRequest",
    "ReleaseResponse",
    "RenewRequest",
    "RenewResponse",
    "Report",
    "ReportRequest",
    "ReportResponse",
    "SpinneretModel",
    "WatchConfigRequest",
    "WatchConfigResponse",
    "WatchItem",
    "format_timestamp",
    "utc_now",
]

#: Marker of a secret reference inside config content (``${secret:<path>}``).
SECRET_REF_MARKER = "${secret:"  # noqa: S105 - marker, not a secret

#: Maximum number of reports accepted by one ``ReportService/Report`` call.
MAX_REPORTS_PER_CALL = 500

#: Maximum number of items of one ``BatchGetConfig`` or ``WatchConfig`` call.
MAX_CONFIG_ITEMS = 200


class ErrorKind:
    """Allowed values of :attr:`Report.error_kind`."""

    NONE = ""
    TIMEOUT = "timeout"
    CONN_RESET = "conn_reset"
    CONN_REFUSED = "conn_refused"
    PROXY_AUTH = "proxy_auth"
    TLS = "tls"
    DNS = "dns"
    OTHER = "other"


#: Values accepted by the server for :attr:`Report.error_kind`.
ERROR_KINDS = frozenset(
    {
        ErrorKind.NONE,
        ErrorKind.TIMEOUT,
        ErrorKind.CONN_RESET,
        ErrorKind.CONN_REFUSED,
        ErrorKind.PROXY_AUTH,
        ErrorKind.TLS,
        ErrorKind.DNS,
        ErrorKind.OTHER,
    }
)


class Outcome:
    """Outcome classes that may be sent as :attr:`Report.outcome_hint`."""

    SUCCESS = "success"
    EMPTY = "empty"
    RATE_LIMITED = "rate_limited"
    CAPTCHA = "captcha"
    AUTH_INVALID = "auth_invalid"
    FORBIDDEN = "forbidden"
    BANNED = "banned"
    PROXY_ERROR = "proxy_error"
    NETWORK_ERROR = "network_error"
    TARGET_ERROR = "target_error"
    CLIENT_ERROR = "client_error"
    UNKNOWN = "unknown"


def utc_now() -> datetime:
    """Return the current time as an aware UTC datetime."""
    return datetime.now(timezone.utc)


def _ensure_aware(value: datetime) -> datetime:
    # Naive datetimes follow the Python convention of representing local time.
    return value if value.tzinfo is not None else value.astimezone()


def format_timestamp(value: datetime) -> str:
    """Format a datetime as an RFC 3339 UTC timestamp with microseconds."""
    return _ensure_aware(value).astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _number_to_str(value: Any) -> Any:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return str(value)
    return value


Timestamp = Annotated[
    datetime,
    AfterValidator(_ensure_aware),
    PlainSerializer(format_timestamp, return_type=str, when_used="json"),
]
NumericString = Annotated[str, BeforeValidator(_number_to_str)]


def _check_error_kind(value: str) -> str:
    if value not in ERROR_KINDS:
        allowed = ", ".join(sorted(kind for kind in ERROR_KINDS if kind))
        raise ValueError(f"error_kind must be empty or one of: {allowed}")
    return value


class SpinneretModel(BaseModel):
    """Base model: immutable, ignores unknown fields, treats ``null`` as unset."""

    model_config = ConfigDict(extra="ignore", frozen=True, populate_by_name=True)

    @model_validator(mode="before")
    @classmethod
    def _drop_nulls(cls, data: Any) -> Any:
        if isinstance(data, dict):
            return {key: value for key, value in data.items() if value is not None}
        return data

    def to_json_dict(self) -> dict[str, Any]:
        """Return the JSON-compatible wire representation of the message."""
        return self.model_dump(mode="json", by_alias=True, exclude_none=True)


# ---------------------------------------------------------------------------
# LeaseService
# ---------------------------------------------------------------------------


class AcquireRequest(SpinneretModel):
    """Request of ``LeaseService/Acquire``."""

    site: str = Field(min_length=1, max_length=64)
    client: str = Field(min_length=1, max_length=32)
    uri: str = Field(default="", max_length=2048)
    endpoint_group: str = Field(default="", max_length=64)
    session_key: str = Field(default="", max_length=256)
    wait_ms: int = Field(default=0, ge=0, le=5000)


class AcquireBatchRequest(AcquireRequest):
    """Request of ``LeaseService/AcquireBatch``."""

    count: int = Field(default=1, ge=1, le=50)


class Lease(SpinneretModel):
    """Metadata of an issued lease."""

    lease_id: str = ""
    identity_id: str = ""
    identity_type: str = ""
    endpoint_group: str = ""
    expires_at: Optional[Timestamp] = None
    sticky: bool = False
    probe: bool = False


class Credential(SpinneretModel):
    """Rendered credential of a leased identity. Its values are never shown in ``repr``."""

    cookies: dict[str, str] = Field(default_factory=dict, repr=False)
    cookie_header: str = Field(default="", repr=False)
    headers: dict[str, str] = Field(default_factory=dict, repr=False)
    query: dict[str, str] = Field(default_factory=dict, repr=False)
    json_value: Any = Field(default=None, alias="json", repr=False)
    values: dict[str, Any] = Field(default_factory=dict, repr=False)


class Proxy(SpinneretModel):
    """Proxy assigned to a lease. The URL (with credentials) is hidden from ``repr``."""

    proxy_id: str = ""
    url: str = Field(default="", repr=False)
    kind: str = ""
    region: str = ""


#: Name of :class:`Proxy` in the protobuf schema.
ProxyAssignment = Proxy


class Hints(SpinneretModel):
    """Advisory values for lease handling."""

    renew_before_ms: int = 0


class AcquireResponse(SpinneretModel):
    """Response of ``LeaseService/Acquire``: one lease with credential and proxy."""

    lease: Lease = Field(default_factory=Lease)
    credential: Credential = Field(default_factory=Credential)
    proxy: Optional[Proxy] = None
    hints: Hints = Field(default_factory=Hints)


class AcquireBatchResponse(SpinneretModel):
    """Response of ``LeaseService/AcquireBatch``."""

    leases: list[AcquireResponse] = Field(default_factory=list)
    requested: int = 0


class RenewRequest(SpinneretModel):
    """Request of ``LeaseService/Renew``; ``extend_ms=0`` uses the policy lease TTL."""

    lease_id: str = Field(min_length=1, max_length=128)
    extend_ms: int = Field(default=0, ge=0, le=1_800_000)


class RenewResponse(SpinneretModel):
    """Response of ``LeaseService/Renew``."""

    expires_at: Optional[Timestamp] = None


class ReleaseRequest(SpinneretModel):
    """Request of ``LeaseService/Release``."""

    lease_id: str = Field(min_length=1, max_length=128)


class ReleaseResponse(SpinneretModel):
    """Response of ``LeaseService/Release``; ``released`` is false when already ended."""

    released: bool = False


# ---------------------------------------------------------------------------
# ReportService
# ---------------------------------------------------------------------------


def _new_report_id() -> str:
    return str(uuid.uuid4())


class Report(SpinneretModel):
    """Observed result of one request made with a lease.

    ``report_id`` defaults to a random UUID4. When ``finished_at`` is omitted it
    defaults to now; when ``started_at`` is omitted it is derived from
    ``finished_at - latency_ms``; when ``latency_ms`` is omitted it is derived
    from the two timestamps.
    """

    report_id: str = Field(default_factory=_new_report_id, pattern=r"^[A-Za-z0-9_.:-]{1,64}$")
    lease_id: str = Field(min_length=1, max_length=128)
    uri: str = Field(min_length=1, max_length=2048)
    method: str = Field(default="", max_length=16)
    http_status: int = Field(default=0, ge=0, le=999)
    business_code: NumericString = Field(default="", max_length=64)
    error_kind: Annotated[str, AfterValidator(_check_error_kind)] = ""
    markers: list[Annotated[str, Field(min_length=1, max_length=64)]] = Field(
        default_factory=list, max_length=32
    )
    outcome_hint: str = Field(default="", max_length=32)
    latency_ms: int = Field(default=0, ge=0, le=2_147_483_647)
    response_bytes: int = Field(default=0, ge=0)
    started_at: Timestamp
    finished_at: Timestamp
    release: bool = False

    @model_validator(mode="before")
    @classmethod
    def _fill_times(cls, data: Any) -> Any:
        if not isinstance(data, dict):
            return data
        filled = {key: value for key, value in data.items() if value is not None}
        finished = filled.get("finished_at")
        started = filled.get("started_at")
        latency = filled.get("latency_ms")
        if finished is None:
            finished = utc_now()
            filled["finished_at"] = finished
        if started is None:
            if isinstance(finished, datetime) and isinstance(latency, int) and latency > 0:
                started = finished - timedelta(milliseconds=latency)
            else:
                started = finished
            filled["started_at"] = started
        if latency is None and isinstance(started, datetime) and isinstance(finished, datetime):
            delta_ms = (_ensure_aware(finished) - _ensure_aware(started)).total_seconds() * 1000
            filled["latency_ms"] = max(0, round(delta_ms))
        return filled

    @model_validator(mode="after")
    def _check_order(self) -> "Report":
        if self.finished_at < self.started_at:
            raise ValueError("finished_at must not be before started_at")
        return self


class ReportRequest(SpinneretModel):
    """Request of ``ReportService/Report`` (1..500 reports)."""

    reports: list[Report] = Field(min_length=1, max_length=MAX_REPORTS_PER_CALL)


class RejectedReport(SpinneretModel):
    """A report that the server did not accept."""

    report_id: str = ""
    reason: str = ""
    message: str = ""


class ReportResponse(SpinneretModel):
    """Response of ``ReportService/Report``."""

    accepted: int = 0
    duplicated: int = 0
    rejected: list[RejectedReport] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# ConfigService
# ---------------------------------------------------------------------------


class ConfigKey(SpinneretModel):
    """Location of a config item inside a namespace (``ConfigRef`` in the protobuf schema).

    Validation also accepts ``"group/key"`` strings and ``(group, key)`` tuples.
    """

    group: str = Field(min_length=1, max_length=128)
    key: str = Field(min_length=1, max_length=256)

    @model_validator(mode="before")
    @classmethod
    def _parse_path(cls, data: Any) -> Any:
        if isinstance(data, str):
            group, sep, key = data.partition("/")
            return {"group": group, "key": key} if sep else {"group": data}
        if isinstance(data, (tuple, list)) and len(data) == 2:
            return {"group": data[0], "key": data[1]}
        return data


#: Name of :class:`ConfigKey` in the protobuf schema.
ConfigRef = ConfigKey


class ConfigItem(SpinneretModel):
    """A published config item. Its content is hidden from ``repr``.

    Attributes:
        content: Content of the published version with ``${secret:...}``
            references resolved.
        has_secret_refs: True when the published content contained
            ``${secret:...}`` references that the server resolved into
            ``content``. Such content must not be persisted in plain text;
            :class:`ConfigWatcher` snapshots honour it.
    """

    namespace: str = ""
    group: str = ""
    key: str = ""
    format: str = ""
    version: int = 0
    content: str = Field(default="", repr=False)
    updated_at: Optional[Timestamp] = None
    has_secret_refs: bool = False

    @property
    def config_key(self) -> ConfigKey:
        """The :class:`ConfigKey` of this item."""
        return ConfigKey(group=self.group, key=self.key)

    @property
    def references_secrets(self) -> bool:
        """Whether the item must be treated as secret.

        True when :attr:`has_secret_refs` is set. As a safeguard, content that
        still contains an unresolved ``${secret:`` marker counts as secret too.
        """
        return self.has_secret_refs or SECRET_REF_MARKER in self.content


class GetConfigRequest(SpinneretModel):
    """Request of ``ConfigService/GetConfig``."""

    namespace: str = Field(default="", max_length=64)
    group: str = Field(min_length=1, max_length=128)
    key: str = Field(min_length=1, max_length=256)


class GetConfigResponse(SpinneretModel):
    """Response of ``ConfigService/GetConfig``."""

    item: ConfigItem = Field(default_factory=ConfigItem)


class BatchGetConfigRequest(SpinneretModel):
    """Request of ``ConfigService/BatchGetConfig``."""

    namespace: str = Field(default="", max_length=64)
    items: list[ConfigKey] = Field(min_length=1, max_length=MAX_CONFIG_ITEMS)


class BatchGetConfigResponse(SpinneretModel):
    """Response of ``ConfigService/BatchGetConfig``."""

    items: list[ConfigItem] = Field(default_factory=list)
    missing: list[ConfigKey] = Field(default_factory=list)


class WatchItem(SpinneretModel):
    """A watched config item with the version known by the caller (0 = none)."""

    group: str = Field(min_length=1, max_length=128)
    key: str = Field(min_length=1, max_length=256)
    version: int = Field(default=0, ge=0)


class WatchConfigRequest(SpinneretModel):
    """Request of ``ConfigService/WatchConfig`` (long poll)."""

    namespace: str = Field(default="", max_length=64)
    items: list[WatchItem] = Field(min_length=1, max_length=MAX_CONFIG_ITEMS)
    timeout_ms: int = Field(default=30_000, ge=0, le=60_000)


class WatchConfigResponse(SpinneretModel):
    """Response of ``ConfigService/WatchConfig``: the items that changed."""

    items: list[ConfigItem] = Field(default_factory=list)


# ---------------------------------------------------------------------------
# SecretService
# ---------------------------------------------------------------------------


class GetSecretRequest(SpinneretModel):
    """Request of ``SecretService/GetSecret``; ``version=0`` means the current version."""

    path: str = Field(min_length=1, max_length=256, pattern=r"^[a-z0-9][a-z0-9_./-]*$")
    version: int = Field(default=0, ge=0)


class GetSecretResponse(SpinneretModel):
    """Response of ``SecretService/GetSecret``. The value is hidden from ``repr``."""

    path: str = ""
    version: int = 0
    value: str = Field(default="", repr=False)
    expires_at: Optional[Timestamp] = None
