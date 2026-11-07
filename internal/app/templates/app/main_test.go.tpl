package main

import "testing"

func TestTheAppsFitTogether(t *testing.T) {
	if err := apps().Check(); err != nil {
		t.Fatal(err)
	}
}

func TestTheBinaryRunsItsCommandLine(t *testing.T) {
	if code := apps().RunArgs("--version"); code != 0 {
		t.Errorf("--version: exit code %d", code)
	}
	// A usage error is exit code 2, and nothing is started.
	if code := apps().RunArgs("no-such-command"); code != 2 {
		t.Errorf("an unknown command: exit code %d, want 2", code)
	}
}
