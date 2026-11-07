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
		// A token that looks like a flag is parsed as an argument before it can be the value (parser.rs).
		{[]string{"doc", "delete", "a", "b", "--version", "-1"}, "error: unexpected argument '-1' found\n\n  tip: to pass '-1' as a value, use '-- -1'\n\nUsage: forgebase doc delete [OPTIONS] --version <N> <DOCTYPE> <NAME>" + hint},
		{[]string{"doc", "delete", "a", "b", "--version", "--bogus"}, "error: unexpected argument '--bogus' found\n\n  tip: to pass '--bogus' as a value, use '-- --bogus'\n\nUsage: forgebase doc delete --version <N> <DOCTYPE> <NAME>" + hint},
		{[]string{"worker", "--concurrency", "0"}, "error: invalid value '0' for '--concurrency <N>': 0 is not in 1..=64" + hint},
		{[]string{"worker", "--concurrency", "65"}, "error: invalid value '65' for '--concurrency <N>': 65 is not in 1..=64" + hint},
		{[]string{"worker", "--concurrency", "x"}, "error: invalid value 'x' for '--concurrency <N>': invalid digit found in string" + hint},
		{[]string{"worker", "--concurrency", ""}, "error: invalid value '' for '--concurrency <N>': cannot parse integer from empty string" + hint},
		// u32 values are read as i64 and then range checked (value_parser.rs:1400-1455).
		{[]string{"worker", "--concurrency", "99999999999"}, "error: invalid value '99999999999' for '--concurrency <N>': 99999999999 is not in 1..=64" + hint},
		{[]string{"worker", "--concurrency=-5"}, "error: invalid value '-5' for '--concurrency <N>': -5 is not in 1..=64" + hint},
		{[]string{"doc", "list", "c", "--limit=99999999999"}, "error: invalid value '99999999999' for '--limit <N>': 99999999999 is not in 0..=4294967295" + hint},
		{[]string{"doc", "list", "c", "--limit=9223372036854775808"}, "error: invalid value '9223372036854775808' for '--limit <N>': number too large to fit in target type" + hint},
		{[]string{"doc", "list", "c", "--offset=-1"}, "error: invalid value '-1' for '--offset <N>': invalid digit found in string" + hint},
		{[]string{"job", "list", "--status", "zz"}, "error: invalid value 'zz' for '--status <STATUS>'\n  [possible values: queued, running, done, dead]" + hint},
		{[]string{"migrate", "--dry-run=yes"}, "error: unexpected value 'yes' for '--dry-run' found; no more were expected\n\nUsage: forgebase migrate --dry-run" + hint},
		{[]string{"--site", "a", "--site", "b", "migrate"}, "error: the argument '--site <NAME>' cannot be used multiple times\n\nUsage: forgebase [OPTIONS] <COMMAND>" + hint},
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
	// site create --empty needs no --doctypes: only the commands that need DocTypes do.
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
	// long_help_exists is a property of the command alone (command.rs:5157-5180): doc has no long text of
	// its own, so `doc --help` is the short form, with "Print help", although doc list has a long one.
	for _, c := range [][]string{{"doc"}, {"data"}} {
		help := renderHelp(root.find(c...), true)
		if help != renderHelp(root.find(c...), false) || !strings.Contains(help, "  -h, --help ") || strings.Contains(help, "see more with") {
			t.Errorf("%v: %s", c, help)
		}
	}
	// The default and the possible values are in the lines under the description, after a blank line that
	// holds the indent (help_template.rs:630, styled_str.rs:69-75).
	data := renderHelp(root.find("data", "export"), true)
	want := "      --format <FORMAT>\n          The file format\n          \n          [default: csv]\n          [possible values: csv, json]\n\n"
	if !strings.Contains(data, want) {
		t.Errorf("%s", data)
	}
	// In the short form they follow the text on the same line.
	short := renderHelp(root.find("data", "export"), false)
	if !strings.Contains(short, "The file format [default: csv] [possible values: csv, json]\n") {
		t.Errorf("%s", short)
	}
}

// The options are in clap's order (help_template.rs option_sort_key): by display order, which counts a
// command's own non-positional arguments from 0 and leaves the global options at the root's 0 and 1, then
// by name; -h and -V last.
func TestOptionsAreListedInClapsOrder(t *testing.T) {
	root := rootCommand()
	root.link()
	options := func(c *command) []string {
		var names []string
		for _, e := range optionEntries(c, false) {
			names = append(names, strings.TrimSpace(strings.Split(strings.TrimSpace(e.left), " <")[0]))
		}
		return names
	}
	for _, c := range []struct {
		path []string
		want string
	}{
		{[]string{"doc", "update"}, "--config --version --doctypes --site --data --actor -h, --help"},
		{[]string{"migrate"}, "--config --doctypes --dry-run --site --lock-timeout-ms -h, --help"},
		{[]string{"doc", "get"}, "--config --doctypes --site -h, --help"},
		{nil, "--config --site -h, --help -V, --version"},
		{[]string{"worker"}, "--config --doctypes --concurrency --site -h, --help"},
	} {
		cmd := root.find(c.path...)
		if got := strings.Join(options(cmd), " "); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.path, got, c.want)
		}
	}
}

// `help [COMMAND]...` ends in the long help of the command (parse_help_subcommand), and every argument
// after help is a subcommand name.
func TestTheHelpSubcommand(t *testing.T) {
	exit := mustExit(t, "help")
	if exit.code != 0 || !exit.stdout || !strings.Contains(exit.text, "Print help (see a summary with '-h')") {
		t.Errorf("%+v", exit)
	}
	exit = mustExit(t, "help", "doc", "list")
	if !strings.Contains(exit.text, "--filter is a JSON array") || !strings.Contains(exit.text, "  <DOCTYPE>\n          DocType name") {
		t.Errorf("%s", exit.text)
	}
	exit = mustExit(t, "doc", "help", "get")
	if !strings.HasPrefix(exit.text, "Print a document\n\nUsage: forgebase doc get [OPTIONS] <DOCTYPE> <NAME>") {
		t.Errorf("%s", exit.text)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"help", "bogus"}, "error: unrecognized subcommand 'bogus'\n\nUsage: forgebase [OPTIONS] <COMMAND>"},
		{[]string{"help", "doc", "--x"}, "error: unrecognized subcommand '--x'\n\nUsage: forgebase doc [OPTIONS] <COMMAND>"},
		{[]string{"help", "-h"}, "error: unrecognized subcommand '-h'\n\nUsage: forgebase [OPTIONS] <COMMAND>"},
		{[]string{"help", "migrate", "doc"}, "error: unrecognized subcommand 'doc'\n\nUsage: forgebase migrate [OPTIONS]"},
	} {
		exit := mustExit(t, c.args...)
		if exit.code != 2 || !strings.HasPrefix(exit.text, c.want) {
			t.Errorf("%v: %q", c.args, exit.text)
		}
	}
}

// The usage line of an error (usage.rs write_usage_no_title, validator.rs missing_required_error,
// parser.rs did_you_mean_error and react): shortened as soon as an argument was used, with no [OPTIONS],
// the required arguments first.
func TestTheUsageLineOfAnError(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"doc", "insert", "customer"}, "Usage: forgebase doc insert --data <JSON|-> <DOCTYPE>"},
		{[]string{"doc", "get"}, "Usage: forgebase doc get <DOCTYPE> <NAME>"},
		{[]string{"doc", "get", "--doctypes", "d"}, "Usage: forgebase doc get --doctypes <DIR> <DOCTYPE> <NAME>"},
		{[]string{"doc", "get", "--site", "x", "c"}, "Usage: forgebase doc get --site <NAME> <DOCTYPE> <NAME>"},
		{[]string{"doc", "insert", "--data", "{}", "--bogus"}, "Usage: forgebase doc insert --data <JSON|-> <DOCTYPE>"},
		{[]string{"doc", "get", "--bogus"}, "Usage: forgebase doc get [OPTIONS] <DOCTYPE> <NAME>"},
		{[]string{"doc", "get", "x", "--bogus"}, "Usage: forgebase doc get <DOCTYPE> <NAME>"},
		{[]string{"--site", "x", "--bogus"}, "Usage: forgebase --site <NAME> <COMMAND>"},
		{[]string{"--site", "a", "--site", "b", "migrate"}, "Usage: forgebase [OPTIONS] <COMMAND>"},
		{[]string{"migrate", "--dry-run", "-x"}, "Usage: forgebase migrate [OPTIONS]"},
		{[]string{"migrate", "--dry-run", "extra"}, "Usage: forgebase migrate [OPTIONS]"},
		{[]string{"--site", "x", "bogus"}, "Usage: forgebase [OPTIONS] <COMMAND>"},
		{[]string{"--site", "x"}, "Usage: forgebase [OPTIONS] <COMMAND>"},
		{[]string{"--help=x"}, "Usage: forgebase --help <COMMAND>"},
		{[]string{"--version=x"}, "Usage: forgebase --version <COMMAND>"},
		{[]string{"migrate", "--dry-run", "--help=x"}, "Usage: forgebase migrate --dry-run --help"},
	} {
		exit := mustExit(t, c.args...)
		if exit.code != 2 || !strings.Contains(exit.text, "\n\n"+c.want+"\n\nFor more information") {
			t.Errorf("%v: %q\nwant usage %q", c.args, exit.text, c.want)
		}
	}
	exit := mustExit(t, "--help=x")
	if !strings.HasPrefix(exit.text, "error: unexpected value 'x' for '--help' found; no more were expected\n\n") {
		t.Errorf("%q", exit.text)
	}
}

func TestEmptyPathValuesAreUsageErrors(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"migrate", "--doctypes", ""}, "error: a value is required for '--doctypes <DIR>' but none was supplied" + hint},
		{[]string{"migrate", "--doctypes="}, "error: a value is required for '--doctypes <DIR>' but none was supplied" + hint},
		{[]string{"--config", "", "migrate", "--doctypes", "d"}, "error: a value is required for '--config <PATH>' but none was supplied" + hint},
		{[]string{"site", "backup", "a", "--out", ""}, "error: a value is required for '--out <FILE>' but none was supplied" + hint},
		{[]string{"site", "restore", "a", "--from", ""}, "error: a value is required for '--from <FILE>' but none was supplied" + hint},
		{[]string{"new-app", "x", "--forgebase-path", ""}, "error: a value is required for '--forgebase-path <DIR>' but none was supplied" + hint},
		{[]string{"new-app", "x", "--forgebase-path", "/f", "--dir", ""}, "error: a value is required for '--dir <DIR>' but none was supplied" + hint},
		{[]string{"data", "import", "c", "", "--format", "csv", "--doctypes", "d"}, "error: a value is required for '<FILE>' but none was supplied" + hint},
		{[]string{"job", "list", "--status", ""}, "error: a value is required for '--status <STATUS>' but none was supplied\n  [possible values: queued, running, done, dead]" + hint},
	} {
		exit := mustExit(t, c.args...)
		if exit.code != 2 || exit.text != c.want {
			t.Errorf("%v:\n got %q\nwant %q", c.args, exit.text, c.want)
		}
	}
	// An empty String is a value (the actor).
	if m := mustParseArgs(t, "doc", "delete", "a", "b", "--version", "1", "--doctypes", "d", "--actor", ""); m.str("actor") != "" {
		t.Errorf("%v", m.vals)
	}
}

// A global option may be given on two levels: one value per level, and the deepest wins (arg_matcher.rs).
func TestAGlobalOptionOnTwoLevelsTheDeepestWins(t *testing.T) {
	m := mustParseArgs(t, "--site", "a", "migrate", "--site", "b", "--doctypes", "d")
	if m.str("site") != "b" {
		t.Errorf("%v", m.vals)
	}
	m = mustParseArgs(t, "--config", "x", "doc", "get", "c", "n", "--config", "y", "--doctypes", "d")
	if m.str("config") != "y" {
		t.Errorf("%v", m.vals)
	}
	// ... but twice on one level is an error.
	exit := mustExit(t, "migrate", "--site", "a", "--site", "b", "--doctypes", "d")
	if !strings.Contains(exit.text, "the argument '--site <NAME>' cannot be used multiple times") {
		t.Errorf("%q", exit.text)
	}
}

func TestDoubleDashAndTheNits(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	// `-- migrate`: the subcommand exists; the tip says to remove the dashes.
	exit := mustExit(t, "--", "migrate")
	want := "error: unexpected argument 'migrate' found\n\n  tip: subcommand 'migrate' exists; to use it, remove the '--' before it\n\nUsage: forgebase [OPTIONS] <COMMAND>" + hint
	if exit.code != 2 || exit.text != want {
		t.Errorf("%q", exit.text)
	}
	exit = mustExit(t, "--", "bogus")
	if exit.text != "error: unrecognized subcommand 'bogus'\n\nUsage: forgebase [OPTIONS] <COMMAND>"+hint {
		t.Errorf("%q", exit.text)
	}
	// `--` alone is no argument: the help on standard error.
	exit = mustExit(t, "--")
	if exit.code != 2 || exit.stdout || !strings.HasPrefix(exit.text, "Forgebase command-line tool") {
		t.Errorf("%+v", exit)
	}
	exit = mustExit(t, "doc", "--")
	if exit.code != 2 || exit.stdout || !strings.Contains(exit.text, "Usage: forgebase doc [OPTIONS] <COMMAND>") {
		t.Errorf("%+v", exit)
	}
	// A value after -- is a positional, even with a flag-like name; the pending option is then empty.
	exit = mustExit(t, "doc", "get", "c", "--doctypes", "--", "n")
	if exit.text != "error: a value is required for '--doctypes <DIR>' but none was supplied"+hint {
		t.Errorf("%q", exit.text)
	}
	// An option that waits for a value and meets --help or the end.
	exit = mustExit(t, "doc", "get", "c", "n", "--doctypes", "--help")
	if exit.code != 2 || !strings.Contains(exit.text, "a value is required for '--doctypes <DIR>'") {
		t.Errorf("%q", exit.text)
	}
	// An invalid value is reported when the next argument arrives, before a later unknown flag.
	exit = mustExit(t, "doc", "delete", "a", "b", "--version", "abc", "--help")
	if !strings.Contains(exit.text, "invalid value 'abc' for '--version <N>'") {
		t.Errorf("%q", exit.text)
	}
	// ... but an unknown flag wins over the pending value's error (parser.rs discards it).
	exit = mustExit(t, "doc", "delete", "a", "b", "--version", "abc", "--bogus")
	if !strings.Contains(exit.text, "unexpected argument '--bogus' found") {
		t.Errorf("%q", exit.text)
	}
	// The unknown long flag is printed without its attached value.
	exit = mustExit(t, "worker", "--bogus=1")
	if !strings.HasPrefix(exit.text, "error: unexpected argument '--bogus' found\n") {
		t.Errorf("%q", exit.text)
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
	if !strings.Contains(exit.text, "unexpected argument '-9' found") {
		t.Errorf("%q", exit.text)
	}
	if m := mustParseArgs(t, "doc", "delete", "a", "b", "--version=-9", "--doctypes", "d"); m.i64("expected_version") != -9 {
		t.Errorf("%v", m.vals)
	}
	// "+5" is a number to Rust.
	if m := mustParseArgs(t, "worker", "--doctypes", "d", "--concurrency=+5"); m.str("concurrency") != "+5" {
		t.Errorf("%v", m.vals)
	}
	if m := mustParseArgs(t, "migrate", "--doctypes", "d", "--lock-timeout-ms=+5"); m.str("lock_timeout_ms") != "+5" {
		t.Errorf("%v", m.vals)
	}
}

func TestNotYetPortedCommandsParseCompletelyThenFailWithThePhase(t *testing.T) {
	for _, c := range []struct {
		args  []string
		phase string
		name  string
	}{
		{[]string{"data", "export", "customer", "--doctypes", "d"}, "P7", "data export"},
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

// The conflict is judged before the value is parsed (parser.rs:1172-1193).
func TestAnOptionGivenTwiceIsRefusedBeforeItsValueIsRead(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"doc", "list", "T", "--limit", "5", "--limit", "abc"}, "error: the argument '--limit <N>' cannot be used multiple times\n\nUsage: forgebase doc list [OPTIONS] <DOCTYPE>" + hint},
		{[]string{"migrate", "--doctypes", "a", "--doctypes", ""}, "error: the argument '--doctypes <DIR>' cannot be used multiple times\n\nUsage: forgebase migrate [OPTIONS]" + hint},
		{[]string{"migrate", "--lock-timeout-ms=1", "--lock-timeout-ms=x"}, "error: the argument '--lock-timeout-ms <MS>' cannot be used multiple times\n\nUsage: forgebase migrate [OPTIONS]" + hint},
	} {
		if exit := mustExit(t, c.args...); exit.text != c.want {
			t.Errorf("%v:\n got %q\nwant %q", c.args, exit.text, c.want)
		}
	}
}

// An argument whose value was refused still counts as used (start_custom_arg precedes push_arg_values),
// and the groups of the flattened Args structs are in the usage of a `--flag=value` error
// (parser.rs:831-850, usage.rs write_args, command.rs format_group).
func TestTheUsedListOfAnErrorHoldsRefusedValuesAndGroups(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"migrate", "--lock-timeout-ms", "abc", "--bogus"}, "Usage: forgebase migrate --lock-timeout-ms <MS>"},
		{[]string{"migrate", "--doctypes=dt", "--dry-run=true"}, "Usage: forgebase migrate --dry-run <--doctypes <DIR>>"},
		{[]string{"migrate", "--dry-run=true"}, "Usage: forgebase migrate --dry-run"},
		{[]string{"doc", "insert", "c", "--actor", "a", "--data", "{}", "--bogus"}, "Usage: forgebase doc insert --data <JSON|-> --actor <NAME> <DOCTYPE>"},
		{[]string{"--site", "x", "--config", "c", "--version=y"}, "Usage: forgebase --version <--config <PATH>|--site <NAME>> <COMMAND>"},
	} {
		exit := mustExit(t, c.args...)
		if !strings.Contains(exit.text, "\n\n"+c.want+"\n\n") {
			t.Errorf("%v: %q\nwant usage %q", c.args, exit.text, c.want)
		}
	}
}

// The generated help command (command.rs:4850-4870) has no options and one optional argument.
func TestHelpHelp(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	want := "Print this message or the help of the given subcommand(s)\n\nUsage: forgebase help [COMMAND]...\n\nArguments:\n  [COMMAND]...  Print help for the subcommand(s)\n"
	for _, args := range [][]string{{"help", "help"}} {
		if exit := mustExit(t, args...); exit.code != 0 || !exit.stdout || exit.text != want {
			t.Errorf("%v: %q", args, exit.text)
		}
	}
	nested := strings.ReplaceAll(want, "forgebase help", "forgebase doc help")
	for _, args := range [][]string{{"help", "doc", "help"}, {"doc", "help", "help"}} {
		if exit := mustExit(t, args...); exit.code != 0 || exit.text != nested {
			t.Errorf("%v: %q", args, exit.text)
		}
	}
	exit := mustExit(t, "help", "help", "doc")
	if exit.code != 2 || exit.text != "error: unrecognized subcommand 'doc'\n\nUsage: forgebase help [COMMAND]..."+hint {
		t.Errorf("%q", exit.text)
	}
	// A leaf command has no help command.
	exit = mustExit(t, "migrate", "help")
	if !strings.HasPrefix(exit.text, "error: unexpected argument 'help' found") {
		t.Errorf("%q", exit.text)
	}
}

// Values other than paths must be UTF-8; a path takes any bytes (value_parser.rs).
func TestInvalidUTF8Arguments(t *testing.T) {
	const hint = "\n\nFor more information, try '--help'.\n"
	bad := "a\xffb"
	for _, args := range [][]string{
		{"doc", "insert", "c", "--data", bad},
		{"doc", "insert", "c", "--data=" + bad},
		{"doc", "get", bad, "n"},
		{"worker", "--concurrency", bad},
		{"job", "list", "--status", bad},
		{"--site", bad, "migrate"},
	} {
		exit := mustExit(t, args...)
		if exit.code != 2 || !strings.HasPrefix(exit.text, "error: invalid UTF-8 was detected in one or more arguments\n\nUsage: forgebase") || !strings.HasSuffix(exit.text, hint) {
			t.Errorf("%v: %q", args, exit.text)
		}
	}
	if m := mustParseArgs(t, "migrate", "--doctypes", bad); m.str("doctypes") != bad {
		t.Errorf("a path takes any bytes: %q", m.str("doctypes"))
	}
	// The name in an error is shown lossily.
	exit := mustExit(t, "migrate", "--bo"+bad)
	if !strings.HasPrefix(exit.text, "error: unexpected argument '--boa\uFFFDb' found") {
		t.Errorf("%q", exit.text)
	}
}

// lossy is String::from_utf8_lossy: one U+FFFD per maximal invalid subpart.
func TestLossyReplacesMaximalInvalidSubparts(t *testing.T) {
	for in, want := range map[string]string{
		"ok":               "ok",
		"a\xffb":           "a\uFFFDb",
		"\xe2\x82":         "\uFFFD",
		"\xe2\x82x":        "\uFFFDx",
		"\xe2\x82\xac":     "\u20ac",
		"\xf0\x9f\x98":     "\uFFFD",
		"\xf0\x9fx":        "\uFFFDx",
		"\xc0\xaf":         "\uFFFD\uFFFD",
		"\xed\xa0\x80":     "\uFFFD\uFFFD\uFFFD",
		"\xe0\x80\x80":     "\uFFFD\uFFFD\uFFFD",
		"\xf4\x90\x80\x80": "\uFFFD\uFFFD\uFFFD\uFFFD",
		"\x80\x80":         "\uFFFD\uFFFD",
		"\xe2\xe2\x82\xac": "\uFFFD\u20ac",
	} {
		if got := lossy(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	// The value of --flag=value is shown lossily too.
	exit := mustExit(t, "migrate", "--dry-run=a\xe2\x82")
	if !strings.Contains(exit.text, "unexpected value 'a\uFFFD' for '--dry-run' found") {
		t.Errorf("%q", exit.text)
	}
}
