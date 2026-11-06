#!/usr/bin/env python3
"""Tests for classify-paths.py. Run: python3 .github/ci/test_classify_paths.py"""

import importlib.util
import sys
import pathlib
import unittest

sys.dont_write_bytecode = True  # do not leave __pycache__ in the repository
_spec = importlib.util.spec_from_file_location(
    "classify_paths", pathlib.Path(__file__).with_name("classify-paths.py")
)
cp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cp)


def flags(paths, event="pull_request"):
    return cp.classify(paths, event)


def expect(go=False, desk=False, e2e=False, docs_only=False, all_=False, docker=False):
    return {"go": go, "desk": desk, "e2e": e2e, "docker": docker, "docs_only": docs_only,
            "all": all_}


class ClassifyTest(unittest.TestCase):
    def test_docs_only(self):
        paths = ["docs/blueprint/vision.md", "README.md", "AGENTS.md", "CHANGELOG.md", "LICENSE",
                 "CONTRIBUTING.md"]
        self.assertEqual(flags(paths), expect(docs_only=True))

    def test_desk_only(self):
        self.assertEqual(flags(["desk/src/main.tsx", "desk/package.json"]),
                         expect(desk=True, e2e=True))

    def test_desk_markdown_is_docs(self):
        self.assertEqual(flags(["desk/README.md"]), expect(docs_only=True))

    def test_desk_package_json_is_desk(self):
        self.assertEqual(flags(["desk/package.json"]), expect(desk=True, e2e=True))

    def test_go_directories_only(self):
        for path in ["cmd/forgebase/main.go", "internal/core/doc.go", "testing/doc.go",
                     "examples/sales/main.go", "internal/db/migrations/0001_init.sql"]:
            self.assertEqual(flags([path]), expect(go=True, e2e=True), path)

    def test_go_file_anywhere_outside_desk(self):
        self.assertEqual(flags(["forgebase.go"]), expect(go=True, e2e=True))
        self.assertEqual(flags(["forgebase_test.go"]), expect(go=True, e2e=True))
        self.assertEqual(flags(["tools/gen/main.go"]), expect(go=True, e2e=True))

    def test_desk_dir_is_not_go(self):
        # A path under desk/ is desk, whatever its extension (a .go file there is unexpected but
        # the desk job reads it, not the Go jobs).
        self.assertEqual(flags(["desk/e2e/seed.go"]), expect(desk=True, e2e=True))

    def test_desk_also_is_empty_for_now(self):
        self.assertEqual(cp.DESK_ALSO, set())

    def test_desk_also_file_runs_desk_and_go(self):
        # The set is empty in P0; P3 adds the icon list. Exercise the logic with a stand-in path.
        original = cp.DESK_ALSO
        cp.DESK_ALSO = {"internal/core/doctype_icons.txt"}
        try:
            self.assertEqual(flags(["internal/core/doctype_icons.txt"]),
                             expect(go=True, desk=True, e2e=True))
            self.assertEqual(flags(["./internal/core/doctype_icons.txt"]),
                             expect(go=True, desk=True, e2e=True))
        finally:
            cp.DESK_ALSO = original

    def test_go_file_under_deploy_is_go_and_docker(self):
        self.assertEqual(flags(["deploy/tools/check.go"]), expect(go=True, e2e=True))
        self.assertEqual(flags(["deploy/compose.yaml"]), expect(docker=True))

    def test_markdown_inside_go_directories_is_go(self):
        # //go:embed compiles files there into the binary.
        self.assertEqual(flags(["internal/app/templates/app/README.md"]),
                         expect(go=True, e2e=True))
        self.assertEqual(flags(["examples/sales/README.md"]), expect(go=True, e2e=True))

    def test_workflow_changed(self):
        self.assertEqual(flags([".github/workflows/ci.yml"]), expect(all_=True))
        self.assertEqual(flags([".github/ci/linkcheck.py", "README.md"]), expect(all_=True))

    def test_mixed(self):
        self.assertEqual(flags(["docs/a.md", "desk/src/a.ts", "internal/x/x.go"]),
                         expect(go=True, desk=True, e2e=True))
        self.assertEqual(flags(["docs/a.md", "desk/src/a.ts"]), expect(desk=True, e2e=True))

    def test_unknown_file_counts_as_go(self):
        self.assertEqual(flags(["scripts/foo.sh"]), expect(go=True, e2e=True))
        self.assertEqual(flags([".gitignore"]), expect(go=True, e2e=True))
        self.assertEqual(flags(["Makefile"]), expect(go=True, e2e=True))

    def test_go_mod_and_sum(self):
        for path in ["go.mod", "go.sum"]:
            self.assertEqual(flags([path]), expect(go=True, e2e=True), path)

    def test_empty_diff(self):
        self.assertEqual(flags([]), expect())
        self.assertEqual(flags(["", "  "]), expect())

    def test_leading_dot_slash(self):
        self.assertEqual(flags(["./docs/a.md"]), expect(docs_only=True))

    def test_schedule_and_dispatch_run_everything(self):
        everything = expect(go=True, desk=True, e2e=True, all_=True, docker=True)
        self.assertEqual(flags([], "schedule"), everything)
        self.assertEqual(flags([], "workflow_dispatch"), everything)
        self.assertEqual(flags(["README.md"], "workflow_dispatch"), everything)

    def test_docker_paths(self):
        for path in ["Dockerfile", ".dockerignore", "deploy/compose.yaml", "deploy/entrypoint.sh",
                     "deploy/.env.example", "deploy/README.md"]:
            self.assertEqual(flags([path]), expect(docker=True), path)

    def test_docker_with_other_paths(self):
        self.assertEqual(flags(["Dockerfile", "docs/a.md"]), expect(docker=True))
        self.assertEqual(flags(["deploy/compose.yaml", "internal/x/x.go"]),
                         expect(go=True, e2e=True, docker=True))

    def test_similar_paths_are_not_docker(self):
        # Only the exact file names and the deploy/ directory count.
        self.assertEqual(flags(["docs/guide/deploy.md"]), expect(docs_only=True))
        self.assertEqual(flags(["examples/deploy/x.go"]), expect(go=True, e2e=True))
        self.assertEqual(flags(["desk/Dockerfile"]), expect(desk=True, e2e=True))

    def test_render(self):
        self.assertEqual(
            cp.render(flags(["README.md"])),
            "go=false\ndesk=false\ne2e=false\ndocker=false\ndocs_only=true\nall=false\n",
        )


if __name__ == "__main__":
    unittest.main()
