from __future__ import annotations

import contextlib
import hashlib
import io
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.error import URLError

if __package__:
    from scripts import check_harness_integrity as checker
else:
    import check_harness_integrity as checker


class HarnessIntegrityTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(temporary_directory.cleanup)
        self.root = Path(temporary_directory.name)
        self.harness_dir = self.root / ".fullsend" / "harness"
        self.harness_dir.mkdir(parents=True)
        self.path = self.harness_dir / "review.yaml"

    def write_harness(self, content: str) -> Path:
        self.path.write_text(content, encoding="utf-8")
        return self.path

    def test_valid_base_entry_is_accepted_offline(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'---\nbase: "{base}" \t\n\ntimeout_minutes: 5\n')

        with patch.object(checker.urllib.request, "urlopen") as fetch:
            self.assertEqual([], checker.check_harness_file(path))

        fetch.assert_not_called()

    def test_invalid_url_and_unsafe_path_are_rejected(self) -> None:
        invalid_urls = (
            "http://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64,
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/../review.yaml#sha256=" + "a" * 64,
            "https://example.com/fullsend-ai/agents/v1.2.3/review.yaml#sha256="
            + "a" * 64,
            (
                "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
                "harness/review.yaml#sha256=bad-hash"
            ),
        )
        for url in invalid_urls:
            with self.subTest(url=url):
                path = self.write_harness(f'base: "{url}"\n')
                diagnostic = checker.check_harness_file(path)[0]
                self.assertIn("review.yaml", diagnostic)
                self.assertIn("base", diagnostic)

    def test_noncanonical_base_key_and_value_quoting_are_rejected(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        entries = (
            f"base: {base}\n",
            f"base: '{base}'\n",
            f'"base": "{base}"\n',
            f"'base': \"{base}\"\n",
        )
        for entry in entries:
            with self.subTest(entry=entry):
                path = self.write_harness(entry)
                self.assertTrue(checker.check_harness_file(path))

    def test_trailing_comment_after_base_scalar_is_rejected(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}" # pinned upstream file\n')

        diagnostic = checker.check_harness_file(path)[0]

        self.assertIn("base value must be one YAML scalar", diagnostic)

    def test_uppercase_sha256_is_rejected(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "A" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')

        diagnostic = checker.check_harness_file(path)[0]

        self.assertIn("base must be", diagnostic)

    def test_missing_base_is_rejected(self) -> None:
        path = self.write_harness(
            "# base: this comment does not count\n"
            "env:\n"
            "  base: this nested value does not count\n"
        )

        diagnostic = checker.check_harness_file(path)[0]

        self.assertIn("review.yaml", diagnostic)
        self.assertIn("missing top-level base entry", diagnostic)

    def test_duplicate_base_entries_are_rejected(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}"\nbase: "{base}"\n')

        diagnostic = checker.check_harness_file(path)[0]

        self.assertIn("duplicate top-level base entries", diagnostic)
        self.assertIn("lines 1, 2", diagnostic)

    def test_malformed_base_scalar_is_rejected(self) -> None:
        path = self.write_harness('base: "not a URL" trailing text\n')

        diagnostic = checker.check_harness_file(path)[0]

        self.assertIn("review.yaml", diagnostic)
        self.assertIn("base value must be one YAML scalar", diagnostic)

    def test_online_hash_matches_exact_response_bytes(self) -> None:
        response_bytes = b"line one\r\n\x00line two"
        digest = hashlib.sha256(response_bytes).hexdigest()
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + digest
        )
        path = self.write_harness(f'base: "{base}"\n')

        with patch.object(
            checker.urllib.request,
            "urlopen",
            return_value=io.BytesIO(response_bytes),
        ) as fetch:
            diagnostics = checker.check_harness_file(path, online=True)

        self.assertEqual([], diagnostics)
        fetch.assert_called_once_with(
            base.rsplit("#", maxsplit=1)[0],
            timeout=checker.NETWORK_TIMEOUT_SECONDS,
        )

    def test_online_hash_mismatch_fails(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "0" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')

        with patch.object(
            checker.urllib.request,
            "urlopen",
            return_value=io.BytesIO(b"different content"),
        ):
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("review.yaml", diagnostic)
        self.assertIn("SHA-256 mismatch", diagnostic)

    def test_online_fetch_failure_fails(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')

        with patch.object(
            checker.urllib.request,
            "urlopen",
            side_effect=URLError("network unavailable"),
        ):
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("review.yaml", diagnostic)
        self.assertIn("failed to fetch", diagnostic)

    def test_online_response_size_limit_fails(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')

        with (
            patch.object(checker, "MAX_RESPONSE_BYTES", 8),
            patch.object(checker, "RESPONSE_READ_CHUNK_BYTES", 4),
            patch.object(
                checker.urllib.request,
                "urlopen",
                return_value=io.BytesIO(b"x" * 9),
            ),
        ):
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("exceeds the 8-byte limit", diagnostic)

    def test_online_redirect_is_rejected(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')

        class RedirectedResponse(io.BytesIO):
            def geturl(self) -> str:
                return "https://example.com/redirected"

        with patch.object(
            checker.urllib.request,
            "urlopen",
            return_value=RedirectedResponse(b"pinned bytes"),
        ):
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("unexpected redirect", diagnostic)

    def test_online_response_read_deadline_fails(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        path = self.write_harness(f'base: "{base}"\n')
        current_time = [0.0]

        class SlowResponse(io.BytesIO):
            def read1(self, size: int = -1) -> bytes:
                current_time[0] += checker.NETWORK_READ_DEADLINE_SECONDS + 1
                return super().read1(size)

        with (
            patch.object(checker, "monotonic", side_effect=lambda: current_time[0]),
            patch.object(
                checker.urllib.request,
                "urlopen",
                return_value=SlowResponse(b"pinned bytes"),
            ),
        ):
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("response read exceeded", diagnostic)

    def test_malformed_url_is_rejected_before_online_fetch(self) -> None:
        path = self.write_harness(
            'base: "https://example.com/not-a-pinned-harness.yaml"\n'
        )

        with patch.object(checker.urllib.request, "urlopen") as fetch:
            diagnostic = checker.check_harness_file(path, online=True)[0]

        self.assertIn("base must be", diagnostic)
        fetch.assert_not_called()

    def test_cli_scans_yaml_files_and_defaults_to_offline(self) -> None:
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + "a" * 64
        )
        self.write_harness(f'base: "{base}"\n')
        (self.harness_dir / "ignored.yml").write_text("not YAML config\n")

        stdout = io.StringIO()
        with (
            patch.object(checker, "REPO_ROOT", self.root),
            patch.object(checker.urllib.request, "urlopen") as fetch,
            contextlib.redirect_stdout(stdout),
        ):
            result = checker.main([])

        self.assertEqual(0, result)
        self.assertIn("Validated 1 harness file(s)", stdout.getvalue())
        self.assertIn("offline structure", stdout.getvalue())
        fetch.assert_not_called()

    def test_cli_online_fetches_discovered_harness_files(self) -> None:
        response_bytes = b"pinned bytes"
        digest = hashlib.sha256(response_bytes).hexdigest()
        base = (
            "https://raw.githubusercontent.com/fullsend-ai/agents/v1.2.3/"
            "harness/review.yaml#sha256=" + digest
        )
        self.write_harness(f'base: "{base}"\n')
        stdout = io.StringIO()

        with (
            patch.object(checker, "REPO_ROOT", self.root),
            patch.object(
                checker.urllib.request,
                "urlopen",
                return_value=io.BytesIO(response_bytes),
            ) as fetch,
            contextlib.redirect_stdout(stdout),
        ):
            result = checker.main(["--online"])

        self.assertEqual(0, result)
        self.assertIn("online integrity", stdout.getvalue())
        fetch.assert_called_once_with(
            base.rsplit("#", maxsplit=1)[0],
            timeout=checker.NETWORK_TIMEOUT_SECONDS,
        )


if __name__ == "__main__":
    unittest.main()
