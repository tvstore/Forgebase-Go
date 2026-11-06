package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// Sites and the config file (M7.1, ADR 0014; the Rust module config.rs).
//
// A site is one database and the host names that reach it. Without a config file there is one
// implicit site called `default`, whose database URL is FORGEBASE_DATABASE_URL. With a config file
// (--config PATH or FORGEBASE_CONFIG; never discovered implicitly) the sites are listed in it, and
// each names the environment variable that holds its database URL: the file has no secrets.
//
// A site may list the apps installed on it (M7.2): a subset of the apps compiled into the binary,
// closed under depends_on. Without the key the site has all of them. The stock binary has no apps,
// so every name is unknown; the app model arrives in P4 (binaryApp is its seam).
//
// There is no process-global pool or URL. A command picks exactly one site (serve picks all of
// them), turns it into a siteContext (its own pool and the registry of its apps) and passes that down.

const (
	// implicitURLVar is the environment variable of the implicit site's database URL. This is the
	// only place that names it (a test checks that).
	implicitURLVar = "FORGEBASE_DATABASE_URL"
	// implicitSite is the name of the implicit site.
	implicitSite = "default"
	// configVar is the environment variable with the path of the config file (the option wins).
	configVar = "FORGEBASE_CONFIG"
	// maxPoolSize is the largest pool_size of a site.
	maxPoolSize = 256
)

// binaryApp is what the config file needs to know of an app compiled into the binary: its name and
// the apps it depends on.
type binaryApp struct {
	name      string
	dependsOn []string
}

// site is one site: a name, the hosts that reach it and where its database URL comes from.
type site struct {
	name string
	// hosts are lower-cased host names without a port. Empty for the implicit site (see serve).
	hosts []string
	// databaseURLEnv is the environment variable that holds the PostgreSQL URL.
	databaseURLEnv string
	// poolSize is the pool maximum, or 0 for the default of db.ConnectSized.
	poolSize int
	// apps are the installed apps as listed in the config file; appsListed false means every app
	// of the binary.
	apps       []string
	appsListed bool
}

// implicitSiteValue is the implicit site of a setup without a config file.
func implicitSiteValue() *site {
	return &site{name: implicitSite, databaseURLEnv: implicitURLVar}
}

// databaseURL reads the database URL from the environment when the site is used. The error names
// the site and the variable, never a value.
func (s *site) databaseURL(getenv func(string) string) (string, error) {
	if url := getenv(s.databaseURLEnv); url != "" {
		return url, nil
	}
	if s.name == implicitSite && s.databaseURLEnv == implicitURLVar {
		return "", errf("%s is not set; set it to the PostgreSQL URL, for example postgres://user:password@localhost:5432/database", implicitURLVar)
	}
	return "", errf("site `%s`: the environment variable %s is not set or is empty; set it to the PostgreSQL URL of the site",
		s.name, s.databaseURLEnv)
}

// siteContext is everything a command needs for one site: the site, its pool and its registry,
// which holds only the DocTypes of the apps installed on the site (and, from P7, the site's custom
// fields).
type siteContext struct {
	site     *site
	pool     *pgxpool.Pool
	registry *core.Registry
}

// open opens the pool of the site (poolSize overrides the configured size) and joins it with
// registry, the registry of the site's apps. This is the only way a command gets a database
// connection. A connection error names the site (and never the URL), so that serve with several
// sites says which one failed.
func (s *site) open(ctx context.Context, getenv func(string) string, registry *core.Registry, poolSize int) (*siteContext, error) {
	url, err := s.databaseURL(getenv)
	if err != nil {
		return nil, err
	}
	if poolSize <= 0 {
		poolSize = s.poolSize
	}
	pool, err := db.ConnectSized(ctx, url, poolSize)
	if err != nil {
		return nil, errSite(s.name, err)
	}
	return &siteContext{site: s, pool: pool, registry: registry}, nil
}

func (c *siteContext) close() { c.pool.Close() }

// config is the sites of this run: those of the config file, or the implicit default.
type config struct {
	sites      []*site
	configured bool
}

// implicitConfig is the setup without a config file: one implicit site.
func implicitConfig() *config {
	return &config{sites: []*site{implicitSiteValue()}}
}

// loadConfig reads the config file named by --config or FORGEBASE_CONFIG (the option wins), else
// the implicit single site. The current directory is never searched.
func loadConfig(option string, getenv func(string) string, apps []binaryApp) (*config, error) {
	path := option
	if path == "" {
		path = getenv(configVar)
	}
	if path == "" {
		return implicitConfig(), nil
	}
	return readConfig(path, apps)
}

// readConfig reads and validates the config file at path against the apps of the binary. Every
// validation problem is in the error.
func readConfig(path string, apps []binaryApp) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errf("cannot read the config file %s: %s", path, ioErrorText(err))
	}
	c, message := parseConfig(string(data), apps)
	if message != "" {
		return nil, errf("invalid config file %s:\n%s", path, message)
	}
	return c, nil
}

// parseConfig parses and validates config text. The message holds every problem; it is empty when
// the text is valid.
func parseConfig(text string, apps []binaryApp) (*config, string) {
	raw, err := decodeConfig(text)
	if err != nil {
		return nil, bullets([]string{err.Error()})
	}
	sites, problems := validateConfig(raw, apps)
	if len(problems) > 0 {
		return nil, bullets(problems)
	}
	return &config{sites: sites, configured: true}, ""
}

func (c *config) names() string {
	names := make([]string, len(c.sites))
	for i, s := range c.sites {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

func (c *config) find(name string) (*site, error) {
	for _, s := range c.sites {
		if s.name == name {
			return s, nil
		}
	}
	if c.configured {
		return nil, errUsage(fmt.Sprintf("unknown site `%s`; the sites of the config file are: %s", name, c.names()))
	}
	return nil, errUsage(fmt.Sprintf("unknown site `%s`; without a config file (--config) the only site is `%s`", name, implicitSite))
}

// select is the one site of a database command. --site is required when the config file has more
// than one site, and optional (the only site) otherwise.
func (c *config) selectSite(name string, named bool) (*site, error) {
	switch {
	case named:
		return c.find(name)
	case len(c.sites) == 1:
		return c.sites[0], nil
	}
	return nil, errUsage(fmt.Sprintf("the config file has several sites, so this command needs --site NAME; the sites are: %s", c.names()))
}

// serveSet is the sites serve serves: the one named by --site, else every site.
func (c *config) serveSet(name string, named bool) ([]*site, error) {
	if named {
		s, err := c.find(name)
		if err != nil {
			return nil, err
		}
		return []*site{s}, nil
	}
	return c.sites, nil
}

// rawSite is one [[sites]] table as the file has it. Missing keys read as empty, so that validation
// reports them with the other problems.
type rawSite struct {
	name           string
	hosts          []string
	databaseURLEnv string
	poolSize       *int64
	apps           []string
	appsListed     bool
}

type rawConfig struct {
	sites []rawSite
}

// decodeConfig reads the TOML text with the rules of the Rust serde structs (deny_unknown_fields,
// the value types): the generic tree is checked key by key, because decoding into Go structs would
// match keys without regard to case and accept unknown ones (the same choice as internal/core/load.go).
func decodeConfig(text string) (*rawConfig, error) {
	var root map[string]any
	if _, err := toml.Decode(text, &root); err != nil {
		return nil, err
	}
	if err := unknownKeys(root, "sites"); err != nil {
		return nil, err
	}
	raw := &rawConfig{}
	value, ok := root["sites"]
	if !ok {
		return raw, nil
	}
	var tables []map[string]any
	switch v := value.(type) {
	case []map[string]any:
		tables = v
	case []any:
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid type: %s, expected a map", describeTOML(item))
			}
			tables = append(tables, m)
		}
	default:
		return nil, fmt.Errorf("invalid type: %s, expected a sequence", describeTOML(value))
	}
	for _, table := range tables {
		site, err := decodeSite(table)
		if err != nil {
			return nil, err
		}
		raw.sites = append(raw.sites, site)
	}
	return raw, nil
}

func decodeSite(table map[string]any) (rawSite, error) {
	var site rawSite
	if err := unknownKeys(table, "name", "hosts", "database_url_env", "pool_size", "apps"); err != nil {
		return site, err
	}
	var err error
	if v, ok := table["name"]; ok {
		if site.name, err = asString(v); err != nil {
			return site, err
		}
	}
	if v, ok := table["hosts"]; ok {
		if site.hosts, err = asStrings(v); err != nil {
			return site, err
		}
	}
	if v, ok := table["database_url_env"]; ok {
		if site.databaseURLEnv, err = asString(v); err != nil {
			return site, err
		}
	}
	if v, ok := table["pool_size"]; ok {
		n, ok := v.(int64)
		if !ok {
			return site, fmt.Errorf("invalid type: %s, expected i64", describeTOML(v))
		}
		site.poolSize = &n
	}
	if v, ok := table["apps"]; ok {
		site.appsListed = true
		if site.apps, err = asStrings(v); err != nil {
			return site, err
		}
	}
	return site, nil
}

// unknownKeys is serde's deny_unknown_fields: the first key (in sorted order) that is not allowed.
func unknownKeys(table map[string]any, allowed ...string) error {
	var unknown []string
	for key := range table {
		found := false
		for _, a := range allowed {
			found = found || a == key
		}
		if !found {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "`" + a + "`"
	}
	expected := "expected one of " + strings.Join(quoted, ", ")
	if len(allowed) == 1 {
		expected = "expected " + quoted[0]
	}
	return fmt.Errorf("unknown field `%s`, %s", unknown[0], expected)
}

func asString(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid type: %s, expected a string", describeTOML(v))
	}
	return s, nil
}

func asStrings(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		if strs, isStrs := v.([]string); isStrs {
			return strs, nil
		}
		return nil, fmt.Errorf("invalid type: %s, expected a sequence", describeTOML(v))
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, err := asString(item)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// describeTOML is serde's "invalid type" description of a TOML value.
func describeTOML(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("string %q", x)
	case int64:
		return fmt.Sprintf("integer `%d`", x)
	case float64:
		return fmt.Sprintf("floating point `%v`", x)
	case bool:
		return fmt.Sprintf("boolean `%v`", x)
	case map[string]any:
		return "map"
	case []any, []map[string]any, []string:
		return "sequence"
	}
	return fmt.Sprintf("%T", v)
}

func validateConfig(raw *rawConfig, binaryApps []binaryApp) ([]*site, []string) {
	var problems []string
	if len(raw.sites) == 0 {
		problems = append(problems, "no sites: add at least one [[sites]] table")
	}
	var sites []*site
	for index, r := range raw.sites {
		label := fmt.Sprintf("site #%d", index+1)
		if r.name != "" {
			label = fmt.Sprintf("site `%s`", r.name)
		}
		if !validSiteName(r.name) {
			problems = append(problems, fmt.Sprintf("%s: the name must be 1 to 63 characters: lower-case ASCII letters, digits and underscores, starting with a letter", label))
		} else if siteNamed(sites, r.name) != nil {
			problems = append(problems, fmt.Sprintf("%s: the site name is used twice", label))
		}
		if len(r.hosts) == 0 {
			problems = append(problems, fmt.Sprintf("%s: `hosts` must list at least one host name", label))
		}
		var hosts []string
		for _, host := range r.hosts {
			host = strings.ToLower(host)
			if problem := hostProblem(host); problem != "" {
				problems = append(problems, fmt.Sprintf("%s: host %s %s", label, rustQuote(host), problem))
			} else if other := siteWithHost(sites, host); other != nil {
				problems = append(problems, fmt.Sprintf("%s: host %s is already used by site `%s`", label, rustQuote(host), other.name))
			} else if !containsString(hosts, host) {
				hosts = append(hosts, host)
			}
		}
		if !validEnvName(r.databaseURLEnv) {
			problems = append(problems, fmt.Sprintf("%s: `database_url_env` must be the name of an environment variable (letters, digits and underscores, not starting with a digit)", label))
		}
		poolSize := 0
		if r.poolSize != nil {
			n := *r.poolSize
			if n >= 1 && n <= maxPoolSize {
				poolSize = int(n)
			} else {
				problems = append(problems, fmt.Sprintf("%s: `pool_size` is %d, it must be 1 to %d", label, n, maxPoolSize))
			}
		}
		if r.appsListed {
			problems = append(problems, appsProblems(label, r.apps, binaryApps)...)
		}
		sites = append(sites, &site{
			name: r.name, hosts: hosts, databaseURLEnv: r.databaseURLEnv, poolSize: poolSize,
			apps: r.apps, appsListed: r.appsListed,
		})
	}
	return sites, problems
}

func siteNamed(sites []*site, name string) *site {
	for _, s := range sites {
		if s.name == name {
			return s
		}
	}
	return nil
}

func siteWithHost(sites []*site, host string) *site {
	for _, s := range sites {
		if containsString(s.hosts, host) {
			return s
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// rustQuote is a string as Rust's {:?} writes it, for the host names the messages repeat.
func rustQuote(s string) string { return fmt.Sprintf("%q", s) }

// appsProblems is what is wrong with the apps of a site: an app the binary does not have, an app
// listed twice, or an app whose dependency is not listed. Dependencies are never added
// automatically: the list says what is installed.
func appsProblems(label string, listed []string, binary []binaryApp) []string {
	var problems []string
	var seen []string
	for _, name := range listed {
		if containsString(seen, name) {
			problems = append(problems, fmt.Sprintf("%s: app `%s` is listed twice in `apps`", label, name))
			continue
		}
		seen = append(seen, name)
		var app *binaryApp
		for i := range binary {
			if binary[i].name == name {
				app = &binary[i]
			}
		}
		if app == nil {
			known := "none"
			if len(binary) > 0 {
				names := make([]string, len(binary))
				for i, b := range binary {
					names[i] = b.name
				}
				known = strings.Join(names, ", ")
			}
			problems = append(problems, fmt.Sprintf("%s: unknown app `%s` in `apps`; the apps of this binary are: %s", label, name, known))
			continue
		}
		for _, dependency := range app.dependsOn {
			if !containsString(listed, dependency) {
				problems = append(problems, fmt.Sprintf("%s: app `%s` needs `%s`, which is not in `apps`", label, name, dependency))
			}
		}
	}
	return problems
}

// validSiteName is the rule of a site name: [a-z][a-z0-9_]*, at most 63 bytes.
func validSiteName(name string) bool {
	if len(name) == 0 || len(name) > 63 || !(name[0] >= 'a' && name[0] <= 'z') {
		return false
	}
	for i := 1; i < len(name); i++ {
		b := name[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_') {
			return false
		}
	}
	return true
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		alpha := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
		if !(alpha || i > 0 && b >= '0' && b <= '9') {
			return false
		}
	}
	return true
}

// hostProblem is why host cannot be a host name of a site, or "" when it can. It is the rule that
// request routing applies to the Host header (httpapi.IsWellformedHost), so a host that could never
// match a request is refused here. The same rule applies to FORGEBASE_ALLOWED_HOSTS. A port is not
// part of a host name (the port of the Host header is ignored), and IPv6 is written in brackets.
func hostProblem(host string) string {
	if httpapi.IsWellformedHost(host) {
		return ""
	}
	return "must be a host name without a port (the port of the Host header is ignored): letters, digits, `-`, `.` and `_`, or IPv6 in brackets such as [::1]"
}
