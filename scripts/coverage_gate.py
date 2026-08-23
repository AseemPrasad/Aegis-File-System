"""Go coverage gate.

Usage: python scripts/coverage_gate.py coverage.out 85

Parses a raw `go test -coverprofile` file and fails (exit 1) when total
statement coverage is below the threshold. Mirrors the 85% acceptance gate
from PROMPT 1.2 / PROMPT 9.1.
"""

from __future__ import annotations

import sys


def total_coverage(profile_path: str) -> float:
    covered = uncovered = 0
    with open(profile_path, encoding="utf-8") as fh:
        header = fh.readline()
        if not header.startswith("mode:"):
            raise ValueError("not a go coverage profile (missing 'mode:' header)")
        for line in fh:
            parts = line.strip().split()
            if len(parts) != 3:
                continue
            num_stmts = int(parts[1])
            hit_count = int(parts[2])
            if hit_count > 0:
                covered += num_stmts
            else:
                uncovered += num_stmts
    total = covered + uncovered
    return (covered / total) * 100 if total else 100.0


def main() -> int:
    profile = sys.argv[1] if len(sys.argv) > 1 else "coverage.out"
    threshold = float(sys.argv[2]) if len(sys.argv) > 2 else 85.0

    pct = total_coverage(profile)
    print(f"total coverage: {pct:.1f}% (gate: {threshold:.1f}%)")
    if pct < threshold:
        print("COVERAGE GATE FAILED")
        return 1
    print("COVERAGE GATE PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
