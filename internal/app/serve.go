package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// The serve command (the Rust function serve in run.rs): the HTTP API until Ctrl-C or SIGTERM. It does
// not run migrations.
//
// Without a config file it serves the implicit site `default` for the hosts of FORGEBASE_ALLOWED_HOSTS.
// With one it serves every site (or only --site), one pool per site, and routes each request by its Host
// to the site that owns the host.
//
// The first signal stops accepting connections and lets running requests finish, for at most
// shutdownGrace; a second signal, or the end of the grace period, exits at once.
//
// Environment: FORGEBASE_LISTEN (default 127.0.0.1:8000), FORGEBASE_ALLOWED_HOSTS (default
// localhost,127.0.0.1), FORGEBASE_DEV_INSECURE_COOKIES=1 and FORGEBASE_DESK_DIR (the built desk).

// shutdownGrace is how long serve waits for open requests after the first stop signal.
const shutdownGrace = 10 * time.Second

func (r *runner) getenvOr(name, def string) string {
	if v := r.o.Getenv(name); strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// serve runs the server. The sites are opened after the registry is composed, so that a problem with the
// DocType files is reported without touching a database.
func (r *runner) serve(ctx context.Context, cfg *config) error {
	// First thing, so that no announcement or warning below can end the process over a closed pipe.
	ignoreBrokenPipe()
	siteName, named := r.m.opt("site")
	sites, err := cfg.serveSet(siteName, named)
	if err != nil {
		return err
	}
	listen := r.getenvOr("FORGEBASE_LISTEN", "127.0.0.1:8000")
	insecure := r.o.Getenv("FORGEBASE_DEV_INSECURE_COOKIES") == "1"
	if insecure {
		r.warn("warning: FORGEBASE_DEV_INSECURE_COOKIES=1: the session cookie has no Secure attribute; " +
			"use this for local development over plain HTTP only\n")
	}
	deskDir, err := r.deskDir()
	if err != nil {
		return err
	}
	var implicitHosts []string
	if cfg.configured {
		if strings.TrimSpace(r.o.Getenv("FORGEBASE_ALLOWED_HOSTS")) != "" {
			r.warn("warning: FORGEBASE_ALLOWED_HOSTS is ignored: the hosts come from the config file\n")
		}
	} else {
		var err error
		implicitHosts, err = parseAllowedHosts(r.getenvOr("FORGEBASE_ALLOWED_HOSTS", "localhost,127.0.0.1"))
		if err != nil {
			return err
		}
	}
	// Every site's registry is composed before any pool is opened, so a configuration or composition
	// error is reported without touching a database. Each site gets a registry of its own, of the apps
	// installed on it.
	registries, err := r.siteRegistries(sites, r.m.str("doctypes"))
	if err != nil {
		return err
	}
	var opened []*siteContext
	defer func() {
		for _, sc := range opened {
			sc.close()
		}
	}()
	for i, site := range sites {
		sc, err := site.open(ctx, r.o.Getenv, registries[i], 0)
		if err != nil {
			return err
		}
		opened = append(opened, sc)
	}
	base := httpapi.DefaultConfig()
	base.SecureCookies = !insecure
	base.DeskDir = deskDir
	routing, lines, err := routeSites(cfg.configured, opened, implicitHosts, base)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return errf("cannot listen on %s: %s", listen, listenErrorText(err))
	}
	defer func() { _ = listener.Close() }()
	signals, stopSignals := stopSignals()
	defer stopSignals()
	// The server's own announcements: a closed standard output must not stop a running server.
	r.announce("listening on http://" + listener.Addr().String() + "\n")
	for _, line := range lines {
		r.announce(line)
	}
	if deskDir != "" {
		r.announce(fmt.Sprintf("serving the desk from %s at /desk/\n", deskDir))
	}
	if err := runUntilStopped(httpapi.NewServer(routing), listener, signals, shutdownGrace, r.o.Stderr); err != nil {
		return errf("server error: %s", ioErrorText(err))
	}
	return nil
}

// warn writes a line to standard error and ignores a failure.
func (r *runner) warn(text string) { _, _ = io.WriteString(r.o.Stderr, text) }

// announce writes a line of a long-running command to standard output and ignores a failure, so that a
// closed pipe never takes the server down.
func (r *runner) announce(text string) { _, _ = io.WriteString(r.o.Stdout, text) }

// deskDir is the directory of the built desk from FORGEBASE_DESK_DIR (typically desk/dist), or "" when it
// is unset or empty (no desk is served). It must be a directory that has an index.html, so a wrong path
// fails at startup instead of giving 404s later.
func (r *runner) deskDir() (string, error) {
	dir := r.o.Getenv("FORGEBASE_DESK_DIR")
	if dir == "" {
		return "", nil
	}
	if info, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || !info.Mode().IsRegular() {
		return "", errf("FORGEBASE_DESK_DIR %s has no index.html (build the desk first: cd desk && npm ci && npm run build)", dir)
	}
	return dir, nil
}

// parseAllowedHosts are the host names of FORGEBASE_ALLOWED_HOSTS: comma separated, trimmed, no empty
// entry. An entry with a port can never match (the port of the Host header is ignored), so it is refused
// at startup instead of silently locking everyone out.
func parseAllowedHosts(text string) ([]string, error) {
	var hosts []string
	for _, h := range strings.Split(text, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return nil, errf("FORGEBASE_ALLOWED_HOSTS has no host name")
	}
	for _, host := range hosts {
		if problem := hostProblem(host); problem != "" {
			return nil, errf("FORGEBASE_ALLOWED_HOSTS entry %s %s", rustQuote(host), problem)
		}
	}
	return hosts, nil
}

// listenErrorText is the text of the error of net.Listen as Rust prints the error of bind: the OS error
// with its number, or "invalid socket address" for an address that cannot be parsed.
func listenErrorText(err error) string {
	var addr *net.AddrError
	if errors.As(err, &addr) {
		return "invalid socket address"
	}
	return ioErrorText(err)
}

// stopSignals gives a channel that receives one value per stop signal (SIGINT and SIGTERM), and the
// function that stops the delivery. It also keeps a write to a closed standard output or standard error
// from ending the process (see ignoreBrokenPipe).
func stopSignals() (<-chan struct{}, func()) {
	ignoreBrokenPipe()
	raw := make(chan os.Signal, 4)
	signal.Notify(raw, os.Interrupt, syscall.SIGTERM)
	out := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-raw:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { signal.Stop(raw); close(done) }
}

// httpServer is what runUntilStopped needs of an *http.Server.
type httpServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

// runUntilStopped runs the server until it ends. After the first message on signals it asks the server
// to stop (graceful shutdown) and waits for it at most grace; a second message or the end of grace stops
// waiting and drops the open connections.
func runUntilStopped(srv httpServer, l net.Listener, signals <-chan struct{}, grace time.Duration, stderr io.Writer) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err := <-done:
		return serveError(err)
	case <-signals:
	}
	_, _ = fmt.Fprintf(stderr, "shutting down: waiting up to %d s for open requests (send the signal again to exit now)\n",
		int(grace.Seconds()))
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	shut := make(chan error, 1)
	go func() { shut <- srv.Shutdown(ctx) }()
	var why string
	select {
	case err := <-shut:
		if err == nil {
			return serveError(<-done)
		}
		why = "grace period over"
	case <-signals:
		why = "second signal"
	}
	_, _ = fmt.Fprintf(stderr, "exiting now (%s); open connections are dropped\n", why)
	_ = srv.Close()
	return nil
}

// serveError is nil for the end of a server that was asked to stop.
func serveError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// siteRegistries composes the registry of each site from the apps installed on it (and the --doctypes
// directory local, when given): serve and the tests of its per-site wiring use it.
func (r *runner) siteRegistries(sites []*site, local string) ([]*core.Registry, error) {
	registries := make([]*core.Registry, len(sites))
	for i, site := range sites {
		composed, err := r.siteComposed(site, local)
		if err != nil {
			return nil, err
		}
		registries[i] = composed.registry
	}
	return registries, nil
}

// routeSites is the Host routing of serve: one httpapi.State per opened site, with that site's pool and
// registry, behind one router. With a config file (configured) each site answers to its own hosts and a
// line "site NAME: HOSTS" is returned for the announcement; without one the only site answers to
// implicitHosts. base has the settings that are the same for every site.
func routeSites(configured bool, sites []*siteContext, implicitHosts []string, base httpapi.Config) (*httpapi.Sites, []string, error) {
	var (
		routing *httpapi.Sites
		lines   []string
	)
	if configured {
		routing = httpapi.ConfiguredSites()
	}
	for _, sc := range sites {
		appConfig := base
		appConfig.AllowedHosts = implicitHosts
		if configured {
			appConfig.AllowedHosts = sc.site.hosts
		}
		state := httpapi.NewState(sc.pool, sc.registry, appConfig)
		if configured {
			if err := routing.AddSite(sc.site.hosts, state); err != nil {
				return nil, nil, errf("%s", err)
			}
			lines = append(lines, fmt.Sprintf("site %s: %s\n", sc.site.name, strings.Join(sc.site.hosts, ", ")))
		} else {
			routing = httpapi.ImplicitSites(state)
		}
	}
	return routing, lines, nil
}
