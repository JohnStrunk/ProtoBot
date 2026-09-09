#!/usr/bin/env python3
"""Offline pre-commit hook runner for sandboxed environments.

Reads ``.pre-commit-config.yaml`` and runs each configured hook using
locally installed tools, without network access to pre-commit's hook
repository cache.

Usage::

    python scripts/lint.py --all-files          # every tracked file
    python scripts/lint.py --files a.py b.yaml  # specific files
    python scripts/lint.py                      # changed files vs HEAD
    python scripts/lint.py --check-parity       # verify registry coverage

Tool provenance
---------------
Tools are installed on first use via ``pip`` or ``npx``, pinned to the
version recorded in the frozen comment of ``.pre-commit-config.yaml``.
The YAML config is the **single source of truth** for which hooks run
and with what arguments; this script only provides the *how* — a
registry mapping each repo URL to an installation method and command
template.

When a hook has no registered handler the script reports it as
**UNSUPPORTED** and exits non-zero, ensuring silent coverage reduction
cannot happen.
"""

from __future__ import annotations

import argparse
import re
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:
    sys.exit(
        "ERROR: PyYAML is required but not installed.\n"
        "       Install with:  pip install pyyaml"
    )

# ── Paths ──────────────────────────────────────────────────────

REPO_ROOT = Path(__file__).resolve().parent.parent
CONFIG_PATH = REPO_ROOT / ".pre-commit-config.yaml"

# ── File-type detection ────────────────────────────────────────

_EXT_TO_TYPE: dict[str, str] = {
    ".py": "python",
    ".pyi": "python",
    ".ipynb": "jupyter",
    ".json": "json",
    ".json5": "json5",
    ".jsonc": "json5",
    ".yaml": "yaml",
    ".yml": "yaml",
    ".toml": "toml",
    ".xml": "xml",
    ".md": "markdown",
    ".markdown": "markdown",
    ".sh": "shell",
    ".bash": "shell",
    ".zsh": "shell",
    ".ksh": "shell",
}

_NAME_PREFIX_TYPES: list[tuple[str, str]] = [
    ("Dockerfile", "dockerfile"),
    ("Containerfile", "dockerfile"),
]


def _file_type(path: str) -> str:
    """Return a type tag for *path* based on name / extension."""
    name = Path(path).name
    for prefix, ftype in _NAME_PREFIX_TYPES:
        if name == prefix or name.startswith(prefix + "."):
            return ftype
    return _EXT_TO_TYPE.get(Path(path).suffix.lower(), "text")


def _filter_by_types(
    files: list[str],
    types: list[str] | None,
) -> list[str]:
    if not types:
        return files
    ts = set(types)
    if "all" in ts or "text" in ts:
        return files
    return [f for f in files if _file_type(f) in ts]


def _filter_by_regex(
    files: list[str],
    include: str | None = None,
    exclude: str | None = None,
) -> list[str]:
    result = files
    if include:
        pat = re.compile(include)
        result = [f for f in result if pat.search(f)]
    if exclude:
        pat = re.compile(exclude, re.VERBOSE)
        result = [f for f in result if not pat.search(f)]
    return result


# ── Config helpers ─────────────────────────────────────────────


def _load_config() -> dict[str, Any]:
    with open(CONFIG_PATH) as fh:
        return yaml.safe_load(fh)


def _extract_frozen_versions() -> dict[str, str]:
    """Return ``{rev_sha: version}`` from ``# frozen: vX.Y.Z`` comments."""
    mapping: dict[str, str] = {}
    with open(CONFIG_PATH) as fh:
        for line in fh:
            m = re.match(r"\s*rev:\s*(\S+)\s+#\s*frozen:\s*v?([\d.]+)", line)
            if m:
                mapping[m.group(1)] = m.group(2)
    return mapping


def _normalize_url(url: str) -> str:
    url = re.sub(r"^https?://", "", url)
    url = url.rstrip("/")
    url = url.removesuffix(".git")
    return url


# ── Tool installation ──────────────────────────────────────────


def _pip_install(package: str, version: str | None) -> bool:
    spec = f"{package}=={version}" if version else package
    r = subprocess.run(
        [sys.executable, "-m", "pip", "install", "--quiet", spec],
        capture_output=True,
        text=True,
        check=False,
    )
    if r.returncode != 0:
        print(f"  pip install {spec} failed:\n  {r.stderr.strip()}")
        return False
    return True


def _ensure_tool(
    cmd: str,
    installer: str,
    package: str | None,
    version: str | None,
) -> bool:
    """Return *True* when *cmd* is (or becomes) available on ``PATH``."""
    if shutil.which(cmd):
        return True
    if installer == "pip" and package:
        print(f"  Installing {package} via pip …")
        if _pip_install(package, version):
            return shutil.which(cmd) is not None
        return False
    if installer == "system":
        return False
    return installer in ("npx", "builtin")


# ── Built-in hook implementations ─────────────────────────────


def _builtin_unicode_replacement(files: list[str]) -> int:
    """Detect the UTF-8 replacement character U+FFFD."""
    bad = False
    replacement = b"\xef\xbf\xbd"
    for fp in files:
        try:
            data = (REPO_ROOT / fp).read_bytes()
        except OSError:
            continue
        if replacement in data:
            print(f"  {fp}: contains Unicode replacement character (U+FFFD)")
            bad = True
    return 1 if bad else 0


def _builtin_check_json5(files: list[str]) -> int:
    """Validate JSON5 files using the ``json5`` Python package."""
    try:
        import json5  # type: ignore[import-untyped]
    except ImportError:
        if not _pip_install("json5", None):
            print("  Cannot install json5 package")
            return 1
        import json5  # type: ignore[import-untyped]

    bad = False
    for fp in files:
        try:
            with open(REPO_ROOT / fp) as fh:
                json5.load(fh)
        except (ValueError, OSError) as exc:
            print(f"  {fp}: {exc}")
            bad = True
    return 1 if bad else 0


_BUILTINS: dict[str, Any] = {
    "check_unicode_replacement": _builtin_unicode_replacement,
    "check_json5": _builtin_check_json5,
}

# ── Hook registry ─────────────────────────────────────────────
#
# Maps normalised repo URLs → tool metadata.  The YAML config is
# the single source of truth for WHICH hooks to run and with what
# arguments; this registry only says HOW to install and invoke
# each tool.
#
# If a hook is added to the YAML without a corresponding entry
# here, the script reports it as UNSUPPORTED and exits non-zero.

REGISTRY: dict[str, dict[str, Any]] = {
    # ── Python: pre-commit-hooks ───────────────────────────────
    "github.com/pre-commit/pre-commit-hooks": {
        "installer": "pip",
        "package": "pre-commit-hooks",
        "hooks": {
            "check-added-large-files": {"types": ["all"]},
            "check-docstring-first": {"types": ["python"]},
            "check-json": {"types": ["json"]},
            "check-merge-conflict": {"types": ["text"]},
            "check-symlinks": {"types": ["all"]},
            "check-toml": {"types": ["toml"]},
            "check-xml": {"types": ["xml"]},
            "debug-statements": {
                "cmd": "debug-statement-hook",
                "types": ["python"],
            },
            "end-of-file-fixer": {"types": ["text"]},
            "fix-byte-order-marker": {"types": ["text"]},
            "trailing-whitespace": {
                "cmd": "trailing-whitespace-fixer",
                "types": ["text"],
            },
        },
    },
    # ── Python: pygrep-hooks (builtin) ─────────────────────────
    "github.com/pre-commit/pygrep-hooks": {
        "installer": "builtin",
        "hooks": {
            "text-unicode-replacement-char": {
                "builtin_fn": "check_unicode_replacement",
                "types": ["text"],
            },
        },
    },
    # ── Python: yamllint ───────────────────────────────────────
    "github.com/adrienverge/yamllint": {
        "installer": "pip",
        "package": "yamllint",
        "hooks": {
            "yamllint": {"types": ["yaml"]},
        },
    },
    # ── Python/Rust: ruff ──────────────────────────────────────
    "github.com/astral-sh/ruff-pre-commit": {
        "installer": "pip",
        "package": "ruff",
        "hooks": {
            "ruff-check": {
                "cmd": "ruff",
                "prepend_args": ["check"],
                "types": ["python", "jupyter"],
            },
            "ruff-format": {
                "cmd": "ruff",
                "prepend_args": ["format", "--check", "--diff"],
                "types": ["python", "jupyter"],
            },
        },
    },
    # ── System: uv ─────────────────────────────────────────────
    "github.com/astral-sh/uv-pre-commit": {
        "installer": "system",
        "hooks": {
            "uv-lock": {
                "cmd": "uv",
                "fixed_args": ["lock", "--check"],
                "pass_filenames": False,
                "default_files": r"(^|/)pyproject\.toml$",
            },
        },
    },
    # ── Python: skillsaw ───────────────────────────────────────
    "github.com/stbenjam/skillsaw": {
        "installer": "pip",
        "package": "skillsaw",
        "hooks": {
            "skillsaw": {"pass_filenames": False},
        },
    },
    # ── Node: markdownlint-cli2 ────────────────────────────────
    "github.com/DavidAnson/markdownlint-cli2": {
        "installer": "npx",
        "package": "markdownlint-cli2",
        "hooks": {
            "markdownlint-cli2": {"types": ["markdown"]},
        },
    },
    # ── System binary: hadolint ────────────────────────────────
    "github.com/hadolint/hadolint": {
        "installer": "system",
        "hooks": {
            "hadolint": {"types": ["dockerfile"]},
        },
    },
    # ── Python (bundles binary): shellcheck ────────────────────
    "github.com/shellcheck-py/shellcheck-py": {
        "installer": "pip",
        "package": "shellcheck-py",
        "hooks": {
            "shellcheck": {"types": ["shell"]},
        },
    },
    # ── Python: detect-secrets ─────────────────────────────────
    "github.com/Yelp/detect-secrets": {
        "installer": "pip",
        "package": "detect-secrets",
        "hooks": {
            "detect-secrets": {
                "cmd": "detect-secrets-hook",
                "types": ["text"],
            },
        },
    },
    # ── Builtin (json5 package): check-json5 ───────────────────
    "gitlab.com/bmares/check-json5": {
        "installer": "builtin",
        "hooks": {
            "check-json5": {
                "builtin_fn": "check_json5",
                "types": ["json5"],
            },
        },
    },
    # ── Python/Rust: zizmor ────────────────────────────────────
    "github.com/zizmorcore/zizmor-pre-commit": {
        "installer": "pip",
        "package": "zizmor",
        "hooks": {
            "zizmor": {
                "types": ["yaml"],
                "default_files": r"^\.github/",
                # --offline avoids network-dependent audits that
                # cannot run in sandboxed environments.
                "prepend_args": ["--offline"],
            },
        },
    },
    # ── Node: renovate-config-validator ────────────────────────
    "github.com/renovatebot/pre-commit-hooks": {
        "installer": "npx",
        "package": "renovate",
        "hooks": {
            "renovate-config-validator": {
                "pass_filenames": False,
                "default_files": r"(^|/)\.?renovate(rc)?\.json5?$",
            },
        },
    },
}


# ── Hook execution ─────────────────────────────────────────────


def _run_hook(
    hook_id: str,
    hook_info: dict[str, Any],
    repo_info: dict[str, Any],
    hook_yaml: dict[str, Any],
    files: list[str],
    version: str | None,
) -> tuple[str, str]:
    """Run one hook.  Returns ``(status, detail)``."""

    # ── Common file filtering ─────────────────────────────────
    # The YAML config may override the upstream hook's ``files``
    # pattern.  When absent, fall back to the registry's
    # ``default_files`` (which mirrors the upstream definition).
    files_pattern = hook_yaml.get("files") or hook_info.get("default_files")
    exclude_pattern = hook_yaml.get("exclude")

    # ── Early file filtering (all hook types) ───────────────────
    # Determine matching files BEFORE installing or locating the
    # tool so that hooks with no applicable files are skipped
    # cheaply (e.g. hadolint in a repo with no Dockerfiles).
    pass_filenames = hook_info.get("pass_filenames", True)
    hook_files = _filter_by_types(files, hook_info.get("types"))
    hook_files = _filter_by_regex(
        hook_files,
        files_pattern,
        exclude_pattern,
    )
    if not hook_files:
        return "skip", "no matching files"

    # ── Builtin hooks ──────────────────────────────────────────
    if "builtin_fn" in hook_info:
        fn = _BUILTINS.get(hook_info["builtin_fn"])
        if fn is None:
            return "unsupported", f"Unknown builtin: {hook_info['builtin_fn']}"
        rc = fn(hook_files)
        return ("fail" if rc else "pass"), ""

    # ── External tools ─────────────────────────────────────────
    cmd = hook_info.get("cmd", hook_id)
    installer = repo_info["installer"]
    package = repo_info.get("package")

    # For npx tools, the command is run via npx
    if installer == "npx":
        pkg = package or cmd
        pkg_spec = f"{pkg}@{version}" if version else pkg
        base_cmd: list[str] = ["npx", "--yes", pkg_spec]
        # When the hook command differs from the package name
        # (e.g. renovate-config-validator from renovate), append it.
        if cmd != pkg:
            base_cmd.append(cmd)
    else:
        if not _ensure_tool(cmd, installer, package, version):
            return "unavailable", f"{cmd} is not installed"
        base_cmd = [cmd]

    full_cmd = list(base_cmd)

    # Sub-commands (e.g. "ruff check", "ruff format --check")
    if "prepend_args" in hook_info:
        full_cmd.extend(hook_info["prepend_args"])

    # Fixed args replace the YAML-configured args entirely
    if "fixed_args" in hook_info:
        full_cmd.extend(hook_info["fixed_args"])
    elif "args" in hook_yaml:
        full_cmd.extend(hook_yaml["args"])

    if pass_filenames:
        full_cmd.extend(hook_files)

    try:
        result = subprocess.run(
            full_cmd,
            capture_output=True,
            text=True,
            cwd=str(REPO_ROOT),
            timeout=300,
            check=False,
        )
    except FileNotFoundError:
        return "unavailable", f"command not found: {full_cmd[0]}"
    except subprocess.TimeoutExpired:
        return "fail", "timed out after 300 s"

    if result.returncode == 0:
        return "pass", ""

    detail = (result.stdout + result.stderr).strip()
    return "fail", detail


# ── File collection ────────────────────────────────────────────


def _git(*args: str) -> str:
    r = subprocess.run(
        ["git", *args],
        capture_output=True,
        text=True,
        cwd=str(REPO_ROOT),
        check=False,
    )
    return r.stdout.strip()


def _get_files(args: argparse.Namespace) -> list[str]:
    if args.files:
        return [str(f) for f in args.files]

    if args.all_files:
        raw = [f for f in _git("ls-files").splitlines() if f]
    else:
        # Changed + staged files vs HEAD
        changed = _git("diff", "--name-only", "--diff-filter=ACMR", "HEAD")
        staged = _git(
            "diff",
            "--name-only",
            "--cached",
            "--diff-filter=ACMR",
        )
        combined: set[str] = set()
        for line in (changed + "\n" + staged).splitlines():
            if line.strip():
                combined.add(line.strip())
        raw = sorted(combined)

    # Exclude entries that resolve to directories (e.g. symlinks
    # pointing at directories — ``git ls-files`` lists them but
    # file-oriented tools choke on them).
    return [f for f in raw if not (REPO_ROOT / f).is_dir()]


# ── Parity check ───────────────────────────────────────────────


def _check_parity(config: dict[str, Any]) -> bool:
    """Verify every configured hook has a registered handler."""
    ok = True
    for repo in config.get("repos", []):
        key = _normalize_url(repo["repo"])
        reg = REGISTRY.get(key)
        if reg is None:
            print(f"MISSING REPO: {repo['repo']}")
            for h in repo.get("hooks", []):
                print(f"  - {h['id']}")
            ok = False
            continue
        for h in repo.get("hooks", []):
            if h["id"] not in reg.get("hooks", {}):
                print(f"MISSING HOOK: {h['id']} (from {repo['repo']})")
                ok = False

    if ok:
        print(
            "PARITY CHECK PASSED: all hooks in "
            ".pre-commit-config.yaml have registered handlers."
        )
    else:
        print(
            "\nPARITY CHECK FAILED: some hooks have no registered handler — see above."
        )
    return ok


# ── Entrypoint ─────────────────────────────────────────────────

_STATUS_ICON = {
    "pass": "\033[32m✓\033[0m",
    "fail": "\033[31m✗\033[0m",
    "skip": "\033[33m○\033[0m",
    "unsupported": "\033[35m?\033[0m",
    "unavailable": "\033[35m!\033[0m",
}


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    grp = parser.add_mutually_exclusive_group()
    grp.add_argument(
        "--all-files",
        action="store_true",
        help="check all tracked files",
    )
    grp.add_argument(
        "--files",
        nargs="+",
        metavar="FILE",
        help="check only these files",
    )
    parser.add_argument(
        "--check-parity",
        action="store_true",
        help="verify every configured hook has a registered handler",
    )
    args = parser.parse_args()

    config = _load_config()

    if args.check_parity:
        return 0 if _check_parity(config) else 1

    frozen = _extract_frozen_versions()
    files = _get_files(args)

    if not files:
        print("No files to check.")
        return 0

    results: list[tuple[str, str, str]] = []

    for repo in config.get("repos", []):
        repo_url = _normalize_url(repo["repo"])
        rev = repo.get("rev", "")
        version = frozen.get(rev)

        reg = REGISTRY.get(repo_url)
        if reg is None:
            for h in repo.get("hooks", []):
                hid = h["id"]
                msg = f"unknown repo: {repo['repo']}"
                print(f"{_STATUS_ICON['unsupported']} {hid}  ({msg})")
                results.append(("unsupported", hid, msg))
            continue

        for h in repo.get("hooks", []):
            hid = h["id"]
            hinfo = reg.get("hooks", {}).get(hid)

            if hinfo is None:
                msg = f"unknown hook in {repo['repo']}"
                print(f"{_STATUS_ICON['unsupported']} {hid}  ({msg})")
                results.append(("unsupported", hid, msg))
                continue

            # Progress indicator (overwritten by result line)
            print(f"  Running {hid} …", end="\r", flush=True)
            status, detail = _run_hook(hid, hinfo, reg, h, files, version)

            icon = _STATUS_ICON.get(status, "?")
            suffix = f"  ({detail})" if detail and status == "skip" else ""
            print(f"{icon} {hid}{suffix}")

            if detail and status == "fail":
                for line in detail.splitlines()[:30]:
                    print(f"  {line}")
                total = len(detail.splitlines())
                if total > 30:
                    print(f"  … ({total} lines total)")

            if detail and status in ("unsupported", "unavailable"):
                print(f"  {detail}")

            results.append((status, hid, detail))

    # ── Summary ────────────────────────────────────────────────
    print()
    print("─" * 60)
    passed = sum(1 for s, *_ in results if s == "pass")
    failed = sum(1 for s, *_ in results if s == "fail")
    skipped = sum(1 for s, *_ in results if s == "skip")
    unsup = sum(1 for s, *_ in results if s in ("unsupported", "unavailable"))
    total = len(results)
    print(
        f"Results: {passed} passed, {failed} failed, "
        f"{skipped} skipped, {unsup} unsupported  ({total} hooks)"
    )

    if failed:
        print("\nFailed hooks:")
        for s, h, _ in results:
            if s == "fail":
                print(f"  - {h}")

    if unsup:
        print("\nUnsupported / unavailable hooks:")
        for s, h, d in results:
            if s in ("unsupported", "unavailable"):
                print(f"  - {h}: {d}")

    return 1 if (failed or unsup) else 0


if __name__ == "__main__":
    sys.exit(main())
