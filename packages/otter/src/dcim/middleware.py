"""HTTP middleware: request ID, access log, timing."""

import time
import uuid

import structlog
from fastapi import FastAPI, Request
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import Response

log = structlog.get_logger("dcim.http")


class RequestContextMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: Request, call_next) -> Response:  # type: ignore[override]
        rid = request.headers.get("x-request-id") or uuid.uuid4().hex
        structlog.contextvars.bind_contextvars(request_id=rid, path=request.url.path, method=request.method)
        started = time.perf_counter()
        try:
            response = await call_next(request)
        finally:
            elapsed_ms = int((time.perf_counter() - started) * 1000)
            structlog.contextvars.bind_contextvars(elapsed_ms=elapsed_ms)
            log.info("http_request")
            structlog.contextvars.clear_contextvars()
        response.headers["x-request-id"] = rid
        return response


def install(app: FastAPI) -> None:
    app.add_middleware(RequestContextMiddleware)
