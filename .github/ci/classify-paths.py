#!/usr/bin/env python3
"""Classify the paths a pull request changes, to decide which CI jobs run.

Reads one changed path per line on stdin and prints `key=value` lines for $GITHUB_OUTPUT:

  go=true|false         a Go job reads one of the paths (anything unknown counts as Go)
  desk=true|false       a path under desk/ other than a Markdown file, or a DESK_ALSO path
  e2e=true|false        go or desk
  docker=true|false     Dockerfile, .dockerignore or deploy/ (all of deploy/ counts, Markdown included)
  docs_only=true|false  at least one path, and every path is documentation
  all=true|false        .github/ changed, or the event is schedule or workflow_dispatch

`--event NAME` is the GitHub event name. For `schedule` and `workflow_dispatch` nothing is
diffed (stdin is not read) and everything runs. Standard library only; see
docs/blueprint/maintenance-policy.md (CI) and .github/ci/test_classify_paths.py.
"""

import sys

ALWAYS_ALL_EVENTS = {"schedule", "workflow_dispatch"}

# Read by the Go jobs (check, licenses and the jobs that later phases add). Markdown and any other
# file inside these directories still counts as Go: files there are compiled in with //go:embed
# (DocTypes, migrations, templates), so a README there can be code. A `.go` file anywhere outside
# desk/ counts as Go too.
GO_PREFIXES = ("cmd/", "internal/", "testing/", "examples/")
GO_FILES = {"go.mod", "go.sum"}
DOCKER_FILES = {"Dockerfile", ".dockerignore"}
DOC_FILES = {"AGENTS.md", "CHANGELOG.md", "LICENSE"}
# Files outside desk/ that the desk reads (desk/src/icons.test.ts imports the first one). They stay Go
# and also run the desk job. Add a path here whenever a desk file imports from outside desk/.
DESK_ALSO = {"internal/core/doctype_icons.txt"}


def classify_path(path):
    """Return "all", "go", "docker", "desk" or "docs" for one changed path."""
    path = path.strip()
    if path.startswith("./"):
        path = path[2:]
    if path.startswith(".github/"):
        return "all"
    # A .go file outside desk/ is Go wherever it is, deploy/ included.
    if path.endswith(".go") and not path.startswith("desk/"):
        return "go"
    # The image build copies the whole module, but only these paths make it run: the weekly run
    # (schedule) covers the rest. deploy/ is not Go: nothing under it is compiled.
    if path in DOCKER_FILES or path.startswith("deploy/"):
        return "docker"
    if path.startswith("desk/"):
        return "docs" if path.endswith(".md") else "desk"
    if path in GO_FILES or path.startswith(GO_PREFIXES) or path.endswith(".go"):
        return "go"
    if path.startswith("docs/") or path.endswith(".md") or path in DOC_FILES:
        return "docs"
    # Unknown: the safe default is to run the Go jobs.
    return "go"


def classify(paths, event=""):
    """Return the outputs as a dict of bools."""
    if event in ALWAYS_ALL_EVENTS:
        return {"go": True, "desk": True, "e2e": True, "docker": True, "docs_only": False, "all": True}
    kinds = [classify_path(p) for p in paths if p.strip()]
    go = "go" in kinds
    desk = "desk" in kinds or any(p.strip().removeprefix("./") in DESK_ALSO for p in paths)
    return {
        "go": go,
        "desk": desk,
        "e2e": go or desk,
        "docker": "docker" in kinds,
        "docs_only": bool(kinds) and all(k == "docs" for k in kinds),
        "all": "all" in kinds,
    }


def render(result):
    return "".join(f"{k}={'true' if v else 'false'}\n" for k, v in result.items())


def main(argv):
    event = ""
    if argv[:1] == ["--event"] and len(argv) == 2:
        event = argv[1]
    elif argv:
        print("usage: classify-paths.py [--event NAME] < paths", file=sys.stderr)
        return 2
    paths = [] if event in ALWAYS_ALL_EVENTS else sys.stdin.read().splitlines()
    sys.stdout.write(render(classify(paths, event)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
