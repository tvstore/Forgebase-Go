package app

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The built-in jobs and the jobs and schedules a composition registers. Not ported from a Rust unit test (builtin.rs
// and the job part of compose.rs have none, their behaviour is tested through the binaries): these pin the names,
// the specs and the registration rules the Rust tests rely on.

func TestTheBuiltInJobsAreTheThreeCleanUpsWithTheirSchedules(t *testing.T) {
	got := map[string]db.Spec{}
	for _, b := range builtinJobs() {
		if b.spec == nil {
			t.Errorf("%s has no schedule", b.def.name)
			continue
		}
		got[b.def.name] = *b.spec
		if b.def.options != NewJobOptions() || b.def.handler == nil {
			t.Errorf("%s: %+v", b.def.name, b.def.options)
		}
	}
	want := map[string]db.Spec{
		"forgebase.purge_jobs":           db.DailyAt(3, 0),
		"forgebase.purge_login_failures": db.EveryMinutes(60),
		"forgebase.purge_sessions":       db.EveryMinutes(60),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func TestACompositionRegistersJobsAndSchedulesByFullName(t *testing.T) {
	a := NewApp("crm").
		Job("welcome", noop).
		JobWith("sync", noop, NewJobOptions().MaxAttempts(2)).
		Job("Bad-Name", noop).
		JobWith("tuned", noop, NewJobOptions().MaxAttempts(0)).
		Schedule("welcome", DailyAt(6, 30)).
		Schedule("sync", EveryMinutes(0))
	if _, startup := compose([]*App{a}, ""); startup == nil {
		t.Fatal("the invalid ones are accepted")
	}

	good := NewApp("crm").Job("welcome", noop).JobWith("sync", noop, NewJobOptions().MaxAttempts(2)).
		Schedule("welcome", DailyAt(6, 30))
	c, startup := compose([]*App{good}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	var names []string
	for name := range c.jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"crm.sync", "crm.welcome", "forgebase.purge_jobs", "forgebase.purge_login_failures", "forgebase.purge_sessions"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("%v", names)
	}
	for _, name := range want {
		if !c.registry.HasJob(name) {
			t.Errorf("the registry does not know %s", name)
		}
	}
	if c.jobs["crm.sync"].options.maxAttempts != 2 || c.jobs["crm.sync"].name != "crm.sync" {
		t.Errorf("%+v", c.jobs["crm.sync"])
	}
	if got := c.schedules["crm.welcome"]; got != db.DailyAt(6, 30) {
		t.Errorf("%+v", got)
	}
	if len(c.schedules) != 4 {
		t.Errorf("%v", c.schedules)
	}
	if c.registry.HasJob("crm.nobody") {
		t.Error("an unknown job is known")
	}
}

func TestEveryAppOfABinaryHasTheBuiltInJobs(t *testing.T) {
	c, startup := compose(nil, "")
	if startup != nil {
		t.Fatal(startup)
	}
	if len(c.jobs) != 3 || len(c.schedules) != 3 {
		t.Errorf("%d jobs, %d schedules", len(c.jobs), len(c.schedules))
	}
}

func TestAnInvalidJobTimeoutOrScheduleIsNotRegistered(t *testing.T) {
	a := NewApp("crm").
		JobWith("slow", noop, NewJobOptions().Timeout(48*time.Hour)).
		Job("daily", noop).
		Schedule("daily", DailyAt(24, 0))
	_, startup := compose([]*App{a}, "")
	if startup == nil || len(startup.Problems()) != 2 {
		t.Fatalf("%v", startup)
	}
}
