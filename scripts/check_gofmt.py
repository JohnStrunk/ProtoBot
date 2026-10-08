#!/usr/bin/env python3
"""Fail if any given Go file is not formatted by gofmt.

Used as the ``gofmt`` local pre-commit hook (and via ``scripts/lint.py``)
so unformatted Go source fails in the same change. ``gofmt -l`` lists
files that differ from gofmt's formatting but still exits 0, so this
wrapper treats a non-empty list as failure.
"""

from __future__ import annotations

import argparse
import subprocess
import sys


def check(files: list[str]) -> int:
    """Run ``gofmt -l`` on *files* and return 0 only when all are formatted."""
    go_files = [path for path in files if path.endswith(".go")]
    if not go_files:
        return 0
    try:
        result = subprocess.run(
            ["gofmt", "-l", *go_files],
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        print("ERROR: gofmt not found on PATH", file=sys.stderr)
        return 1
    if result.stderr:
        sys.stderr.write(result.stderr)
        if not result.stderr.endswith("\n"):
            sys.stderr.write("\n")
    unformatted = [line for line in result.stdout.splitlines() if line.strip()]
    if unformatted:
        print("Go files need gofmt:")
        print("\n".join(unformatted))
        return 1
    if result.returncode != 0:
        return 1
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "files",
        nargs="*",
        help="Go source files (paths relative to the repository root)",
    )
    args = parser.parse_args(argv)
    return check(args.files)


if __name__ == "__main__":
    sys.exit(main())
