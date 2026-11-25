#!/usr/bin/env python3
"""Run with python3 scripts/test_main_bisect.py (no third-party dependencies).

Execute the actual inline workflow scripts, not copies of their logic. A Bash
function stands in for Go so argv and failing statuses are deterministic, with
no checkout, toolchain download, network, or real test execution.
"""
import os
from pathlib import Path
import re
import subprocess
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/main-bisect.yml"


def step_script(name):
    text = WORKFLOW.read_text()
    match = re.search(
        rf"^      - name: {re.escape(name)}\n        run: \|\n"
        r"((?:          [^\n]*\n|\n)+)", text, re.M,
    )
    if not match:
        raise AssertionError(f"Missing inline Bash step: {name}")
    return "\n".join(line[10:] for line in match[1].splitlines())


class MainBisectTest(unittest.TestCase):
    def invoke(self, step, changes=None, prefix=""):
        env = {**os.environ, "IN_SHA": "a" * 40, "IN_TOKEN": "bisect-123",
               "IN_PKGS": "internal/version", "IN_RUN": "TestVersion",
               "IN_TAGS": "", "IN_OS": "ubuntu-latest", "IN_RACE": "true"}
        env.update(changes or {})
        return subprocess.run(
            ["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c",
             prefix + "\n" + step_script(step)], env=env,
            capture_output=True, text=True, timeout=5, check=False,
        )

    def test_validator_accepts_supported_inputs_and_limits(self):
        for changes in ({}, {"IN_PKGS": ". internal/a/ pkg/foo-bar_2",
                             "IN_RUN": "TestA|ExampleB|FuzzC|BenchmarkD",
                             "IN_TAGS": "integration,custom_tag",
                             "IN_OS": "windows-latest", "IN_RACE": "false"},
                        {"IN_RUN": "", "IN_TOKEN": "Z" * 40},
                        {"IN_PKGS": " ".join(f"pkg/p{i}" for i in range(20)),
                         "IN_RUN": "|".join(f"Test{i}" for i in range(50))}):
            with self.subTest(changes=changes):
                result = self.invoke("Validate inputs", changes)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("MAIN_BISECT=valid ", result.stdout)
                self.assertNotIn("MAIN_BISECT=invalid", result.stdout)

    def test_validator_rejects_unsafe_or_out_of_bounds_inputs(self):
        invalid = {
            "IN_SHA": ["", "a" * 39, "a" * 41, "A" * 40, "$(echo unsafe)"],
            "IN_TOKEN": ["", "x" * 41, "token;echo unsafe", "two words"],
            "IN_TAGS": ["-race", "a;echo unsafe", "a b", "$(echo unsafe)"],
            "IN_OS": ["macos-latest", "", "ubuntu-latest;echo unsafe"],
            "IN_RACE": ["", "TRUE", "1", "true;echo unsafe"],
            "IN_PKGS": ["", " ", " ".join(["pkg/a"] * 21), "/tmp/pkg",
                        "../pkg", "pkg/../other", "pkg/..", "-args", "./pkg",
                        "pkg;echo unsafe", "$(echo unsafe)", "pkg/*"],
            "IN_RUN": ["|TestA", "TestA|", "TestA||TestB", "TestA/child",
                       "Test.*", "TestA$", "TestA;echo unsafe", "Other",
                       "|".join(["TestA"] * 51)],
        }
        for key, values in invalid.items():
            for value in values:
                with self.subTest(key=key, value=value):
                    result = self.invoke("Validate inputs", {key: value})
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                    self.assertIn("MAIN_BISECT=invalid ", result.stdout)
                    self.assertNotIn("MAIN_BISECT=valid ", result.stdout)

    def judge(self, changes, status, expected):
        # Each argv entry is its own line: this detects accidental splitting,
        # missing packages, or unquoted regular expressions/tags.
        stub = ('go() { printf "ARG=%s\\n" "$@"; '
                'echo "--- FAIL: TestReproduced"; return "$STUB_STATUS"; }')
        result = self.invoke("Run the tests", {**changes, "STUB_STATUS": str(status)}, stub)
        self.assertEqual(result.returncode, status, result.stdout + result.stderr)
        args = [line[4:] for line in result.stdout.splitlines() if line.startswith("ARG=")]
        self.assertEqual(args, expected)
        self.assertIn("--- FAIL: TestReproduced\n", result.stdout)
        self.assertEqual(result.stdout.splitlines()[-1], f"MAIN_BISECT_EXIT={status}")

    def test_exact_argv_and_exit_status(self):
        for status in (0, 1, 2, 17):
            with self.subTest(status=status):
                self.judge({"IN_PKGS": ". internal/version/ pkg/foo-bar",
                            "IN_RUN": "TestOne|ExampleTwo", "IN_TAGS": "integration,extra"},
                           status, ["test", "-count=1", "-race", "-tags", "integration,extra",
                                    "-run", "^(TestOne|ExampleTwo)$", "./",
                                    "./internal/version/", "./pkg/foo-bar/"])

    def test_empty_optional_inputs_and_race_policy(self):
        for runner, race in (("ubuntu-latest", "false"), ("windows-latest", "true"),
                             ("windows-latest", "false")):
            with self.subTest(runner=runner, race=race):
                self.judge({"IN_OS": runner, "IN_RACE": race, "IN_RUN": "", "IN_TAGS": ""},
                           0, ["test", "-count=1", "./internal/version/"])


if __name__ == "__main__":
    unittest.main()
