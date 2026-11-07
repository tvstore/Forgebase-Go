#!/usr/bin/env python3
"""Map every test of the frozen Rust implementation to the Go test that ports it.

Usage: python3 .github/ci/test-mapping.py RUST_CHECKOUT [--go ROOT] [--notes FILE] [--out FILE] [--strict]

RUST_CHECKOUT is a checkout of https://github.com/tvstore/Forgebase (it is not part of CI: the
output, docs/parity/test-mapping.md, is a snapshot written for the parity report, docs/parity-report.md).
ROOT is the Go repository (default: the repository of this script). Without --out the Markdown goes
to standard output. Standard library only; see .github/ci/test_test_mapping.py.

Rust side: every function after a line that is exactly `#[test]` or `#[tokio::test]` in a `.rs`
file (crates/ and examples/). Doc comments (`//!`, `///`) are not tests and are not counted. Two
integration tests have `harness = false` in Cargo.toml (their own `main`, no `#[test]`): their
scenario functions, listed in HARNESS_LESS, are counted as scenarios, next to the tests.

Go side: in the `_test.go` files, a comment block (consecutive `//` lines) that has `Rust:` or mentions a `.rs` file. It
names Rust tests as `file.rs::name`, `file.rs::tests::name` (a unit test in src/), or a bare
`name` after a `file.rs::` in the same block. The block belongs to the function that follows it
or, inside a function, to the one it is in. A block that says `Not ported: <reason>` records a
Rust test that has no Go counterpart, with the reason. Two Rust files with the same name and the
same test name are told apart by `crates/<crate>/` in the block, by `tests::`, and by the Go
package.

NOTES is a file of lines `RUST_PATH::NAME | go | GO_FILE::GO_FUNC`, `... | not ported | REASON` or
`... | unmatched | WHAT IS KNOWN` for what the comments cannot say (default:
.github/ci/test-mapping-notes.txt next to this script). Status words are checked: a `go` note must
name a function that exists. With --strict the exit status is 1 when a Rust test is unmatched.
"""

import argparse
import collections
import os
import re
import sys

sys.dont_write_bytecode = True

SKIP_DIRS = {".git", "target", "node_modules"}
TEST_ATTR = re.compile(r"#\[(tokio::)?test(\(.*\))?\]")
FN = re.compile(r"(?:pub\s+)?(?:async\s+)?fn\s+(\w+)")
GO_FUNC = re.compile(r"func\s+(?:\([^)]*\)\s*)?(\w+)")
FILE_REF = re.compile(r"([A-Za-z0-9_]+)\.rs\b")
IDENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]{3,}")
CRATE_HINT = re.compile(r"crates/([a-z_-]+)")
NOT_PORTED = re.compile(r"Not ported:\s*(.*)", re.S)


# Test targets with `harness = false` (crates/forgebase/Cargo.toml): one `main` that runs scenario
# functions, so they have no `#[test]`. The scenarios are the functions that `main` runs (site_apps)
# or that `drive` runs (app_binary); each one must exist in its file (checked).
HARNESS_LESS = {
    "crates/forgebase/tests/app_binary.rs": ["drive", "fixtures", "reports", "jobs"],
    "crates/forgebase/tests/site_apps.rs": [
        "config_and_site_list", "migrate_per_site", "cli_per_site", "http_per_site",
        "worker_per_site", "removed_app", "site_create", "follow_ups",
    ],
}


def read_lines(path):
    with open(path, encoding="utf-8") as f:
        return f.read().split("\n")


def walk(root, suffix):
    for directory, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for name in sorted(files):
            if name.endswith(suffix):
                yield os.path.join(directory, name)


def rust_tests(root):
    """[(path relative to root, test name)] in file order."""
    found = []
    for path in walk(root, ".rs"):
        lines = read_lines(path)
        for i, line in enumerate(lines):
            if not TEST_ATTR.fullmatch(line.strip()):
                continue
            for later in lines[i + 1:]:
                match = FN.match(later.strip())
                if match:
                    found.append((os.path.relpath(path, root), match.group(1)))
                    break
    return found


def rust_scenarios(root):
    """[(path relative to root, scenario name)] of the harness-less test targets that exist."""
    found = []
    for rel, names in HARNESS_LESS.items():
        path = os.path.join(root, rel)
        if not os.path.exists(path):
            continue
        defined = {m.group(1) for line in read_lines(path) for m in [re.match(r"fn\s+(\w+)", line)] if m}
        for name in names:
            if name not in defined:
                raise SystemExit("HARNESS_LESS names %s::%s, which is not a function of the file" % (rel, name))
            found.append((rel, name))
    return found


def comment_blocks(path):
    """Yield (first line number, comment text, name of the function it belongs to or None).

    An indented block is inside a function; a block at the left margin belongs to the function
    that follows it directly, and to none otherwise.
    """
    lines = read_lines(path)
    funcs = [(i, GO_FUNC.match(l).group(1)) for i, l in enumerate(lines) if l.startswith("func ")]
    i = 0
    while i < len(lines):
        if not lines[i].lstrip().startswith("//"):
            i += 1
            continue
        j = i
        while j < len(lines) and lines[j].lstrip().startswith("//"):
            j += 1
        text = "\n".join(l.lstrip()[2:] for l in lines[i:j])
        if j < len(lines) and lines[j].startswith("func "):
            owner = GO_FUNC.match(lines[j]).group(1)
        elif lines[i].startswith("//"):
            owner = None  # a comment between declarations that no function follows
        else:
            before = [name for (n, name) in funcs if n < i]
            owner = before[-1] if before else None
        yield i + 1, text, owner
        i = j


def go_crate(rel):
    """The Rust crate that a Go path stands for (to tell two Rust files of one name apart)."""
    if rel.startswith(("internal/core/", "internal/decimal/")):
        return "forgebase-core"
    if rel.startswith("internal/db/"):
        return "forgebase-db"
    if rel.startswith("internal/httpapi/"):
        return "forgebase-http"
    return "forgebase"


def pick(candidates, block, go_rel):
    """Narrow the Rust tests of one (file, name) pair to those that a Go block names."""
    if len(candidates) == 1:
        return candidates
    hint = CRATE_HINT.search(block)
    wanted = hint.group(1) if hint else go_crate(go_rel)
    narrowed = [c for c in candidates if "crates/" + wanted + "/" in c[0] + "/"] or candidates
    unit = re.search(r"\.rs::tests::", block) is not None
    by_kind = [c for c in narrowed if ("/src/" in c[0]) == unit]
    return by_kind or narrowed


def map_tests(rust_root, go_root, notes_path=None):
    """Return (tests, mapped, not_ported, unmatched, problems); tests includes the scenarios.

    mapped: {(path, name): sorted list of "go/file.go::Func"}; not_ported and unmatched:
    {(path, name): text}; problems: notes that name a test or function that does not exist.
    """
    tests = rust_tests(rust_root) + rust_scenarios(rust_root)
    by_pair = collections.defaultdict(list)
    for t in tests:
        by_pair[(os.path.basename(t[0])[:-3], t[1])].append(t)
    by_name = collections.defaultdict(list)
    for t in tests:
        by_name[t[1]].append(t)

    mapped = collections.defaultdict(set)
    header_only = collections.defaultdict(set)
    not_ported = {}
    go_funcs = set()
    for path in list(walk(go_root, ".go")) + list(walk(go_root, ".go.tpl")):
        rel = os.path.relpath(path, go_root)
        for line in read_lines(path):
            if line.startswith("func "):
                go_funcs.add(rel + "::" + GO_FUNC.match(line).group(1))
        if not path.endswith("_test.go"):
            continue  # only the comments of test files name ported tests
        for _, text, owner in comment_blocks(path):
            if "Rust:" not in text and not FILE_REF.search(text):
                continue
            files = set(FILE_REF.findall(text))
            idents = set(IDENT.findall(text))
            hits = []
            for (base, name), candidates in by_pair.items():
                if name in idents and base in files:
                    hits += pick(candidates, text, rel)
            if not files:
                hits += [t for name in idents for t in by_name.get(name, []) if len(by_name[name]) == 1]
            reason = NOT_PORTED.search(text)
            for t in hits:
                if reason:
                    not_ported[(t[0], t[1])] = " ".join(reason.group(1).split())
                elif owner:
                    mapped[(t[0], t[1])].add(rel + "::" + owner)
                else:
                    header_only[(t[0], t[1])].add(rel + "::(file header)")
    for key, where in header_only.items():
        if key not in mapped and key not in not_ported:
            mapped[key] = where

    unmatched = {}
    problems = []
    known = {(t[0], t[1]) for t in tests}
    for line in read_notes(notes_path):
        key, status, text = line
        if key not in known:
            problems.append("notes: no such Rust test: %s::%s" % key)
        elif status == "go":
            if text not in go_funcs:
                problems.append("notes: no such Go function: %s" % text)
            mapped[key].add(text)
        elif status == "not ported":
            not_ported[key] = text
        elif status == "unmatched":
            unmatched[key] = text
        else:
            problems.append("notes: unknown status %r for %s::%s" % ((status,) + key))
    for t in tests:
        key = (t[0], t[1])
        if key not in mapped and key not in not_ported and key not in unmatched:
            unmatched[key] = "no Go test names it"
    return tests, {k: sorted(v) for k, v in mapped.items()}, not_ported, unmatched, problems


def read_notes(path):
    if not path or not os.path.exists(path):
        return []
    rows = []
    for line in read_lines(path):
        if not line.strip() or line.startswith("#"):
            continue
        key, status, text = [part.strip() for part in line.split("|", 2)]
        rust_path, name = key.rsplit("::", 1)
        rows.append(((rust_path, name), status, text))
    return rows


def render(tests, mapped, not_ported, unmatched):
    scenarios = sum(1 for t in tests if t[0] in HARNESS_LESS and t[1] in HARNESS_LESS[t[0]])
    out = [
        "# Test mapping: Rust tests to Go tests",
        "",
        "Generated by `python3 .github/ci/test-mapping.py RUST_CHECKOUT --out docs/parity/test-mapping.md`; do not edit by hand.",
        "The method and the totals are in the [parity report](../parity-report.md#1-test-mapping).",
        "",
        "| | Tests |",
        "|---|---|",
        "| Rust tests and scenarios | %d |" % len(tests),
        "| of which scenarios of the two targets without a test harness | %d |" % scenarios,
        "| Mapped to Go tests | %d |" % len(mapped),
        "| Not ported, with the reason | %d |" % len(not_ported),
        "| Unmatched | %d |" % len(unmatched),
        "",
    ]
    by_file = collections.OrderedDict()
    for path, name in tests:
        by_file.setdefault(path, []).append(name)
    for path in sorted(by_file):
        out += ["## `%s`" % path, "", "| Rust test | Go test | Status |", "|---|---|---|"]
        for name in by_file[path]:
            key = (path, name)
            if key in mapped:
                go = "<br>".join("`%s`" % g for g in mapped[key])
                out.append("| `%s` | %s | mapped |" % (name, go))
            elif key in not_ported:
                out.append("| `%s` | none | not ported: %s |" % (name, cell(not_ported[key])))
            else:
                out.append("| `%s` | none | unmatched: %s |" % (name, cell(unmatched[key])))
        out.append("")
    return "\n".join(out)


def cell(text):
    return text.replace("|", "\\|")


def main(argv):
    here = os.path.dirname(os.path.abspath(__file__))
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("rust")
    parser.add_argument("--go", default=os.path.dirname(os.path.dirname(here)))
    parser.add_argument("--notes", default=os.path.join(here, "test-mapping-notes.txt"))
    parser.add_argument("--out")
    parser.add_argument("--strict", action="store_true")
    args = parser.parse_args(argv)
    tests, mapped, not_ported, unmatched, problems = map_tests(args.rust, args.go, args.notes)
    text = render(tests, mapped, not_ported, unmatched)
    if args.out:
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(text)
    else:
        sys.stdout.write(text)
    summary = "rust=%d mapped=%d not_ported=%d unmatched=%d" % (
        len(tests), len(mapped), len(not_ported), len(unmatched))
    print(summary, file=sys.stderr)
    for problem in problems:
        print(problem, file=sys.stderr)
    for key, why in sorted(unmatched.items()):
        print("unmatched: %s::%s (%s)" % (key + (why,)), file=sys.stderr)
    if problems or (args.strict and unmatched):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
