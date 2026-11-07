package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The customize field commands (M10.1, ADR 0023; the Rust module customize_cmd.rs): add, list, disable and
// enable the custom fields of a site. They run as the system context, like webhook and user; the change is
// recorded as made by cli. The work is in internal/db (one transaction under the migrate lock), which the
// Administrator-only API uses too.

// customizeActor is who a command line change is recorded as.
const customizeActor = "cli"

// customizeLockWait is the longest wait for the migrate lock (the default of migrate).
const customizeLockWait = db.DefaultLockTimeout

// readFieldText is the text of the field file of add, or standard input for `-`.
func (r *runner) readFieldText(arg string) (string, error) {
	if arg == "-" {
		in := r.o.Stdin
		if in == nil {
			in = strings.NewReader("")
		}
		data, err := io.ReadAll(in)
		if err != nil {
			return "", errf("cannot read standard input: %s", ioErrorText(err))
		}
		if !utf8.Valid(data) {
			return "", errf("cannot read standard input: stream did not contain valid UTF-8")
		}
		return string(data), nil
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return "", errf("cannot read %s: %s", arg, ioErrorText(err))
	}
	if !utf8.Valid(data) {
		return "", errf("cannot read %s: stream did not contain valid UTF-8", arg)
	}
	return string(data), nil
}

// customize runs a customize field command on site. add checks the field against the DocTypes of the site, the
// --doctypes directory included; the field is read and parsed before the database is opened, so a bad file
// never touches it.
func (r *runner) customize(ctx context.Context, site *site) error {
	verb := r.m.path[2]
	local := ""
	if verb == "add" {
		local = r.m.str("doctypes")
	}
	composed, err := r.siteComposed(site, local)
	if err != nil {
		return err
	}
	var field core.DocField
	if verb == "add" {
		text, err := r.readFieldText(r.m.str("field"))
		if err != nil {
			return err
		}
		if field, err = core.ParseFieldTOML(text); err != nil {
			return errf("%s", err)
		}
	}
	sc, err := site.open(ctx, r.o, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	doctype := r.m.str("doctype")
	switch verb {
	case "add":
		stored, err := db.AddCustomField(ctx, sc.pool, composed.registry, sc.site.name, doctype, field, customizeActor, customizeLockWait)
		if err != nil {
			return err
		}
		return r.write(fmt.Sprintf("added custom field %s.%s (%s); its column exists now, and a running server or worker uses it within 10 seconds\n",
			stored.DocType, stored.Name, stored.TypeName()))
	case "list":
		var only *string
		if _, ok := r.m.opt("doctype"); ok {
			only = &doctype
		}
		fields, err := db.ListCustomFields(ctx, sc.pool, only)
		if err != nil {
			return err
		}
		return r.write(customFieldTable(fields))
	case "disable":
		name := r.m.str("name")
		if _, err := db.SetCustomFieldEnabled(ctx, sc.pool, doctype, name, false, customizeActor, customizeLockWait); err != nil {
			return err
		}
		return r.write(fmt.Sprintf("disabled custom field %s.%s; it is hidden everywhere, its column and data are kept\n", doctype, name))
	case "enable":
		name := r.m.str("name")
		if _, err := db.SetCustomFieldEnabled(ctx, sc.pool, doctype, name, true, customizeActor, customizeLockWait); err != nil {
			return err
		}
		return r.write(fmt.Sprintf("enabled custom field %s.%s; it is shown again with its data\n", doctype, name))
	}
	return fmt.Errorf("unknown customize field command %q", verb)
}

// rfc3339 is t in UTC as the Rust time crate's Rfc3339 writes it; "" for a time that has no such form.
func rfc3339(t time.Time) string {
	text, err := db.UTCRFC3339(t)
	if err != nil {
		return ""
	}
	return text
}

// customFieldTable is the table of list.
func customFieldTable(fields []*db.CustomField) string {
	rows := make([][]string, len(fields))
	for i, f := range fields {
		enabled := "no"
		if f.Enabled {
			enabled = "yes"
		}
		rows[i] = []string{f.DocType, f.Name, f.TypeName(), f.Label(), enabled, rfc3339(f.ModifiedAt), f.ModifiedBy}
	}
	return textTable([]string{"DOCTYPE", "NAME", "TYPE", "LABEL", "ENABLED", "MODIFIED", "BY"}, rows)
}
