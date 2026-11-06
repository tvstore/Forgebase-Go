package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from crates/forgebase/src/config.rs (the unit tests of the module).

const goodConfig = `
[[sites]]
name = "acme"
hosts = ["Acme.Example.com", "acme.localhost"]
database_url_env = "ACME_DATABASE_URL"
pool_size = 4

[[sites]]
name = "beta"
hosts = ["beta.localhost"]
database_url_env = "BETA_DATABASE_URL"
`

// testApps are the apps of the test binary: sales needs crm, sales_extension needs sales (and so
// crm), tools stands alone.
func testApps() []binaryApp {
	return []binaryApp{
		{name: "crm"},
		{name: "sales", dependsOn: []string{"crm"}},
		{name: "sales_extension", dependsOn: []string{"sales"}},
		{name: "tools"},
	}
}

func withApps(list string) string {
	return "[[sites]]\nname = \"a\"\nhosts = [\"a.test\"]\ndatabase_url_env = \"A_URL\"\napps = " + list + "\n"
}

func mustParse(t *testing.T, text string, apps []binaryApp) *config {
	t.Helper()
	c, message := parseConfig(text, apps)
	if message != "" {
		t.Fatalf("valid config refused:\n%s", message)
	}
	return c
}

func problems(t *testing.T, text string) string {
	t.Helper()
	c, message := parseConfig(text, testApps())
	if message == "" {
		t.Fatalf("must be refused: %+v", c)
	}
	return message
}

// Rust: config.rs::a_valid_file_is_read
func TestAValidFileIsRead(t *testing.T) {
	c := mustParse(t, goodConfig, nil)
	if !c.configured || c.names() != "acme, beta" {
		t.Fatalf("configured=%v names=%q", c.configured, c.names())
	}
	acme, err := c.selectSite("acme", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(acme.hosts, ",") != "acme.example.com,acme.localhost" || acme.poolSize != 4 {
		t.Fatalf("%+v", acme)
	}
	beta, _ := c.find("beta")
	if beta.poolSize != 0 {
		t.Fatalf("pool size of beta: %d", beta.poolSize)
	}
}

// Rust: config.rs::site_selection_follows_the_number_of_sites
func TestSiteSelectionFollowsTheNumberOfSites(t *testing.T) {
	two := mustParse(t, goodConfig, nil)
	_, err := two.selectSite("", false)
	if err == nil || !strings.Contains(err.Error(), "--site") || !strings.Contains(err.Error(), "acme, beta") || !isUsage(err) {
		t.Fatalf("several sites need --site: %v", err)
	}
	if _, err := two.selectSite("nope", true); !isUsage(err) {
		t.Fatalf("an unknown site is a usage error: %v", err)
	}
	all, _ := two.serveSet("", false)
	one, _ := two.serveSet("beta", true)
	if len(all) != 2 || len(one) != 1 {
		t.Fatalf("serve set: %d and %d", len(all), len(one))
	}

	single := mustParse(t, "[[sites]]\nname = \"a\"\nhosts = [\"a.test\"]\ndatabase_url_env = \"A_URL\"\n", nil)
	if s, err := single.selectSite("", false); err != nil || s.name != "a" {
		t.Fatalf("the only site: %v %v", s, err)
	}

	none := implicitConfig()
	if none.configured {
		t.Fatal("the implicit config is not configured")
	}
	if s, err := none.selectSite("", false); err != nil || s.name != "default" {
		t.Fatalf("default: %v %v", s, err)
	}
	if s, err := none.selectSite("default", true); err != nil || s.name != "default" {
		t.Fatalf("default by name: %v %v", s, err)
	}
	if _, err := none.selectSite("acme", true); err == nil || !strings.Contains(err.Error(), "only site is `default`") {
		t.Fatalf("%v", err)
	}
}

// Rust: config.rs::every_problem_is_reported_together
func TestEveryProblemIsReportedTogether(t *testing.T) {
	message := problems(t, `
[[sites]]
name = "Bad Name"
hosts = []
database_url_env = "1X"
pool_size = 0

[[sites]]
name = "a"
hosts = ["a.test", "a.test:8000", "[::1"]
database_url_env = "A URL"
pool_size = 9999

[[sites]]
name = "a"
hosts = ["A.TEST"]
database_url_env = "A_URL"

[[sites]]
hosts = ["x.test"]
database_url_env = "X_URL"
`)
	for _, expected := range []string{
		"site `Bad Name`: the name must be",
		"site `Bad Name`: `hosts` must list at least one",
		"site `Bad Name`: `database_url_env` must be",
		"site `Bad Name`: `pool_size` is 0",
		"site `a`: host \"a.test:8000\" must be a host name without a port",
		"site `a`: host \"[::1\" must be a host name without a port",
		"site `a`: `database_url_env` must be",
		"site `a`: `pool_size` is 9999",
		"site `a`: the site name is used twice",
		"site `a`: host \"a.test\" is already used by site `a`",
		"site #4: the name must be",
	} {
		if !strings.Contains(message, expected) {
			t.Errorf("missing %q in:\n%s", expected, message)
		}
	}
}

func installed(c *config, name string, binary []binaryApp) []string {
	s, _ := c.find(name)
	var out []string
	for _, a := range binary {
		if !s.appsListed || containsString(s.apps, a.name) {
			out = append(out, a.name)
		}
	}
	return out
}

// Rust: config.rs::apps_default_to_all_and_may_be_empty_or_a_closed_subset
func TestAppsDefaultToAllAndMayBeEmptyOrAClosedSubset(t *testing.T) {
	binary := testApps()
	absent := mustParse(t, goodConfig, binary)
	if got := strings.Join(installed(absent, "acme", binary), ","); got != "crm,sales,sales_extension,tools" {
		t.Fatalf("all apps by default: %s", got)
	}
	if s, _ := absent.find("acme"); s.appsListed {
		t.Fatal("no apps key means every app")
	}
	// Listed in any order; installed in the order of the binary.
	closed := mustParse(t, withApps(`["tools", "sales", "crm"]`), binary)
	if got := strings.Join(installed(closed, "a", binary), ","); got != "crm,sales,tools" {
		t.Fatalf("closed subset: %s", got)
	}
	// An empty list is allowed: the site has only the framework.
	none := mustParse(t, withApps("[]"), binary)
	if got := installed(none, "a", binary); len(got) != 0 {
		t.Fatalf("empty list: %v", got)
	}
}

// Rust: config.rs::apps_are_checked_and_every_problem_names_the_site
func TestAppsAreCheckedAndEveryProblemNamesTheSite(t *testing.T) {
	message := problems(t, withApps(`["sales_extension", "nope", "tools", "tools"]`))
	for _, expected := range []string{
		"site `a`: app `sales_extension` needs `sales`, which is not in `apps`",
		"site `a`: unknown app `nope` in `apps`; the apps of this binary are: crm, sales, sales_extension, tools",
		"site `a`: app `tools` is listed twice in `apps`",
	} {
		if !strings.Contains(message, expected) {
			t.Errorf("missing %q in:\n%s", expected, message)
		}
	}
	// Dependencies are never added for the operator: sales without crm is refused.
	if message := problems(t, withApps(`["sales"]`)); !strings.Contains(message, "site `a`: app `sales` needs `crm`, which is not in `apps`") {
		t.Errorf("%s", message)
	}
	// local and the framework are not apps of the binary.
	message = problems(t, withApps(`["local", "forgebase"]`))
	if !strings.Contains(message, "unknown app `local`") || !strings.Contains(message, "unknown app `forgebase`") {
		t.Errorf("%s", message)
	}
	// With a binary that has no apps every name is unknown.
	_, message = parseConfig(withApps(`["x"]`), nil)
	if !strings.Contains(message, "are: none") {
		t.Errorf("%s", message)
	}
}

// Rust: config.rs::a_host_listed_by_two_sites_names_both
func TestAHostListedByTwoSitesNamesBoth(t *testing.T) {
	message := problems(t, `
[[sites]]
name = "a"
hosts = ["x.test"]
database_url_env = "A_URL"
[[sites]]
name = "b"
hosts = ["X.test"]
database_url_env = "B_URL"
`)
	if !strings.Contains(message, "site `b`: host \"x.test\" is already used by site `a`") {
		t.Errorf("%s", message)
	}
}

// Rust: config.rs::no_sites_and_unknown_keys_are_refused
func TestNoSitesAndUnknownKeysAreRefused(t *testing.T) {
	if !strings.Contains(problems(t, ""), "no sites") || !strings.Contains(problems(t, "sites = []"), "no sites") {
		t.Fatal("an empty file has no sites")
	}
	for _, text := range []string{
		"bogus = 1\n[[sites]]\nname = \"a\"",
		"[[sites]]\nname = \"a\"\nhosts = [\"a.test\"]\ndatabase_url = \"postgres://u:p@h/d\"\n",
	} {
		if message := problems(t, text); !strings.Contains(message, "unknown field") {
			t.Errorf("%q: %s", text, message)
		}
	}
	if _, message := parseConfig("not toml [", nil); message == "" {
		t.Fatal("invalid TOML is refused")
	}
	// A wrong value type is refused with the key's problem, not accepted.
	for _, text := range []string{
		"[[sites]]\nname = 5\n",
		"[[sites]]\nname = \"a\"\nhosts = \"a.test\"\n",
		"[[sites]]\nname = \"a\"\npool_size = \"4\"\n",
		"sites = \"x\"\n",
	} {
		if message := problems(t, text); !strings.Contains(message, "invalid type") {
			t.Errorf("%q: %s", text, message)
		}
	}
}

// Rust: config.rs::the_value_of_the_variable_is_never_in_a_message
func TestTheValueOfTheVariableIsNeverInAMessage(t *testing.T) {
	s := &site{name: "acme", hosts: []string{"acme.test"}, databaseURLEnv: "ACME_DATABASE_URL"}
	secret := "postgres://user:hunter2@host/db"
	set := func(name string) string {
		if name == "ACME_DATABASE_URL" {
			return secret
		}
		return ""
	}
	if got, err := s.databaseURL(set); err != nil || got != secret {
		t.Fatalf("set: %q %v", got, err)
	}
	// Unset, empty, or only another variable set: the error names the site and the variable.
	other := func(name string) string {
		if name == "OTHER" {
			return secret
		}
		return ""
	}
	for _, getenv := range []func(string) string{other, func(string) string { return "" }} {
		_, err := s.databaseURL(getenv)
		if err == nil || !strings.Contains(err.Error(), "site `acme`") || !strings.Contains(err.Error(), "ACME_DATABASE_URL") || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("%v", err)
		}
	}
	// The implicit site has its own message.
	_, err := implicitSiteValue().databaseURL(func(string) string { return "" })
	if err == nil || !strings.HasPrefix(err.Error(), "FORGEBASE_DATABASE_URL is not set; set it to the PostgreSQL URL") {
		t.Fatalf("%v", err)
	}
	// A bad config file never echoes the environment either: validation does not read it.
	if strings.Contains(problems(t, "[[sites]]\nname = \"a\""), "hunter2") {
		t.Fatal("a message echoed the secret")
	}
}

// Rust: config.rs::host_rules_match_the_allowed_hosts_rules
func TestHostRulesMatchTheAllowedHostsRules(t *testing.T) {
	for _, good := range []string{"localhost", "127.0.0.1", "[::1]", "erp.example.com"} {
		if p := hostProblem(good); p != "" {
			t.Errorf("%q: %s", good, p)
		}
	}
	// The same rule as request routing: a host that could never match is refused.
	for _, bad := range []string{"", "a:80", "[::1]:80", "::1", "a b", "[::1", "é.test", "a/b", "a@b", "[]", "a,b", "*.x"} {
		if hostProblem(bad) == "" {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// Rust: config.rs::nothing_but_the_implicit_site_reads_the_database_url_variable. Only the implicit
// site's constant names FORGEBASE_DATABASE_URL in code (the help text of the root command, which
// documents it, is data in cli_commands.go), and only config.go calls databaseURL.
func TestNothingButTheImplicitSiteReadsTheDatabaseURLVariable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || path == "cli_commands.go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if strings.Contains(code, "FORGEBASE_DATABASE_URL") {
				// The constant, and the message of the missing variable (which prints the constant's value).
				if path != "config.go" || !(strings.Contains(code, "implicitURLVar = ") || strings.Contains(code, "is not set; set it to the PostgreSQL URL")) {
					t.Errorf("%s:%d: %s", path, number+1, line)
				}
				if strings.Contains(code, "implicitURLVar = ") {
					seen++
				}
			}
			if strings.Contains(code, ".databaseURL(") && path != "config.go" {
				t.Errorf("%s:%d: %s", path, number+1, line)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("the constant of the implicit site: %d", seen)
	}
}

func TestLoadConfigReadsTheOptionBeforeTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	option := filepath.Join(dir, "option.toml")
	env := filepath.Join(dir, "env.toml")
	for path, name := range map[string]string{option: "from_option", env: "from_env"} {
		text := "[[sites]]\nname = \"" + name + "\"\nhosts = [\"" + name + ".test\"]\ndatabase_url_env = \"X_URL\"\n"
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	getenv := func(name string) string {
		if name == "FORGEBASE_CONFIG" {
			return env
		}
		return ""
	}
	c, err := loadConfig(option, getenv, nil)
	if err != nil || c.names() != "from_option" {
		t.Fatalf("the option wins: %v %v", c, err)
	}
	c, err = loadConfig("", getenv, nil)
	if err != nil || c.names() != "from_env" {
		t.Fatalf("the environment is the fallback: %v %v", c, err)
	}
	c, err = loadConfig("", func(string) string { return "" }, nil)
	if err != nil || c.configured || c.names() != "default" {
		t.Fatalf("no config means the implicit site: %v %v", c, err)
	}
	_, err = loadConfig(filepath.Join(dir, "missing.toml"), getenv, nil)
	var ce *cliError
	if err == nil || !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), "cannot read the config file ") {
		t.Fatalf("%v", err)
	}
}
