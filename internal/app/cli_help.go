package app

import (
	"fmt"
	"sort"
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

// longHelpExists is clap's long_help_exists (command.rs:5157-5180): some text exists only in the long
// form. It is a property of the command itself: the subcommands are not looked at, because clap prefers
// the short help for them.
func longHelpExists(c *command) bool {
	if c.longAbout != "" {
		return true
	}
	for i := range c.args {
		if c.args[i].longHelp != "" {
			return true
		}
	}
	return false
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

// smartUsage is the usage of an error that has arguments that were used (usage.rs write_smart_usage and
// write_args): no [OPTIONS]; the required options in the order they are declared, then the used ones in
// the order they were given; then the positionals that are required or used, by position; then <COMMAND>
// when a subcommand is required.
func smartUsage(c *command, used []string) string {
	var b strings.Builder
	b.WriteString(c.fullName())
	var options []*argSpec
	add := func(spec *argSpec) {
		for _, o := range options {
			if o == spec {
				return
			}
		}
		options = append(options, spec)
	}
	for _, spec := range requiredOptions(c) {
		add(spec)
	}
	isUsed := map[string]bool{}
	for _, id := range used {
		isUsed[id] = true
		if spec := lookupSpec(c, id); spec != nil && !spec.positional {
			add(spec)
		}
	}
	for _, spec := range options {
		b.WriteString(" " + spec.display())
	}
	for _, p := range c.positionals() {
		if p.required || isUsed[p.id] {
			b.WriteString(" " + p.display())
		}
	}
	if len(c.subs) > 0 {
		b.WriteString(" <COMMAND>")
	}
	return b.String()
}

// entry is one line of a section: the left column, the description and the order it sorts by.
type entry struct {
	left, text string
	// order and key are clap's sort key of an option (help_template.rs option_sort_key): the display
	// order, then the lower-cased short flag with 0 (lower case) or 1 (upper case), else the long name.
	order int
	key   string
}

func width(s string) int { return utf8.RuneCountInString(s) }

// specValues is the "[default: ...]" and "[possible values: ...]" part of an argument's help
// (help_template.rs spec_vals); the parts are joined with a space in the short form and a newline in the
// long form.
func specValues(a *argSpec, long bool) string {
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
	if long {
		return strings.Join(parts, "\n")
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

// argEntry is the entry of an argument. In the long form the description is followed by a blank line
// and the specs (help_template.rs help).
func argEntry(a *argSpec, long bool, short string) entry {
	text := a.help
	if long && a.longHelp != "" {
		text = a.longHelp
	}
	if spec := specValues(a, long); spec != "" {
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
// column; in the long form each is on the lines under its left column, every line of it indented by
// ten spaces (also the blank ones: clap replaces each newline with a newline and the indent,
// styled_str.rs:69-75), with an empty line between the entries.
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
			b.WriteString(nextLineIndent + strings.ReplaceAll(e.text, "\n", "\n"+nextLineIndent) + "\n")
			continue
		}
		b.WriteString("  " + e.left)
		if e.text != "" {
			b.WriteString(strings.Repeat(" ", column-width(e.left)+2) + e.text)
		}
		b.WriteString("\n")
	}
}

// sortKey is clap's option_sort_key for a short flag or a long name.
func sortKey(short, long string) string {
	if short != "" {
		if short[0] >= 'A' && short[0] <= 'Z' {
			return strings.ToLower(short) + "1"
		}
		return short + "0"
	}
	return long
}

// optionEntries are the options of c in the order clap lists them: sorted by display order and then by
// name. The display order of an argument is its position among the non-positional arguments of the
// command that declares it, from 0 (command.rs:177-188); a global option keeps the order it has in the
// root (command.rs:4740-4772), and -h and -V have none, which is 999.
func optionEntries(c *command, long bool) []entry {
	var entries []entry
	order := 0
	for i := range c.args {
		if c.args[i].positional {
			continue
		}
		e := argEntry(&c.args[i], long, "")
		e.order, e.key = order, sortKey("", c.args[i].long)
		entries = append(entries, e)
		order++
	}
	if !c.isRoot() {
		globalOrder := 0
		for _, g := range c.globals() {
			e := argEntry(g, long, "")
			e.order, e.key = globalOrder, sortKey("", g.long)
			entries = append(entries, e)
			globalOrder++
		}
	}
	helpText := "Print help"
	if longHelpExists(c) {
		helpText = "Print help (see more with '--help')"
		if long {
			helpText = "Print help (see a summary with '-h')"
		}
	}
	entries = append(entries, entry{left: "-h, --help", text: helpText, order: 999, key: sortKey("h", "help")})
	if c.isRoot() {
		entries = append(entries, entry{left: "-V, --version", text: "Print version", order: 999, key: sortKey("V", "version")})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].key < entries[j].key
	})
	return entries
}

// renderHelp is the help of a command (help_template.rs). long asks for `--help`; it only differs from
// `-h` when longHelpExists.
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
	b.WriteString("\n")
	section(&b, "Options", optionEntries(c, long), long)
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
