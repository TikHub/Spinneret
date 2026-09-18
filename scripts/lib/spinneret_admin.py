#!/usr/bin/env python3
"""Minimal, dependency-free client of the Spinneret admin API for the helper scripts.

It signs in as a console user (session cookie + CSRF header), selects a tenant and offers idempotent
"ensure" helpers used by scripts/example-quickstart.sh and scripts/e2e-failover.sh:

    python3 scripts/lib/spinneret_admin.py example  --env-file deploy/compose/.env [--write-env]
    python3 scripts/lib/spinneret_admin.py failover --env-file deploy/compose/.env

Each command prints one JSON object on stdout. Secrets (passwords, tokens) are never logged; the
token is only part of the JSON output (and of the .env file with --write-env).
"""

from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import sys
import tempfile
import time
import urllib.error
import urllib.request
from typing import Any

JSONDict = dict[str, Any]


class ApiError(Exception):
    """A Connect error returned by Spinneret."""

    def __init__(self, status: int, code: str, message: str, reason: str) -> None:
        super().__init__(f"{code} ({reason or 'no reason'}): {message}")
        self.status = status
        self.code = code
        self.reason = reason


def read_env_file(path: str) -> dict[str, str]:
    """Parses a docker compose .env file (KEY=VALUE lines, # comments)."""
    values: dict[str, str] = {}
    if not os.path.exists(path):
        return values
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, value = line.partition("=")
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
                value = value[1:-1]
            values[key.strip()] = value
    return values


def set_env_value(path: str, key: str, value: str) -> None:
    """Sets KEY=value in an .env file atomically, keeping every other line and the file mode."""
    lines: list[str] = []
    mode = 0o600
    if os.path.exists(path):
        mode = os.stat(path).st_mode & 0o777
        with open(path, encoding="utf-8") as fh:
            lines = fh.read().splitlines()
    replaced = False
    for i, line in enumerate(lines):
        if line.split("=", 1)[0].strip() == key:
            lines[i] = f"{key}={value}"
            replaced = True
    if not replaced:
        lines.append(f"{key}={value}")
    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(prefix=".env.", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write("\n".join(lines) + "\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise


class Admin:
    """Cookie-authenticated admin API client."""

    def __init__(self, base_url: str, timeout: float = 60.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.tenant_id = ""
        self._opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, service: str, method: str, body: JSONDict | None = None, retries: int = 3) -> JSONDict:
        """Calls /spinneret.v1.<service>/<method> with a JSON body; retries LB-level unavailability."""
        headers = {"Content-Type": "application/json", "X-Spinneret-CSRF": "1"}
        if self.tenant_id:
            headers["X-Spinneret-Tenant"] = self.tenant_id
        data = json.dumps(body or {}).encode()
        url = f"{self.base_url}/spinneret.v1.{service}/{method}"
        for attempt in range(retries + 1):
            request = urllib.request.Request(url, data=data, headers=headers, method="POST")
            try:
                with self._opener.open(request, timeout=self.timeout) as resp:
                    return json.loads(resp.read() or b"{}")
            except urllib.error.HTTPError as err:
                raw = err.read()
                try:
                    payload = json.loads(raw or b"{}")
                except ValueError:
                    payload = {}
                code = payload.get("code", "")
                if err.code in (502, 503, 504) and not code and attempt < retries:
                    time.sleep(0.5 * (attempt + 1))
                    continue
                raise ApiError(
                    err.code,
                    code or f"http_{err.code}",
                    payload.get("message", raw[:200].decode(errors="replace")),
                    err.headers.get("Spinneret-Reason", ""),
                ) from None
            except urllib.error.URLError as err:
                if attempt < retries:
                    time.sleep(0.5 * (attempt + 1))
                    continue
                raise ApiError(0, "unavailable", f"cannot reach {self.base_url}: {err.reason}", "") from None
        raise AssertionError("unreachable")

    def login(self, username: str, password: str, tenant: str = "default") -> JSONDict:
        # Only load-balancer level 502/503/504 answers (no Connect error body) are retried, so a wrong
        # password still counts as one attempt against the login throttle.
        me = self.call("AuthService", "Login", {"username": username, "password": password})
        for access in me.get("tenants", []):
            if access["tenant"]["name"] == tenant:
                self.tenant_id = access["tenant"]["id"]
                return me
        raise SystemExit(f"tenant {tenant!r} is not accessible for user {username!r}")

    # --- idempotent helpers -------------------------------------------------------------------------

    def ensure_site(self, namespace: str, name: str, clients: list[str], display_name: str) -> tuple[JSONDict, bool]:
        try:
            site = self.call("SiteAdminService", "GetSite", {"namespace": namespace, "name": name})["site"]
            missing = [c for c in clients if c not in site["clients"]]
            if missing:
                site = self.call(
                    "SiteAdminService", "UpdateSite", {"id": site["id"], "clients": site["clients"] + missing}
                )["site"]
            return site, False
        except ApiError as err:
            if err.code != "not_found" and err.reason != "site_unknown":
                raise
        site = self.call(
            "SiteAdminService",
            "CreateSite",
            {"namespace": namespace, "name": name, "display_name": display_name, "clients": clients},
        )["site"]
        return site, True

    def ensure_group(self, namespace: str, site: str, client: str, name: str, rules: list[JSONDict]) -> bool:
        groups = self.call(
            "SiteAdminService",
            "ListEndpointGroups",
            {"namespace": namespace, "site": site, "client": client, "page_size": 500},
        )["endpoint_groups"]
        for group in groups:
            if group["name"] == name:
                current = [(r["kind"], r["pattern"]) for r in group["rules"]]
                if current != [(r["kind"], r["pattern"]) for r in rules]:
                    self.call("SiteAdminService", "ReplaceURIRules", {"endpoint_group_id": group["id"], "rules": rules})
                return False
        self.call(
            "SiteAdminService",
            "CreateEndpointGroup",
            {"namespace": namespace, "site": site, "client": client, "name": name, "rules": rules},
        )
        return True

    def ensure_identity_type(self, namespace: str, site: str, name: str, spec_yaml: str) -> bool:
        types = self.call(
            "IdentityAdminService",
            "ListIdentityTypes",
            {"namespace": namespace, "site": site, "search": name, "page_size": 500},
        )["identity_types"]
        for t in types:
            if t["name"] == name:
                if t["spec_yaml"].strip() != spec_yaml.strip():
                    self.call("IdentityAdminService", "UpdateIdentityType", {"id": t["id"], "spec_yaml": spec_yaml})
                return False
        self.call(
            "IdentityAdminService", "CreateIdentityType", {"namespace": namespace, "site": site, "spec_yaml": spec_yaml}
        )
        return True

    def import_identities(self, namespace: str, site: str, type_name: str, rows: list[JSONDict]) -> JSONDict:
        data = "\n".join(json.dumps(row, separators=(",", ":")) for row in rows) + "\n"
        result = self.call(
            "IdentityAdminService",
            "ImportIdentities",
            {"namespace": namespace, "site": site, "type": type_name, "format": "jsonl", "data": data},
        )
        if result.get("failed"):
            raise SystemExit(f"identity import failed: {result['failed'][:3]}")
        return result

    def import_proxies(self, namespace: str, lines: list[str]) -> JSONDict:
        result = self.call(
            "ProxyAdminService",
            "ImportProxies",
            {"namespace": namespace, "format": "lines", "data": "\n".join(lines) + "\n"},
        )
        if result.get("failed"):
            raise SystemExit(f"proxy import failed: {result['failed'][:3]}")
        return result

    def ensure_policy(self, namespace: str, kind: str, name: str, yaml: str) -> str:
        """Creates and publishes the policy, or publishes a new version when its YAML changed."""
        policies = self.call(
            "PolicyAdminService",
            "ListPolicies",
            {"namespace": namespace, "kind": kind, "search": name, "page_size": 500},
        )["policies"]
        for p in policies:
            if p["name"] == name:
                # Published YAML is stored normalized (defaults filled in): compare normalized forms.
                published = self.call("PolicyAdminService", "GetPolicy", {"id": p["id"]})["policy"]["published_yaml"]
                wanted = self.call("PolicyAdminService", "ValidatePolicy", {"kind": kind, "yaml": yaml})
                if not wanted.get("valid"):
                    raise SystemExit(f"policy {name} is invalid: {wanted.get('errors')}")
                if published.strip() == wanted["normalized_yaml"].strip():
                    return "unchanged"
                self.call("PolicyAdminService", "SaveDraft", {"id": p["id"], "yaml": yaml})
                self.call("PolicyAdminService", "PublishPolicy", {"id": p["id"], "comment": "quickstart update"})
                return "updated"
        self.call(
            "PolicyAdminService",
            "CreatePolicy",
            {"namespace": namespace, "kind": kind, "yaml": yaml, "publish": True, "comment": "quickstart"},
        )
        return "created"

    def ensure_config(self, namespace: str, group: str, key: str, fmt: str, content: str) -> str:
        try:
            item = self.call(
                "ConfigAdminService", "GetConfigItem", {"locator": {"namespace": namespace, "group": group, "key": key}}
            )["item"]
        except ApiError as err:
            if err.code != "not_found":
                raise
            self.call(
                "ConfigAdminService",
                "CreateConfigItem",
                {
                    "namespace": namespace,
                    "group": group,
                    "key": key,
                    "format": fmt,
                    "content": content,
                    "publish": True,
                },
            )
            return "created"
        if item["current_version"] > 0 and item["published_content"] == content:
            return "unchanged"
        self.call("ConfigAdminService", "SaveConfigDraft", {"id": item["id"], "content": content})
        self.call("ConfigAdminService", "PublishConfig", {"id": item["id"], "comment": "quickstart update"})
        return "updated"

    def tokens_named(self, namespace: str, prefix: str) -> list[JSONDict]:
        """Active tokens named `prefix` or `prefix-<suffix>`."""
        tokens = self.call("AccessAdminService", "ListTokens", {"namespace": namespace, "page_size": 500})["tokens"]
        return [t for t in tokens if t["name"] == prefix or t["name"].startswith(prefix + "-")]

    def ensure_token(
        self, namespace: str, prefix: str, scopes: list[str], current: str = ""
    ) -> tuple[JSONDict, str, bool]:
        """Returns (token, plaintext, reused). An active token named `prefix[-*]` with the same scopes whose
        token prefix matches `current` is reused; otherwise those tokens are revoked and a new one named
        `prefix-<UTC timestamp>` is created (token names stay reserved after revocation)."""
        named = self.tokens_named(namespace, prefix)
        for t in named:
            if current and current.startswith(t["token_prefix"]) and sorted(t["scopes"]) == sorted(scopes):
                return t, current, True
        for t in named:
            self.revoke_token(t["id"])
        created = self.call(
            "AccessAdminService",
            "CreateToken",
            {
                "namespace": namespace,
                "name": f"{prefix}-{time.strftime('%Y%m%d%H%M%S', time.gmtime())}",
                "scopes": scopes,
                "description": "created by scripts/lib/spinneret_admin.py",
            },
        )
        return created["token"], created["plaintext"], False

    def revoke_token(self, token_id: str) -> None:
        self.call("AccessAdminService", "RevokeToken", {"id": token_id})


# --- commands ----------------------------------------------------------------------------------------

EXAMPLE_TYPE_YAML = """name: example_web_cookie
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
activation: immediate
deliver:
  cookies: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
"""


def example_policies(site: str, with_proxies: bool) -> dict[str, tuple[str, str]]:
    bind = f"bind: {{ site: {site} }}\n"
    proxy = "proxy:\n  mode: bind_identity\n  kinds: [datacenter]\n" if with_proxies else "proxy:\n  mode: none\n"
    return {
        "rotation": (
            "example-rotation",
            "name: example-rotation\n" + bind + "rotation:\n  strategy: weighted_random\n  lease_ttl: 60s\n"
            "  max_concurrent_leases: 1\n  reuse_interval: 1s\n" + proxy,
        ),
        "signal": (
            "example-signal",
            "name: example-signal\n" + bind + "rules:\n"
            "  - { name: proxy-error, when: { error_kind: [proxy_auth, conn_refused] }, outcome: proxy_error }\n"
            "  - { name: network-error, when: { error_kind: [timeout, conn_reset, tls, dns] }, outcome: network_error }\n"
            "  - { name: captcha, when: { markers: [captcha_page] }, outcome: captcha }\n"
            "  - { name: login-redirect, when: { markers: [login_redirect] }, outcome: auth_invalid }\n"
            "  - { name: empty-list, when: { markers: [empty_list] }, outcome: empty }\n"
            "  - { name: rate-limited, when: { http_status: [429] }, outcome: rate_limited }\n"
            "  - { name: target-error, when: { http_status: { gte: 500 } }, outcome: target_error }\n"
            "  - { name: success, when: { http_status: { gte: 200, lt: 300 } }, outcome: success }\n",
        ),
    }


EXAMPLE_CONFIG = json.dumps(
    {"search_page_size": 10, "item_fields": ["id", "title"], "greeting": "hello from Spinneret"}
)


def cmd_example(admin: Admin, args: argparse.Namespace, env: dict[str, str]) -> JSONDict:
    ns, site = args.namespace, args.site
    started = time.monotonic()
    _, site_created = admin.ensure_site(ns, site, ["web"], "Example site (mock target)")
    groups = [
        admin.ensure_group(ns, site, "web", "search", [{"kind": "prefix", "pattern": "/site/search"}]),
        admin.ensure_group(ns, site, "web", "detail", [{"kind": "template", "pattern": "/site/item/{id}"}]),
    ]
    admin.ensure_identity_type(ns, site, "example_web_cookie", EXAMPLE_TYPE_YAML)
    rows = [
        {
            "cookies": {"sessionid": f"example-{i:02d}", "csrftoken": f"csrf-{i:02d}"},
            "user_agent": f"ExampleCrawler/{i:02d}",
        }
        for i in range(args.identities)
    ]
    imported = admin.import_identities(ns, site, "example_web_cookie", rows)
    proxies: JSONDict = {}
    if args.proxies > 0:
        lines = [
            f"http://expx{i}:{args.proxy_password}@{args.proxy_host} kind=datacenter provider=mock max_concurrency=20"
            for i in range(1, args.proxies + 1)
        ]
        proxies = admin.import_proxies(ns, lines)
    policies = {
        kind: admin.ensure_policy(ns, kind, name, yaml)
        for kind, (name, yaml) in example_policies(site, args.proxies > 0).items()
    }
    config = admin.ensure_config(ns, "crawler", "example.json", "json", EXAMPLE_CONFIG)
    _, token, reused = admin.ensure_token(
        ns,
        "example-crawler",
        [f"lease:acquire:{site}", f"report:write:{site}", "config:read:crawler"],
        current=env.get("EXAMPLE_TOKEN", ""),
    )
    if args.write_env and not reused:
        set_env_value(args.env_file, "EXAMPLE_TOKEN", token)
    return {
        "namespace": ns,
        "site": site,
        "site_created": site_created,
        "groups_created": sum(groups),
        "identities": {k: imported.get(k, 0) for k in ("created", "updated", "unchanged")},
        "proxies": {k: proxies.get(k, 0) for k in ("created", "updated", "unchanged")},
        "policies": policies,
        "config": config,
        "token_reused": reused,
        "token": token,
        "seconds": round(time.monotonic() - started, 3),
    }


def cmd_failover(admin: Admin, args: argparse.Namespace, env: dict[str, str]) -> JSONDict:
    ns, site = args.namespace, args.site
    admin.ensure_site(ns, site, ["web"], "Failover drill")
    admin.ensure_group(ns, site, "web", "api", [{"kind": "prefix", "pattern": "/api/"}])
    admin.ensure_identity_type(ns, site, "failover_token", FAILOVER_TYPE_YAML)
    rows = [{"api_key": f"failover-{i:04d}"} for i in range(args.identities)]
    imported = admin.import_identities(ns, site, "failover_token", rows)
    info, token, _ = admin.ensure_token(ns, f"{site}-drill", [f"lease:acquire:{site}", f"report:write:{site}"])
    return {"namespace": ns, "site": site, "identities": imported, "token": token, "token_id": info["id"]}


def cmd_example_clean(admin: Admin, args: argparse.Namespace, env: dict[str, str]) -> JSONDict:
    """Deletes everything cmd_example creates, so that the quickstart can be timed from a clean state."""
    ns, site = args.namespace, args.site
    removed: JSONDict = {"site": False, "tokens": 0, "proxies": 0, "policies": 0, "config": False}
    try:
        current = admin.call("SiteAdminService", "GetSite", {"namespace": ns, "name": site})["site"]
        # Deleting the site removes its endpoint groups, identity types, identities and policy bindings.
        admin.call("SiteAdminService", "DeleteSite", {"id": current["id"], "force": True})
        removed["site"] = True
    except ApiError as err:
        if err.code != "not_found" and err.reason != "site_unknown":
            raise
    for t in admin.tokens_named(ns, "example-crawler"):
        admin.revoke_token(t["id"])
        removed["tokens"] += 1
    proxies = admin.call(
        "ProxyAdminService", "ListProxies", {"namespace": ns, "search": args.proxy_host, "page_size": 500}
    )["proxies"]
    ids = [p["id"] for p in proxies if p["username_hint"].startswith("expx")]
    if ids:
        admin.call("ProxyAdminService", "DeleteProxies", {"ids": ids})
        removed["proxies"] = len(ids)
    for kind, (name, _) in example_policies(site, True).items():
        for p in admin.call("PolicyAdminService", "ListPolicies", {"namespace": ns, "kind": kind, "search": name})[
            "policies"
        ]:
            if p["name"] == name:
                for b in p.get("bindings", []):
                    admin.call("PolicyAdminService", "DeleteBinding", {"id": b["id"]})
                admin.call("PolicyAdminService", "DeletePolicy", {"id": p["id"]})
                removed["policies"] += 1
    try:
        item = admin.call(
            "ConfigAdminService",
            "GetConfigItem",
            {"locator": {"namespace": ns, "group": "crawler", "key": "example.json"}},
        )["item"]
        admin.call("ConfigAdminService", "DeleteConfigItem", {"id": item["id"]})
        removed["config"] = True
    except ApiError as err:
        if err.code != "not_found":
            raise
    return removed


def cmd_revoke_token(admin: Admin, args: argparse.Namespace, env: dict[str, str]) -> JSONDict:
    admin.revoke_token(args.token_id)
    return {"revoked": args.token_id}


FAILOVER_TYPE_YAML = """name: failover_token
client: web
fields:
  api_key: { type: string, required: true, sensitive: true }
unique_by: [api_key]
activation: immediate
deliver:
  headers:
    X-Api-Key: "{{ api_key }}"
"""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=["example", "example-clean", "failover", "revoke-token"])
    parser.add_argument("--env-file", default="deploy/compose/.env")
    parser.add_argument("--url", default="")
    parser.add_argument("--tenant", default="default")
    parser.add_argument("--namespace", default="default")
    parser.add_argument("--site", default="")
    parser.add_argument("--identities", type=int, default=0)
    parser.add_argument("--proxies", type=int, default=2)
    parser.add_argument("--proxy-host", default="mocktarget:9091")
    parser.add_argument("--proxy-password", default="secret")
    parser.add_argument("--write-env", action="store_true", help="store EXAMPLE_TOKEN in --env-file")
    parser.add_argument("--token-id", default="", help="token to revoke (revoke-token)")
    args = parser.parse_args(argv)

    env = read_env_file(args.env_file)
    username = os.environ.get("SPINNERET_ADMIN_USERNAME") or env.get("SPINNERET_ADMIN_USERNAME") or "admin"
    password = os.environ.get("SPINNERET_ADMIN_PASSWORD") or env.get("SPINNERET_ADMIN_PASSWORD")
    if not password:
        print("SPINNERET_ADMIN_PASSWORD is not set (run scripts/compose-init.sh)", file=sys.stderr)
        return 2
    url = args.url or f"http://localhost:{env.get('SPINNERET_PORT', '8080')}"
    if args.command in ("example", "example-clean"):
        args.site = args.site or "example"
        args.identities = args.identities or 20
        handler = cmd_example if args.command == "example" else cmd_example_clean
    elif args.command == "failover":
        args.site = args.site or "failover"
        args.identities = args.identities or 200
        handler = cmd_failover
    else:
        if not args.token_id:
            print("--token-id is required", file=sys.stderr)
            return 2
        handler = cmd_revoke_token

    admin = Admin(url)
    try:
        admin.login(username, password, args.tenant)
        result = handler(admin, args, env)
    except ApiError as err:
        print(f"spinneret API error: {err}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
