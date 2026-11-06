package app

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Reading the DocType files of a directory (--doctypes DIR; the Rust module doctypes.rs).

// tomlFiles are the *.toml files directly in dir, sorted by name, and the problems found on the way.
//
// Rules: not recursive (subdirectories are ignored); the extension match is case-sensitive
// (*.TOML is ignored); names starting with '.' are skipped (editor and macOS ._ files); an entry
// named *.toml that is not a readable regular file (for example a dangling symlink) is reported as
// a problem; a directory with such a name is ignored. Symlinks to files are followed. The error is
// set when the directory cannot be read.
func tomlFiles(dir string) (files []string, problems []string, err error) {
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return nil, nil, fmt.Errorf("cannot read DocType directory %s: %s", dir, ioErrorText(readErr))
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".toml") {
			continue
		}
		path := joinPath(dir, name)
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			names = append(names, name)
		case statErr == nil && info.IsDir():
			// A directory named x.toml is not a DocType file.
		default:
			// For example a dangling symlink: do not skip it silently.
			problems = append(problems, fmt.Sprintf("%s: cannot read: not a readable file (dangling symlink?)", path))
		}
	}
	sort.Strings(names)
	for _, name := range names {
		files = append(files, joinPath(dir, name))
	}
	return files, problems, nil
}

// joinPath joins like Rust's Path::join, which keeps the directory as the user wrote it ("." stays
// "./a.toml"); filepath.Join would clean it.
func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// file is one DocType file: where it came from (the path as shown in messages) and its text.
type file struct {
	path string
	text string
}

// readDir reads every *.toml file of dir (tomlFiles has the rules). Every problem is returned, each
// naming its file; the readable files are returned whatever the problems.
func readDir(dir string) ([]file, []string) {
	files, problems, err := tomlFiles(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var sources []file
	for _, path := range files {
		text, readErr := os.ReadFile(path)
		switch {
		case readErr != nil:
			problems = append(problems, fmt.Sprintf("%s: cannot read: %s", path, ioErrorText(readErr)))
		case !validUTF8(text):
			problems = append(problems, fmt.Sprintf("%s: cannot read: stream did not contain valid UTF-8", path))
		default:
			sources = append(sources, file{path: path, text: string(text)})
		}
	}
	return sources, problems
}
