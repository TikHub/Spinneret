from __future__ import annotations

import json
import pickle

import pytest

import spinneret as sp
from spinneret.errors import error_class_for, error_from_response


def _body(code: str, message: str = "boom") -> bytes:
    return json.dumps({"code": code, "message": message}).encode()


@pytest.mark.parametrize(
    ("status", "code", "reason", "expected"),
    [
        (401, "unauthenticated", "token_invalid", sp.Unauthenticated),
        (403, "permission_denied", "scope_missing", sp.PermissionDenied),
        (400, "invalid_argument", "site_unknown", sp.InvalidArgument),
        (400, "out_of_range", "", sp.InvalidArgument),
        (404, "not_found", "not_found", sp.NotFound),
        (404, "not_found", "lease_unknown", sp.LeaseUnknown),
        (409, "already_exists", "already_exists", sp.AlreadyExists),
        (409, "aborted", "conflict", sp.Aborted),
        (400, "failed_precondition", "failed_precondition", sp.FailedPrecondition),
        (400, "failed_precondition", "lease_released", sp.LeaseReleased),
        (400, "failed_precondition", "lease_expired", sp.LeaseExpired),
        (400, "failed_precondition", "lease_lifetime_exceeded", sp.LeaseExpired),
        (429, "resource_exhausted", "no_identity_available", sp.NoIdentityAvailable),
        (429, "resource_exhausted", "no_proxy_available", sp.NoProxyAvailable),
        (429, "resource_exhausted", "rate_limited", sp.ResourceExhausted),
        (503, "unavailable", "circuit_open", sp.CircuitOpen),
        (503, "unavailable", "site_paused", sp.SitePaused),
        (503, "unavailable", "rebuilding", sp.Unavailable),
        (504, "deadline_exceeded", "", sp.DeadlineExceeded),
        (501, "unimplemented", "", sp.Unimplemented),
        (500, "internal", "internal", sp.InternalError),
        (500, "unknown", "", sp.InternalError),
        (500, "data_loss", "", sp.InternalError),
        (499, "canceled", "", sp.SpinneretError),
    ],
)
def test_error_mapping(status: int, code: str, reason: str, expected: type) -> None:
    headers = {"Spinneret-Reason": reason, "Spinneret-Retry-After-Ms": "1200"}
    error, parsed = error_from_response(status, headers, _body(code))
    assert parsed is True
    assert type(error) is expected
    assert error.code == code
    assert error.reason == reason
    assert error.message == "boom"
    assert error.http_status == status
    assert error.retry_after_ms == 1200
    assert error.retry_after == pytest.approx(1.2)


def test_reason_class_requires_matching_code() -> None:
    # A reason that does not belong to the code falls back to the code class.
    assert error_class_for("unavailable", "no_identity_available") is sp.Unavailable
    assert error_class_for("mystery", "circuit_open") is sp.SpinneretError


def test_reason_header_lookup_is_case_insensitive() -> None:
    error, _ = error_from_response(503, {"spinneret-reason": "circuit_open"}, _body("unavailable"))
    assert isinstance(error, sp.CircuitOpen)


@pytest.mark.parametrize(
    ("status", "code", "expected"),
    [
        (400, "internal", sp.InternalError),
        (401, "unauthenticated", sp.Unauthenticated),
        (403, "permission_denied", sp.PermissionDenied),
        (404, "unimplemented", sp.Unimplemented),
        (429, "unavailable", sp.Unavailable),
        (502, "unavailable", sp.Unavailable),
        (503, "unavailable", sp.Unavailable),
        (504, "unavailable", sp.Unavailable),
        (418, "unknown", sp.InternalError),
    ],
)
def test_fallback_mapping_without_connect_body(status: int, code: str, expected: type) -> None:
    error, parsed = error_from_response(status, {}, b"<html>bad gateway</html>")
    assert parsed is False
    assert type(error) is expected
    assert error.code == code
    assert "bad gateway" in error.message


@pytest.mark.parametrize(
    "body",
    [b"", b"not json", b"[1,2]", b'{"code": ""}', b'{"code": 5}', b"\xff\xfe"],
)
def test_unparseable_bodies(body: bytes) -> None:
    error, parsed = error_from_response(502, {}, body)
    assert parsed is False
    assert isinstance(error, sp.Unavailable)


@pytest.mark.parametrize("raw", ["abc", "-5", ""])
def test_invalid_retry_after_is_ignored(raw: str) -> None:
    error, _ = error_from_response(
        429, {"Spinneret-Retry-After-Ms": raw}, _body("resource_exhausted")
    )
    assert error.retry_after_ms is None
    assert error.retry_after is None


def test_message_missing_in_body() -> None:
    error, parsed = error_from_response(404, {}, b'{"code": "not_found"}')
    assert parsed
    assert error.message == ""
    assert str(error) == "not_found"


def test_str_and_repr() -> None:
    error = sp.NoIdentityAvailable("none left", reason="no_identity_available", retry_after_ms=5)
    assert str(error) == "resource_exhausted (no_identity_available): none left"
    assert "retry_after_ms=5" in repr(error)
    assert "NoIdentityAvailable" in repr(error)


@pytest.mark.parametrize(
    "error",
    [
        sp.CircuitOpen("open", reason="circuit_open", retry_after_ms=10, http_status=503),
        sp.TransportError("refused", error_kind="conn_refused"),
        sp.ConfigurationError("bad"),
    ],
)
def test_errors_pickle(error: sp.SpinneretError) -> None:
    # Round-trips an object created by this test (errors cross process pools); no untrusted data.
    restored = pickle.loads(pickle.dumps(error))  # noqa: S301
    assert type(restored) is type(error)
    assert restored.code == error.code
    assert restored.reason == error.reason
    assert restored.message == error.message
    assert restored.retry_after_ms == error.retry_after_ms
    if isinstance(error, sp.TransportError):
        assert isinstance(restored, sp.TransportError)
        assert restored.error_kind == "conn_refused"


def test_hierarchy() -> None:
    assert issubclass(sp.TransportError, sp.Unavailable)
    assert issubclass(sp.ReporterClosedError, RuntimeError)
    assert issubclass(sp.ConfigurationError, ValueError)
    assert sp.TransportError("x").reason == "transport"
