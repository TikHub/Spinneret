"""FastAPI crawler node: HTTP endpoints that crawl the target through Spinneret leases.

Run locally::

    SPINNERET_URL=http://localhost:8080 SPINNERET_TOKEN=spn_... MOCK_TARGET_URL=http://localhost:19090 \\
        uvicorn app.main:app --port 8000
"""

from __future__ import annotations

import json
import logging
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Callable, Dict, Optional

import spinneret
from fastapi import FastAPI, Path, Query, Request
from fastapi.responses import JSONResponse

from .crawler import UpstreamError, crawl
from .settings import Settings

logger = logging.getLogger("example_crawler")

ClientFactory = Callable[[], spinneret.AsyncClient]


def _error(status: int, error: str, reason: str = "", retry_after: Optional[float] = None) -> JSONResponse:
    headers = {}
    if retry_after is not None:
        headers["Retry-After"] = str(max(1, round(retry_after)))
    return JSONResponse({"ok": False, "error": error, "reason": reason}, status_code=status, headers=headers)


def _spinneret_error(err: spinneret.SpinneretError) -> JSONResponse:
    """Maps lease errors to HTTP answers of the crawler API."""
    if isinstance(err, (spinneret.CircuitOpen, spinneret.SitePaused)):
        # The endpoint group or the whole site is switched off: stop crawling it for now.
        return _error(503, err.code, err.reason, err.retry_after)
    if isinstance(err, spinneret.ResourceExhausted):
        # No identity (or proxy) is available right now: retry after the server's hint.
        return _error(429, err.code, err.reason, err.retry_after)
    logger.warning("spinneret call failed: code=%s reason=%s", err.code, err.reason)
    return _error(502, err.code, err.reason)


def create_app(settings: Optional[Settings] = None, client_factory: Optional[ClientFactory] = None) -> FastAPI:
    """Build the application. ``client_factory`` defaults to ``spinneret.AsyncClient()`` (env settings)."""

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        cfg = settings or Settings.from_env()
        client = (client_factory or spinneret.AsyncClient)()
        # Secret-free config only is written to local snapshots; the watcher long-polls in the background.
        watcher = client.config_watcher([(cfg.config_group, cfg.config_key)])
        app.state.settings = cfg
        app.state.client = client
        app.state.watcher = watcher
        try:
            try:
                await watcher.start()
            except spinneret.SpinneretError as err:
                # The API keeps serving leases; /config reports the problem.
                logger.warning("config watcher did not start: code=%s reason=%s", err.code, err.reason)
            yield
        finally:
            await watcher.stop()
            # Flushes queued reports (lease releases) and closes the HTTP pools.
            await client.aclose()

    app = FastAPI(title="Spinneret example crawler", version="1.0.0", lifespan=lifespan)

    @app.get("/healthz")
    async def healthz(request: Request) -> Dict[str, Any]:
        watcher = request.app.state.watcher
        return {"ok": True, "config_watcher_running": watcher.running}

    @app.get("/crawl/search")
    async def crawl_search(request: Request, q: str = Query(..., min_length=1, max_length=256)) -> Any:
        return await _crawl(request, "/site/search", {"q": q})

    @app.get("/crawl/item/{item_id}")
    async def crawl_item(request: Request, item_id: str = Path(..., pattern=r"^[A-Za-z0-9_-]{1,64}$")) -> Any:
        return await _crawl(request, f"/site/item/{item_id}", None)

    @app.get("/config")
    async def config(request: Request) -> Any:
        cfg: Settings = request.app.state.settings
        watcher = request.app.state.watcher
        item = watcher.get(cfg.config_group, cfg.config_key)
        if item is None:
            return _error(404, "not_found", "config item not loaded")
        content: Any = item.content
        if item.format == "json":
            try:
                content = json.loads(item.content)
            except ValueError:
                pass
        return {
            "ok": True,
            "group": item.group,
            "key": item.key,
            "version": item.version,
            "format": item.format,
            "from_snapshot": watcher.from_snapshot,
            # Items that reference vault secrets are never echoed back.
            "content": None if item.references_secrets else content,
        }

    return app


async def _crawl(request: Request, path: str, params: Optional[Dict[str, str]]) -> Any:
    try:
        result = await crawl(request.app.state.client, request.app.state.settings, path, params)
    except spinneret.SpinneretError as err:
        return _spinneret_error(err)
    except UpstreamError as err:
        return _error(502, "upstream_error", str(err))
    return result.as_dict()


app = create_app()
