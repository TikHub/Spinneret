"""Scripted in-memory ConfigService used by the watcher tests."""

from __future__ import annotations

import asyncio
import json
import threading
import time
from collections import deque
from typing import Any

import httpx

from ._helpers import config_item

WATCH_IDLE_SECONDS = 0.02


class ConfigServer:
    """Serves BatchGetConfig/WatchConfig from scripted state."""

    def __init__(self, items: list[dict[str, Any]] | None = None) -> None:
        self.items = {(i["group"], i["key"]): i for i in (items or [config_item()])}
        self.batch_responses: deque[httpx.Response] = deque()
        self.watch_responses: deque[httpx.Response | dict[str, Any]] = deque()
        self.watch_bodies: list[dict[str, Any]] = []
        self.batch_calls = 0
        self.lock = threading.Lock()

    def publish(self, item: dict[str, Any]) -> None:
        """Queue a change returned by the next watch call."""
        with self.lock:
            self.items[(item["group"], item["key"])] = item
            self.watch_responses.append({"items": [item]})

    def _batch(self, body: dict[str, Any]) -> httpx.Response:
        self.batch_calls += 1
        if self.batch_responses:
            return self.batch_responses.popleft()
        found, missing = [], []
        for ref in body["items"]:
            item = self.items.get((ref["group"], ref["key"]))
            if item is None:
                missing.append(ref)
            else:
                found.append(item)
        return httpx.Response(200, json={"items": found, "missing": missing})

    def _watch(self, body: dict[str, Any]) -> httpx.Response | None:
        self.watch_bodies.append(body)
        if not self.watch_responses:
            return None
        scripted = self.watch_responses.popleft()
        if isinstance(scripted, httpx.Response):
            return scripted
        return httpx.Response(200, json=scripted)

    def _dispatch(self, request: httpx.Request) -> httpx.Response | None:
        body = json.loads(request.content)
        with self.lock:
            if request.url.path.endswith("/BatchGetConfig"):
                return self._batch(body)
            if request.url.path.endswith("/WatchConfig"):
                return self._watch(body)
        return httpx.Response(404, json={"code": "unimplemented", "message": "no route"})

    def handler(self, request: httpx.Request) -> httpx.Response:
        """Synchronous transport handler."""
        response = self._dispatch(request)
        if response is None:
            time.sleep(WATCH_IDLE_SECONDS)
            return httpx.Response(200, json={"items": []})
        return response

    async def async_handler(self, request: httpx.Request) -> httpx.Response:
        """Asynchronous transport handler."""
        response = self._dispatch(request)
        if response is None:
            await asyncio.sleep(WATCH_IDLE_SECONDS)
            return httpx.Response(200, json={"items": []})
        return response


def unavailable() -> httpx.Response:
    """A Connect ``unavailable`` error response."""
    return httpx.Response(503, json={"code": "unavailable", "message": "down"})


def denied() -> httpx.Response:
    """A Connect ``permission_denied`` error response."""
    return httpx.Response(
        403,
        json={"code": "permission_denied", "message": "no"},
        headers={"Spinneret-Reason": "scope_missing"},
    )
