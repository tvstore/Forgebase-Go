package app

import (
	"strings"
	"testing"
)

// The tests of the parser and of the help and usage rendering. The Rust crate has no unit tests of
// args.rs (clap is a library there); these check the behaviour that the Rust binary's tests rely on
// and the clap formats that this parser reproduces.

func parseArgs(args ...string) (*matches, *exitRequest) {
	return parse(rootCommand(), "1.2.3", args)
}

func mustParseArgs(t *testing.T, args ...string) *matches {
	t.Helper()
	m, exit := parseArgs(args...)
	if exit != nil {
		t.Fatalf("%v: %s", args, exit.text)
	}
	return m
}

func mustExit(t *testing.T, args ...string) *exitRequest {
	t.Helper()
	m, exit := parseArgs(args...)
	if exit == nil {
		t.Fatalf("%v parsed: %v", args, m.path)
	}
	return exit
}

func TestVersionAndHelpGoToStandardOutputWithCodeZero(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		exit := mustExit(t, flag)
		if exit.code != 0 || !exit.stdout || exit.text != "forgebase 1.2.3\n" {
			t.Errorf("%s: %+v", flag, exit)
		}
	}
	// Version ends the parse where it is met, as in clap: what follows is never looked at.
	if exit := mustExit(t, "--version", "extra"); exit.code != 0 {
		t.Errorf("%+v", exit)
	}
	// ... and what precedes it is.
	if exit := mustExit(t, "extra", "--version"); exit.code != 2 {
		t.Errorf("%+v", exit)
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"migrate", "--help"}, {"doc", "get", "-h"}, {"help"}, {"help", "doc", "get"}, {"--site", "x", "migrate", "-h"}} {
		exit := mustExit(t, args...)
		if exit.code != 0 || !exit.stdout || !strings.Contains(exit.text, "Usage: forgebase") {
			t.Errorf("%v: %+v", args, exit)
		}
	}
	// -V is only a flag of the program, not of its subcommands.
	if exit := mustExit(t, "migrate", "-V"); exit.code != 2 || !strings.Contains(exit.text, "unexpected argument '-V' found") {
		t.Errorf("%+v", exit)
	}
}

func TestNoArgumentsAndNoSubcommandShowTheHelpOnStandardError(t *testing.T) {
	for _, args := range [][]string{nil, {"doc"}, {"user"}, {"user", "permission"}, {"customize", "field"}} {
		exit := mustExit(t, args...)
		if exit.code != 2 || exit.stdout || !strings.Contains(exit.text, "Usage: forgebase") || !strings.Contains(exit.text, "Commands:") || strings.HasPrefix(exit.text, "error:") {
			t.Errorf("%v: %+v", args, exit)
		}
	}
	// With an argument but no subcommand it is an error that lists the subcommands.
	exit := mustExit(t, "--site", "x")
	for _, want := range []string{
		"error: 'forgebase' requires a subcommand but one was not provided\n  [subcommands: migrate, doc, data, user, token, webhook, customize, site, new-app, worker, job, serve, help]\n\n",
		"Usage: forgebase [OPTIONS] <COMMAND>\n\nFor more information, try '--help'.\n",
	} {
		if !strings.Contains(exit.text, want) || exit.code != 2 {
			t.Errorf("missing %q in %q", want, exit.text)
		}
	}
}

func TestUsageErrorsHaveTheClapFormatAndCodeTwo(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"bogus"}, "error: unrecognized subcommand 'bogus'\n\nUsage: forgebase [OPTIONS] <COMMAND>" + hint},
		{[]string{"doc", "bogus"}, "error: unrecognized subcommand 'bogus'\n\nUsage: forgebase doc [OPTIONS] <COMMAND>" + hint},
		{[]string{"--bogus"}, "error: unexpected argument '--bogus' found\n\nUsage: forgebase [OPTIONS] <COMMAND>" + hint},
		{[]string{"migrate", "--bogus"}, "error: unexpected argument '--bogus' found\n\nUsage: forgebase migrate [OPTIONS]" + hint},
		{[]string{"doc", "get", "a", "b", "c", "--doctypes", "d"}, "error: unexpected argument 'c' found\n\nUsage: forgebase doc get [OPTIONS] <DOCTYPE> <NAME>" + hint},
		{[]string{"doc", "get", "--doctypes", "d"}, "error: the following required arguments were not provided:\n  <DOCTYPE>\n  <NAME>\n\nUsage: forgebase doc get --doctypes <DIR> <DOCTYPE> <NAME>" + hint},
		{[]string{"doc", "delete", "a", "b", "--doctypes", "d"}, "error: the following required arguments were not provided:\n  --version <N>\n\nUsage: forgebase doc delete --version <N> --doctypes <DIR> <DOCTYPE> <NAME>" + hint},
		{[]string{"doc", "delete", "a", "b", "--version", "abc"}, "error: invalid value 'abc' for '--version <N>': invalid digit found in string" + hint},
		{[]string{"doc", "delete", "a", "b", "--version"}, "error: a value is required for '--version <N>' but none was supplied" + hint},
		{[]string{"doc", "delete", "a", "b", "--version", "-1"}, "error: a value is required for '--version <N>' but none was supplied" + hint},
		{[]string{"worker", "--concurrency", "0"}, "error: invalid value '0' for '--concurrency <N>': 0 is not in 1..=64" + hint},
		{[]string{"worker", "--concurrency", "65"}, "error: invalid value '65' for '--concurrency <N>': 65 is not in 1..=64" + hint},
		{[]string{"worker", "--concurrency", "x"}, "error: invalid value 'x' for '--concurrency <N>': invalid digit found in string" + hint},
		{[]string{"worker", "--concurrency", ""}, "error: invalid value '' for '--concurrency <N>': cannot parse integer from empty string" + hint},
		{[]string{"worker", "--concurrency", "99999999999"}, "error: invalid value '99999999999' for '--concurrency <N>': number too large to fit in target type" + hint},
		{[]string{"job", "list", "--status", "zz"}, "error: invalid value 'zz' for '--status <STATUS>'\n  [possible values: queued, running, done, dead]" + hint},
		{[]string{"migrate", "--dry-run=yes"}, "error: unexpected value 'yes' for '--dry-run' found; no more were expected\n\nUsage: forgebase migrate [OPTIONS]" + hint},
		{[]string{"--site", "a", "--site", "b", "migrate"}, "error: the argument '--site <NAME>' cannot be used multiple times\n\nUsage: forgebase --site <NAME>" + hint},
	} {
		exit := mustExit(t, c.args...)
		want := c.want
		if exit.code != 2 || exit.stdout {
			t.Errorf("%v: code %d stdout %v", c.args, exit.code, exit.stdout)
		}
		if exit.text != want {
			t.Errorf("%v:\n got %q\nwant %q", c.args, exit.text, want)
		}
	}
}

func TestAFlagLikeArgumentGetsTheDashDashTipWhenTheCommandHasPositionals(t *testing.T) {
	exit := mustExit(t, "doc", "amend", "a", "b", "--version", "1", "--doctypes", "d", "--data", "{}")
	want := "error: unexpected argument '--data' found\n\n  tip: to pass '--data' as a value, use '-- --data'\n\nUsage: forgebase doc amend --version <N> --doctypes <DIR> <DOCTYPE> <NAME>\n\nFor more information, try '--help'.\n"
	if exit.code != 2 || exit.text != want {
		t.Fatalf("%q", exit.text)
	}
	// No positionals, no tip.
	if exit := mustExit(t, "worker", "--bogus"); strings.Contains(exit.text, "tip:") {
		t.Fatalf("%q", exit.text)
	}
}

func TestGlobalOptionsAreAcceptedBeforeAndAfterTheSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "c.toml", "--site", "acme", "migrate", "--doctypes", "d"},
		{"migrate", "--doctypes", "d", "--config", "c.toml", "--site", "acme"},
		{"--site=acme", "migrate", "--config=c.toml", "--doctypes=d"},
		{"--config", "c.toml", "migrate", "--site", "acme", "--doctypes", "d"},
	} {
		m := mustParseArgs(t, args...)
		if m.str("config") != "c.toml" || m.str("site") != "acme" || m.str("doctypes") != "d" || strings.Join(m.path, " ") != "migrate" {
			t.Errorf("%v: %v", args, m.vals)
		}
	}
	// Global options on a nested command.
	m := mustParseArgs(t, "doc", "get", "customer", "x", "--site", "acme", "--doctypes", "d")
	if m.str("site") != "acme" || m.str("doctype") != "customer" || m.str("name") != "x" {
		t.Errorf("%v", m.vals)
	}
}

func TestDefaultsRangesAndRepeatedOptions(t *testing.T) {
	m := mustParseArgs(t, "migrate", "--doctypes", "d")
	if m.str("lock_timeout_ms") != "5000" || m.flag("dry_run") {
		t.Errorf("%v %v", m.vals, m.flags)
	}
	if v, ok := m.opt("site"); ok {
		t.Errorf("site has no default: %q", v)
	}
	m = mustParseArgs(t, "migrate", "--doctypes", "d", "--dry-run", "--lock-timeout-ms", "10")
	if m.str("lock_timeout_ms") != "10" || !m.flag("dry_run") {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "worker", "--doctypes", "d")
	if m.str("concurrency") != "1" {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "worker", "--doctypes", "d", "--concurrency", "64")
	if m.str("concurrency") != "64" {
		t.Errorf("%v", m.vals)
	}
	// A Vec option repeats and a value delimiter splits.
	m = mustParseArgs(t, "user", "create", "ann", "--role", "a", "--role", "b")
	if strings.Join(m.list("roles"), ",") != "a,b" {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "webhook", "add", "w", "--doctype", "c", "--events", "insert,update", "--events", "delete", "--url", "https://x")
	if strings.Join(m.list("events"), ",") != "insert,update,delete" {
		t.Errorf("%v", m.vals)
	}
	// The actor defaults to cli.
	m = mustParseArgs(t, "doc", "insert", "customer", "--doctypes", "d", "--data", "{}")
	if m.str("actor") != "cli" {
		t.Errorf("%v", m.vals)
	}
}

func TestAValueMayBeALoneDashOrStartWithADashAfterAnEquals(t *testing.T) {
	m := mustParseArgs(t, "doc", "insert", "customer", "--doctypes", "d", "--data", "-")
	if m.str("data") != "-" {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "doc", "insert", "customer", "--doctypes", "d", "--data={\"a\":1}")
	if m.str("data") != `{"a":1}` {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "doc", "insert", "--doctypes", "d", "--data", "{}", "--", "-weird-name")
	if m.str("doctype") != "-weird-name" {
		t.Errorf("after -- everything is positional: %v", m.vals)
	}
}

func TestStockBinaryRuleNeedsTheDoctypesOption(t *testing.T) {
	for _, c := range []struct {
		args  []string
		usage string
	}{
		{[]string{"migrate"}, "Usage: forgebase migrate [OPTIONS] --doctypes <DIR>"},
		{[]string{"serve"}, "Usage: forgebase serve [OPTIONS] --doctypes <DIR>"},
		{[]string{"doc", "get", "customer", "x"}, "Usage: forgebase doc get [OPTIONS] --doctypes <DIR> <DOCTYPE> <NAME>"},
	} {
		var out, errOut strings.Builder
		code := Run(Options{Version: "1", Args: c.args, Getenv: func(string) string { return "" }, Stdout: &out, Stderr: &errOut})
		if code != 2 || out.Len() != 0 {
			t.Errorf("%v: code %d stdout %q", c.args, code, out.String())
		}
		want := "error: the following required arguments were not provided:\n  --doctypes <DIR>\n\n" + c.usage + "\n\nFor more information, try '--help'.\n"
		if errOut.String() != want {
			t.Errorf("%v:\n%q", c.args, errOut.String())
		}
	}
	// site create --empty (P5) needs no --doctypes: only the commands that need DocTypes do.
	m := mustParseArgs(t, "site", "create", "acme", "--empty")
	if !m.flag("empty") || m.cmd.arg("doctypes") == nil {
		t.Errorf("%v", m.flags)
	}
}

// walk visits every command of the tree.
func walk(c *command, visit func(*command)) {
	visit(c)
	for _, s := range c.subs {
		walk(s, visit)
	}
}

func TestEveryCommandHasHelpInBothFormsAndEveryArgumentIsListed(t *testing.T) {
	root := rootCommand()
	root.link()
	count := 0
	walk(root, func(c *command) {
		count++
		short, long := renderHelp(c, false), renderHelp(c, true)
		for _, text := range []string{short, long} {
			if !strings.Contains(text, "Usage: "+c.fullName()+" [OPTIONS]") || !strings.Contains(text, "-h, --help") {
				t.Errorf("%s: %q", c.fullName(), text)
			}
			if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
				t.Errorf("%s: the help ends with exactly one newline", c.fullName())
			}
			for _, a := range c.args {
				if !strings.Contains(text, a.display()) || !strings.Contains(text, a.help[:min(20, len(a.help))]) {
					t.Errorf("%s: argument %s is missing", c.fullName(), a.display())
				}
			}
			for _, s := range c.subs {
				if !strings.Contains(text, "  "+s.name+" ") {
					t.Errorf("%s: subcommand %s is missing", c.fullName(), s.name)
				}
			}
		}
		if !longHelpExists(c) && short != long {
			t.Errorf("%s: without long help both forms are the same", c.fullName())
		}
		if len(c.subs) > 0 && !strings.Contains(short, "  help  ") {
			t.Errorf("%s: the generated help command is missing", c.fullName())
		}
	})
	if count != 56 {
		t.Errorf("the tree has %d commands", count)
	}
}

func TestTheShortHelpAlignsTheDescriptionsAndShowsDefaults(t *testing.T) {
	root := rootCommand()
	root.link()
	help := renderHelp(root.find("migrate"), false)
	want := "Apply framework migrations, sync the DocType schema (additive changes only) and apply the SQL data migrations of the apps installed on the site (all the binary's apps unless the site lists `apps`). The apps recorded in the database but not installed are reported as a warning; their tables and data are kept. The schedules of apps that are not installed are deleted\n\n" +
		"Usage: forgebase migrate [OPTIONS]\n\nOptions:\n"
	if !strings.HasPrefix(help, want) {
		t.Fatalf("%s", help)
	}
	for _, line := range []string{
		"      --dry-run               Print what would change and keep nothing\n",
		"      --lock-timeout-ms <MS>  Longest wait for a database lock, in milliseconds [default: 5000]\n",
		"  -h, --help                  Print help\n",
	} {
		if !strings.Contains(help, line) {
			t.Errorf("missing %q in\n%s", line, help)
		}
	}
	// The help says when --doctypes is required (a test of the Rust CLI).
	if !strings.Contains(help, "Required when the binary has no apps") {
		t.Error("the help of --doctypes must say when it is required")
	}
	// The position of the generated and the global options.
	job := renderHelp(root.find("job", "list"), false)
	if !strings.Contains(job, "--status <STATUS>  Only jobs with this status [possible values: queued, running, done, dead]\n") ||
		!strings.Contains(job, "--limit <N>        How many jobs, 1 to 1000 [default: 50]\n") {
		t.Errorf("%s", job)
	}
}

func TestTheTopLevelHelpHasTheLongFormWithEachOptionOnTwoLines(t *testing.T) {
	root := rootCommand()
	root.link()
	long := renderHelp(root, true)
	if !strings.HasPrefix(long, "Forgebase command-line tool.\n\nDatabase commands work on one site") {
		t.Errorf("%s", long[:80])
	}
	for _, want := range []string{
		"\nOptions:\n      --config <PATH>\n          The config file",
		"\n\n  -h, --help\n          Print help (see a summary with '-h')\n\n  -V, --version\n          Print version\n",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("missing %q", want)
		}
	}
	short := renderHelp(root, false)
	if !strings.HasPrefix(short, "Forgebase command-line tool\n\nUsage:") || !strings.Contains(short, "  -h, --help           Print help (see more with '--help')\n") {
		t.Errorf("%s", short[:60])
	}
	// The command list stays in the short form in the long help.
	if !strings.Contains(long, "Commands:\n  migrate    Apply framework") {
		t.Errorf("%s", long)
	}
}

func TestTheLongHelpOfACommandPutsDefaultsUnderTheText(t *testing.T) {
	root := rootCommand()
	root.link()
	long := renderHelp(root.find("doc", "list"), true)
	// doc list has a long about; its arguments have no defaults, so check the layout of the text.
	if !strings.Contains(long, "--filter is a JSON array of conditions") || !strings.Contains(long, "  <DOCTYPE>\n          DocType name\n") {
		t.Errorf("%s", long)
	}
	// The doc command only has a long help through its subcommand; its own help is the same in both forms except for the hint.
	doc := renderHelp(root.find("doc"), true)
	if !strings.Contains(doc, "Print help (see a summary with '-h')") {
		t.Errorf("%s", doc)
	}
	// A default in the long form is on its own paragraph.
	data := renderHelp(root.find("data", "export"), true)
	if !strings.Contains(data, "The file format\n\n          [default: csv]\n          [possible values: csv, json]") {
		// clap puts all the specs in one paragraph, one per line
		t.Logf("%s", data)
	}
}

func TestIntegerErrorsReadLikeRusts(t *testing.T) {
	for _, c := range []struct{ args []string }{
		{[]string{"doc", "delete", "a", "b", "--version", "9223372036854775808"}},
	} {
		exit := mustExit(t, c.args...)
		if !strings.Contains(exit.text, "number too large to fit in target type") {
			t.Errorf("%q", exit.text)
		}
	}
	m := mustParseArgs(t, "doc", "delete", "a", "b", "--version", "9223372036854775807", "--doctypes", "d")
	if m.i64("expected_version") != 9223372036854775807 {
		t.Errorf("%v", m.vals)
	}
	exit := mustExit(t, "doc", "delete", "a", "b", "--version", "-9", "--doctypes", "d")
	if !strings.Contains(exit.text, "a value is required for '--version <N>'") {
		t.Errorf("%q", exit.text)
	}
}

func TestNotYetPortedCommandsParseCompletelyThenFailWithThePhase(t *testing.T) {
	for _, c := range []struct {
		args  []string
		phase string
		name  string
	}{
		{[]string{"serve", "--doctypes", "d"}, "P3", "serve"},
		{[]string{"doc", "list", "customer", "--doctypes", "d"}, "P3", "doc list"},
		{[]string{"user", "create", "ann"}, "P3", "user create"},
		{[]string{"token", "revoke", "k"}, "P3", "token revoke"},
		{[]string{"new-app", "crm", "--forgebase-path", "/x"}, "P4", "new-app"},
		{[]string{"worker", "--doctypes", "d"}, "P5", "worker"},
		{[]string{"job", "list"}, "P5", "job list"},
		{[]string{"site", "list"}, "P5", "site list"},
		{[]string{"doc", "transition", "c", "x", "Approve", "--version", "1", "--doctypes", "d"}, "P6", "doc transition"},
		{[]string{"data", "export", "customer", "--doctypes", "d"}, "P7", "data export"},
		{[]string{"webhook", "list"}, "P7", "webhook list"},
		{[]string{"customize", "field", "list"}, "P7", "customize field list"},
		{[]string{"user", "permission", "list"}, "P7", "user permission list"},
	} {
		var out, errOut strings.Builder
		code := Run(Options{Version: "1", Args: c.args, Getenv: func(string) string { return "" }, Stdout: &out, Stderr: &errOut})
		want := "error: " + c.name + " is not available yet in the Go port (arrives in phase " + c.phase + ")\n"
		if code != 1 || errOut.String() != want || out.Len() != 0 {
			t.Errorf("%v: code %d stderr %q", c.args, code, errOut.String())
		}
	}
	// A mistake in their arguments is still a usage error with the right usage.
	var errOut strings.Builder
	code := Run(Options{Version: "1", Args: []string{"worker", "--concurrency", "0", "--doctypes", "d"}, Getenv: func(string) string { return "" }, Stdout: &strings.Builder{}, Stderr: &errOut})
	if code != 2 {
		t.Errorf("code %d: %s", code, errOut.String())
	}
}
