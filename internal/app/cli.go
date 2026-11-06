package app

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The command-line parser. Forgebase has no command-line framework (ADR 0026, G2); the Rust
// implementation uses clap 4 (derive, without the wrap_help, color and suggestions features), and the
// goal here is the same observable behaviour: the same commands, flags, defaults and ranges, the same
// `--help` layout and the same usage errors with exit code 2.
//
// The command tree is data (cli_commands.go, from the Rust args.rs); this file is the parser and
// the error texts, cli_help.go the help and usage rendering. The differences to clap that are known
// are listed in docs/guide/cli.md ("Parity with the Rust command line") and in the CHANGELOG.

type valueKind int

const (
	kindString valueKind = iota
	kindU32
	kindU64
	kindI64
	// kindPath is a string that may not be empty (clap's PathBufValueParser).
	kindPath
)

// argSpec is one argument of a command: a flag, an option with a value or a positional.
type argSpec struct {
	// id is the key the commands read the value with (the field name in the Rust structs).
	id string
	// long is the name after `--`; empty for a positional.
	long string
	// valueName is the placeholder in the usage and help (`<N>`); for a positional, its name.
	valueName  string
	help       string
	longHelp   string
	flag       bool
	positional bool
	required   bool
	// multiple is a Vec: the option may repeat and its values are collected.
	multiple   bool
	delimiter  string
	def        string
	hasDefault bool
	possible   []string
	kind       valueKind
	min, max   int64
	ranged     bool
}

// command is one node of the command tree.
type command struct {
	name      string
	about     string
	longAbout string
	args      []argSpec
	subs      []*command
	parent    *command
}

// link sets the parent of every command below c.
func (c *command) link() {
	for _, s := range c.subs {
		s.parent = c
		s.link()
	}
}

// fullName is the program name and the subcommand names, as in `forgebase doc get`.
func (c *command) fullName() string {
	if c.parent == nil {
		return c.name
	}
	return c.parent.fullName() + " " + c.name
}

func (c *command) isRoot() bool { return c.parent == nil }

func (c *command) positionals() []*argSpec {
	var out []*argSpec
	for i := range c.args {
		if c.args[i].positional {
			out = append(out, &c.args[i])
		}
	}
	return out
}

func (c *command) arg(id string) *argSpec {
	for i := range c.args {
		if c.args[i].id == id {
			return &c.args[i]
		}
	}
	return nil
}

// globals are the options of the ancestors that every subcommand accepts (`--config`, `--site`:
// clap's `global = true`). The root of this tree has no other options but help and version.
func (c *command) globals() []*argSpec {
	if c.parent == nil {
		return nil
	}
	root := c
	for root.parent != nil {
		root = root.parent
	}
	var out []*argSpec
	for i := range root.args {
		if !root.args[i].positional {
			out = append(out, &root.args[i])
		}
	}
	return out
}

func (c *command) sub(name string) *command {
	for _, s := range c.subs {
		if s.name == name {
			return s
		}
	}
	return nil
}

// find follows a list of subcommand names from c.
func (c *command) find(path ...string) *command {
	cur := c
	for _, name := range path {
		if cur = cur.sub(name); cur == nil {
			return nil
		}
	}
	return cur
}

// display is the argument as clap names it in an error: `--data <JSON|->`, `--dry-run`, `<NAME>`.
func (a *argSpec) display() string {
	switch {
	case a.positional:
		return "<" + a.valueName + ">"
	case a.flag:
		return "--" + a.long
	}
	return "--" + a.long + " <" + a.valueName + ">"
}

// matches is the result of a successful parse.
type matches struct {
	// cmd is the command that was selected, path the subcommand names that lead to it.
	cmd  *command
	path []string
	vals map[string][]string
	// flags are the flags that were given.
	flags map[string]bool
}

func (m *matches) flag(id string) bool { return m.flags[id] }

// str is the value of an argument, or "" when it has none.
func (m *matches) str(id string) string {
	if v := m.vals[id]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// opt is the value of an option, and whether it was given (or has a default).
func (m *matches) opt(id string) (string, bool) {
	if v := m.vals[id]; len(v) > 0 {
		return v[0], true
	}
	return "", false
}

func (m *matches) list(id string) []string { return m.vals[id] }

// i64 is the value of an integer argument; the parser checked it, so a failure is a program defect.
func (m *matches) i64(id string) int64 {
	n, err := strconv.ParseInt(m.str(id), 10, 64)
	if err != nil {
		panic("argument " + id + " is not an integer: " + err.Error())
	}
	return n
}

func (m *matches) u64(id string) uint64 {
	n, err := strconv.ParseUint(m.str(id), 10, 64)
	if err != nil {
		panic("argument " + id + " is not an unsigned integer: " + err.Error())
	}
	return n
}

// exitRequest is what the parser asks the program to do instead of running a command: print text
// and exit. Help and version go to standard output with code 0, errors to standard error with code 2.
type exitRequest struct {
	text   string
	stdout bool
	code   int
}

func (e *exitRequest) Error() string { return strings.TrimSuffix(e.text, "\n") }

// usageErr is clap's error format: the message, the usage when there is one, and the hint.
func usageErr(message, usage string) *exitRequest {
	text := "error: " + message
	if usage != "" {
		text += "\n\n" + usage
	}
	text += "\n\nFor more information, try '--help'.\n"
	return &exitRequest{text: text, code: 2}
}

// The arguments clap adds to every command: -h/--help, and -V/--version on the root.
var (
	helpSpec    = argSpec{id: "help", long: "help", flag: true}
	versionSpec = argSpec{id: "version", long: "version", flag: true}
)

// lookupSpec finds an argument of c by id: its own, a global one, or help and version.
func lookupSpec(c *command, id string) *argSpec {
	if s := c.arg(id); s != nil {
		return s
	}
	for _, g := range c.globals() {
		if g.id == id {
			return g
		}
	}
	switch {
	case id == helpSpec.id:
		return &helpSpec
	case id == versionSpec.id && c.isRoot():
		return &versionSpec
	}
	return nil
}

// lookupLong finds a long option of the command, a global one, or --help (and --version on the root).
func lookupLong(c *command, name string) *argSpec {
	for i := range c.args {
		if !c.args[i].positional && c.args[i].long == name {
			return &c.args[i]
		}
	}
	for _, g := range c.globals() {
		if g.long == name {
			return g
		}
	}
	switch {
	case name == "help":
		return &helpSpec
	case name == "version" && c.isRoot():
		return &versionSpec
	}
	return nil
}

// A parse level is one command on the way to the selected one, with what was given to it. clap keeps a
// matcher per level and merges the global options at the end of the parse (arg_matcher.rs:53-91), so
// "given twice" and "nothing given" are judged per level, and the deepest value of a global option wins.
type level struct {
	cmd *command
	// explicit are the ids of the arguments given on this level, in the order they were first seen
	// (resolved).
	explicit []string
	// pending is the argument whose values are not resolved yet (clap's pending): an option waits for
	// its value, and an argument is only checked when the next one comes (parser.rs resolve_pending).
	pending *pendingValue
	// needsValue is clap's ParseState::Opt: the next argument is the value of the pending option.
	needsValue bool
	// next is the index of the next positional.
	next int
	// vals are the values given on this level, by argument id.
	vals map[string][]string
}

type pendingValue struct {
	spec *argSpec
	vals []string
}

func newLevel(c *command) *level { return &level{cmd: c, vals: map[string][]string{}} }

func (l *level) has(id string) bool {
	for _, e := range l.explicit {
		if e == id {
			return true
		}
	}
	return false
}

// usageUsed is the usage of an error that lists the arguments that were used (clap's
// create_usage_with_title(&used)): the full usage when there are none.
func usageUsed(c *command, used []string) string {
	if len(used) == 0 {
		return "Usage: " + helpUsage(c)
	}
	return "Usage: " + smartUsage(c, used)
}

// plainUsage is create_usage_with_title(&[]).
func plainUsage(c *command) string { return "Usage: " + helpUsage(c) }

// resolve is clap's resolve_pending: the argument that waited for its values gets them, checked.
func (p *parser) resolve(l *level) *exitRequest {
	pending := l.pending
	if pending == nil {
		return nil
	}
	l.pending = nil
	return p.react(l, pending.spec, pending.vals)
}

// react stores the values of an option or positional (parser.rs react, verify_num_args and the value
// parsers).
func (p *parser) react(l *level, spec *argSpec, vals []string) *exitRequest {
	if len(vals) == 0 {
		return emptyValue(spec)
	}
	if spec.delimiter != "" {
		var split []string
		for _, v := range vals {
			split = append(split, strings.Split(v, spec.delimiter)...)
		}
		vals = split
	}
	for _, v := range vals {
		if exit := checkValue(spec, v); exit != nil {
			return exit
		}
	}
	if !spec.multiple && l.has(spec.id) {
		return p.conflict(l, spec)
	}
	if spec.multiple {
		l.vals[spec.id] = append(l.vals[spec.id], vals...)
	} else {
		l.vals[spec.id] = vals
	}
	if !l.has(spec.id) {
		l.explicit = append(l.explicit, spec.id)
	}
	return nil
}

func (p *parser) conflict(l *level, spec *argSpec) *exitRequest {
	return usageErr(fmt.Sprintf("the argument '%s' cannot be used multiple times", spec.display()), plainUsage(l.cmd))
}

type parser struct {
	m       *matches
	version string
}

// possibleSub is the subcommand an argument names; "help" names the generated help command.
func possibleSub(c *command, name string) string {
	if len(c.subs) == 0 {
		return ""
	}
	if name == "help" {
		return name
	}
	if c.sub(name) != nil {
		return name
	}
	return ""
}

// parse reads the command line (without the program name) against the tree. It follows the main loop of
// clap's parser.rs: in order, so help and version end the parse where they are met and a mistake before
// them is reported first; the values of options are checked when the next argument arrives; the required
// arguments are checked at the end, deepest command first.
func parse(root *command, version string, args []string) (*matches, *exitRequest) {
	root.link()
	m := &matches{cmd: root, vals: map[string][]string{}, flags: map[string]bool{}}
	p := &parser{m: m, version: version}
	levels := []*level{newLevel(root)}
	cur := levels[0]
	afterDashes := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !afterDashes {
			// A subcommand name, unless an option waits for its value.
			if !cur.needsValue {
				if name := possibleSub(cur.cmd, a); name != "" {
					if name == "help" {
						return nil, helpSubcommand(cur.cmd, args[i+1:])
					}
					cur = newLevel(cur.cmd.sub(name))
					levels = append(levels, cur)
					m.path = append(m.path, name)
					continue
				}
			}
			switch {
			case a == "--":
				afterDashes = true
				continue
			case strings.HasPrefix(a, "--"):
				if exit := p.long(cur, a); exit != nil {
					return nil, exit
				}
				continue
			case len(a) > 1 && a[0] == '-':
				// A short flag always ends the parse (-h, -V) or is refused.
				return nil, p.short(cur, a)
			}
			if cur.needsValue {
				cur.pending.vals = append(cur.pending.vals, a)
				cur.needsValue = false
				continue
			}
		}
		// A positional argument.
		pos := cur.cmd.positionals()
		if cur.next < len(pos) {
			if exit := p.resolve(cur); exit != nil {
				return nil, exit
			}
			cur.pending = &pendingValue{spec: pos[cur.next], vals: []string{a}}
			cur.next++
			continue
		}
		_ = p.resolve(cur)
		return nil, matchArgError(cur, a, afterDashes)
	}

	m.cmd = cur.cmd
	for idx := len(levels) - 1; idx >= 0; idx-- {
		l := levels[idx]
		if exit := p.resolve(l); exit != nil {
			return nil, exit
		}
		if exit := validateLevel(p, l, idx == len(levels)-1); exit != nil {
			return nil, exit
		}
	}
	// The levels are merged from the root down: for a global option given on several levels the deepest
	// value wins (arg_matcher.rs:53-91).
	for _, l := range levels {
		for id, vals := range l.vals {
			m.vals[id] = vals
		}
	}
	for _, l := range levels {
		for i := range l.cmd.args {
			spec := &l.cmd.args[i]
			if spec.hasDefault && len(m.vals[spec.id]) == 0 {
				m.vals[spec.id] = []string{spec.def}
			}
		}
	}
	for _, l := range levels {
		for _, id := range l.explicit {
			if spec := lookupSpec(l.cmd, id); spec != nil && spec.flag {
				m.flags[id] = true
			}
		}
	}
	return m, nil
}

// long handles an argument that starts with "--" (parse_long_arg); nil means it was taken.
func (p *parser) long(l *level, a string) *exitRequest {
	name, value, hasEq := strings.Cut(a[2:], "=")
	spec := lookupLong(l.cmd, name)
	if spec == nil {
		// resolve_pending, its error discarded, then did_you_mean_error with every argument given.
		_ = p.resolve(l)
		message := fmt.Sprintf("unexpected argument '--%s' found", name)
		if len(l.cmd.positionals()) > 0 {
			message += fmt.Sprintf("\n\n  tip: to pass '--%s' as a value, use '-- --%s'", name, name)
		}
		return usageErr(message, usageUsed(l.cmd, l.explicit))
	}
	if spec.flag {
		if hasEq {
			// The used arguments are those given, not required and not pending, and this one.
			var used []string
			for _, id := range l.explicit {
				if s := lookupSpec(l.cmd, id); s != nil && !s.required {
					used = append(used, id)
				}
			}
			used = append(used, spec.id)
			_ = p.resolve(l)
			return usageErr(fmt.Sprintf("unexpected value '%s' for '%s' found; no more were expected", value, spec.display()),
				usageUsed(l.cmd, used))
		}
		if exit := p.resolve(l); exit != nil {
			return exit
		}
		switch spec {
		case &helpSpec:
			return helpExit(l.cmd, true)
		case &versionSpec:
			return p.versionExit(l.cmd)
		}
		if l.has(spec.id) {
			return p.conflict(l, spec)
		}
		l.explicit = append(l.explicit, spec.id)
		return nil
	}
	// An option: its value is attached with "=" or is the next argument.
	if exit := p.resolve(l); exit != nil {
		return exit
	}
	if hasEq {
		l.needsValue = false
		return p.react(l, spec, []string{value})
	}
	l.pending = &pendingValue{spec: spec}
	l.needsValue = true
	return nil
}

// short handles an argument that starts with a single "-" and has more characters (parse_short_arg).
// Only -h, and -V on the root, exist, and each ends the parse.
func (p *parser) short(l *level, a string) *exitRequest {
	r := []rune(a)[1]
	if r == 'h' || r == 'V' && l.cmd.isRoot() {
		if exit := p.resolve(l); exit != nil {
			return exit
		}
		if r == 'h' {
			return helpExit(l.cmd, false)
		}
		return p.versionExit(l.cmd)
	}
	_ = p.resolve(l)
	message := fmt.Sprintf("unexpected argument '-%s' found", string(r))
	if len(l.cmd.positionals()) > 0 {
		message += fmt.Sprintf("\n\n  tip: to pass '-%s' as a value, use '-- -%s'", string(r), string(r))
	}
	return usageErr(message, plainUsage(l.cmd))
}

func (p *parser) versionExit(c *command) *exitRequest {
	return &exitRequest{text: c.name + " " + p.version + "\n", stdout: true}
}

// matchArgError is the error for an argument that is no option, no subcommand and has no positional to
// go to (match_arg_error).
func matchArgError(l *level, a string, afterDashes bool) *exitRequest {
	c := l.cmd
	if afterDashes && possibleSub(c, a) != "" {
		return usageErr(fmt.Sprintf("unexpected argument '%s' found\n\n  tip: subcommand '%s' exists; to use it, remove the '--' before it", a, a),
			plainUsage(c))
	}
	if len(c.subs) > 0 && len(c.positionals()) == 0 {
		return usageErr(fmt.Sprintf("unrecognized subcommand '%s'", a), plainUsage(c))
	}
	return usageErr(fmt.Sprintf("unexpected argument '%s' found", a), plainUsage(c))
}

// validateLevel is the Validator of one command: a command with subcommands and nothing given shows its
// help, one without its subcommand is an error, and the required arguments must be there.
func validateLevel(p *parser, l *level, deepest bool) *exitRequest {
	c := l.cmd
	if deepest && len(c.subs) > 0 {
		if len(l.explicit) == 0 {
			return &exitRequest{text: renderHelp(c, false), code: 2}
		}
		names := make([]string, 0, len(c.subs)+1)
		for _, s := range c.subs {
			names = append(names, s.name)
		}
		names = append(names, "help")
		return usageErr(fmt.Sprintf("'%s' requires a subcommand but one was not provided\n  [subcommands: %s]",
			c.fullName(), strings.Join(names, ", ")), plainUsage(c))
	}
	var missing []string
	var missingIDs []string
	for i := range c.args {
		spec := &c.args[i]
		if !spec.positional && spec.required && !l.has(spec.id) {
			missing = append(missing, spec.display())
			missingIDs = append(missingIDs, spec.id)
		}
	}
	for _, spec := range c.positionals() {
		if spec.required && !l.has(spec.id) {
			missing = append(missing, spec.display())
			missingIDs = append(missingIDs, spec.id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// The missing arguments count as used, after the ones that were given.
	used := append(append([]string(nil), l.explicit...), missingIDs...)
	return usageErr("the following required arguments were not provided:\n  "+strings.Join(missing, "\n  "),
		"Usage: "+smartUsage(c, used))
}

func emptyValue(spec *argSpec) *exitRequest {
	message := fmt.Sprintf("a value is required for '%s' but none was supplied", spec.display())
	if len(spec.possible) > 0 {
		message += "\n  [possible values: " + strings.Join(spec.possible, ", ") + "]"
	}
	return usageErr(message, "")
}

// checkValue validates one value of an option with the rules of its value parser (value_parser.rs).
func checkValue(spec *argSpec, value string) *exitRequest {
	invalid := func(reason string) *exitRequest {
		return usageErr(fmt.Sprintf("invalid value '%s' for '%s': %s", value, spec.display(), reason), "")
	}
	switch {
	case len(spec.possible) > 0:
		if value == "" {
			return emptyValue(spec)
		}
		for _, p := range spec.possible {
			if p == value {
				return nil
			}
		}
		return usageErr(fmt.Sprintf("invalid value '%s' for '%s'\n  [possible values: %s]", value, spec.display(),
			strings.Join(spec.possible, ", ")), "")
	case spec.kind == kindPath:
		if value == "" {
			return emptyValue(spec)
		}
	case spec.kind == kindU64:
		// Rust reads "+5" as 5; ParseUint refuses the sign.
		digits := value
		if len(digits) > 1 && digits[0] == '+' && digits[1] >= '0' && digits[1] <= '9' {
			digits = digits[1:]
		}
		if _, err := strconv.ParseUint(digits, 10, 64); err != nil {
			return invalid(intError(value, err))
		}
	case spec.kind == kindI64:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return invalid(intError(value, err))
		}
	case spec.kind == kindU32:
		// RangedI64ValueParser: the text is read as an i64, then checked against the bounds.
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return invalid(intError(value, err))
		}
		lo, hi := int64(0), int64(math.MaxUint32)
		if spec.ranged {
			lo, hi = spec.min, spec.max
		}
		if n < lo || n > hi {
			return invalid(fmt.Sprintf("%d is not in %d..=%d", n, lo, hi))
		}
	}
	return nil
}

// intError is the text of Rust's ParseIntError for the same input.
func intError(value string, err error) string {
	switch {
	case value == "":
		return "cannot parse integer from empty string"
	case errors.Is(err, strconv.ErrRange):
		if strings.HasPrefix(value, "-") {
			return "number too small to fit in target type"
		}
		return "number too large to fit in target type"
	}
	return "invalid digit found in string"
}

// helpExit is `--help` (long) or `-h` (short) of a command, on standard output.
func helpExit(c *command, long bool) *exitRequest {
	return &exitRequest{text: renderHelp(c, long), stdout: true}
}

// helpSubcommand is `forgebase help [COMMAND]...` (parse_help_subcommand): the long help of the named
// command; every argument after help is a subcommand name, so a flag is an unrecognized subcommand.
func helpSubcommand(c *command, rest []string) *exitRequest {
	cur := c
	for _, name := range rest {
		next := cur.sub(name)
		if next == nil {
			if name == "help" {
				break
			}
			return usageErr(fmt.Sprintf("unrecognized subcommand '%s'", name), plainUsage(cur))
		}
		cur = next
	}
	return helpExit(cur, true)
}
