"""Python SDK for Spinneret, the control plane for crawler and API nodes.

Quick start::

    import httpx
    import spinneret

    with spinneret.Client() as client:  # reads SPINNERET_URL / SPINNERET_TOKEN
        with client.lease(site="shop", client="web", uri="/api/v1/feed") as lease:
            with httpx.Client(**lease.httpx_kwargs()) as http:
                response = http.get("https://target.example.com/api/v1/feed")
            lease.report_response(response)

The SDK logs through ``logging.getLogger("spinneret")`` and never logs tokens,
credentials, proxy URLs, config contents or secret values.
"""

import logging

from ._calls import ConfigKeyLike, Timeouts
from ._report_buffer import ReporterOptions, ReporterStats
from ._retry import NO_RETRY, Backoff, RetryPolicy
from ._snapshot import SecretPredicate, SnapshotStore
from ._version import __version__
from ._watch_core import WatchOptions
from .async_client import AsyncClient
from .async_lease import AsyncManagedLease
from .async_reporter import AsyncReporter
from .async_watcher import AsyncChangeCallback, AsyncConfigWatcher
from .client import Client
from .errorkind import classify_exception
from .errors import (
    Aborted,
    AlreadyExists,
    CircuitOpen,
    ConfigurationError,
    DeadlineExceeded,
    FailedPrecondition,
    InternalError,
    InvalidArgument,
    LeaseExpired,
    LeaseReleased,
    LeaseUnknown,
    NoIdentityAvailable,
    NoProxyAvailable,
    NotFound,
    PermissionDenied,
    ReporterClosedError,
    ResourceExhausted,
    SitePaused,
    SpinneretError,
    TransportError,
    Unauthenticated,
    Unavailable,
    Unimplemented,
)
from .lease import ManagedLease
from .models import (
    SECRET_REF_MARKER,
    AcquireBatchRequest,
    AcquireBatchResponse,
    AcquireRequest,
    AcquireResponse,
    BatchGetConfigRequest,
    BatchGetConfigResponse,
    ConfigItem,
    ConfigKey,
    ConfigRef,
    Credential,
    ErrorKind,
    GetConfigRequest,
    GetConfigResponse,
    GetSecretRequest,
    GetSecretResponse,
    Hints,
    Lease,
    Outcome,
    Proxy,
    ProxyAssignment,
    RejectedReport,
    ReleaseRequest,
    ReleaseResponse,
    RenewRequest,
    RenewResponse,
    Report,
    ReportRequest,
    ReportResponse,
    WatchConfigRequest,
    WatchConfigResponse,
    WatchItem,
)
from .reporter import Reporter
from .settings import Settings
from .watcher import ChangeCallback, ConfigWatcher

logging.getLogger("spinneret").addHandler(logging.NullHandler())

__all__ = [
    "NO_RETRY",
    "SECRET_REF_MARKER",
    "Aborted",
    "AcquireBatchRequest",
    "AcquireBatchResponse",
    "AcquireRequest",
    "AcquireResponse",
    "AlreadyExists",
    "AsyncChangeCallback",
    "AsyncClient",
    "AsyncConfigWatcher",
    "AsyncManagedLease",
    "AsyncReporter",
    "Backoff",
    "BatchGetConfigRequest",
    "BatchGetConfigResponse",
    "ChangeCallback",
    "CircuitOpen",
    "Client",
    "ConfigItem",
    "ConfigKey",
    "ConfigKeyLike",
    "ConfigRef",
    "ConfigWatcher",
    "ConfigurationError",
    "Credential",
    "DeadlineExceeded",
    "ErrorKind",
    "FailedPrecondition",
    "GetConfigRequest",
    "GetConfigResponse",
    "GetSecretRequest",
    "GetSecretResponse",
    "Hints",
    "InternalError",
    "InvalidArgument",
    "Lease",
    "LeaseExpired",
    "LeaseReleased",
    "LeaseUnknown",
    "ManagedLease",
    "NoIdentityAvailable",
    "NoProxyAvailable",
    "NotFound",
    "Outcome",
    "PermissionDenied",
    "Proxy",
    "ProxyAssignment",
    "RejectedReport",
    "ReleaseRequest",
    "ReleaseResponse",
    "RenewRequest",
    "RenewResponse",
    "Report",
    "ReportRequest",
    "ReportResponse",
    "Reporter",
    "ReporterClosedError",
    "ReporterOptions",
    "ReporterStats",
    "ResourceExhausted",
    "RetryPolicy",
    "SecretPredicate",
    "Settings",
    "SitePaused",
    "SnapshotStore",
    "SpinneretError",
    "Timeouts",
    "TransportError",
    "Unauthenticated",
    "Unavailable",
    "Unimplemented",
    "WatchConfigRequest",
    "WatchConfigResponse",
    "WatchItem",
    "WatchOptions",
    "__version__",
    "classify_exception",
]
