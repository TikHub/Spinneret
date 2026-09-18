"""The crawl flow of one request: lease → request through the leased proxy → report.

Only facts are reported (status, markers, business code, latency); Spinneret's signal and action
policies decide what they mean (success, rate limited, captcha, ...) and cool down, ban or expire
identities accordingly.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Mapping, Optional

import httpx
import spinneret

from .settings import Settings

#: Response headers of the mock target that describe what a real crawler would parse from the page.
MARKER_HEADER = "X-Mock-Marker"
BUSINESS_CODE_HEADER = "X-Mock-Business-Code"


class UpstreamError(Exception):
    """The target site could not be reached (the failure was reported to Spinneret)."""


@dataclass
class CrawlResult:
    """Outcome of one crawl request."""

    status: int
    identity_id: str
    proxy_id: Optional[str]
    endpoint_group: str
    markers: List[str] = field(default_factory=list)
    business_code: str = ""
    data: Any = None

    def as_dict(self) -> Dict[str, Any]:
        return {
            "ok": 200 <= self.status < 300 and not self.markers and not self.business_code,
            "status": self.status,
            "identity_id": self.identity_id,
            "proxy_id": self.proxy_id,
            "endpoint_group": self.endpoint_group,
            "markers": self.markers,
            "business_code": self.business_code,
            "data": self.data,
        }


def markers_from(headers: Mapping[str, str]) -> List[str]:
    """Response features a real crawler would detect in the page (here: announced by the mock target)."""
    raw = headers.get(MARKER_HEADER, "")
    return [m.strip() for m in raw.split(",") if m.strip()]


async def crawl(
    client: spinneret.AsyncClient,
    settings: Settings,
    path: str,
    params: Optional[Mapping[str, str]] = None,
) -> CrawlResult:
    """Fetch ``path`` from the target with a leased identity and report the result.

    The lease is released with the report when the ``async with`` block ends.

    Raises:
        spinneret.SpinneretError: No lease could be acquired (circuit open, site paused, exhausted, ...).
        UpstreamError: The request failed without a response (reported with its ``error_kind``).
    """
    async with client.lease(settings.site, client=settings.client, uri=path, wait_ms=settings.lease_wait_ms) as lease:
        async with httpx.AsyncClient(
            **lease.httpx_kwargs(),
            timeout=settings.request_timeout,
            follow_redirects=False,  # a login redirect is a signal to report, not to follow
        ) as http:
            try:
                response = await http.get(settings.mock_target_url + path, params=dict(params or {}))
            except httpx.HTTPError as exc:
                lease.report_exception(exc)
                raise UpstreamError(f"request to the target failed: {type(exc).__name__}") from exc
        markers = markers_from(response.headers)
        business_code = response.headers.get(BUSINESS_CODE_HEADER, "")
        lease.report_response(response, markers=markers, business_code=business_code)
        data: Any = None
        if response.headers.get("content-type", "").startswith("application/json"):
            try:
                data = response.json()
            except ValueError:
                data = None
        return CrawlResult(
            status=response.status_code,
            identity_id=lease.identity_id,
            proxy_id=lease.proxy.proxy_id if lease.proxy else None,
            endpoint_group=lease.info.endpoint_group,
            markers=markers,
            business_code=business_code,
            data=data,
        )
