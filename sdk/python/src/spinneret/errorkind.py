"""Classification of request exceptions into report ``error_kind`` values."""

from __future__ import annotations

import asyncio
import errno
import socket
import ssl

import httpx

from .models import ErrorKind

__all__ = ["classify_exception"]

_MAX_CHAIN_DEPTH = 16

_DNS_HINTS = (
    "name or service not known",
    "nodename nor servname",
    "temporary failure in name resolution",
    "getaddrinfo failed",
    "no address associated with hostname",
    "name resolution",
    "failed to resolve",
)
_TLS_HINTS = ("ssl", "tls", "certificate", "handshake")
_RESET_HINTS = (
    "connection reset",
    "reset by peer",
    "broken pipe",
    "server disconnected",
    "peer closed",
    "connection aborted",
    "unexpected eof",
    "incomplete",
)
_REFUSED_HINTS = ("connection refused", "no route to host", "network is unreachable")
_PROXY_AUTH_HINTS = ("407", "proxy authentication")

_REFUSED_ERRNOS = frozenset(
    code
    for code in (
        getattr(errno, "ECONNREFUSED", None),
        getattr(errno, "EHOSTUNREACH", None),
        getattr(errno, "ENETUNREACH", None),
    )
    if code is not None
)
_RESET_ERRNOS = frozenset(
    code
    for code in (
        getattr(errno, "ECONNRESET", None),
        getattr(errno, "ECONNABORTED", None),
        getattr(errno, "EPIPE", None),
    )
    if code is not None
)


def _chain(exc: BaseException) -> list[BaseException]:
    seen: list[BaseException] = []
    current: BaseException | None = exc
    while current is not None and len(seen) < _MAX_CHAIN_DEPTH and current not in seen:
        seen.append(current)
        current = current.__cause__ or current.__context__
    return seen


def _classify_builtin(exc: BaseException) -> str | None:
    kind: str | None = None
    if isinstance(exc, socket.gaierror):
        kind = ErrorKind.DNS
    elif isinstance(exc, ssl.SSLError):
        kind = ErrorKind.TLS
    elif isinstance(exc, (TimeoutError, socket.timeout, asyncio.TimeoutError)):
        kind = ErrorKind.TIMEOUT
    elif isinstance(exc, ConnectionRefusedError):
        kind = ErrorKind.CONN_REFUSED
    elif isinstance(exc, (ConnectionResetError, ConnectionAbortedError, BrokenPipeError)):
        kind = ErrorKind.CONN_RESET
    elif isinstance(exc, OSError) and exc.errno is not None:
        if exc.errno in _REFUSED_ERRNOS:
            kind = ErrorKind.CONN_REFUSED
        elif exc.errno in _RESET_ERRNOS:
            kind = ErrorKind.CONN_RESET
    return kind


def _classify_message(message: str) -> str | None:
    text = message.lower()
    for hints, kind in (
        (_PROXY_AUTH_HINTS, ErrorKind.PROXY_AUTH),
        (_DNS_HINTS, ErrorKind.DNS),
        (_TLS_HINTS, ErrorKind.TLS),
        (_REFUSED_HINTS, ErrorKind.CONN_REFUSED),
        (_RESET_HINTS, ErrorKind.CONN_RESET),
    ):
        if any(hint in text for hint in hints):
            return kind
    return None


def _classify_httpx(exc: httpx.HTTPError) -> str:
    if isinstance(exc, httpx.HTTPStatusError):
        return ErrorKind.PROXY_AUTH if exc.response.status_code == 407 else ErrorKind.NONE
    if isinstance(exc, httpx.TimeoutException):
        return ErrorKind.TIMEOUT
    for link in _chain(exc)[1:]:
        kind = _classify_builtin(link)
        if kind is not None:
            return kind
    by_message = _classify_message(" ".join(str(link) for link in _chain(exc)))
    if isinstance(exc, httpx.ProxyError):
        return ErrorKind.PROXY_AUTH if by_message == ErrorKind.PROXY_AUTH else ErrorKind.OTHER
    if by_message is not None and by_message != ErrorKind.PROXY_AUTH:
        return by_message
    if isinstance(exc, httpx.ConnectError):
        return ErrorKind.CONN_REFUSED
    if isinstance(exc, (httpx.ReadError, httpx.WriteError, httpx.RemoteProtocolError)):
        return ErrorKind.CONN_RESET
    return ErrorKind.OTHER


def classify_exception(exc: BaseException) -> str:
    """Map a request exception to a report ``error_kind``.

    Understands httpx exceptions (including their causes) and the standard
    library socket/SSL/timeout exceptions; other exceptions are classified
    from their cause chain and message.

    Returns:
        One of ``timeout``, ``conn_reset``, ``conn_refused``, ``proxy_auth``,
        ``tls``, ``dns`` or ``other``. An :class:`httpx.HTTPStatusError`
        carries an HTTP response, so it yields ``""`` (report its status code
        instead) unless the status is 407 (``proxy_auth``).
    """
    if isinstance(exc, httpx.HTTPError):
        return _classify_httpx(exc)
    for link in _chain(exc):
        kind = _classify_builtin(link)
        if kind is not None:
            return kind
    by_message = _classify_message(" ".join(str(link) for link in _chain(exc)))
    return by_message if by_message is not None else ErrorKind.OTHER
