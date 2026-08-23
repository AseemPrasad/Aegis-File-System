"""Environment verification for Project Aegis development.

Usage: python scripts/check_env.py

Checks toolchain presence and minimum versions required by PROMPT 1.2.
Exit code 0 = environment ready; nonzero lists what to install.
Optional tools warn instead of failing (CI enforces them).
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys

CHECKS = [
    # (tool, min_version, version_cmd)
    ("python", (3, 12), ["--version"]),
    ("git", (2, 40), ["--version"]),
    ("go", (1, 23), ["version"]),
    ("docker", (24, 0), ["--version"]),
    ("cargo", (1, 79), ["--version"]),
    ("terraform", (1, 9), ["-version"]),
    ("golangci-lint", (1, 60), ["--version"]),
    ("trivy", (0, 55), ["--version"]),
]

OPTIONAL = {"trivy", "golangci-lint"}  # CI-enforced; local absence warns only


def parse_semver(text: str) -> tuple[int, ...]:
    m = re.search(r"(\d+\.\d+(?:\.\d+)?)", text or "")
    return tuple(int(p) for p in m.group(1).split(".")) if m else ()


def main() -> int:
    failures: list[str] = []
    warnings: list[str] = []

    for tool, min_ver, cmd in CHECKS:
        path = shutil.which(tool)
        if not path:
            msg = f"{tool} >= {'.'.join(map(str, min_ver))}: MISSING"
            (warnings if tool in OPTIONAL else failures).append(msg)
            continue
        try:
            out = subprocess.run(
                [path, *cmd], capture_output=True, text=True, timeout=30, shell=False
            )
        except Exception as exc:  # noqa: BLE001 - diagnostic utility
            failures.append(f"{tool}: probe failed ({exc})")
            continue

        version = parse_semver(out.stdout + out.stderr)
        label = f"{tool} {'.'.join(map(str, version)) if version else '?'}"
        if not version or version < min_ver:
            failures.append(f"{label} below required {'.'.join(map(str, min_ver))}")
        else:
            print(f"  OK   {label}")

    for w in warnings:
        print(f"  WARN {w} (CI-enforced; install locally when convenient)")
    if failures:
        print("\nENVIRONMENT INCOMPLETE:")
        print("\n".join(f"  FAIL {f}" for f in failures))
        return 1
    print("\nENVIRONMENT READY")
    return 0


if __name__ == "__main__":
    sys.exit(main())
