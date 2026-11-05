#!/usr/bin/env python3
"""Keep docs/forge/linting/ in step with forge's lint rule registry.

forge declares every rule `forge lint` can grade in
internal/cli/lint/lint_rules.go (gradableRules(): the rule IDs, grouped) and
internal/linter/staticexport. The registry carries no prose, so the one source
of a rule's description is its row in the hand-written family pages
(docs/forge/linting/rules-*.mdx), whose last column is the description.

  --write   rewrites the generated table between the BEGIN/END GENERATED
            markers in rules-index.mdx: Rule (linked to its section on the
            family page), Group, and the description copied from the family
            page row.
  --check   fails if a rule in forge has no row in the family pages, if a row
            names a rule forge no longer has, if a rule is documented twice
            with different descriptions, if a description is empty, or if a
            description holds a character that breaks an MDX table cell.

Regenerate after a forge release (check out the forge ref you document first;
the script reads files, not git history):

  python3 scripts/generate-forge-lint-rules.py --forge ../forge --write
  python3 scripts/generate-forge-lint-rules.py --forge ../forge --check
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


def slug(heading):
    """The anchor a docs site derives from a heading, for plain ASCII IDs."""
    text = heading.replace("`", "").lower()
    text = re.sub(r"[^a-z0-9 \-]", "", text)
    return re.sub(r" +", "-", text.strip())


ROW = re.compile(r"^\|\s*`([a-z][a-z0-9/\-]+)`\s*\|(.*)\|\s*$")
BAD_CELL = re.compile(r"[|<>{}]")


def family_pages():
    for path in sorted(glob.glob(os.path.join(DOCS_DIR, "rules-*.mdx"))):
        if os.path.basename(path) != INDEX_PAGE:
            yield path


def documented():
    """({rule: description}, {rule: (page, anchor or '')}, problems) from the family pages."""
    desc, where, problems = {}, {}, []
    for path in family_pages():
        page = os.path.basename(path)[: -len(".mdx")]
        headings = []
        in_fence = False
        for n, line in enumerate(read(path).splitlines(), 1):
            if line.startswith("```"):
                in_fence = not in_fence
            if in_fence:
                continue
            if line.startswith("### "):
                headings.append(line[4:])
            m = ROW.match(line)
            if not m:
                continue
            rule = m.group(1)
            text = m.group(2).split("|")[-1].strip()
            where_ = "%s.mdx:%d: `%s`" % (page, n, rule)
            if not text:
                problems.append("%s has an empty description" % where_)
            # a description cell may hold backticked <, > and {}: only a bare one breaks MDX
            bare = BAD_CELL.search(re.sub(r"`[^`]*`", "", text))
            if bare:
                problems.append("%s: description has a bare %r, which breaks an MDX table cell" % (where_, bare.group(0)))
            if rule in desc and desc[rule] != text:
                problems.append("%s is documented twice with different descriptions" % where_)
            desc.setdefault(rule, text)
            where.setdefault(rule, page)
        # anchors: the ### heading that names the rule in backticks
        for h in headings:
            for r in re.findall(r"`([^`]+)`", h):
                if r in where and where[r] == page and re.fullmatch(r"[a-z0-9\-]+", r):
                    where[r] = (page, slug(h))
    return desc, where, problems


def table(forge):
    consts = string_consts(forge)
    grade = gradable_rules(forge, consts)
    desc, where, problems = documented()
    ids = [r for r, _ in grade]
    for rule in sorted(set(ids) - set(desc)):
        problems.append("no row for `%s` in rules-*.mdx" % rule)
    for rule in sorted(set(desc) - set(ids)):
        problems.append("a row names `%s`, which is not a rule in forge" % rule)
    seen, rows = set(), []
    for rule, group in grade:
        if rule in seen or rule not in desc:
            continue
        seen.add(rule)
        loc = where[rule]
        page, anchor = loc if isinstance(loc, tuple) else (loc, "")
        link = "/forge/linting/%s%s" % (page, "#" + anchor if anchor else "")
        rows.append((rule, group, desc[rule], link))
    return rows, problems


def render(rows):
    lines = [
        "| Rule | Group | Description |",
        "|------|-------|-------------|",
    ]
    for rule, group, text, link in sorted(rows, key=lambda r: (r[1], r[0])):
        lines.append("| [`%s`](%s) | %s | %s |" % (rule, link, group, text))
    return "\n".join(lines)


def write(forge):
    rows, problems = table(forge)
    if problems:
        print("\n".join(problems))
        sys.exit(1)
    page = os.path.join(DOCS_DIR, INDEX_PAGE)
    text = read(page)
    head, rest = text.split(BEGIN, 1)
    _, tail = rest.split(END, 1)
    new = head + BEGIN + "\n\n" + render(rows) + "\n\n" + END + tail
    with open(page, "w", encoding="utf-8") as f:
        f.write(new)
    print("wrote %d rules to %s" % (len(rows), page))


def check(forge):
    rows, problems = table(forge)
    if problems:
        print("\n".join(problems))
        sys.exit(1)
    page = read(os.path.join(DOCS_DIR, INDEX_PAGE))
    block = page.split(BEGIN, 1)[1].split(END, 1)[0].strip()
    if block != render(rows):
        print("rules-index.mdx is stale: run the script with --write")
        sys.exit(1)
    print("ok: %d rules documented with a description, index up to date" % len(rows))


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
