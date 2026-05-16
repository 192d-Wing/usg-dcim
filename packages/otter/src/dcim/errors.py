"""Domain exceptions and error envelope."""

from fastapi import FastAPI, Request, status
from fastapi.responses import ORJSONResponse


class DcimError(Exception):
    status_code: int = 500
    code: str = "internal_error"

    def __init__(self, message: str, *, details: dict | None = None):
        super().__init__(message)
        self.message = message
        self.details = details or {}


class NotFoundError(DcimError):
    status_code = 404
    code = "not_found"


class ConflictError(DcimError):
    status_code = 409
    code = "conflict"


class ValidationError(DcimError):
    status_code = 422
    code = "validation_failed"


class AuthError(DcimError):
    status_code = 401
    code = "unauthenticated"


class ForbiddenError(DcimError):
    status_code = 403
    code = "forbidden"


class ScopeError(ForbiddenError):
    code = "out_of_scope"


def install(app: FastAPI) -> None:
    @app.exception_handler(DcimError)
    async def _handle(_: Request, exc: DcimError) -> ORJSONResponse:
        return ORJSONResponse(
            status_code=exc.status_code,
            content={"error": {"code": exc.code, "message": exc.message, "details": exc.details}},
        )

    @app.exception_handler(Exception)
    async def _unhandled(_: Request, exc: Exception) -> ORJSONResponse:
        return ORJSONResponse(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            content={"error": {"code": "internal_error", "message": str(exc)}},
        )
