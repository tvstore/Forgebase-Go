package app

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Help and usage rendering, in the layout of clap 4:
//
//	<about>
//
//	Usage: <usage>
//
//	Commands:       (subcommands, then the generated `help`)
//	Arguments:      (positionals)
//	Options:        (options, the global ones, -h and -V)
//
// `-h` prints the short form (one line per argument). `--help` prints the long form when the command
// or one below it has a long help text: every argument on two lines, the description indented under
// it, a blank line between arguments, and the [default: ...] and [possible values: ...] lines under
// the text. The text is never wrapped (clap without its wrap_help feature, which the Rust
// implementation is built without).

const (
	helpCommandAbout = "Print this message or the help of the given subcommand(s)"
	nextLineIndent   = "          "
)

// longHelpExists is clap's long_help_exists: some text exists only in the long form.
func longHelpExists(c *command) bool {
	if c.longAbout != "" {
		return true
	}
	for i := range c.args {
		if c.args[i].longHelp != "" {
			return true
		}
	}
	for _, s := range c.subs {
		if longHelpExists(s) {
			return true
		}
	}
	return false
}

// usageOf is the "Usage:" paragraph of an error: the full usage, or, when optional arguments were
// given (explicit), the command with the required arguments and those.
func usageOf(c *command, explicit []string) string {
	var used []*argSpec
	for _, id := range explicit {
		spec := c.arg(id)
		if spec == nil {
			for _, g := range c.globals() {
				if g.id == id {
					spec = g
				}
			}
		}
		if spec != nil && !spec.required {
			used = append(used, spec)
		}
	}
	if len(used) == 0 {
		return "Usage: " + helpUsage(c)
	}
	return "Usage: " + smartUsage(c, used)
}

// requiredOptions are the required options in the order they are declared.
func requiredOptions(c *command) []*argSpec {
	var out []*argSpec
	for i := range c.args {
		if !c.args[i].positional && c.args[i].required {
			out = append(out, &c.args[i])
		}
	}
	return out
}

// helpUsage is the usage line without its title: `forgebase doc update [OPTIONS] --version <N>
// --data <JSON|-> <DOCTYPE> <NAME>`.
func helpUsage(c *command) string {
	var b strings.Builder
	b.WriteString(c.fullName())
	// -h is always there, so there is always something optional.
	b.WriteString(" [OPTIONS]")
	for _, a := range requiredOptions(c) {
		b.WriteString(" " + a.display())
	}
	for _, p := range c.positionals() {
		if p.required {
			b.WriteString(" " + p.display())
		}
	}
	if len(c.subs) > 0 {
		b.WriteString(" <COMMAND>")
	}
	return b.String()
}

// smartUsage is the usage of an error about a command that already got some optional arguments: they
// are listed instead of [OPTIONS].
func smartUsage(c *command, used []*argSpec) string {
	isUsed := func(a *argSpec) bool {
		for _, u := range used {
			if u == a {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	b.WriteString(c.fullName())
	for i := range c.args {
		a := &c.args[i]
		if !a.positional && (a.required || isUsed(a)) {
			b.WriteString(" " + a.display())
		}
	}
	for _, g := range used {
		if c.arg(g.id) == nil {
			b.WriteString(" " + g.display())
		}
	}
	for _, p := range c.positionals() {
		if p.required || isUsed(p) {
			b.WriteString(" " + p.display())
		}
	}
	return b.String()
}

// entry is one line of a section: the left column and the description.
type entry struct {
	left, text string
}

func width(s string) int { return utf8.RuneCountInString(s) }

// specValues is the "[default: ...] [possible values: ...]" part of an argument's help.
func specValues(a *argSpec) string {
	var parts []string
	if a.hasDefault {
		def := a.def
		if strings.IndexFunc(def, unicode.IsSpace) >= 0 {
			def = fmt.Sprintf("%q", def)
		}
		parts = append(parts, "[default: "+def+"]")
	}
	if len(a.possible) > 0 {
		parts = append(parts, "[possible values: "+strings.Join(a.possible, ", ")+"]")
	}
	return strings.Join(parts, " ")
}

// optionLeft is the left column of an option: `-h, --help`, `    --site <NAME>`.
func optionLeft(a *argSpec, short string) string {
	left := "    "
	if short != "" {
		left = "-" + short + ", "
	}
	return left + a.display()
}

func argEntry(a *argSpec, long bool, short string) entry {
	text := a.help
	if long && a.longHelp != "" {
		text = a.longHelp
	}
	spec := specValues(a)
	if spec != "" {
		switch {
		case text == "":
			text = spec
		case long:
			text += "\n\n" + spec
		default:
			text += " " + spec
		}
	}
	left := a.display()
	if !a.positional {
		left = optionLeft(a, short)
	}
	return entry{left: left, text: text}
}

// section renders `Title:` and its entries. In the short form the descriptions are aligned in a
// column; in the long form each is on the lines under its left column.
func section(b *strings.Builder, title string, entries []entry, long bool) {
	b.WriteString(title + ":\n")
	column := 0
	for _, e := range entries {
		column = max(column, width(e.left))
	}
	for i, e := range entries {
		if long {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + e.left + "\n")
			for _, line := range strings.Split(e.text, "\n") {
				if line == "" {
					b.WriteString("\n")
				} else {
					b.WriteString(nextLineIndent + line + "\n")
				}
			}
			continue
		}
		b.WriteString("  " + e.left)
		if e.text != "" {
			b.WriteString(strings.Repeat(" ", column-width(e.left)+2) + e.text)
		}
		b.WriteString("\n")
	}
}

// renderHelp is the help of a command. long asks for `--help`; it only differs from `-h` when
// longHelpExists.
func renderHelp(c *command, long bool) string {
	long = long && longHelpExists(c)
	var b strings.Builder
	about := c.about
	if long && c.longAbout != "" {
		about = c.longAbout
	} else if about == "" {
		about = c.longAbout
	}
	if about != "" {
		b.WriteString(about + "\n\n")
	}
	b.WriteString("Usage: " + helpUsage(c) + "\n")

	if len(c.subs) > 0 {
		entries := make([]entry, 0, len(c.subs)+1)
		for _, s := range c.subs {
			text := s.about
			if text == "" {
				text = s.longAbout
			}
			entries = append(entries, entry{left: s.name, text: text})
		}
		entries = append(entries, entry{left: "help", text: helpCommandAbout})
		b.WriteString("\n")
		// The subcommand list is always in the short form.
		section(&b, "Commands", entries, false)
	}
	if pos := c.positionals(); len(pos) > 0 {
		entries := make([]entry, len(pos))
		for i, p := range pos {
			entries[i] = argEntry(p, long, "")
		}
		b.WriteString("\n")
		section(&b, "Arguments", entries, long)
	}
	var entries []entry
	for i := range c.args {
		if !c.args[i].positional {
			entries = append(entries, argEntry(&c.args[i], long, ""))
		}
	}
	for _, g := range c.globals() {
		entries = append(entries, argEntry(g, long, ""))
	}
	helpText := "Print help"
	if longHelpExists(c) {
		helpText = "Print help (see more with '--help')"
		if long {
			helpText = "Print help (see a summary with '-h')"
		}
	}
	entries = append(entries, entry{left: "-h, --help", text: helpText})
	if c.isRoot() {
		entries = append(entries, entry{left: "-V, --version", text: "Print version"})
	}
	b.WriteString("\n")
	section(&b, "Options", entries, long)
	return b.String()
}

// withRequired is a copy of c in which the option id is required: the usage of the stock binary's
// "needs --doctypes" error, which is clap's own error for a required option.
func withRequired(c *command, id string) *command {
	clone := *c
	clone.args = append([]argSpec(nil), c.args...)
	for i := range clone.args {
		if clone.args[i].id == id {
			clone.args[i].required = true
		}
	}
	return &clone
}
