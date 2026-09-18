"""Basic usage of the Spinneret Python SDK.

Run with a Spinneret server and a node token::

    export SPINNERET_URL=https://spinneret.internal
    export SPINNERET_TOKEN=spn_xxx
    python examples/basic_usage.py

The script watches a config item, leases an identity for a request, sends the
request with the leased credential and proxy, and reports the outcome. The
lease is released by the last report when the ``with`` block ends.
"""

from __future__ import annotations

import json
import logging
import time

import httpx

import spinneret

SITE = "example-site"
CLIENT = "web"
TARGET = "https://target.example.com/api/v1/search?keyword=spinneret"

logger = logging.getLogger("example")


def detect_markers(response: httpx.Response) -> list[str]:
    """Recognize response features the server cannot see (the body is not uploaded)."""
    markers: list[str] = []
    if "verify" in str(response.url) or "captcha" in response.text[:2048]:
        markers.append("captcha_page")
    try:
        payload = response.json()
    except ValueError:
        return markers
    if isinstance(payload, dict) and not payload.get("data"):
        markers.append("empty_list")
    return markers


def crawl_once(client: spinneret.Client) -> None:
    """Lease an identity, send one request and report what happened."""
    try:
        with client.lease(site=SITE, client=CLIENT, uri=TARGET, session_key="task-8842") as lease:
            with httpx.Client(timeout=15, **lease.httpx_kwargs()) as http:
                try:
                    response = http.get(TARGET)
                except httpx.HTTPError as exc:
                    lease.report_exception(exc)
                    return
            lease.report_response(response, markers=detect_markers(response))
    except spinneret.NoIdentityAvailable as exc:
        logger.info("no identity available, retry in %.1fs", exc.retry_after or 1.0)
        time.sleep(exc.retry_after or 1.0)
    except (spinneret.CircuitOpen, spinneret.SitePaused) as exc:
        logger.warning("endpoint paused by Spinneret (%s), backing off", exc.reason)
        time.sleep(exc.retry_after or 30.0)


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    with spinneret.Client() as client:
        watcher = client.config_watcher(["crawler/search.json"], on_change=log_change)
        with watcher:
            item = watcher.get("crawler", "search.json")
            settings = json.loads(item.content) if item is not None else {}
            for _ in range(int(settings.get("requests", 3))):
                crawl_once(client)
        stats = client.reporter.stats
        logger.info("reports sent=%d dropped=%d", stats.sent, stats.dropped)


def log_change(item: spinneret.ConfigItem) -> None:
    """Config change callback (never log the content: it may contain resolved secrets)."""
    logger.info("config %s/%s is now version %d", item.group, item.key, item.version)


if __name__ == "__main__":
    main()
