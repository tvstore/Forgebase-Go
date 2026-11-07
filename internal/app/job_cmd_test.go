package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Unit tests of the job command output (no database). Ported from the #[cfg(test)] module of
// crates/forgebase/src/job_cmd.rs, plus the table and the JSON of a job.

// Rust: job_cmd.rs::an_error_is_shortened_to_one_line
func TestAnErrorIsShortenedToOneLine(t *testing.T) {
	if got := shortError("a\nb"); got != "a b" {
		t.Errorf("%q", got)
	}
	cut := shortError(strings.Repeat("x", 100))
	if n := len([]rune(cut)); n != errorChars || !strings.HasSuffix(cut, "…") {
		t.Errorf("%q", cut)
	}
	if got := shortError(strings.Repeat("é", errorChars)); got != strings.Repeat("é", errorChars) {
		t.Errorf("exactly the limit is kept: %q", got)
	}
}

// Rust: job_cmd.rs::the_payload_must_be_json
func TestTheJobPayloadMustBeJSON(t *testing.T) {
	v, err := parseJobPayload(" {\"a\": 1}\n")
	if err != nil || v.(map[string]any)["a"] != json.Number("1") {
		t.Errorf("%v %v", v, err)
	}
	for _, bad := range []string{"{", "", "}", "{} {}", "nope"} {
		if _, err := parseJobPayload(bad); err == nil || !strings.Contains(err.Error(), "the payload is not valid JSON: ") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A number keeps its digits.
	v, _ = parseJobPayload(`{"n": 12345678901234567890}`)
	if text, _ := marshalForTest(v); text != `{"n":12345678901234567890}` {
		t.Errorf("%s", text)
	}
}

func marshalForTest(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func jobFixture() db.Job {
	at := time.Date(2026, 11, 7, 10, 0, 0, 123456000, time.UTC)
	boom := "boom\nsecond line"
	return db.Job{
		ID: uuid.MustParse("01a1b674-93fb-771f-a41d-c9675c0c6221"), Name: "crm.touch", Payload: json.RawMessage(`{"b": 1, "a": {"z": 1, "y": 2}}`),
		Status: "dead", Attempts: 2, RunAt: at, LastError: &boom, EnqueuedBy: "ann", CreatedAt: at, FinishedAt: &at,
	}
}

func TestTheJobTableHasAlignedColumnsAndNoTrailingSpaces(t *testing.T) {
	job := jobFixture()
	other := jobFixture()
	other.Name = "a.b"
	other.LastError = nil
	got := jobTable([]db.Job{job, other})
	want := "ID                                    NAME       STATUS  ATTEMPTS  RUN_AT                       LAST_ERROR\n" +
		"01a1b674-93fb-771f-a41d-c9675c0c6221  crm.touch  dead    2         2026-11-07T10:00:00.123456Z  boom second line\n" +
		"01a1b674-93fb-771f-a41d-c9675c0c6221  a.b        dead    2         2026-11-07T10:00:00.123456Z\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := jobTable(nil); got != "ID  NAME  STATUS  ATTEMPTS  RUN_AT  LAST_ERROR\n" {
		t.Errorf("%q", got)
	}
}

func TestTheJobJSONHasEveryFieldWithSortedKeys(t *testing.T) {
	job := jobFixture()
	text, err := prettyJSON(jobJSON(&job))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "attempts": 2,
  "created_at": "2026-11-07T10:00:00.123456Z",
  "enqueued_by": "ann",
  "finished_at": "2026-11-07T10:00:00.123456Z",
  "id": "01a1b674-93fb-771f-a41d-c9675c0c6221",
  "last_error": "boom\nsecond line",
  "locked_by": null,
  "locked_until": null,
  "name": "crm.touch",
  "payload": {
    "a": {
      "y": 2,
      "z": 1
    },
    "b": 1
  },
  "run_at": "2026-11-07T10:00:00.123456Z",
  "status": "dead"
}`
	if text != want {
		t.Errorf("got:\n%s\nwant:\n%s", text, want)
	}
}

func TestOnlyEnqueueStripsThePrefixOfARefusedJob(t *testing.T) {
	refused := &db.Error{Kind: db.ErrJob, Message: "unknown job `x`"}
	if got := enqueueError(refused).Error(); got != "unknown job `x`" {
		t.Errorf("%q", got)
	}
	other := &db.Error{Kind: db.ErrNotFound, DocType: "a", Name: "b"}
	if enqueueError(other) != error(other) {
		t.Error("another error was changed")
	}
	// The other commands print the database error as it is.
	notID := &db.Error{Kind: db.ErrJob, Message: "`x` is not a job id (a UUID)"}
	if got := describeError(notID); got != "job: `x` is not a job id (a UUID)" {
		t.Errorf("%q", got)
	}
}
