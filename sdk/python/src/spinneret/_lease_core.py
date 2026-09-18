"""I/O-free lease bookkeeping shared by the sync and async lease helpers."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any, Optional, Union
from urllib.parse import urlsplit

import httpx

from .errorkind import classify_exception
from .errors import LeaseReleased
from .models import (
    AcquireRequest,
    AcquireResponse,
    Credential,
    ErrorKind,
    Lease,
    Proxy,
    Report,
    utc_now,
)

__all__ = [
    "LEASE_ENDED_REASONS",
    "MAX_REPORT_URI_LENGTH",
    "FinishPlan",
    "LeaseState",
    "as_report_kwargs",
    "build_httpx_kwargs",
    "parse_cookie_header",
    "report_fields_from_exception",
    "report_fields_from_response",
    "report_uri",
]


#: Maximum length of :attr:`Report.uri` accepted by the server.
MAX_REPORT_URI_LENGTH = 2048

HTTP_PROXY_AUTH_REQUIRED = 407

#: Reasons of a failed ``LeaseService/Release`` call meaning the lease has already ended.
LEASE_ENDED_REASONS = frozenset({"lease_released", "lease_unknown", "lease_expired"})


def parse_cookie_header(header: str) -> dict[str, str]:
    """Parse a ``Cookie`` header value (``k1=v1; k2=v2``) into a mapping."""
    cookies: dict[str, str] = {}
    for part in header.split(";"):
        name, sep, value = part.strip().partition("=")
        if sep and name.strip():
            cookies[name.strip()] = value.strip()
    return cookies


def _without_header(headers: Mapping[str, str], name: str) -> dict[str, str]:
    lowered = name.lower()
    return {key: value for key, value in headers.items() if key.lower() != lowered}


def build_httpx_kwargs(
    credential: Credential,
    proxy: Optional[Proxy],
    *,
    headers: Optional[Mapping[str, str]] = None,
    params: Optional[Mapping[str, str]] = None,
    cookies: Optional[Mapping[str, str]] = None,
) -> dict[str, Any]:
    """Merge a credential and proxy into ``httpx.Client`` keyword arguments.

    Credential headers, query parameters and cookies come first; explicitly
    passed values override them (header names compare case-insensitively).
    When the credential has no cookie map, its ``cookie_header`` is sent as a
    ``Cookie`` header, or parsed into cookies when extra cookies are passed.
    """
    merged_headers: dict[str, str] = dict(credential.headers)
    merged_cookies: dict[str, str] = dict(credential.cookies)
    has_cookie_header = any(key.lower() == "cookie" for key in merged_headers)
    if not merged_cookies and credential.cookie_header and not has_cookie_header:
        if cookies:
            merged_cookies = parse_cookie_header(credential.cookie_header)
        else:
            merged_headers["Cookie"] = credential.cookie_header
    for name, value in (headers or {}).items():
        merged_headers = _without_header(merged_headers, name)
        merged_headers[name] = value
    merged_cookies.update(cookies or {})
    merged_params: dict[str, str] = dict(credential.query)
    merged_params.update(params or {})
    return {
        "headers": merged_headers,
        "cookies": merged_cookies,
        "params": merged_params,
        "proxy": proxy.url if proxy is not None and proxy.url else None,
    }


def report_uri(uri: str) -> str:
    """Reduce a request URI or absolute URL to the path reported to the server.

    The query string and fragment are removed: the server matches endpoint
    groups on the path only, and query parameters frequently carry signed
    credential values that must not end up in request analytics. The result is
    capped at the 2048 characters accepted by the server. An empty input stays
    empty (the lease has no URI).
    """
    if not uri:
        return ""
    if uri.startswith(("http://", "https://")):
        path = urlsplit(uri).path
    else:
        path = uri.split("#", 1)[0].split("?", 1)[0]
    return (path or "/")[:MAX_REPORT_URI_LENGTH]


def _request_target(request: httpx.Request) -> str:
    return report_uri(request.url.raw_path.decode("ascii", errors="replace")) or "/"


def report_fields_from_response(response: httpx.Response) -> dict[str, Any]:
    """Extract report fields (status, method, path, latency, size, times) from a response.

    A ``407 Proxy Authentication Required`` response also sets ``error_kind`` to
    ``proxy_auth``.
    """
    fields: dict[str, Any] = {"http_status": response.status_code}
    if response.status_code == HTTP_PROXY_AUTH_REQUIRED:
        fields["error_kind"] = ErrorKind.PROXY_AUTH
    try:
        request = response.request
    except RuntimeError:
        request = None
    if request is not None:
        fields["method"] = request.method
        fields["uri"] = _request_target(request)
    finished = utc_now()
    fields["finished_at"] = finished
    try:
        elapsed: Optional[timedelta] = response.elapsed
    except RuntimeError:
        elapsed = None
    if elapsed is not None:
        fields["latency_ms"] = max(0, round(elapsed.total_seconds() * 1000))
        fields["started_at"] = finished - elapsed
    try:
        fields["response_bytes"] = len(response.content)
    except httpx.ResponseNotRead:
        length = response.headers.get("content-length", "")
        fields["response_bytes"] = int(length) if length.isdigit() else 0
    return fields


def report_fields_from_exception(exc: BaseException) -> dict[str, Any]:
    """Extract report fields (error kind, status, method, uri) from a request exception."""
    if isinstance(exc, httpx.HTTPStatusError):
        return report_fields_from_response(exc.response)
    fields: dict[str, Any] = {"error_kind": classify_exception(exc)}
    if isinstance(exc, httpx.RequestError):
        try:
            request = exc.request
        except RuntimeError:
            return fields
        fields["method"] = request.method
        fields["uri"] = _request_target(request)
    return fields


@dataclass(frozen=True)
class FinishPlan:
    """What must happen to end a lease.

    Attributes:
        report: Last report queued by the caller, marked ``release=True``, to
            deliver; ``None`` when nothing was reported.
        rpc_release: Whether ``LeaseService/Release`` must be called (the lease
            is still open and nothing was reported).
    """

    report: Optional[Report] = None
    rpc_release: bool = False


class LeaseState:
    """Tracks one lease: its acquire response and the report buffered for release.

    The most recent unreleased report is held back so that it can carry
    ``release=True`` when the lease ends; each new report hands the previous
    one over for delivery.
    """

    def __init__(self, request: AcquireRequest) -> None:
        self.request = request
        self._response: Optional[AcquireResponse] = None
        self._pending: Optional[Report] = None
        self._expires_at: Optional[datetime] = None
        self._released = False

    @property
    def acquired(self) -> bool:
        """Whether the lease has been acquired."""
        return self._response is not None

    @property
    def released(self) -> bool:
        """Whether the lease has been released (or its release queued)."""
        return self._released

    @property
    def response(self) -> AcquireResponse:
        """The acquire response; raises ``RuntimeError`` before acquisition."""
        if self._response is None:
            raise RuntimeError("lease is not acquired; use it as a context manager")
        return self._response

    @property
    def lease(self) -> Lease:
        """Lease metadata."""
        return self.response.lease

    @property
    def expires_at(self) -> Optional[datetime]:
        """Current expiry (updated by renewals)."""
        return self._expires_at if self._expires_at is not None else self.lease.expires_at

    def activate(self, response: AcquireResponse) -> None:
        """Record the acquire response."""
        self._response = response
        self._expires_at = None

    def renewed(self, expires_at: Optional[datetime]) -> None:
        """Record a new expiry after a renewal."""
        if expires_at is not None:
            self._expires_at = expires_at

    def ensure_open(self) -> None:
        """Raise :class:`LeaseReleased` when the lease has already been released."""
        if self._released:
            raise LeaseReleased("lease has already been released", reason="lease_released")

    def build_report(
        self,
        *,
        status: int = 0,
        latency_ms: Optional[int] = None,
        markers: Sequence[str] = (),
        business_code: Union[str, int] = "",
        error_kind: str = "",
        outcome_hint: str = "",
        uri: Optional[str] = None,
        method: str = "",
        response_bytes: int = 0,
        started_at: Optional[datetime] = None,
        finished_at: Optional[datetime] = None,
        release: bool = False,
        report_id: Optional[str] = None,
    ) -> Report:
        """Build a report for this lease, defaulting the URI to the acquired one."""
        data: dict[str, Any] = {
            "lease_id": self.lease.lease_id,
            "uri": report_uri(uri if uri is not None else self.request.uri),
            "method": method,
            "http_status": status,
            "business_code": business_code,
            "error_kind": error_kind,
            "markers": list(markers),
            "outcome_hint": outcome_hint,
            "latency_ms": latency_ms,
            "response_bytes": response_bytes,
            "started_at": started_at,
            "finished_at": finished_at,
            "release": release,
        }
        if report_id is not None:
            data["report_id"] = report_id
        return Report.model_validate(data)

    def push(self, report: Report) -> list[Report]:
        """Register a new report and return the reports ready for delivery."""
        self.ensure_open()
        ready = [self._pending] if self._pending is not None else []
        if report.release:
            self._pending = None
            self._released = True
            ready.append(report)
        else:
            self._pending = report
        return ready

    def finish(self) -> FinishPlan:
        """Plan the release of the lease when the caller is done with it.

        The last queued report is marked as the releasing one; when nothing was
        reported the lease must be released with ``LeaseService/Release``. The
        lease counts as released from now on, whatever the outcome of the plan.
        """
        if self._released or self._response is None:
            return FinishPlan()
        self._released = True
        pending, self._pending = self._pending, None
        if pending is None:
            return FinishPlan(rpc_release=True)
        return FinishPlan(report=pending.model_copy(update={"release": True}))


def as_report_kwargs(fields: Mapping[str, Any]) -> dict[str, Any]:
    """Rename extracted ``http_status`` to the ``status`` argument of ``report``."""
    kwargs = dict(fields)
    if "http_status" in kwargs:
        kwargs["status"] = kwargs.pop("http_status")
    return kwargs
