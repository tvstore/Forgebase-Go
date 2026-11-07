package main

import (
	"testing"

	"github.com/tvstore/forgebase-go"
)

// The app fits together: its report is valid (the SQL is checked against a database by `migrate`, and by the
// e2e job of CI, which runs the report through the desk).
func TestTheAppFitsTogether(t *testing.T) {
	if err := forgebase.New().App(e2e()).Check(); err != nil {
		t.Fatal(err)
	}
}
