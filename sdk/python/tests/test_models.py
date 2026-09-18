from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest
from pydantic import ValidationError

import spinneret as sp
from spinneret.models import ERROR_KINDS, format_timestamp

from ._helpers import RESOLVED_SECRET_VALUE, acquire_payload, config_item, secret_config_item


def test_acquire_response_parses_full_payload() -> None:
    response = sp.AcquireResponse.model_validate(acquire_payload())
    assert response.lease.lease_id.startswith("lse_")
    assert response.lease.expires_at == datetime(2026, 9, 16, 8, 32, 10, tzinfo=timezone.utc)
    assert response.credential.cookies == {"sessionid": "a1b2c3"}
    assert response.credential.values == {"csrf_token": "x9y8z7"}
    assert response.credential.json_value is None
    assert response.proxy is not None
    assert response.proxy.kind == "residential"
    assert response.hints.renew_before_ms == 30000


def test_tolerant_parsing_nulls_unknown_fields_and_int64_strings() -> None:
    payload = {
        "lease": {"lease_id": "lse_x", "expires_at": None, "future_field": 1},
        "credential": {"cookies": None, "headers": None, "values": None, "json": [1, 2]},
        "proxy": None,
        "hints": None,
        "unknown": {"nested": True},
    }
    response = sp.AcquireResponse.model_validate(payload)
    assert response.lease.expires_at is None
    assert response.credential.cookies == {}
    assert response.credential.values == {}
    assert response.credential.json_value == [1, 2]
    assert response.proxy is None
    assert response.hints.renew_before_ms == 0
    item = sp.ConfigItem.model_validate(config_item(version=9_007_199_254_740_993))
    assert item.version == 9_007_199_254_740_993


def test_nanosecond_timestamps_are_accepted() -> None:
    renew = sp.RenewResponse.model_validate({"expires_at": "2026-09-16T08:32:10.123456789Z"})
    assert renew.expires_at is not None
    assert renew.expires_at.microsecond == 123456


def test_sensitive_fields_hidden_from_repr() -> None:
    response = sp.AcquireResponse.model_validate(acquire_payload())
    text = repr(response)
    assert "a1b2c3" not in text
    assert "user:pass" not in text
    assert "x9y8z7" not in text
    secret = sp.GetSecretResponse(path="p", version=1, value="hunter2")
    assert "hunter2" not in repr(secret)
    item = sp.ConfigItem.model_validate(secret_config_item())
    assert RESOLVED_SECRET_VALUE not in repr(item)


def test_models_are_immutable() -> None:
    lease = sp.Lease(lease_id="a")
    with pytest.raises(ValidationError):
        lease.lease_id = "b"  # type: ignore[misc]


@pytest.mark.parametrize(
    "kwargs",
    [
        {"site": "", "client": "web"},
        {"site": "s", "client": ""},
        {"site": "s", "client": "web", "wait_ms": 5001},
        {"site": "s", "client": "web", "wait_ms": -1},
        {"site": "s" * 65, "client": "web"},
    ],
)
def test_acquire_request_validation(kwargs: dict[str, object]) -> None:
    with pytest.raises(ValidationError):
        sp.AcquireRequest.model_validate(kwargs)


def test_acquire_batch_count_bounds() -> None:
    with pytest.raises(ValidationError):
        sp.AcquireBatchRequest(site="s", client="c", count=51)
    assert sp.AcquireBatchRequest(site="s", client="c", count=50).to_json_dict()["count"] == 50


def test_report_defaults_and_serialization() -> None:
    report = sp.Report(lease_id="lse_1", uri="/a", business_code=10001, latency_ms=250)
    assert len(report.report_id) == 36
    assert report.business_code == "10001"
    assert report.finished_at - report.started_at == timedelta(milliseconds=250)
    wire = report.to_json_dict()
    assert wire["business_code"] == "10001"
    assert wire["started_at"].endswith("Z")
    assert wire["finished_at"].endswith("Z")
    assert set(wire) == {
        "report_id",
        "lease_id",
        "uri",
        "method",
        "http_status",
        "business_code",
        "error_kind",
        "markers",
        "outcome_hint",
        "latency_ms",
        "response_bytes",
        "started_at",
        "finished_at",
        "release",
    }


def test_report_latency_derived_from_timestamps() -> None:
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    report = sp.Report(
        lease_id="l", uri="/", started_at=start, finished_at=start + timedelta(seconds=1.5)
    )
    assert report.latency_ms == 1500


def test_report_started_defaults_to_finished_without_latency() -> None:
    finished = datetime(2026, 1, 1, tzinfo=timezone.utc)
    report = sp.Report(lease_id="l", uri="/", finished_at=finished)
    assert report.started_at == finished
    assert report.latency_ms == 0


def test_report_from_json_strings() -> None:
    report = sp.Report.model_validate(
        {
            "lease_id": "l",
            "uri": "/",
            "started_at": "2026-01-01T00:00:00Z",
            "finished_at": "2026-01-01T00:00:01Z",
            "latency_ms": None,
        }
    )
    assert report.latency_ms == 0


def test_naive_datetimes_are_local_time() -> None:
    naive = datetime(2026, 1, 1, 12, 0, 0)
    expected = naive.astimezone().astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
    assert format_timestamp(naive) == expected
    report = sp.Report(lease_id="l", uri="/", started_at=naive, finished_at=naive)
    assert report.started_at.tzinfo is not None


@pytest.mark.parametrize(
    "kwargs",
    [
        {"report_id": "bad id!"},
        {"report_id": "x" * 65},
        {"uri": ""},
        {"lease_id": ""},
        {"http_status": 1000},
        {"markers": [""]},
        {"markers": ["m"] * 33},
        {"latency_ms": -1},
        {"response_bytes": -1},
        {
            "started_at": datetime(2026, 1, 2, tzinfo=timezone.utc),
            "finished_at": datetime(2026, 1, 1, tzinfo=timezone.utc),
        },
    ],
)
def test_report_validation(kwargs: dict[str, object]) -> None:
    data: dict[str, object] = {"lease_id": "l", "uri": "/"}
    data.update(kwargs)
    with pytest.raises(ValidationError):
        sp.Report.model_validate(data)


def test_report_request_bounds() -> None:
    report = sp.Report(lease_id="l", uri="/")
    with pytest.raises(ValidationError):
        sp.ReportRequest(reports=[])
    with pytest.raises(ValidationError):
        sp.ReportRequest(reports=[report] * 501)


@pytest.mark.parametrize(
    ("raw", "group", "key"),
    [
        ("crawler/search.json", "crawler", "search.json"),
        ("_runtime/breakers", "_runtime", "breakers"),
        (("g", "k/with/slash"), "g", "k/with/slash"),
        ({"group": "g", "key": "k"}, "g", "k"),
    ],
)
def test_config_key_spellings(raw: object, group: str, key: str) -> None:
    parsed = sp.ConfigKey.model_validate(raw)
    assert (parsed.group, parsed.key) == (group, key)


@pytest.mark.parametrize("raw", ["no-slash", "/k", "g/", ("g",)])
def test_invalid_config_keys(raw: object) -> None:
    with pytest.raises(ValidationError):
        sp.ConfigKey.model_validate(raw)


def test_config_item_helpers() -> None:
    flagged = sp.ConfigItem.model_validate(secret_config_item())
    assert flagged.has_secret_refs
    assert flagged.references_secrets
    assert flagged.config_key == sp.ConfigRef(group="crawler", key="search.json")
    plain = sp.ConfigItem.model_validate(config_item(content="{}"))
    assert not plain.has_secret_refs
    assert not plain.references_secrets
    # The flag defaults to false when absent or null.
    raw = config_item()
    del raw["has_secret_refs"]
    assert not sp.ConfigItem.model_validate(raw).has_secret_refs
    assert not sp.ConfigItem.model_validate({**raw, "has_secret_refs": None}).has_secret_refs
    # Content that still carries an unresolved reference marker counts as secret too.
    unresolved = sp.ConfigItem.model_validate(
        config_item(content='{"k": "${secret:signing/api_key}"}')
    )
    assert not unresolved.has_secret_refs
    assert unresolved.references_secrets


def test_batch_get_missing_accepts_strings_and_objects() -> None:
    response = sp.BatchGetConfigResponse.model_validate(
        {"items": [], "missing": ["g/k", {"group": "g2", "key": "k2"}]}
    )
    assert [(m.group, m.key) for m in response.missing] == [("g", "k"), ("g2", "k2")]


def test_watch_request_bounds() -> None:
    with pytest.raises(ValidationError):
        sp.WatchConfigRequest(items=[sp.WatchItem(group="g", key="k")], timeout_ms=60_001)
    with pytest.raises(ValidationError):
        sp.WatchConfigRequest(items=[])


def test_proto_limits_and_aliases() -> None:
    assert sp.ProxyAssignment is sp.Proxy
    assert sp.ConfigRef is sp.ConfigKey
    with pytest.raises(ValidationError):
        sp.ConfigKey(group="g" * 129, key="k")
    with pytest.raises(ValidationError):
        sp.BatchGetConfigRequest(items=[sp.ConfigKey(group="g", key="k")] * 201)
    with pytest.raises(ValidationError):
        sp.GetSecretRequest(path="Upper/Case")
    assert sp.GetSecretRequest(path="signing/api-key.v2").version == 0


def test_misc_request_validation() -> None:
    with pytest.raises(ValidationError):
        sp.RenewRequest(lease_id="l", extend_ms=1_800_001)
    with pytest.raises(ValidationError):
        sp.ReleaseRequest(lease_id="")
    with pytest.raises(ValidationError):
        sp.GetSecretRequest(path="")
    with pytest.raises(ValidationError):
        sp.GetConfigRequest(group="", key="k")


def test_credential_serializes_json_alias() -> None:
    credential = sp.Credential.model_validate({"json": {"a": 1}})
    assert credential.to_json_dict()["json"] == {"a": 1}


def test_constants() -> None:
    assert sp.ErrorKind.PROXY_AUTH == "proxy_auth"
    assert sp.Outcome.RATE_LIMITED == "rate_limited"
    assert sp.SECRET_REF_MARKER == "${secret:"


@pytest.mark.parametrize("kind", sorted(ERROR_KINDS))
def test_report_accepts_known_error_kinds(kind: str) -> None:
    assert sp.Report(lease_id="l", uri="/a", error_kind=kind).error_kind == kind


@pytest.mark.parametrize("kind", ["TIMEOUT", "reset", " timeout"])
def test_report_rejects_unknown_error_kinds(kind: str) -> None:
    with pytest.raises(ValidationError, match="error_kind"):
        sp.Report(lease_id="l", uri="/a", error_kind=kind)
