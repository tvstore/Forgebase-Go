package app

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The error of the command line (the Rust enum CliError). Every error ends the command with exit
// code 1, except a usage error, which is exit code 2 with the usage printed (as clap does).
//
// Most errors are plain text. A *db.Error and a *StartupError are kept as they are and described
// when they are printed (describeError), so that a caller can still look at their kind.

// cliError is an error with its text. usage marks the ones that are usage errors.
type cliError struct {
	msg   string
	usage bool
}

func (e *cliError) Error() string { return e.msg }

// errUsage is a mistake in the command line that only the program can see (an unknown site).
func errUsage(msg string) error { return &cliError{msg: msg, usage: true} }

// errf is an error with a formatted text.
func errf(format string, args ...any) error { return &cliError{msg: fmt.Sprintf(format, args...)} }

// errSite is a database error while opening a site (connecting); it names the site and never
// contains the database URL.
func errSite(site string, err error) error {
	return &cliError{msg: fmt.Sprintf("site `%s`: %s", site, describeError(err))}
}

// isUsage is true for the errors that end with the usage and exit code 2.
func isUsage(err error) bool {
	var ce *cliError
	return errors.As(err, &ce) && ce.usage
}

// describeError is the text printed after "error: ".
func describeError(err error) string {
	// A user, role or token error prints as it is, a database error inside it too (Rust
	// CliError::Auth is transparent).
	var ae *db.AuthError
	if errors.As(err, &ae) {
		return ae.Error()
	}
	var dbe *db.Error
	if errors.As(err, &dbe) {
		return describeDB(dbe)
	}
	return err.Error()
}

// describeDB is the text of a database error. The errors that carry several problems get one line
// per problem.
func describeDB(e *db.Error) string {
	switch e.Kind {
	case db.ErrValidation:
		lines := make([]string, len(e.Validation))
		for i, v := range e.Validation {
			lines[i] = v.Error()
		}
		return "validation failed:\n" + bullets(lines)
	case db.ErrInvalidMetadata:
		lines := make([]string, len(e.Problems))
		for i, p := range e.Problems {
			lines[i] = p.Error()
		}
		return "invalid DocType metadata:\n" + bullets(lines)
	case db.ErrSyncConflicts:
		lines := make([]string, len(e.Conflicts))
		for i, c := range e.Conflicts {
			lines[i] = c.String()
		}
		return fmt.Sprintf("schema sync refused, %d conflict(s); nothing was changed:\n%s", len(lines), bullets(lines))
	case db.ErrReportCheck:
		return fmt.Sprintf("the migrations were applied, but %d report(s) do not fit their SQL:\n%s\nfix the report and run migrate again",
			len(e.Details), bullets(e.Details))
	}
	return e.Error()
}

// bullets is the lines as an indented list, one "  - " item per line, without a final newline.
func bullets(lines []string) string {
	items := make([]string, len(lines))
	for i, line := range lines {
		items[i] = "  - " + line
	}
	return strings.Join(items, "\n")
}

// ioErrorText is the text of a file error as Rust prints it ("No such file or directory (os error
// 2)"), so that the messages of the two implementations agree.
func ioErrorText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		if text != "" {
			text = strings.ToUpper(text[:1]) + text[1:]
		}
		return fmt.Sprintf("%s (os error %d)", text, int(errno))
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
