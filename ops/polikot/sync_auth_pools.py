#!/usr/bin/env python3
"""Add new auth files to every enabled account pool for their provider.

The CLIProxy access policy treats account-pool ``auth_ids`` as an explicit
allowlist. OAuth login and the auth watcher register a new credential, but it
remains invisible to restricted clients until this reconcile updates the
pools. The launchd job combines WatchPaths with a five-minute fallback because
filesystem events can be missed.
"""
from __future__ import annotations

import argparse
import fcntl
import json
import sys
import time
import urllib.request
from datetime import datetime
from pathlib import Path
from typing import Iterable


STATE = Path("/Users/aipolikot/myai/RoutingMainKeys/state/cliproxy")
AUTH_DIR = STATE / "auth"
BASE = "http://127.0.0.1:8317/v0/management"
LOCK_FILE = STATE / ".pool-sync.lock"
EXCLUDE_POOLS: set[str] = set()


def log(message: str) -> None:
    print(f"[{datetime.now():%Y-%m-%d %H:%M:%S}] {message}", flush=True)


def load_management_key(state: Path = STATE) -> str:
    with (state / "dashboard-login.json").open(encoding="utf-8") as handle:
        return str(json.load(handle)["management_key"])


def api(method: str, path: str, key: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(
        f"{BASE}{path}",
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {key}",
            "Content-Type": "application/json",
        },
    )
    last_exception = None
    for attempt in range(3):
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                raw = response.read()
                return json.loads(raw) if raw else {}
        except Exception as exc:  # noqa: BLE001 - never log credential contents
            last_exception = exc
            time.sleep(5 * (attempt + 1))
    raise RuntimeError(f"{method} {path} failed: {type(last_exception).__name__}")


def auth_files_by_provider(auth_dir: Path = AUTH_DIR) -> dict[str, list[str]]:
    result: dict[str, list[str]] = {}
    for path in sorted(auth_dir.glob("*.json")):
        try:
            with path.open(encoding="utf-8") as handle:
                provider = str(json.load(handle).get("type") or "").strip().lower()
        except (OSError, ValueError):
            continue
        if provider:
            result.setdefault(provider, []).append(path.name)
    return result


def reconcile_pools(
    pools: Iterable[dict],
    auths_by_provider: dict[str, list[str]],
    excluded_pools: set[str] = EXCLUDE_POOLS,
) -> tuple[list[dict], dict[str, list[str]]]:
    """Return updated pool copies and the auth IDs added to each pool."""
    updated: list[dict] = []
    changes: dict[str, list[str]] = {}
    for original in pools:
        pool = dict(original)
        pool_id = str(pool.get("id") or "").strip()
        provider = str(pool.get("provider") or "").strip().lower()
        if not pool_id or pool.get("disabled") or pool_id in excluded_pools or not provider:
            updated.append(pool)
            continue
        current = [auth_id for auth_id in (pool.get("auth_ids") or []) if auth_id]
        missing = [
            auth_id
            for auth_id in auths_by_provider.get(provider, [])
            if auth_id not in current
        ]
        if missing:
            pool["auth_ids"] = current + missing
            changes[pool_id] = missing
        updated.append(pool)
    return updated, changes


def parse_args(argv=None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="Report missing pool memberships without changing the live config.",
    )
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    with LOCK_FILE.open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            log("another sync is already running")
            return 0

        key = load_management_key()
        pools = api("GET", "/account-pools", key)["account-pools"]
        updated, changes = reconcile_pools(pools, auth_files_by_provider())
        if not changes:
            log("no changes - every auth already belongs to its provider pools")
            return 0

        for pool_id, missing in changes.items():
            log(f"pool {pool_id} (+{len(missing)}): {', '.join(missing)}")
        if args.check:
            log("FAIL: pool membership drift detected")
            return 1

        api("PUT", "/account-pools", key, body=updated)
        time.sleep(2)
        final = api("GET", "/account-pools", key)["account-pools"]
        actual_counts = {pool["id"]: len(pool.get("auth_ids") or []) for pool in final}
        expected_counts = {pool["id"]: len(pool.get("auth_ids") or []) for pool in updated}
        if actual_counts != expected_counts:
            log(f"FAIL: membership after PUT differs; expected {expected_counts}")
            return 1
        log(f"updated pools: {len(changes)}; membership: {actual_counts}")
        return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # noqa: BLE001
        log(f"FAIL: {type(exc).__name__}: {exc}")
        sys.exit(1)
