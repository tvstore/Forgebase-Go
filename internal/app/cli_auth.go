package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The `user` and `token` commands (the Rust functions user and token of run.rs). They print what they
// did and never a password or a secret, except that `token create` prints the secret once.

// maxPasswordLine is how much of standard input is read for a password: far more than the longest
// valid password, so that a runaway input cannot fill memory.
const maxPasswordLine = 4096

// readPassword is the first line of standard input without its line ending. At most 4096 bytes are
// read.
func (r *runner) readPassword() (string, error) {
	in := r.o.Stdin
	if in == nil {
		in = strings.NewReader("")
	}
	line, err := bufio.NewReader(io.LimitReader(in, maxPasswordLine)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", errf("cannot read standard input: %s", ioErrorText(err))
	}
	if !utf8.ValidString(line) {
		return "", errf("cannot read standard input: stream did not contain valid UTF-8")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// user runs a `user` command. The password of create and set-password is read from standard input
// before the database is opened, so that a bad input never touches it.
func (r *runner) user(ctx context.Context, site *site) error {
	// Every command first checks that the apps fit together.
	if _, err := r.siteComposed(site, ""); err != nil {
		return err
	}
	verb := r.m.path[1]
	password := ""
	if verb == "create" || verb == "set-password" {
		var err error
		if password, err = r.readPassword(); err != nil {
			return err
		}
	}
	sc, err := site.open(ctx, r.o.Getenv, core.NewRegistry(), 0)
	if err != nil {
		return err
	}
	defer sc.close()
	username := r.m.str("username")
	role := r.m.str("role")
	var text string
	switch verb {
	case "create":
		if _, err := db.CreateUser(ctx, sc.pool, username, password, r.m.list("roles")); err != nil {
			return err
		}
		text = fmt.Sprintf("created user %s\n", username)
	case "set-password":
		if err := db.SetPassword(ctx, sc.pool, username, password); err != nil {
			return err
		}
		text = fmt.Sprintf("changed the password of user %s; its sessions were ended\n", username)
	case "add-role":
		added, err := db.AddRole(ctx, sc.pool, username, role)
		if err != nil {
			return err
		}
		text = fmt.Sprintf("user %s has role %s already\n", username, role)
		if added {
			text = fmt.Sprintf("added role %s to user %s\n", role, username)
		}
	case "remove-role":
		removed, err := db.RemoveRole(ctx, sc.pool, username, role)
		if err != nil {
			return err
		}
		text = fmt.Sprintf("user %s does not have role %s\n", username, role)
		if removed {
			text = fmt.Sprintf("removed role %s from user %s\n", role, username)
		}
	case "disable":
		if err := db.SetEnabled(ctx, sc.pool, username, false); err != nil {
			return err
		}
		text = fmt.Sprintf("disabled user %s; its sessions were ended\n", username)
	case "enable":
		if err := db.SetEnabled(ctx, sc.pool, username, true); err != nil {
			return err
		}
		text = fmt.Sprintf("enabled user %s\n", username)
	default:
		return fmt.Errorf("unknown user command %q", verb)
	}
	return r.write(text)
}

// token runs a `token` command. create prints key:secret and nothing else.
func (r *runner) token(ctx context.Context, site *site) error {
	if _, err := r.siteComposed(site, ""); err != nil {
		return err
	}
	sc, err := site.open(ctx, r.o.Getenv, core.NewRegistry(), 0)
	if err != nil {
		return err
	}
	defer sc.close()
	switch r.m.path[1] {
	case "create":
		token, err := db.CreateAPIToken(ctx, sc.pool, r.m.str("username"))
		if err != nil {
			return err
		}
		return r.write(token.Key + ":" + token.Secret + "\n")
	case "revoke":
		key := r.m.str("key")
		if err := db.RevokeAPIToken(ctx, sc.pool, key); err != nil {
			return err
		}
		return r.write(fmt.Sprintf("revoked API token %s\n", key))
	}
	return fmt.Errorf("unknown token command %q", r.m.path[1])
}
