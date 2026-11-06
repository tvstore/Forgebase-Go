// Command licensecheck lists every third-party module in the build graph of the main module and
// fails when the licence of one is missing, unknown or not on the allow-list (docs/blueprint/
// maintenance-policy.md, "CI", job `licenses`). Standard library only.
//
// Usage, from the repository root:
//
//	go run ./.github/ci/licensecheck
//
// It runs `go list -deps -test -json ./...`, takes the packages that are neither in the standard
// library nor in the main module, and reads the licence file in the directory of each module
// (LICENSE, LICENSE.txt, LICENSE.md or COPYING). The licence is identified by distinctive text, not
// by a declared name. With no third-party module it prints "no dependencies" and succeeds.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// allowed is the licence allow-list (docs/blueprint/maintenance-policy.md).
var allowed = map[string]bool{
	"MIT":          true,
	"Apache-2.0":   true,
	"BSD-2-Clause": true,
	"BSD-3-Clause": true,
	"ISC":          true,
}

// licenseFiles are the file names looked up in a module directory, in this order.
var licenseFiles = []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING"}

const unknown = "unknown"

// module is a third-party module of the build graph.
type module struct {
	Path string
	Dir  string
}

// listedPackage is the part of `go list -json` output that is used here.
type listedPackage struct {
	Standard bool
	Module   *struct {
		Path    string
		Main    bool
		Dir     string
		Replace *struct{ Dir string }
	}
}

// modulesFrom decodes the stream of JSON objects printed by `go list -json` and returns the
// third-party modules, sorted by path and without duplicates.
func modulesFrom(r io.Reader) ([]module, error) {
	seen := map[string]module{}
	dec := json.NewDecoder(r)
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		dir := p.Module.Dir
		if p.Module.Replace != nil && p.Module.Replace.Dir != "" {
			dir = p.Module.Replace.Dir
		}
		seen[p.Module.Path] = module{Path: p.Module.Path, Dir: dir}
	}
	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// normalize lower-cases the text and collapses all white space, so that line wrapping and
// indentation do not matter.
func normalize(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

func hasAll(text string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(text, p) {
			return false
		}
	}
	return true
}

// classify identifies a licence text by distinctive phrases. It returns an SPDX identifier, or
// "unknown" when the text matches none or more than one of the known licences.
func classify(text string) string {
	t := normalize(text)
	var found []string
	if hasAll(t, "apache license", "version 2.0, january 2004") {
		found = append(found, "Apache-2.0")
	}
	if hasAll(t, "permission is hereby granted, free of charge, to any person obtaining a copy",
		"the software is provided \"as is\"") {
		found = append(found, "MIT")
	}
	if hasAll(t, "permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted") {
		found = append(found, "ISC")
	}
	if hasAll(t, "redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met",
		"redistributions of source code must retain the above copyright notice",
		"redistributions in binary form must reproduce the above copyright notice") {
		switch {
		case strings.Contains(t, "all advertising materials mentioning features"):
			// The original four-clause BSD licence is not on the allow-list.
		case strings.Contains(t, "neither the name of"):
			found = append(found, "BSD-3-Clause")
		default:
			found = append(found, "BSD-2-Clause")
		}
	}
	if len(found) != 1 {
		return unknown
	}
	return found[0]
}

// licenseOf reads the licence file of a module directory and identifies it. The returned error
// says why a licence could not be determined.
func licenseOf(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("module directory unknown (run `go mod download`)")
	}
	for _, name := range licenseFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		id := classify(string(data))
		if id == unknown {
			return "", fmt.Errorf("%s: licence text not recognised", name)
		}
		return id, nil
	}
	return "", fmt.Errorf("no licence file (%s)", strings.Join(licenseFiles, ", "))
}

// check prints the table and returns the number of modules that fail.
func check(mods []module, out io.Writer) int {
	if len(mods) == 0 {
		fmt.Fprintln(out, "no dependencies")
		return 0
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	failures := 0
	for _, m := range mods {
		id, err := licenseOf(m.Dir)
		switch {
		case err != nil:
			fmt.Fprintf(tw, "%s\tFAIL\t%v\n", m.Path, err)
			failures++
		case !allowed[id]:
			fmt.Fprintf(tw, "%s\tFAIL\t%s is not on the allow-list\n", m.Path, id)
			failures++
		default:
			fmt.Fprintf(tw, "%s\t%s\t\n", m.Path, id)
		}
	}
	tw.Flush()
	return failures
}

func main() {
	cmd := exec.Command("go", "list", "-deps", "-test", "-json", "./...")
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "licensecheck:", err)
		os.Exit(1)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "licensecheck:", err)
		os.Exit(1)
	}
	mods, err := modulesFrom(bufio.NewReader(pipe))
	if werr := cmd.Wait(); werr != nil || err != nil {
		if err == nil {
			err = werr
		}
		fmt.Fprintln(os.Stderr, "licensecheck:", err)
		os.Exit(1)
	}
	if n := check(mods, os.Stdout); n > 0 {
		fmt.Fprintf(os.Stderr, "licensecheck: %d module(s) without an allowed licence\n", n)
		os.Exit(1)
	}
}
