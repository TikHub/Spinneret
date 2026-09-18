from __future__ import annotations

import asyncio
import errno
import socket
import ssl

import httpx
import pytest

from spinneret import classify_exception

_REQUEST = httpx.Request("GET", "https://target.test/a")


def _chained(outer: BaseException, cause: BaseException) -> BaseException:
    outer.__cause__ = cause
    return outer


@pytest.mark.parametrize(
    ("exc", "expected"),
    [
        (httpx.ConnectTimeout("t"), "timeout"),
        (httpx.ReadTimeout("t"), "timeout"),
        (httpx.WriteTimeout("t"), "timeout"),
        (httpx.PoolTimeout("t"), "timeout"),
        (httpx.ConnectError("[Errno 61] Connection refused"), "conn_refused"),
        (httpx.ConnectError("something odd"), "conn_refused"),
        (httpx.ConnectError("[Errno 8] nodename nor servname provided"), "dns"),
        (httpx.ConnectError("[Errno -2] Name or service not known"), "dns"),
        (httpx.ConnectError("[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed"), "tls"),
        (_chained(httpx.ConnectError("x"), socket.gaierror(8, "fail")), "dns"),
        (_chained(httpx.ConnectError("x"), ssl.SSLError("bad")), "tls"),
        (_chained(httpx.ConnectError("x"), ConnectionRefusedError()), "conn_refused"),
        (_chained(httpx.ReadError("x"), ConnectionResetError()), "conn_reset"),
        (httpx.ReadError("[Errno 54] Connection reset by peer"), "conn_reset"),
        (httpx.ReadError("weird"), "conn_reset"),
        (httpx.WriteError("Broken pipe"), "conn_reset"),
        (
            httpx.RemoteProtocolError("Server disconnected without sending a response."),
            "conn_reset",
        ),
        (httpx.ProxyError("407 Proxy Authentication Required"), "proxy_auth"),
        (httpx.ProxyError("502 Bad Gateway"), "other"),
        (httpx.UnsupportedProtocol("Request URL is missing a scheme"), "other"),
        (httpx.DecodingError("bad gzip"), "other"),
        # Three spellings of the same class on modern Python. The point of the three
        # cases is the spelling a caller might hand us, so UP041 must not collapse
        # socket.timeout into the builtin here.
        (TimeoutError(), "timeout"),
        (asyncio.TimeoutError(), "timeout"),
        (socket.timeout(), "timeout"),  # noqa: UP041
        (ConnectionRefusedError(), "conn_refused"),
        (ConnectionResetError(), "conn_reset"),
        (BrokenPipeError(), "conn_reset"),
        (ssl.SSLError("x"), "tls"),
        (socket.gaierror(-2, "Name or service not known"), "dns"),
        (OSError(errno.EHOSTUNREACH, "No route to host"), "conn_refused"),
        (OSError(errno.ECONNRESET, "reset"), "conn_reset"),
        (OSError(errno.ENOENT, "no file"), "other"),
        (_chained(RuntimeError("wrapper"), ConnectionResetError()), "conn_reset"),
        (RuntimeError("Temporary failure in name resolution"), "dns"),
        (ValueError("nothing to see"), "other"),
    ],
)
def test_classify_exception(exc: BaseException, expected: str) -> None:
    assert classify_exception(exc) == expected


@pytest.mark.parametrize(("status", "expected"), [(407, "proxy_auth"), (500, ""), (429, "")])
def test_http_status_errors(status: int, expected: str) -> None:
    response = httpx.Response(status, request=_REQUEST)
    exc = httpx.HTTPStatusError("status", request=_REQUEST, response=response)
    assert classify_exception(exc) == expected


def test_cause_cycles_are_bounded() -> None:
    first = RuntimeError("a")
    second = RuntimeError("b")
    first.__cause__ = second
    second.__cause__ = first
    assert classify_exception(first) == "other"
