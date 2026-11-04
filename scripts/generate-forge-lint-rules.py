#!/usr/bin/env python3
"""Keep docs/forge/linting/ in step with forge's lint rule tables.

forge declares every rule `forge lint` can grade in two places:

  internal/cli/lint/lint_rules.go         gradableRules(): the rule IDs, grouped
  internal/cli/lint/lint_rollout_table.go introduced_in / gates_from per rule

This script reads both from a forge checkout and does two things.

  --write   rewrites the generated table between the BEGIN/END GENERATED
            markers in docs/forge/linting/rules-index.mdx.
  --check   fails if a rule in forge has no row in the hand-written rule
            tables (rules-*.mdx), if a row names a rule forge no longer has,
            or if a row's Rollout cell disagrees with the rollout table.

Regenerate after a forge release (check out the forge ref you document first;
the script reads files, not git history):

  python3 scripts/generate-forge-lint-rules.py --forge ../forge --write
  python3 scripts/generate-forge-lint-rules.py --forge ../forge --check

A Rollout cell reads `v0.1.40` for a rule that has gated since that release
(the table was audited back to v0.1.40, so older rules read "at or before"),
and `v1.0.0 -> v1.0.1` for a rule introduced in the first release that warns
and gates from the second.
"""

import argparse
import glob
import os
import re
import sys

BEGIN = "{/* BEGIN GENERATED: scripts/generate-forge-lint-rules.py */}"
END = "{/* END GENERATED */}"

DOCS_DIR = os.path.join(os.path.dirname(__file__), "..", "docs", "forge", "linting")
INDEX_PAGE = "rules-index.mdx"


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def string_consts(forge):
    """Every string constant under internal/ and pkg/release, by name."""
    consts = {}
    pat = re.compile(
        r'^\s*(?:const\s+)?([A-Za-z_]\w*)\s+(?:[A-Za-z.]+\s+)?=\s+"([^"]+)"', re.M
    )
    roots = [os.path.join(forge, "internal"), os.path.join(forge, "pkg", "release")]
    for root in roots:
        for path in glob.glob(root + "/**/*.go", recursive=True):
            if path.endswith("_test.go"):
                continue
            for m in pat.finditer(read(path)):
                consts.setdefault(m.group(1), m.group(2))
    return consts


def gradable_rules(forge, consts):
    """[(rule id, group)] from gradableRules(), in declaration order."""
    src = read(os.path.join(forge, "internal/cli/lint/lint_rules.go"))
    body = src.split("func gradableRules()")[1].split("rules = append(rules")[0]
    body = body.split("rules := []string{", 1)[1]
    out, group = [], ""
    for raw in body.splitlines():
        line = raw.strip()
        if line.startswith("//"):
            group = line.lstrip("/ ").strip()
            continue
        for q, name in re.findall(r'"([^"]+)"|([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)', line):
            if q:
                out.append((q, group))
            elif name != "string":
                key = name.split(".")[-1]
                if key not in consts:
                    sys.exit("cannot resolve rule constant %r in lint_rules.go" % name)
                out.append((consts[key], group))
    sx = read(os.path.join(forge, "internal/linter/staticexport/staticexport.go"))
    ids = sx.split("func RuleIDs()")[1].split("}")[0]
    for name in re.findall(r"Rule\w+", ids):
        out.append((consts[name], "static export (--static-export)"))
    return out


def rollouts(forge, consts):
    """{rule id: (introduced, gates_from or '', fix or '')} from the table."""
    src = read(os.path.join(forge, "internal/cli/lint/lint_rollout_table.go"))
    body = src.split("var shippedRules")[1].split("// lintRollouts")[0]
    out = {}
    pat = re.compile(
        r'\{IntroducedIn: "([^"]+)"(?:, GatesFrom: "([^"]+)")?(?:, Fix: "([^"]*)")?, Rules: \[\]string\{(.*?)\}\}',
        re.S,
    )
    for m in pat.finditer(body):
        intro, gates, fix, rules = m.group(1), m.group(2) or "", m.group(3) or "", m.group(4)
        rules = re.sub(r"//[^\n]*", "", rules)
        for q, name in re.findall(r'"([^"]+)"|([A-Za-z_]\w*)', rules):
            if q:
                out[q] = (intro, gates, fix)
            elif name in consts:
                out[consts[name]] = (intro, gates, fix)
            else:
                sys.exit("cannot resolve rollout constant %r" % name)
    return out


def cell(intro, gates):
    return "%s -> %s" % (intro, gates) if gates else intro


def table(forge):
    consts = string_consts(forge)
    grade = gradable_rules(forge, consts)
    roll = rollouts(forge, consts)
    ids = [r for r, _ in grade]
    missing = sorted(set(ids) - set(roll))
    extra = sorted(set(roll) - set(ids))
    if missing or extra:
        sys.exit("rollout table and gradableRules disagree: missing=%s extra=%s" % (missing, extra))
    seen, rows = set(), []
    for rule, group in grade:
        if rule in seen:
            continue
        seen.add(rule)
        rows.append((rule, group, roll[rule]))
    return rows


def render(rows):
    lines = [
        "| Rule | Group | Rollout | Codemod |",
        "|------|-------|---------|---------|",
    ]
    for rule, group, (intro, gates, fix) in sorted(rows, key=lambda r: (r[1], r[0])):
        codemod = "`%s`" % fix if fix else ""
        lines.append("| `%s` | %s | %s | %s |" % (rule, group, cell(intro, gates), codemod))
    return "\n".join(lines)


def write(forge):
    rows = table(forge)
    page = os.path.join(DOCS_DIR, INDEX_PAGE)
    text = read(page)
    head, rest = text.split(BEGIN, 1)
    _, tail = rest.split(END, 1)
    new = head + BEGIN + "\n\n" + render(rows) + "\n\n" + END + tail
    with open(page, "w", encoding="utf-8") as f:
        f.write(new)
    print("wrote %d rules to %s" % (len(rows), page))


def check(forge):
    rows = {r: v for r, _, v in table(forge)}
    problems = []
    documented = set()
    row_pat = re.compile(r"^\|\s*`([a-z][a-z0-9/\-]+)`\s*\|")
    for path in sorted(glob.glob(os.path.join(DOCS_DIR, "rules-*.mdx"))):
        if os.path.basename(path) == INDEX_PAGE:
            continue
        for n, line in enumerate(read(path).splitlines(), 1):
            m = row_pat.match(line)
            if not m:
                continue
            rule = m.group(1)
            if rule not in rows:
                problems.append("%s:%d: `%s` is not a rule in forge" % (os.path.basename(path), n, rule))
                continue
            documented.add(rule)
            want = cell(rows[rule][0], rows[rule][1])
            if "| %s |" % want not in line:
                problems.append(
                    "%s:%d: `%s` rollout cell should be %r" % (os.path.basename(path), n, rule, want)
                )
    for rule in sorted(set(rows) - documented):
        problems.append("no row for `%s` in rules-*.mdx" % rule)
    if problems:
        print("\n".join(problems))
        sys.exit(1)
    print("ok: %d rules documented, rollout cells match" % len(rows))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--forge", required=True, help="path to a forge checkout at the ref being documented")
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    args = ap.parse_args()
    if args.write:
        write(args.forge)
    else:
        check(args.forge)


if __name__ == "__main__":
    main()
