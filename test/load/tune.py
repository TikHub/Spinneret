#!/usr/bin/env python3
"""Publish a variant of the load-test rotation policy to measure a tuning knob.

`spnr seed` binds the rotation policy `loadtest-rotation` to the load-test site with
candidate_sample 32, max_concurrent_leases 1 and no reuse interval. This script
republishes that policy with different values so a scenario can be re-run against the
same data, and restores the seeded values afterwards.

    python3 test/load/tune.py --candidate-sample 8
    python3 test/load/tune.py --max-concurrent-leases 4
    python3 test/load/tune.py --restore

The bound policy takes effect on the next acquire (the catalog reload is pushed to
every instance), so no restart is needed. Admin credentials come from
deploy/compose/.env.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "scripts" / "lib"))

from spinneret_admin import Admin, read_env_file  # noqa: E402

# Defaults of cmd/spnr/seeddata.go (seedRotationYAML).
SEED = {
    "policy": "loadtest-rotation",
    "identity_type": "loadtest_cookie",
    "strategy": "weighted_random",
    "candidate_sample": 32,
    "lease_ttl": "60s",
    "max_concurrent_leases": 1,
    "reuse_interval": "0s",
}


def rotation_yaml(v: dict) -> str:
    return (
        f"name: {v['policy']}\n"
        "description: Rotation policy of the load-test site (spnr seed).\n"
        f"identity_types: [{v['identity_type']}]\n"
        "rotation:\n"
        f"  strategy: {v['strategy']}\n"
        f"  candidate_sample: {v['candidate_sample']}\n"
        f"  lease_ttl: {v['lease_ttl']}\n"
        f"  max_concurrent_leases: {v['max_concurrent_leases']}\n"
        f"  reuse_interval: {v['reuse_interval']}\n"
        "proxy:\n"
        "  mode: none\n"
    )


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default="http://localhost:8080")
    ap.add_argument("--env-file", default=str(REPO / "deploy" / "compose" / ".env"))
    ap.add_argument("--namespace", default="default")
    ap.add_argument("--policy", default=SEED["policy"])
    ap.add_argument("--identity-type", default=SEED["identity_type"])
    ap.add_argument("--strategy", default=SEED["strategy"], help="weighted_random | round_robin | least_recently_used | best_score")
    ap.add_argument("--candidate-sample", type=int, default=SEED["candidate_sample"])
    ap.add_argument("--lease-ttl", default=SEED["lease_ttl"])
    ap.add_argument("--max-concurrent-leases", type=int, default=SEED["max_concurrent_leases"])
    ap.add_argument("--reuse-interval", default=SEED["reuse_interval"])
    ap.add_argument("--restore", action="store_true", help="publish the values `spnr seed` uses")
    args = ap.parse_args(argv)

    values = dict(SEED)
    if not args.restore:
        values.update({
            "policy": args.policy,
            "identity_type": args.identity_type,
            "strategy": args.strategy,
            "candidate_sample": args.candidate_sample,
            "lease_ttl": args.lease_ttl,
            "max_concurrent_leases": args.max_concurrent_leases,
            "reuse_interval": args.reuse_interval,
        })
    yaml = rotation_yaml(values)

    env = read_env_file(args.env_file)
    password = env.get("SPINNERET_ADMIN_PASSWORD", "")
    if not password:
        ap.error(f"SPINNERET_ADMIN_PASSWORD is not set in {args.env_file}")
    admin = Admin(args.url)
    admin.login(env.get("SPINNERET_ADMIN_USERNAME", "admin"), password)
    result = admin.ensure_policy(args.namespace, "rotation", values["policy"], yaml)
    print(json.dumps({"policy": values["policy"], "result": result, "values": values}, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
