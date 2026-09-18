"""Lease accessors and report building shared by the sync and async lease helpers."""

from __future__ import annotations

import logging
from collections.abc import Mapping, Sequence
from datetime import datetime
from typing import Any, Optional, Union

import httpx

from ._lease_core import (
    LEASE_ENDED_REASONS,
    LeaseState,
    as_report_kwargs,
    build_httpx_kwargs,
    report_fields_from_exception,
    report_fields_from_response,
)
from .errors import SpinneretError
from .models import AcquireResponse, Credential, Hints, Lease, Proxy, ReleaseResponse, Report

__all__ = ["LeaseBase"]

logger = logging.getLogger("spinneret.lease")


class LeaseBase:
    """Accessors and reporting shared by the sync and async lease helpers."""

    _state: LeaseState
    _flush_on_exit: bool
    _raise_on_release_error: bool

    @property
    def acquired(self) -> bool:
        """Whether the lease has been acquired."""
        return self._state.acquired

    @property
    def released(self) -> bool:
        """Whether the lease has been released or its release queued."""
        return self._state.released

    @property
    def response(self) -> AcquireResponse:
        """Full acquire response."""
        return self._state.response

    @property
    def info(self) -> Lease:
        """Lease metadata (``lease_id``, ``identity_id``, ``probe``, ...)."""
        return self._state.lease

    @property
    def lease_id(self) -> str:
        """ID of the lease."""
        return self._state.lease.lease_id

    @property
    def identity_id(self) -> str:
        """ID of the leased identity."""
        return self._state.lease.identity_id

    @property
    def expires_at(self) -> Optional[datetime]:
        """Expiry of the lease, updated by :meth:`renew`."""
        return self._state.expires_at

    @property
    def credential(self) -> Credential:
        """Rendered credential of the identity."""
        return self._state.response.credential

    @property
    def proxy(self) -> Optional[Proxy]:
        """Assigned proxy, or ``None``."""
        return self._state.response.proxy

    @property
    def hints(self) -> Hints:
        """Lease handling hints."""
        return self._state.response.hints

    def httpx_kwargs(
        self,
        *,
        headers: Optional[Mapping[str, str]] = None,
        params: Optional[Mapping[str, str]] = None,
        cookies: Optional[Mapping[str, str]] = None,
    ) -> dict[str, Any]:
        """Keyword arguments for ``httpx.Client``/``httpx.AsyncClient``.

        Returns a dict with ``headers``, ``cookies``, ``params`` and ``proxy``.
        Explicit arguments override credential values. ``proxy`` must be given
        to the client constructor (httpx does not accept it per request).
        """
        return build_httpx_kwargs(
            self.credential, self.proxy, headers=headers, params=params, cookies=cookies
        )

    def _build(
        self,
        status: int,
        latency_ms: Optional[int],
        markers: Sequence[str],
        business_code: Union[str, int],
        error_kind: str,
        release: bool,
        extra: Mapping[str, Any],
    ) -> Report:
        self._state.ensure_open()
        fields: dict[str, Any] = {
            "status": status,
            "latency_ms": latency_ms,
            "markers": markers,
            "business_code": business_code,
            "error_kind": error_kind,
            "release": release,
        }
        fields.update(extra)
        return self._state.build_report(**fields)

    def report(
        self,
        status: int = 0,
        *,
        latency_ms: Optional[int] = None,
        markers: Sequence[str] = (),
        business_code: Union[str, int] = "",
        error_kind: str = "",
        release: bool = False,
        **extra: Any,
    ) -> Report:
        """Queue a report of one request made with this lease.

        Args:
            status: HTTP status (0 when no response was received).
            latency_ms: Request latency; derived from ``started_at`` when omitted.
            markers: Response features, e.g. ``["captcha_page"]``.
            business_code: Business status code from the response body.
            error_kind: Transport error kind (see :func:`classify_exception`).
            release: Release the lease with this report.
            **extra: Other :class:`Report` fields: ``uri`` (default: the acquired
                URI), ``method``, ``outcome_hint``, ``response_bytes``,
                ``started_at``, ``finished_at``, ``report_id``.

        Raises:
            LeaseReleased: The lease has already been released.
            ReporterClosedError: The client's reporter has been closed.
            pydantic.ValidationError: A field is invalid.
        """
        report = self._build(status, latency_ms, markers, business_code, error_kind, release, extra)
        self._submit(report)
        return report

    def report_response(
        self,
        response: httpx.Response,
        *,
        markers: Sequence[str] = (),
        business_code: Union[str, int] = "",
        release: bool = False,
        **extra: Any,
    ) -> Report:
        """Queue a report built from an ``httpx.Response`` (status, latency, size, URI).

        ``**extra`` accepts the same fields as :meth:`report` and overrides the
        values taken from the response.
        """
        fields = as_report_kwargs(report_fields_from_response(response))
        fields.update(extra)
        return self.report(markers=markers, business_code=business_code, release=release, **fields)

    def report_exception(
        self,
        exc: BaseException,
        *,
        markers: Sequence[str] = (),
        release: bool = False,
        **extra: Any,
    ) -> Report:
        """Queue a report for a failed request, classifying ``error_kind`` from ``exc``.

        ``**extra`` accepts the same fields as :meth:`report` (for example
        ``latency_ms`` or ``uri`` when the exception carries no request).
        """
        fields = as_report_kwargs(report_fields_from_exception(exc))
        fields.update(extra)
        return self.report(markers=markers, release=release, **fields)

    def _submit(self, report: Report) -> None:
        raise NotImplementedError  # pragma: no cover - implemented by subclasses

    def _released_by_rpc(self, response: ReleaseResponse) -> None:
        if not response.released:
            logger.debug("spinneret lease %s had already ended before release", self.lease_id)

    def _release_rpc_failed(self, err: SpinneretError) -> Optional[SpinneretError]:
        """Log a failed ``LeaseService/Release`` call.

        Returns:
            ``err`` when it may be raised to the caller (``raise_on_release_error``
            is set and the failure does not merely say that the lease has
            already ended), otherwise ``None``.
        """
        if err.reason in LEASE_ENDED_REASONS:
            logger.debug(
                "spinneret lease %s had already ended before release: %s",
                self.lease_id,
                err.reason,
            )
            return None
        logger.warning("spinneret release of lease %s failed: %s", self.lease_id, err)
        return err if self._raise_on_release_error else None

    def _safe_lease_id(self) -> str:
        return self.lease_id if self._state.acquired else ""

    def __repr__(self) -> str:
        if not self._state.acquired:
            return f"{type(self).__name__}(site={self._state.request.site!r}, acquired=False)"
        return (
            f"{type(self).__name__}(lease_id={self.lease_id!r}, "
            f"identity_id={self.identity_id!r}, released={self.released!r})"
        )
