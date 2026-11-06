#!/usr/bin/env python3
"""Offline check of relative links and anchors in the Markdown files of the repository.

Usage: python3 .github/ci/linkcheck.py [ROOT]    (default: the repository root)

For every `*.md` file (outside node_modules, target and .git) it checks that each relative link
points to an existing file or directory and, for a link to a Markdown file with `#anchor`, that
the anchor is a heading of that file (GitHub's slug rules). http(s) and mailto links are not
followed. Exit status 1 when there is any problem. Standard library only.
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
    checked = problems = 0
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
            if not os.path.exists(dest):
                print(f"MISSING {rel}: {target}")
                problems += 1
            elif anchor and dest.endswith(".md") and unquote(anchor).lower() not in anchors(dest):
                print(f"ANCHOR  {rel}: {target}")
                problems += 1
    print(f"{checked} relative links checked, {problems} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
