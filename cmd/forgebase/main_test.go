package main

import (
	"bytes"
	"testing"
)

func TestVersion(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		var out, errOut bytes.Buffer
		if code := run([]string{flag}, &out, &errOut); code != 0 {
			t.Fatalf("%s: exit code %d, want 0", flag, code)
		}
		if got, want := out.String(), "forgebase 0.0.0\n"; got != want {
			t.Fatalf("%s: stdout %q, want %q", flag, got, want)
		}
		if errOut.Len() != 0 {
			t.Fatalf("%s: unexpected stderr %q", flag, errOut.String())
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"--version", "extra"}, {"--bogus"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: exit code %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Fatalf("%v: unexpected stdout %q", args, out.String())
		}
		if errOut.Len() == 0 {
			t.Fatalf("%v: usage must be printed to stderr", args)
		}
	}
}
