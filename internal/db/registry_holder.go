package db

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The registry of one running site, replaceable while it runs (M10.1, ADR 0023; the Rust module
// registry_holder.rs).
//
// A site's effective metadata is the registry of the apps plus the custom fields stored in its database.
// Custom fields change while a server or worker runs, so the process keeps the registry behind a
// RegistryHolder:
//
//   - Snapshots. Snapshot returns a *core.Registry. A request takes one at its start and uses it to the end,
//     so it never sees a half-built registry or two different ones. A registry is never changed after it is
//     built, so a registry that is swapped out stays good for the last request that holds it.
//   - Atomic swap. A rebuild builds the new registry completely, then replaces the pointer in one atomic
//     store. Readers never wait for a rebuild.
//   - Version polling. Poll reads fb_meta_version and rebuilds only when it differs from the version the
//     current registry was built from; a server calls it every meta reload interval. Reload rebuilds
//     unconditionally (the process that changed a field uses it to see the change at once). Rebuilds are
//     serialised, so an old build can never replace a newer one.
//   - No request path I/O. Nothing here runs per request.

// RegistryHolder is the current registry of a site, and what it takes to rebuild it.
type RegistryHolder struct {
	site    string
	base    *core.Registry
	current atomic.Pointer[core.Registry]
	warn    io.Writer

	// mu serialises rebuilds; builtFrom is the fb_meta_version of current, nil before the first build.
	mu        sync.Mutex
	builtFrom *int64

	warnMu sync.Mutex
}

// NewRegistryHolder is a holder that serves current until the first rebuild. base is the registry of the apps
// and their overlays (what every rebuild starts from) and site names the site in warnings. A warning for a
// stored field that was skipped is written to warn (standard error when nil).
func NewRegistryHolder(site string, base, current *core.Registry, warn io.Writer) *RegistryHolder {
	if warn == nil {
		warn = os.Stderr
	}
	h := &RegistryHolder{site: site, base: base, warn: warn}
	h.current.Store(current)
	return h
}

// LoadRegistryHolder is a holder whose registry is already built from the database (the first build of a
// site). Warnings of fields that were skipped are written to warn (standard error when nil).
func LoadRegistryHolder(ctx context.Context, site string, base *core.Registry, pool *pgxpool.Pool, warn io.Writer) (*RegistryHolder, error) {
	h := NewRegistryHolder(site, base, base, warn)
	if _, err := h.Reload(ctx, pool); err != nil {
		return nil, err
	}
	return h, nil
}

// Snapshot is the registry as it is now. Cheap; take one per request and keep it.
func (h *RegistryHolder) Snapshot() *core.Registry { return h.current.Load() }

// Base is the registry of the apps and their overlays, without custom fields.
func (h *RegistryHolder) Base() *core.Registry { return h.base }

// Site is the site name used in warnings.
func (h *RegistryHolder) Site() string { return h.site }

// Reload rebuilds the registry from the database now and swaps it in. It returns the version it was built
// from.
func (h *RegistryHolder) Reload(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rebuild(ctx, pool)
}

// Poll reads the metadata version and rebuilds the registry when it is not the one the current registry was
// built from. It returns whether it rebuilt. A failure (the database is down) is returned and the current
// registry is kept.
func (h *RegistryHolder) Poll(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, &Error{Kind: ErrPool, Message: err.Error(), Err: err}
	}
	version, err := MetaVersion(ctx, conn)
	conn.Release()
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.builtFrom != nil && *h.builtFrom == version {
		return false, nil
	}
	if _, err := h.rebuild(ctx, pool); err != nil {
		return false, err
	}
	return true, nil
}

// rebuild is Reload with h.mu held.
func (h *RegistryHolder) rebuild(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, &Error{Kind: ErrPool, Message: err.Error(), Err: err}
	}
	defer conn.Release()
	// One snapshot for the version and the rows, so they belong together.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, wrap(err)
	}
	built, err := EffectiveRegistry(ctx, tx, h.base, h.site, false)
	_ = tx.Rollback(context.WithoutCancel(ctx))
	if err != nil {
		return 0, err
	}
	h.warnMu.Lock()
	for _, warning := range built.Warnings {
		// A warning for every rebuild that meets the field: a bad field stays visible until someone fixes or
		// disables it.
		_, _ = fmt.Fprintf(h.warn, "warning: %s\n", warning)
	}
	h.warnMu.Unlock()
	h.current.Store(built.Registry)
	version := built.Version
	h.builtFrom = &version
	return version, nil
}
