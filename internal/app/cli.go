package app

import (
	"errors"
	"fmt"
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

// parse reads the command line (without the program name) against the tree.
//
// It works in order, like clap: help and version end the parse where they are met (so a mistake after
// them is never seen), a mistake before them is reported first, and the required arguments are
// checked at the end.
func parse(root *command, version string, args []string) (*matches, *exitRequest) {
	root.link()
	m := &matches{cmd: root, vals: map[string][]string{}, flags: map[string]bool{}}
	// explicit are the arguments given, per command level, for the "used" part of an error usage.
	var explicit []string
	given := 0 // the arguments given to the current command level
	positionalIndex := 0
	afterDashes := false
	cur := root

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !afterDashes && a == "--":
			afterDashes = true
			given++
			continue
		case !afterDashes && strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			given++
			if name == "help" && !hasValue {
				return nil, helpExit(cur, true)
			}
			if name == "version" && !hasValue && cur.isRoot() {
				return nil, &exitRequest{text: root.name + " " + version + "\n", stdout: true}
			}
			spec := lookupLong(cur, name)
			if spec == nil {
				return nil, unknownArgument(cur, a, explicit, true, afterDashes)
			}
			if spec.flag {
				if hasValue {
					return nil, usageErr(fmt.Sprintf("unexpected value '%s' for '%s' found; no more were expected", value, spec.display()),
						usageOf(cur, explicit))
				}
				if m.flags[spec.id] {
					return nil, usageErr(fmt.Sprintf("the argument '%s' cannot be used multiple times", spec.display()), usageOf(cur, explicit))
				}
				m.flags[spec.id] = true
				explicit = append(explicit, spec.id)
				continue
			}
			if !hasValue {
				if i+1 >= len(args) || isFlagLike(args[i+1]) {
					return nil, missingValue(spec)
				}
				i++
				value = args[i]
			}
			if len(m.vals[spec.id]) > 0 && !spec.multiple {
				return nil, usageErr(fmt.Sprintf("the argument '%s' cannot be used multiple times", spec.display()), usageOf(cur, explicit))
			}
			values := []string{value}
			if spec.delimiter != "" {
				values = strings.Split(value, spec.delimiter)
			}
			for _, v := range values {
				if exit := checkValue(spec, v); exit != nil {
					return nil, exit
				}
			}
			m.vals[spec.id] = append(m.vals[spec.id], values...)
			explicit = append(explicit, spec.id)
		case !afterDashes && len(a) > 1 && a[0] == '-':
			// Only -h and -V exist; clap reports the first character it does not know.
			r := []rune(a)[1]
			switch {
			case r == 'h':
				return nil, helpExit(cur, false)
			case r == 'V' && cur.isRoot():
				return nil, &exitRequest{text: root.name + " " + version + "\n", stdout: true}
			}
			return nil, unknownArgument(cur, "-"+string(r), explicit, true, afterDashes)
		default:
			given++
			if len(cur.subs) > 0 && len(cur.positionals()) == 0 {
				if a == "help" {
					return nil, helpSubcommand(cur, args[i+1:])
				}
				next := cur.sub(a)
				if next == nil {
					return nil, usageErr(fmt.Sprintf("unrecognized subcommand '%s'", a), usageOf(cur, nil))
				}
				cur = next
				m.path = append(m.path, a)
				explicit = nil
				given = 0
				positionalIndex = 0
				continue
			}
			pos := cur.positionals()
			if positionalIndex >= len(pos) {
				return nil, unknownArgument(cur, a, explicit, false, afterDashes)
			}
			spec := pos[positionalIndex]
			positionalIndex++
			m.vals[spec.id] = []string{a}
			explicit = append(explicit, spec.id)
		}
	}
	m.cmd = cur

	if len(cur.subs) > 0 {
		// A command with subcommands and nothing else on its line shows its help (clap's
		// arg_required_else_help), on standard error with code 2; with some argument but no
		// subcommand it is an error.
		if given == 0 {
			return nil, &exitRequest{text: renderHelp(cur, false), code: 2}
		}
		names := make([]string, 0, len(cur.subs)+1)
		for _, s := range cur.subs {
			names = append(names, s.name)
		}
		names = append(names, "help")
		return nil, usageErr(fmt.Sprintf("'%s' requires a subcommand but one was not provided\n  [subcommands: %s]",
			cur.fullName(), strings.Join(names, ", ")), usageOf(cur, nil))
	}

	// Defaults, then the required arguments.
	var missing []string
	check := func(spec *argSpec) {
		if spec.required && len(m.vals[spec.id]) == 0 {
			missing = append(missing, spec.display())
		}
	}
	for i := range cur.args {
		spec := &cur.args[i]
		if spec.hasDefault && len(m.vals[spec.id]) == 0 {
			m.vals[spec.id] = []string{spec.def}
		}
	}
	for i := range cur.args {
		if !cur.args[i].positional {
			check(&cur.args[i])
		}
	}
	for _, spec := range cur.positionals() {
		check(spec)
	}
	if len(missing) > 0 {
		return nil, usageErr("the following required arguments were not provided:\n  "+strings.Join(missing, "\n  "),
			usageOf(cur, explicit))
	}
	return m, nil
}

// isFlagLike is true for an argument that clap would read as a flag, so that it cannot be the value
// of the option before it. A lone `-` is a value (standard input).
func isFlagLike(a string) bool { return a != "-" && strings.HasPrefix(a, "-") }

// lookupLong finds a long option of the command or one of the global options.
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
	return nil
}

func unknownArgument(c *command, arg string, explicit []string, flagLike, afterDashes bool) *exitRequest {
	message := fmt.Sprintf("unexpected argument '%s' found", arg)
	// clap suggests `--` when the command has positionals and the argument is not one of them.
	if flagLike && !afterDashes && len(c.positionals()) > 0 {
		message += fmt.Sprintf("\n\n  tip: to pass '%s' as a value, use '-- %s'", arg, arg)
	}
	return usageErr(message, usageOf(c, explicit))
}

func missingValue(spec *argSpec) *exitRequest {
	message := fmt.Sprintf("a value is required for '%s' but none was supplied", spec.display())
	if len(spec.possible) > 0 {
		message += "\n  [possible values: " + strings.Join(spec.possible, ", ") + "]"
	}
	return usageErr(message, "")
}

// checkValue validates one value of an option with the rules of its value parser.
func checkValue(spec *argSpec, value string) *exitRequest {
	invalid := func(reason string) *exitRequest {
		return usageErr(fmt.Sprintf("invalid value '%s' for '%s': %s", value, spec.display(), reason), "")
	}
	if len(spec.possible) > 0 {
		for _, p := range spec.possible {
			if p == value {
				return nil
			}
		}
		return usageErr(fmt.Sprintf("invalid value '%s' for '%s'\n  [possible values: %s]", value, spec.display(),
			strings.Join(spec.possible, ", ")), "")
	}
	bits := map[valueKind]int{kindU32: 32, kindU64: 64, kindI64: 64}[spec.kind]
	if spec.kind == kindString {
		return nil
	}
	var n int64
	if spec.kind == kindI64 {
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return invalid(intError(value, err))
		}
		n = v
	} else {
		v, err := strconv.ParseUint(value, 10, bits)
		if err != nil {
			return invalid(intError(value, err))
		}
		if v > 1<<63-1 {
			// Only a u64 above i64 can get here; there is no range on those.
			return nil
		}
		n = int64(v)
	}
	if spec.ranged && (n < spec.min || n > spec.max) {
		return invalid(fmt.Sprintf("%d is not in %d..=%d", n, spec.min, spec.max))
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

// helpSubcommand is `forgebase help [COMMAND]...`: the (short) help of the named command.
func helpSubcommand(c *command, rest []string) *exitRequest {
	cur := c
	for _, name := range rest {
		if strings.HasPrefix(name, "-") {
			break
		}
		next := cur.sub(name)
		if next == nil {
			if name == "help" {
				break
			}
			return usageErr(fmt.Sprintf("unrecognized subcommand '%s'", name), usageOf(c, nil))
		}
		cur = next
	}
	return helpExit(cur, false)
}
