package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mit = `MIT License

Copyright (c) 2026 Someone

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND.`

const isc = `ISC License

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.`

const apache = `
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/`

const bsd2 = `Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation.`

const bsd3 = bsd2 + `

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.`

const bsd4 = bsd3 + `

4. All advertising materials mentioning features or use of this software
   must display the following acknowledgement.`

func TestClassify(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"MIT", mit, "MIT"},
		{"ISC", isc, "ISC"},
		{"Apache", apache, "Apache-2.0"},
		{"BSD-2", bsd2, "BSD-2-Clause"},
		{"BSD-3", bsd3, "BSD-3-Clause"},
		{"BSD-3 re-wrapped", strings.ReplaceAll(bsd3, "\n", "  \n  "), "BSD-3-Clause"},
		{"BSD-4 not allowed", bsd4, unknown},
		{"MIT and ISC in one file", mit + "\n" + isc, unknown},
		{"GPL", "GNU GENERAL PUBLIC LICENSE Version 3, 29 June 2007", unknown},
		{"empty", "", unknown},
	}
	for _, c := range cases {
		if got := classify(c.text); got != c.want {
			t.Errorf("%s: classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAllowList(t *testing.T) {
	for _, id := range []string{"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC"} {
		if !allowed[id] {
			t.Errorf("%s must be allowed", id)
		}
	}
	if allowed[unknown] || allowed["GPL-3.0"] || allowed["MPL-2.0"] {
		t.Error("unknown, GPL and MPL must not be allowed")
	}
}

func TestModulesFrom(t *testing.T) {
	in := `{"ImportPath":"fmt","Standard":true}
{"ImportPath":"example.com/me/x","Module":{"Path":"example.com/me","Main":true,"Dir":"/src"}}
{"ImportPath":"github.com/a/b","Module":{"Path":"github.com/a/b","Dir":"/mod/b"}}
{"ImportPath":"github.com/a/b/sub","Module":{"Path":"github.com/a/b","Dir":"/mod/b"}}
{"ImportPath":"github.com/c/d","Module":{"Path":"github.com/c/d","Dir":"/mod/d","Replace":{"Dir":"/local/d"}}}`
	mods, err := modulesFrom(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []module{{"github.com/a/b", "/mod/b"}, {"github.com/c/d", "/local/d"}}
	if len(mods) != len(want) || mods[0] != want[0] || mods[1] != want[1] {
		t.Fatalf("modules = %v, want %v", mods, want)
	}
}

func TestCheck(t *testing.T) {
	var out bytes.Buffer
	if n := check(nil, &out); n != 0 || strings.TrimSpace(out.String()) != "no dependencies" {
		t.Fatalf("no modules: n=%d out=%q", n, out.String())
	}

	root := t.TempDir()
	write := func(dir, name, text string) string {
		d := filepath.Join(root, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if text != "" {
			if err := os.WriteFile(filepath.Join(d, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	good := write("good", "LICENSE", mit)
	copying := write("copying", "COPYING", bsd3)
	missing := write("missing", "", "")
	weird := write("weird", "LICENSE", "All rights reserved.")
	gpl := write("gpl", "LICENSE", "GNU GENERAL PUBLIC LICENSE")

	out.Reset()
	n := check([]module{{"m/good", good}, {"m/copying", copying}}, &out)
	if n != 0 || !strings.Contains(out.String(), "MIT") || !strings.Contains(out.String(), "BSD-3-Clause") {
		t.Fatalf("allowed modules: n=%d out=%q", n, out.String())
	}

	out.Reset()
	n = check([]module{{"m/missing", missing}, {"m/weird", weird}, {"m/gpl", gpl}, {"m/nodir", ""}}, &out)
	if n != 4 {
		t.Fatalf("failures = %d, want 4; out=%q", n, out.String())
	}
}
