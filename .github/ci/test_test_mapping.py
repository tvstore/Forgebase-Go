#!/usr/bin/env python3
"""Tests for test-mapping.py. Run: python3 .github/ci/test_test_mapping.py"""

import importlib.util
import os
import pathlib
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True  # do not leave __pycache__ in the repository
_spec = importlib.util.spec_from_file_location(
    "test_mapping", pathlib.Path(__file__).with_name("test-mapping.py")
)
tm = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(tm)


def write(root, rel, text):
    path = os.path.join(root, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


RUST_UNIT = """\
//! A doc comment that mentions #[test] is not a test.
/// #[test]
#[cfg(test)]
mod tests {
    #[test]
    fn plain() {}

    #[tokio::test]
    #[should_panic]
    async fn with_other_attribute() {}
}
"""

RUST_INTEGRATION = """\
#[test]
fn plain() {}

#[test]
fn only_here() {}
"""

GO_TEST = """\
package x

// Tests ported from series.rs, for the file header.

// Rust: a.rs::tests::plain
func TestUnitPlain(t *testing.T) {}

// Rust: b.rs::plain and only_here
func TestIntegration(t *testing.T) {}

func TestMerged(t *testing.T) {
	// Rust: a.rs::with_other_attribute
}

// Rust: b.rs::unrelated
// Not ported: nothing to port.
func TestNotPorted(t *testing.T) {}
"""


class MappingTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.rust = os.path.join(self.tmp.name, "rust")
        self.go = os.path.join(self.tmp.name, "go")
        write(self.rust, "crates/c1/src/a.rs", RUST_UNIT)
        write(self.rust, "crates/c2/tests/b.rs", RUST_INTEGRATION)
        write(self.go, "internal/x/x_test.go", GO_TEST)

    def run_mapping(self, notes=None):
        path = None
        if notes is not None:
            path = os.path.join(self.tmp.name, "notes.txt")
            write(self.tmp.name, "notes.txt", notes)
        return tm.map_tests(self.rust, self.go, path)

    def test_rust_tests_are_found_and_doc_comments_are_not(self):
        names = [(os.path.basename(p), n) for p, n in tm.rust_tests(self.rust)]
        self.assertEqual(
            sorted(names),
            [("a.rs", "plain"), ("a.rs", "with_other_attribute"), ("b.rs", "only_here"), ("b.rs", "plain")],
        )

    def test_comments_map_tests_and_tell_equal_names_apart(self):
        tests, mapped, not_ported, unmatched, problems = self.run_mapping()
        unit = ("crates/c1/src/a.rs", "plain")
        integration = ("crates/c2/tests/b.rs", "plain")
        self.assertEqual(mapped[unit], ["internal/x/x_test.go::TestUnitPlain"])
        self.assertEqual(mapped[integration], ["internal/x/x_test.go::TestIntegration"])
        self.assertEqual(mapped[("crates/c2/tests/b.rs", "only_here")], ["internal/x/x_test.go::TestIntegration"])
        self.assertEqual(mapped[("crates/c1/src/a.rs", "with_other_attribute")], ["internal/x/x_test.go::TestMerged"])
        self.assertEqual((len(tests), len(not_ported), len(unmatched), problems), (4, 0, 0, []))

    def test_notes_decide_what_comments_cannot_say(self):
        notes = "# comment\n" + "crates/c2/tests/b.rs::only_here | not ported | no reason to port\n"
        _, mapped, not_ported, unmatched, problems = self.run_mapping(notes)
        self.assertEqual(not_ported, {("crates/c2/tests/b.rs", "only_here"): "no reason to port"})
        self.assertNotIn(("crates/c2/tests/b.rs", "only_here"), unmatched)
        self.assertEqual(problems, [])

    def test_an_unnamed_test_is_unmatched_and_bad_notes_are_problems(self):
        write(self.rust, "crates/c2/tests/c.rs", "#[test]\nfn nobody_names_me() {}\n")
        notes = "crates/c2/tests/c.rs::missing | go | internal/x/x_test.go::TestNothing\n"
        _, _, _, unmatched, problems = self.run_mapping(notes)
        self.assertEqual(list(unmatched), [("crates/c2/tests/c.rs", "nobody_names_me")])
        self.assertEqual(len(problems), 1)
        self.assertIn("no such Rust test", problems[0])

    def test_scenarios_of_a_target_without_a_harness_are_units_too(self):
        write(self.rust, "crates/c2/tests/h.rs", "fn main() {}\nfn first() {}\nfn second() {}\n")
        write(self.go, "internal/x/h_test.go", "package x\n\n// Rust: h.rs::first\nfunc TestFirst(t *testing.T) {}\n")
        saved = tm.HARNESS_LESS
        self.addCleanup(setattr, tm, "HARNESS_LESS", saved)
        tm.HARNESS_LESS = {"crates/c2/tests/h.rs": ["first", "second"]}
        tests, mapped, _, unmatched, _ = self.run_mapping()
        self.assertEqual(len(tests), 6)
        self.assertEqual(mapped[("crates/c2/tests/h.rs", "first")], ["internal/x/h_test.go::TestFirst"])
        self.assertEqual(list(unmatched), [("crates/c2/tests/h.rs", "second")])
        tm.HARNESS_LESS = {"crates/c2/tests/h.rs": ["gone"]}
        with self.assertRaises(SystemExit):
            tm.rust_scenarios(self.rust)

    def test_only_comments_of_test_files_map_tests(self):
        write(self.rust, "crates/c2/tests/l.rs", "#[test]\nfn lonely() {}\n")
        write(self.go, "internal/x/x.go", "package x\n\n// Rust: l.rs::lonely\nfunc helper() {}\n")
        _, mapped, _, unmatched, _ = self.run_mapping()
        self.assertNotIn(("crates/c2/tests/l.rs", "lonely"), mapped)
        self.assertIn(("crates/c2/tests/l.rs", "lonely"), unmatched)

    def test_render_has_a_table_per_file_and_the_totals(self):
        tests, mapped, not_ported, unmatched, _ = self.run_mapping()
        text = tm.render(tests, mapped, not_ported, unmatched)
        self.assertIn("| Rust tests and scenarios | 4 |", text)
        self.assertIn("## `crates/c1/src/a.rs`", text)
        self.assertIn("## `crates/c2/tests/b.rs`", text)
        self.assertIn("| `plain` | `internal/x/x_test.go::TestUnitPlain` | mapped |", text)


if __name__ == "__main__":
    unittest.main()
