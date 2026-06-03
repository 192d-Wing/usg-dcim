"""Unit tests for the zone-freeze write lock.

Skipped module-wide post-PR #33 (DNS cutover): _assert_zone_unfrozen
ported to otter-go (internal/dns/zone_freeze.go). Equivalent coverage
lives in zone_freeze_test.go. The original test body is preserved
below behind the module-level skip so a future re-port or parity
audit has the assertions in tree.
"""

import pytest

pytestmark = pytest.mark.skip(
    reason="ported to otter-go: internal/dns/zone_freeze.go (PR #33)",
)
