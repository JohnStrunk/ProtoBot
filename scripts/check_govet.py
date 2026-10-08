#!/usr/bin/env python3
"""Run ``go vet ./...`` in each Go module that contains given files.

Used as the ``go-vet`` local pre-commit hook (and via ``scripts/lint.py``)
so vet findings fail in the same change. Filenames select the affected
modules; vet itself always runs as ``go vet ./...`` in the module
directory, matching CI.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


def find_go_module(repo_root: Path, file_path: Path) -> Path | None:
    """Return the directory containing ``go.mod`` for *file_path*, if any.

    Walks from the file's directory toward *repo_root* and returns the
    nearest ancestor (or the directory itself) that contains ``go.mod``.
    Paths outside *repo_root* yield ``None``.
    """
    try:
        resolved = file_path.resolve()
        root = repo_root.resolve()
    except OSError:
        return None
    if not resolved.is_relative_to(root):
        return None
    start = resolved if resolved.is_dir() else resolved.parent
    for candidate in [start, *start.parents]:
        if (candidate / "go.mod").is_file():
            return candidate
        if candidate == root:
            break
    return None


def _display_module(module: Path, repo_root: Path) -> str:
    try:
        return str(module.resolve().relative_to(repo_root.resolve()))
    except ValueError:
        return str(module)


def check(files: list[str], repo_root: Path = REPO_ROOT) -> int:
    """Vet each unique Go module that contains a given ``.go`` file."""
    modules: set[Path] = set()
    for raw in files:
        if not raw.endswith(".go"):
            continue
        path = Path(raw)
        if not path.is_absolute():
            path = repo_root / path
        module = find_go_module(repo_root, path)
        if module is not None:
            modules.add(module)
    if not modules:
        return 0

    failed = False
    for module in sorted(modules):
        try:
            result = subprocess.run(
                ["go", "vet", "./..."],
                capture_output=True,
                text=True,
                cwd=str(module),
                check=False,
            )
        except FileNotFoundError:
            print("ERROR: go not found on PATH", file=sys.stderr)
            return 1
        if result.returncode != 0:
            display = _display_module(module, repo_root)
            print(f"go vet failed in {display}:")
            output = (result.stdout + result.stderr).strip()
            if output:
                print(output)
            failed = True
    return 1 if failed else 0


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
