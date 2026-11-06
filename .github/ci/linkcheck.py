#!/usr/bin/env python3
"""Offline check of relative links and anchors in the Markdown files of the repository.

Usage: python3 .github/ci/linkcheck.py [ROOT]    (default: the repository root)

For every `*.md` file (outside node_modules, target and .git) it checks that each relative link
points to an existing file or directory and, for a link to a Markdown file with `#anchor`, that
the anchor is a heading of that file (GitHub's slug rules). http(s) and mailto links are not
followed. Exit status 1 when there is any problem. Standard library only.

Go port (ADR 0026): the documents carried over from the Rust repository link to files that the Go
port has not created yet (examples/, deploy/, the Dockerfile, the desk end-to-end specs) or that
exist only in the Rust repository (crates/, Cargo.toml). `.github/ci/linkcheck-pending.txt` lists
those repository-relative paths, one per line (a trailing `/` means everything under it; `#`
starts a comment). A link to a file that is missing but listed there is counted as pending, not as
a problem; remove the line when the phase that creates the file lands. A listed path that exists
is checked like any other.
"""

import os
import re
import sys
from urllib.parse import unquote

SKIP_DIRS = {"node_modules", "target", ".git"}
FENCE = re.compile(r"^ {0,3}(```|~~~).*?^ {0,3}\1[ \t]*$", re.S | re.M)
INLINE_CODE = re.compile(r"`+[^`\n]*`+")
LINK = re.compile(r"\]\(\s*<?([^)\s>]+)>?[^)]*\)")
SRC = re.compile(r'(?:src|href)="([^"]+)"')
EXTERNAL = re.compile(r"^(?:[a-z][a-z0-9+.-]*:|//)", re.I)


def strip_fences(text):
    return FENCE.sub("", text)


def slug(heading):
    """GitHub's heading anchor: rendered text, lowercase, punctuation dropped, spaces to hyphens."""
    h = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", r"\1", heading)  # [text](url) renders as text
    h = re.sub(r"<[^>]+>", "", h).strip().lower()
    h = re.sub(r"[^\w\- ]", "", h)
    return h.replace(" ", "-")


def anchors(path, cache={}):
    if path not in cache:
        found, seen = set(), {}
        with open(path, encoding="utf-8") as f:
            text = strip_fences(f.read())
        for m in re.finditer(r"^ {0,3}#{1,6}[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$", text, re.M):
            s = slug(m.group(1))
            n = seen.get(s, 0)
            seen[s] = n + 1
            found.add(s if n == 0 else f"{s}-{n}")
        cache[path] = found
    return cache[path]


def pending_paths(root):
    """Repository-relative paths that are known to be missing for now (see the module docstring)."""
    try:
        with open(os.path.join(root, ".github", "ci", "linkcheck-pending.txt"), encoding="utf-8") as f:
            lines = [line.split("#", 1)[0].strip() for line in f]
    except FileNotFoundError:
        return []
    return [line for line in lines if line]


def is_pending(rel_dest, pending):
    for entry in pending:
        if entry.endswith("/"):
            if (rel_dest + "/").startswith(entry):
                return True
        elif rel_dest == entry:
            return True
    return False


def markdown_files(root):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            if name.endswith(".md"):
                yield os.path.join(dirpath, name)


def main(argv):
    root = os.path.abspath(
        argv[0] if argv else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")
    )
    checked = problems = deferred = 0
    pending = pending_paths(root)
    for md in markdown_files(root):
        with open(md, encoding="utf-8") as f:
            text = INLINE_CODE.sub("", strip_fences(f.read()))
        rel = os.path.relpath(md, root)
        for target in LINK.findall(text) + SRC.findall(text):
            if EXTERNAL.match(target):
                continue
            checked += 1
            path, _, anchor = target.partition("#")
            if path:
                base = root if path.startswith("/") else os.path.dirname(md)
                dest = os.path.normpath(os.path.join(base, unquote(path).lstrip("/")))
            else:
                dest = md
            if not os.path.exists(dest) and is_pending(os.path.relpath(dest, root), pending):
                deferred += 1
            elif not os.path.exists(dest):
                print(f"MISSING {rel}: {target}")
                problems += 1
            elif anchor and dest.endswith(".md") and unquote(anchor).lower() not in anchors(dest):
                print(f"ANCHOR  {rel}: {target}")
                problems += 1
    print(f"{checked} relative links checked, {deferred} pending, {problems} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
