from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("sync_auth_pools.py")
SPEC = importlib.util.spec_from_file_location("sync_auth_pools", SCRIPT)
sync = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(sync)


class ReconcilePoolsTest(unittest.TestCase):
    def test_new_auth_is_added_to_every_enabled_pool_for_its_provider(self) -> None:
        pools = [
            {"id": "codex-main", "provider": "codex", "auth_ids": ["codex-old.json"]},
            {"id": "codex-shared", "provider": "codex", "auth_ids": []},
            {"id": "xai-grok", "provider": "xai", "auth_ids": ["xai-old.json"]},
        ]

        updated, changes = sync.reconcile_pools(
            pools,
            {"codex": ["codex-old.json", "codex-new.json"], "xai": ["xai-old.json"]},
        )

        self.assertEqual(updated[0]["auth_ids"], ["codex-old.json", "codex-new.json"])
        self.assertEqual(updated[1]["auth_ids"], ["codex-old.json", "codex-new.json"])
        self.assertEqual(updated[2]["auth_ids"], ["xai-old.json"])
        self.assertEqual(
            changes,
            {"codex-main": ["codex-new.json"], "codex-shared": ["codex-old.json", "codex-new.json"]},
        )

    def test_disabled_and_explicitly_excluded_pools_keep_manual_membership(self) -> None:
        pools = [
            {"id": "disabled", "provider": "codex", "auth_ids": [], "disabled": True},
            {"id": "manual", "provider": "codex", "auth_ids": ["codex-old.json"]},
        ]

        updated, changes = sync.reconcile_pools(
            pools,
            {"codex": ["codex-old.json", "codex-new.json"]},
            excluded_pools={"manual"},
        )

        self.assertEqual(updated, pools)
        self.assertEqual(changes, {})

    def test_reconcile_is_idempotent(self) -> None:
        pools = [{"id": "codex-main", "provider": "codex", "auth_ids": ["codex-new.json"]}]

        updated, changes = sync.reconcile_pools(pools, {"codex": ["codex-new.json"]})

        self.assertEqual(updated, pools)
        self.assertEqual(changes, {})


if __name__ == "__main__":
    unittest.main()
