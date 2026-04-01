#!/usr/bin/env bash

# Static audit for dual-driver (SQLite + Postgres) repository safety.
#
# Checks:
# 1) Repository methods that appear SQLite-only (use r.Querier but no PG branch).
# 2) Raw SQL with '?' placeholders in generic Repo methods that is not routed via bindQuery.
#
# Exit codes:
# - 0: no blocking issues found
# - 1: blocking issues found

set -euo pipefail

ROOT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$ROOT_DIR"

python3 <<'PY'
from __future__ import annotations

import pathlib
import re
import sys
from dataclasses import dataclass

ROOT = pathlib.Path(".")
REPO_FILE = ROOT / "internal/db/repository_impl.go"
HELPERS_FILE = ROOT / "internal/db/helpers.go"
PG_QUERIER_FILE = ROOT / "internal/db/postgres/generated/querier.go"

@dataclass
class FuncBlock:
    name: str
    start_line: int
    text: str


def load_text(path: pathlib.Path) -> str:
    if not path.exists():
        print(f"ERROR: missing required file: {path}")
        sys.exit(2)
    return path.read_text(encoding="utf-8")


def extract_repo_funcs(path: pathlib.Path) -> list[FuncBlock]:
    text = load_text(path)
    pat = re.compile(r"^func \(r \*Repo\) (?P<name>[A-Za-z0-9_]+)\(", re.MULTILINE)
    matches = list(pat.finditer(text))
    funcs: list[FuncBlock] = []
    for i, m in enumerate(matches):
        start = m.start()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
        block = text[start:end]
        line = text.count("\n", 0, start) + 1
        funcs.append(FuncBlock(name=m.group("name"), start_line=line, text=block))
    return funcs


def extract_pg_querier_methods(path: pathlib.Path) -> set[str]:
    text = load_text(path)
    # Extract methods from generated Querier interface signatures:
    #   MethodName(ctx context.Context, ...) ...
    return set(re.findall(r"^\s*([A-Za-z0-9_]+)\(ctx context\.Context", text, re.MULTILINE))


def has_pg_branch(block: str) -> bool:
    return (
        "DriverPostgres" in block
        or "r.PGQuerier." in block
        or "r.driver == DriverPostgres" in block
    )


def has_sql_with_qmark(block: str) -> bool:
    # Inspect raw-string literals and look for SQL-ish content containing '?'
    raw_strings = re.findall(r"`([^`]*)`", block, re.DOTALL)
    for s in raw_strings:
        if "?" not in s:
            continue
        s_upper = s.upper()
        if any(tok in s_upper for tok in ("SELECT", "INSERT", "UPDATE", "DELETE", "FROM", "WHERE", "JOIN")):
            return True
    return False


def main() -> int:
    repo_funcs = extract_repo_funcs(REPO_FILE)
    helper_funcs = extract_repo_funcs(HELPERS_FILE)
    pg_methods = extract_pg_querier_methods(PG_QUERIER_FILE)

    sqlite_only_errors: list[tuple[str, int, list[str]]] = []
    sqlite_only_warnings: list[tuple[str, int, list[str]]] = []
    unbound_sql_errors: list[tuple[str, int]] = []

    # Check 1: likely sqlite-only methods
    querier_call_pat = re.compile(r"r\.Querier\.([A-Za-z0-9_]+)\(")

    for fn in repo_funcs:
        if "r.Querier." not in fn.text:
            continue
        if has_pg_branch(fn.text):
            continue

        calls = sorted(set(querier_call_pat.findall(fn.text)))
        calls_with_pg_equivalent = [c for c in calls if c in pg_methods]

        if calls_with_pg_equivalent:
            sqlite_only_errors.append((fn.name, fn.start_line, calls_with_pg_equivalent))
        else:
            sqlite_only_warnings.append((fn.name, fn.start_line, calls))

    # Check 2: raw SQL '?' in non-PG-branch methods must bindQuery
    for fn in repo_funcs + helper_funcs:
        if has_pg_branch(fn.text):
            continue
        if not has_sql_with_qmark(fn.text):
            continue
        if "bindQuery(" not in fn.text:
            unbound_sql_errors.append((fn.name, fn.start_line))

    print("DB Driver Audit")
    print("===============")

    if sqlite_only_errors:
        print("\n[ERROR] Repo methods that call r.Querier.* with PG equivalents available, but no PG branch:")
        for name, line, calls in sqlite_only_errors:
            print(f"  - internal/db/repository_impl.go:{line} {name} (calls: {', '.join(calls)})")

    if sqlite_only_warnings:
        print("\n[WARN] Repo methods using r.Querier.* without PG branch (no PG equivalent detected):")
        for name, line, calls in sqlite_only_warnings:
            suffix = f" (calls: {', '.join(calls)})" if calls else ""
            print(f"  - internal/db/repository_impl.go:{line} {name}{suffix}")

    if unbound_sql_errors:
        print("\n[ERROR] Raw SQL with '?' in generic Repo methods without bindQuery():")
        for name, line in unbound_sql_errors:
            print(f"  - {name} at line {line}")

    error_count = len(sqlite_only_errors) + len(unbound_sql_errors)
    warn_count = len(sqlite_only_warnings)

    print("\nSummary")
    print("-------")
    print(f"Errors:   {error_count}")
    print(f"Warnings: {warn_count}")

    if error_count > 0:
        print("\nAudit FAILED")
        return 1

    print("\nAudit PASSED")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
PY
