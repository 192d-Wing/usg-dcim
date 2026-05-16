"""In-process sliding-window rate limiter.

Used by /auth/login to throttle credential-stuffing / brute-force. Single
worker, single deque per key; for multi-worker production the same shape
maps cleanly onto a Redis-backed implementation (LPUSH + LREM old) — out
of scope for the dev stack.
"""

from __future__ import annotations

import time
from collections import defaultdict, deque


class SlidingWindowLimiter:
    def __init__(self, max_attempts: int, window_seconds: int) -> None:
        self.max = max_attempts
        self.window = window_seconds
        self._buckets: dict[str, deque[float]] = defaultdict(deque)

    def consume(self, key: str) -> tuple[bool, int]:
        """Record an attempt against `key`. Returns (allowed, count_in_window).

        When the bucket already holds `max_attempts` recent timestamps,
        the attempt is rejected (allowed=False) and the bucket is NOT
        appended — repeated rejections don't extend the lockout.
        """
        now = time.monotonic()
        cutoff = now - self.window
        bucket = self._buckets[key]
        while bucket and bucket[0] < cutoff:
            bucket.popleft()
        if len(bucket) >= self.max:
            return (False, len(bucket))
        bucket.append(now)
        return (True, len(bucket))

    def reset(self, key: str) -> None:
        """Clear the bucket on successful auth so the next attempt starts fresh."""
        self._buckets.pop(key, None)
