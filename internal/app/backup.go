package app

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/tvstore/forgebase-go/internal/db"
)

// `site backup` and `site restore` (M7.3, ADR 0014; the Rust module backup.rs): pg_dump and pg_restore
// run as child processes, never bundled.
//
// Secrets. The database URL and the password are never on a command line (where ps shows them) and never
// in a message: the child gets the connection through PG* environment variables (db.PgEnvironment), and
// every other PG* variable of this process is removed from its environment, so a PGSERVICE, PGPASSFILE or
// PGOPTIONS of the operator cannot change where it connects.
//
// Partial files. A backup is written to FILE.partial, created exclusively (mode 0600: the file holds
// password hashes), and the open handle is the standard output of pg_dump, so the tool never opens a path
// by name. Only a finished dump is published under its final name, with a hard link, which fails when FILE
// exists: nothing is ever overwritten. A failure removes the .partial file; a killed process (SIGINT,
// SIGTERM) can leave only FILE.partial, never a partial FILE.
//
// Versions. The tools must have the server's major version (a newer pg_dump writes an archive that
// pg_restore of the server's version may not read, and a newer pg_restore sends SQL an older server
// rejects, for example SET transaction_timeout). A restore also needs the dump's pg_dump major version to be
// at most the server's (an upgrade is allowed).

// pgBinVar is the environment variable with the directory of the PostgreSQL client tools. Unset: PATH.
const pgBinVar = "FORGEBASE_PG_BIN"

// pgTool is a client tool.
type pgTool string

const (
	pgDump    pgTool = "pg_dump"
	pgRestore pgTool = "pg_restore"
)

// toolRunner starts the tools: where they are, the standard error their lines go to, and the
// environment they inherit.
type toolRunner struct {
	// binDir is the directory of the tools; empty: PATH.
	binDir string
	stderr io.Writer
}

func (t pgTool) notFound() error {
	return errf("%s not found; install the PostgreSQL client tools of the server's major version (see "+
		"docs/guide/operations.md); set %s to their directory if they are not on PATH", t, pgBinVar)
}

func (t pgTool) startError(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return t.notFound()
	}
	return errf("cannot start %s: %s", t, ioErrorText(err))
}

// command is a command for tool with the clean PG* environment of env: no PG* variable of this process is
// inherited.
func (r *toolRunner) command(tool pgTool, env []db.EnvVar, args ...string) *exec.Cmd {
	program := string(tool)
	if r.binDir != "" {
		program = filepath.Join(r.binDir, program)
	}
	cmd := exec.Command(program, args...)
	// Never nil: a nil Env would inherit the whole environment of this process.
	clean := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PG") {
			clean = append(clean, entry)
		}
	}
	for _, v := range env {
		clean = append(clean, v.Name+"="+v.Value)
	}
	cmd.Env = clean
	cmd.Stdin = nil // the null device
	return cmd
}

// dumpArgs are the arguments of pg_dump. No URL, no password: the connection is in the environment. The
// dump goes to the standard output, which is the open .partial file.
func dumpArgs() []string {
	return []string{"--format=custom", "--no-owner", "--no-privileges"}
}

// restoreArgs are the arguments of pg_restore. pg_restore restores into a database only when it is given
// one, and does not read PGDATABASE for that, so the (validated) database name is an argument: it is no
// secret and cannot be read as a connection string (see db.PgEnvironment).
func restoreArgs(database, file string) []string {
	return []string{"--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction",
		"--dbname=" + database, fileArgument(file)}
}

// fileArgument is a file name as a positional argument: one that starts with `-` must not be read as an
// option.
func fileArgument(file string) string {
	if strings.HasPrefix(file, "-") {
		return "./" + file
	}
	return file
}

// parseMajor is the major version in the first line of `pg_dump --version` and its relatives, for example
// `pg_dump (PostgreSQL) 16.4 (Ubuntu 16.4-1)` gives 16, and `17devel` gives 17.
func parseMajor(text string) (uint32, bool) {
	line, _, _ := strings.Cut(text, "\n")
	for _, word := range strings.Fields(line) {
		if word[0] < '0' || word[0] > '9' {
			continue
		}
		end := 0
		for end < len(word) && word[end] >= '0' && word[end] <= '9' {
			end++
		}
		n, err := strconv.ParseUint(word[:end], 10, 32)
		return uint32(n), err == nil
	}
	return 0, false
}

// dumpMajor is the version of the program that wrote a dump, from the header that `pg_restore --list`
// prints (`;     Dumped by pg_dump version: 16.4 (...)`).
func dumpMajor(listing string) (uint32, bool) {
	for _, line := range strings.Split(listing, "\n") {
		rest, ok := strings.CutPrefix(line, ";")
		if !ok {
			continue
		}
		rest, ok = strings.CutPrefix(strings.TrimLeftFunc(rest, unicode.IsSpace), "Dumped by pg_dump version")
		if !ok {
			continue
		}
		return parseMajor(strings.TrimLeftFunc(strings.TrimLeft(rest, ":"), unicode.IsSpace))
	}
	return 0, false
}

// versionProblem is why tool of toolMajor cannot be used with a server of serverMajor, or "": the major
// versions must be equal.
func versionProblem(tool pgTool, toolMajor, serverMajor uint32) string {
	if toolMajor == serverMajor {
		return ""
	}
	return fmt.Sprintf("%s is version %d but the server is version %d; the tool must have the server's major "+
		"version: install the PostgreSQL client tools of version %d (see docs/guide/operations.md) and set %s to "+
		"their directory if they are not first on PATH", tool, toolMajor, serverMajor, serverMajor, pgBinVar)
}

// dumpProblem is why a backup made by pg_dump of dumpMajor cannot be restored into a server of serverMajor
// (with pg_restore of restoreMajor), or "": the dump must not be newer than the server. An older dump is
// fine (an upgrade).
func dumpProblem(restoreMajor, dumpMajor, serverMajor uint32) string {
	if dumpMajor <= serverMajor {
		return ""
	}
	return fmt.Sprintf("the backup was made by pg_dump version %d, which is newer than the server (version %d; "+
		"pg_restore is version %d); a backup can only be restored into a server of the same or a newer major version",
		dumpMajor, serverMajor, restoreMajor)
}

// toolMajor is the major version of tool, from `tool --version`.
func (r *toolRunner) toolMajor(tool pgTool) (uint32, error) {
	var out bytes.Buffer
	cmd := r.command(tool, nil, "--version")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return 0, tool.startError(err)
		}
	}
	major, ok := parseMajor(out.String())
	if !ok {
		return 0, errf("cannot read the version of %s from `%s --version`", tool, tool)
	}
	return major, nil
}

// runStreaming runs the tool to the end, copying each line of its standard error to ours, prefixed with the
// tool's name unless the line starts with it already (`pg_dump: error: ...`). It returns whether it exited
// with success.
func (r *toolRunner) runStreaming(tool pgTool, env []db.EnvVar, args []string, stdout io.Writer) (bool, error) {
	cmd := r.command(tool, env, args...)
	cmd.Stdout = stdout
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return false, errf("cannot start %s: %s", tool, ioErrorText(err))
	}
	if err := cmd.Start(); err != nil {
		return false, tool.startError(err)
	}
	reader := bufio.NewReader(pipe)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			text := strings.TrimRightFunc(strings.ToValidUTF8(string(line), "�"), unicode.IsSpace)
			// A closed standard error must not stop the tool half way: write errors are ignored.
			if strings.HasPrefix(text, string(tool)+":") {
				_, _ = fmt.Fprintln(r.stderr, text)
			} else {
				_, _ = fmt.Fprintf(r.stderr, "%s: %s\n", tool, text)
			}
		}
		if err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return false, nil
		}
		return false, errf("cannot wait for %s: %s", tool, ioErrorText(err))
	}
	return true, nil
}

// partialPath is the name under which a backup is written until it is finished: FILE.partial.
func partialPath(file string) string { return file + ".partial" }

func interrupted(partial string) error {
	return errf("%s exists: an earlier backup was interrupted; remove that file and try again", partial)
}

// checkNewFile refuses early, before any database or tool is touched, a backup file that exists already,
// and the .partial file of an interrupted backup.
func checkNewFile(file string) error {
	if _, err := os.Lstat(file); err == nil {
		return errf("refusing to overwrite %s", file)
	}
	if partial := partialPath(file); lstatOK(partial) {
		return interrupted(partial)
	}
	return nil
}

func lstatOK(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// reserve creates partial, empty, only if it does not exist, and checks that file does not (a backup never
// overwrites anything, also not by a race). Mode 0600.
func reserve(file, partial string) (*os.File, error) {
	if err := checkNewFile(file); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, interrupted(partial)
		}
		return nil, errf("cannot create %s: %s", partial, ioErrorText(err))
	}
	return handle, nil
}

// publish gives the finished partial its final name with a hard link, which fails if file exists, and
// returns the size of the backup.
func publish(partial, file string) (int64, error) {
	if err := os.Link(partial, file); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, errf("refusing to overwrite %s", file)
		}
		return 0, errf("cannot create %s: %s", file, ioErrorText(err))
	}
	info, err := os.Stat(file)
	if err != nil {
		return 0, errf("cannot read %s: %s", file, ioErrorText(err))
	}
	return info.Size(), nil
}

// backup dumps the database of env into file. serverMajor is the server's major version, read by the
// caller. The dump is written to FILE.partial and published as FILE only when pg_dump succeeded; an
// existing FILE is never overwritten. It returns the size of the file.
func (r *toolRunner) backup(env []db.EnvVar, serverMajor uint32, file string) (int64, error) {
	major, err := r.toolMajor(pgDump)
	if err != nil {
		return 0, err
	}
	if problem := versionProblem(pgDump, major, serverMajor); problem != "" {
		return 0, errf("%s", problem)
	}
	partial := partialPath(file)
	handle, err := reserve(file, partial)
	if err != nil {
		return 0, err
	}
	size, err := func() (int64, error) {
		ok, err := r.runStreaming(pgDump, env, dumpArgs(), handle)
		_ = handle.Close()
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, errf("pg_dump failed; no backup file was written")
		}
		return publish(partial, file)
	}()
	// On success this removes the second name of the published file; on failure the dump.
	_ = os.Remove(partial)
	return size, err
}

// restore restores file into the database of env (`site restore`), after checking that pg_restore has the
// server's major version and the dump is not newer than the server. The caller has checked that the
// database is empty.
func (r *toolRunner) restore(env []db.EnvVar, database string, serverMajor uint32, file string) error {
	major, err := r.toolMajor(pgRestore)
	if err != nil {
		return err
	}
	if problem := versionProblem(pgRestore, major, serverMajor); problem != "" {
		return errf("%s", problem)
	}
	var listing, listingErr bytes.Buffer
	list := r.command(pgRestore, nil, "--list", fileArgument(file))
	list.Stdout, list.Stderr = &listing, &listingErr
	if err := list.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return pgRestore.startError(err)
		}
		return errf("%s is not a backup that pg_restore can read: %s", file, strings.TrimSpace(listingErr.String()))
	}
	dumpedBy, ok := dumpMajor(listing.String())
	if !ok {
		return errf("cannot read the pg_dump version of %s; it is not a custom-format backup of `site backup`", file)
	}
	if problem := dumpProblem(major, dumpedBy, serverMajor); problem != "" {
		return errf("%s", problem)
	}
	succeeded, err := r.runStreaming(pgRestore, env, restoreArgs(database, file), nil)
	if err != nil {
		return err
	}
	if !succeeded {
		return errf("pg_restore failed; the restore ran in one transaction, so the database is unchanged")
	}
	return nil
}
