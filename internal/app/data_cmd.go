package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// The data commands (M9.3, ADR 0021; the Rust module data_cmd.rs): export and import of documents as CSV or
// JSON, as the trusted system context. The formats, the checks and the transaction are those of the HTTP API
// (httpapi.Exporter, httpapi.ParseImport, httpapi.RunImport, db.Import), so a file means the same on both.

// exportActor is the system actor of `data export`, which only reads.
const exportActor = "cli"

// data runs `data export` or `data import` on site. The DocType is looked up in the files before the database is
// opened (an unknown one never touches it), the file of an import is read and the syntax of the filter of an
// export is checked before it too; the rest is checked against the registry of the site context.
func (r *runner) data(ctx context.Context, site *site) error {
	composed, err := r.siteComposed(site, r.m.str("doctypes"))
	if err != nil {
		return err
	}
	name := r.m.str("doctype")
	if composed.registry.Get(name) == nil {
		return unknownDocType(composed.registry, name)
	}
	export := r.m.path[1] == "export"
	var body []byte
	if export {
		if filter, ok := r.m.opt("filter"); ok {
			if err := httpapi.CheckFiltersSyntax(filter); err != nil {
				return err
			}
		}
	} else {
		file := r.m.str("file")
		if body, err = os.ReadFile(file); err != nil {
			return errf("cannot read %s: %s", file, ioErrorText(err))
		}
	}
	sc, err := site.open(ctx, r.o, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	doctype := sc.registry.Get(name)
	if doctype == nil {
		return errf("unknown DocType `%s`", name)
	}
	format, ok := httpapi.ParseFormat(r.m.str("format"))
	if !ok {
		return errf("unknown format `%s`", r.m.str("format"))
	}
	if export {
		return r.dataExport(ctx, sc, doctype, format)
	}
	return r.dataImport(ctx, sc, doctype, format, body)
}

func (r *runner) dataExport(ctx context.Context, sc *siteContext, doctype *core.DocType, format httpapi.Format) error {
	var query db.ExportQuery
	var err error
	if text, ok := r.m.opt("filter"); ok {
		if query.Filters, err = httpapi.ParseFilters(doctype, text); err != nil {
			return err
		}
	}
	if query.Sort, err = httpapi.ParseSort(r.m.list("sort")); err != nil {
		return err
	}
	var fields []string
	if _, ok := r.m.opt("fields"); ok {
		fields = r.m.list("fields")
		if fields == nil {
			fields = []string{}
		}
	}
	exporter, err := httpapi.NewExporter(doctype, format, fields)
	if err != nil {
		return err
	}
	query.DocType = doctype.Name
	query.Limit = httpapi.DefaultConfig().ExportMaxRows
	if _, ok := r.m.opt("limit_override"); ok {
		query.Limit = r.m.u64("limit_override")
	}
	query.WithTables = exporter.WithTables()

	conn, err := sc.pool.Acquire(ctx)
	if err != nil {
		return &db.Error{Kind: db.ErrPool, Message: err.Error(), Err: err}
	}
	defer conn.Release()
	tx, err := db.BeginExport(ctx, conn)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	export, err := db.StartExport(ctx, tx, sc.registry, core.SystemCaller(exportActor), query)
	if err != nil {
		return err
	}
	out, hasOut := r.m.opt("out")
	var sink dataSink
	if hasOut {
		// A new file: an existing one is never overwritten.
		file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			return errf("cannot create %s: %s", out, ioErrorText(err))
		}
		sink = &fileSink{file: file, w: bufio.NewWriter(file)}
	} else {
		sink = stdoutSink{r}
	}
	err = writePages(ctx, sink, export, exporter)
	if file, ok := sink.(*fileSink); ok {
		if err == nil {
			err = file.close()
		} else {
			_ = file.file.Close()
		}
	}
	if err != nil {
		if hasOut {
			_ = os.Remove(out)
		}
		return err
	}
	if hasOut {
		return r.write(fmt.Sprintf("wrote %d row(s) to %s\n", export.Count(), out))
	}
	return nil
}

// dataSink is where an export is written: standard output or a new file (removed again if the export fails).
type dataSink interface{ write(text string) error }

type stdoutSink struct{ r *runner }

func (s stdoutSink) write(text string) error { return s.r.write(text) }

type fileSink struct {
	file *os.File
	w    *bufio.Writer
}

func (s *fileSink) write(text string) error {
	if _, err := s.w.WriteString(text); err != nil {
		return errf("cannot write output: %s", ioErrorText(err))
	}
	return nil
}

func (s *fileSink) close() error {
	if err := s.w.Flush(); err != nil {
		_ = s.file.Close()
		return errf("cannot write output: %s", ioErrorText(err))
	}
	if err := s.file.Close(); err != nil {
		return errf("cannot write output: %s", ioErrorText(err))
	}
	return nil
}

func writePages(ctx context.Context, sink dataSink, export *db.Export, exporter *httpapi.Exporter) error {
	if err := sink.write(exporter.Start()); err != nil {
		return err
	}
	for {
		page, err := export.NextPage(ctx)
		if err != nil {
			return err
		}
		if page == nil {
			break
		}
		text, err := exporter.Rows(page)
		if err != nil {
			return outputOrJSON(err)
		}
		if err := sink.write(text); err != nil {
			return err
		}
	}
	return sink.write(exporter.Finish())
}

// outputOrJSON is the error of a stored value that cannot be written.
func outputOrJSON(err error) error {
	var oe *db.OutputError
	if errors.As(err, &oe) {
		return errf("%s", oe)
	}
	return err
}

func (r *runner) dataImport(ctx context.Context, sc *siteContext, doctype *core.DocType, format httpapi.Format, body []byte) error {
	mode := db.ImportInsert
	if r.m.flag("upsert") {
		mode = db.ImportUpsert
	}
	parsed, err := httpapi.ParseImport(sc.registry, doctype, format, mode, body, httpapi.DefaultConfig().ImportMaxRows)
	if err != nil {
		var fe *httpapi.FileError
		if errors.As(err, &fe) {
			if fe.TooLarge {
				return errf("the file has more than %d rows; split it", fe.Limit)
			}
			return errf("the file cannot be imported:\n%s", bullets(fe.Problems))
		}
		return err
	}
	dryRun := r.m.flag("dry_run")
	summary, err := httpapi.RunImport(ctx, sc.pool, sc.registry, doctype.Name, core.SystemCaller(r.m.str("actor")), parsed, mode, dryRun)
	if err != nil {
		return err
	}
	text, err := prettyJSON(summary.JSON())
	if err != nil {
		return errf("cannot serialize the summary as JSON: %s", err)
	}
	if err := r.write(text + "\n"); err != nil {
		return err
	}
	if summary.ErrorCount > 0 {
		suffix := "; nothing was imported"
		if dryRun {
			suffix = " (dry run: nothing was imported)"
		}
		return errf("%d row(s) have errors%s", summary.ErrorCount, suffix)
	}
	return nil
}
