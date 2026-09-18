"""Typed exceptions raised by the Spinneret SDK.

Every error returned by the Spinneret server is a Connect error: a non-2xx
HTTP response whose JSON body carries ``code`` and ``message``, with the
machine-readable reason and retry hint in the ``Spinneret-Reason`` and
``Spinneret-Retry-After-Ms`` response headers. :func:`error_from_response`
maps such a response onto the most specific exception class, so callers can
write ``except NoIdentityAvailable`` or ``except Unavailable``.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import Any, ClassVar, Optional

__all__ = [
    "HEADER_REASON",
    "HEADER_RETRY_AFTER_MS",
    "REASON_CLIENT_CLOSED",
    "Aborted",
    "AlreadyExists",
    "CircuitOpen",
    "ConfigurationError",
    "DeadlineExceeded",
    "FailedPrecondition",
    "InternalError",
    "InvalidArgument",
    "LeaseExpired",
    "LeaseReleased",
    "LeaseUnknown",
    "NoIdentityAvailable",
    "NoProxyAvailable",
    "NotFound",
    "PermissionDenied",
    "ReporterClosedError",
    "ResourceExhausted",
    "SitePaused",
    "SpinneretError",
    "TransportError",
    "Unauthenticated",
    "Unavailable",
    "Unimplemented",
    "error_class_for",
    "error_from_response",
]

HEADER_REASON = "Spinneret-Reason"
HEADER_RETRY_AFTER_MS = "Spinneret-Retry-After-Ms"

#: Reason of the :class:`FailedPrecondition` raised by calls on a closed client.
REASON_CLIENT_CLOSED = "client_closed"

_MAX_MESSAGE_CHARS = 512


class SpinneretError(Exception):
    """Base class of every error raised by the SDK.

    Attributes:
        code: Connect error code, e.g. ``"resource_exhausted"``.
        reason: Spinneret reason, e.g. ``"no_identity_available"``; empty when
            the server did not send one.
        message: Human-readable message.
        retry_after_ms: Server retry hint in milliseconds, or ``None``.
        http_status: HTTP status of the response, ``0`` when no response was
            received.
    """

    default_code: ClassVar[str] = "unknown"

    def __init__(
        self,
        message: str = "",
        *,
        code: Optional[str] = None,
        reason: str = "",
        retry_after_ms: Optional[int] = None,
        http_status: int = 0,
    ) -> None:
        super().__init__(message)
        self.code: str = code or self.default_code
        self.reason: str = reason
        self.message: str = message
        self.retry_after_ms: Optional[int] = retry_after_ms
        self.http_status: int = http_status

    @property
    def retry_after(self) -> Optional[float]:
        """Server retry hint in seconds, or ``None`` when absent."""
        if self.retry_after_ms is None:
            return None
        return self.retry_after_ms / 1000.0

    def __str__(self) -> str:
        head = f"{self.code} ({self.reason})" if self.reason else self.code
        return f"{head}: {self.message}" if self.message else head

    def __repr__(self) -> str:
        return (
            f"{type(self).__name__}(code={self.code!r}, reason={self.reason!r}, "
            f"message={self.message!r}, retry_after_ms={self.retry_after_ms!r}, "
            f"http_status={self.http_status!r})"
        )

    def __reduce__(self) -> tuple[Any, ...]:
        return (
            _rebuild_error,
            (
                type(self),
                self.message,
                self.code,
                self.reason,
                self.retry_after_ms,
                self.http_status,
            ),
        )


def _rebuild_error(
    cls: type[SpinneretError],
    message: str,
    code: str,
    reason: str,
    retry_after_ms: Optional[int],
    http_status: int,
) -> SpinneretError:
    err = cls.__new__(cls)
    SpinneretError.__init__(
        err,
        message,
        code=code,
        reason=reason,
        retry_after_ms=retry_after_ms,
        http_status=http_status,
    )
    return err


class Unauthenticated(SpinneretError):
    """The token is missing, invalid, expired or revoked. Stop and alert."""

    default_code = "unauthenticated"


class PermissionDenied(SpinneretError):
    """The token lacks a required scope. Stop and alert."""

    default_code = "permission_denied"


class InvalidArgument(SpinneretError):
    """The request is malformed (unknown site, invalid URI, ...). Fix the caller."""

    default_code = "invalid_argument"


class NotFound(SpinneretError):
    """The requested resource does not exist."""

    default_code = "not_found"


class LeaseUnknown(NotFound):
    """The lease does not exist (or ended beyond the late-report window). Acquire again."""


class AlreadyExists(SpinneretError):
    """The resource already exists."""

    default_code = "already_exists"


class FailedPrecondition(SpinneretError):
    """The system is not in the state required by the operation."""

    default_code = "failed_precondition"


class LeaseReleased(FailedPrecondition):
    """The lease has already been released. Acquire again."""


class LeaseExpired(FailedPrecondition):
    """The lease expired or reached its lifetime cap. Acquire again."""


class Aborted(SpinneretError):
    """The operation conflicted with a concurrent modification."""

    default_code = "aborted"


class ResourceExhausted(SpinneretError):
    """A resource is exhausted; wait :attr:`retry_after` seconds before retrying."""

    default_code = "resource_exhausted"


class NoIdentityAvailable(ResourceExhausted):
    """No identity is currently available for the endpoint group."""


class NoProxyAvailable(ResourceExhausted):
    """No proxy matching the rotation policy is currently available."""


class Unavailable(SpinneretError):
    """The service or target is temporarily unavailable."""

    default_code = "unavailable"


class CircuitOpen(Unavailable):
    """The endpoint group breaker is open. Pause requests to that endpoint group."""


class SitePaused(Unavailable):
    """The site is paused by an operator. Pause requests to that site."""


class TransportError(Unavailable):
    """No usable HTTP response was received (connection, timeout, TLS, DNS, ...).

    Attributes:
        error_kind: Classification of the underlying exception, one of the
            report ``error_kind`` values.
    """

    def __init__(self, message: str = "", *, error_kind: str = "other") -> None:
        super().__init__(message, reason="transport", http_status=0)
        self.error_kind: str = error_kind

    def __reduce__(self) -> tuple[Any, ...]:
        return (type(self), (self.message,), {"error_kind": self.error_kind})


class DeadlineExceeded(SpinneretError):
    """The server did not complete the operation in time."""

    default_code = "deadline_exceeded"


class Unimplemented(SpinneretError):
    """The procedure is not implemented by the server (version mismatch?)."""

    default_code = "unimplemented"


class InternalError(SpinneretError):
    """The server failed unexpectedly or returned an unreadable response."""

    default_code = "internal"


class ConfigurationError(SpinneretError, ValueError):
    """The SDK was configured incorrectly (missing URL or token, ...)."""

    default_code = "invalid_argument"


class ReporterClosedError(SpinneretError, RuntimeError):
    """A report was submitted to a reporter that has been closed."""

    default_code = "failed_precondition"


_CODE_CLASSES: Mapping[str, type[SpinneretError]] = {
    "unauthenticated": Unauthenticated,
    "permission_denied": PermissionDenied,
    "invalid_argument": InvalidArgument,
    "out_of_range": InvalidArgument,
    "not_found": NotFound,
    "already_exists": AlreadyExists,
    "failed_precondition": FailedPrecondition,
    "aborted": Aborted,
    "resource_exhausted": ResourceExhausted,
    "unavailable": Unavailable,
    "deadline_exceeded": DeadlineExceeded,
    "unimplemented": Unimplemented,
    "internal": InternalError,
    "unknown": InternalError,
    "data_loss": InternalError,
}

_REASON_CLASSES: Mapping[str, type[SpinneretError]] = {
    "no_identity_available": NoIdentityAvailable,
    "no_proxy_available": NoProxyAvailable,
    "circuit_open": CircuitOpen,
    "site_paused": SitePaused,
    "lease_unknown": LeaseUnknown,
    "lease_released": LeaseReleased,
    "lease_expired": LeaseExpired,
    "lease_lifetime_exceeded": LeaseExpired,
}

# Connect protocol mapping for responses without a Connect error body
# (for example an HTML error page from a load balancer).
_HTTP_STATUS_CODES: Mapping[int, str] = {
    400: "internal",
    401: "unauthenticated",
    403: "permission_denied",
    404: "unimplemented",
    429: "unavailable",
    502: "unavailable",
    503: "unavailable",
    504: "unavailable",
}


def error_class_for(code: str, reason: str) -> type[SpinneretError]:
    """Return the most specific exception class for a Connect code and reason."""
    base = _CODE_CLASSES.get(code)
    if base is None:
        return SpinneretError
    specific = _REASON_CLASSES.get(reason)
    if specific is not None and issubclass(specific, base):
        return specific
    return base


def _parse_retry_after(headers: Mapping[str, str]) -> Optional[int]:
    raw = _header(headers, HEADER_RETRY_AFTER_MS)
    if raw is None:
        return None
    try:
        value = int(raw.strip())
    except ValueError:
        return None
    return value if value >= 0 else None


def _header(headers: Mapping[str, str], name: str) -> Optional[str]:
    value = headers.get(name)
    if value is not None:
        return value
    lowered = name.lower()
    for key, candidate in headers.items():
        if key.lower() == lowered:
            return candidate
    return None


def _parse_body(content: bytes) -> tuple[Optional[str], str]:
    """Return the Connect ``code`` (or ``None``) and message of an error body."""
    try:
        payload = json.loads(content.decode("utf-8")) if content else None
    except (UnicodeDecodeError, ValueError):
        return None, ""
    if not isinstance(payload, dict):
        return None, ""
    code = payload.get("code")
    message = payload.get("message")
    return (
        code if isinstance(code, str) and code else None,
        message if isinstance(message, str) else "",
    )


def error_from_response(
    status: int, headers: Mapping[str, str], content: bytes
) -> tuple[SpinneretError, bool]:
    """Build the exception for a non-2xx response.

    Returns:
        The exception and whether the body was a Connect error (``True``) or
        the error was inferred from the HTTP status only (``False``), which
        matters for deciding whether a non-idempotent call may be retried.
    """
    code, message = _parse_body(content)
    reason = (_header(headers, HEADER_REASON) or "").strip()
    retry_after_ms = _parse_retry_after(headers)
    parsed = code is not None
    if code is None:
        code = _HTTP_STATUS_CODES.get(status, "unknown")
        text = content[:_MAX_MESSAGE_CHARS].decode("utf-8", errors="replace").strip()
        message = f"HTTP {status}" + (f": {text}" if text else "")
    cls = error_class_for(code, reason)
    error = cls(
        message,
        code=code,
        reason=reason,
        retry_after_ms=retry_after_ms,
        http_status=status,
    )
    return error, parsed
