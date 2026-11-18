package layout

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// forbidden lists, for each package (and the packages under it), the
// packages (and those under them) it must never depend on, directly or
// through another package in this module.
//
// The point is that an application can import the client half
// (client, collection) without the server, or the server without the
// client apps, and that the revs binary (cmd/revs) needs nothing the
// packages don't export.
var forbidden = map[string][]string{
	// The sync protocol client and its stores: no app, no server.
	"client": {"collection", "server", "ui", "tui", "revstest", "domain", "sqlc"},
	// The local data layer client apps are built on.
	"collection": {"server", "ui", "tui", "revstest", "domain", "sqlc"},
	// Client apps.
	"ui":  {"server", "tui", "revstest", "domain", "sqlc"},
	"tui": {"server", "ui", "revstest", "domain", "sqlc"},
	// The publishing server: no client apps. (It uses the client
	// package itself, to pull from upstream servers.)
	"server": {"collection", "ui", "tui", "revstest"},
	// Shared formats sit below everything else.
	"publication": {"client", "collection", "server", "ratingset", "ui", "tui", "revstest", "domain", "sqlc"},
	"ratingset":   {"client", "collection", "server", "ui", "tui", "revstest", "domain", "sqlc"},
	"keys":        {"client", "collection", "server", "ratingset", "ui", "tui", "revstest", "domain", "sqlc"},
}

func TestLayering(t *testing.T) {
	root, module := moduleRoot(t)
	deps := moduleImports(t, root, module)

	if len(deps) < len(forbidden) {
		t.Fatalf("found only %d packages under %s", len(deps), root)
	}
	for pkg := range forbidden {
		if _, ok := deps[pkg]; !ok {
			t.Errorf("rule for %q matches no package; update the rules", pkg)
		}
	}

	for pkg := range deps {
		for rule, banned := range forbidden {
			if !within(pkg, rule) {
				continue
			}
			for _, path := range reachable(deps, pkg) {
				dep := path[len(path)-1]
				for _, b := range banned {
					if within(dep, b) {
						t.Errorf("%s must not depend on %s: %s", pkg, b, strings.Join(path, " -> "))
					}
				}
			}
		}
	}
}

// within reports whether pkg is dir or a package under it.
func within(pkg, dir string) bool {
	return pkg == dir || strings.HasPrefix(pkg, dir+"/")
}

// reachable returns, for every package pkg depends on, one import chain
// from pkg to it.
func reachable(deps map[string][]string, pkg string) [][]string {
	var out [][]string
	seen := map[string]bool{pkg: true}
	queue := [][]string{{pkg}}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		for _, d := range deps[path[len(path)-1]] {
			if seen[d] {
				continue
			}
			seen[d] = true
			p := append(slices.Clone(path), d)
			out = append(out, p)
			queue = append(queue, p)
		}
	}
	return out
}

// moduleRoot finds the directory holding go.mod and the module path it
// declares.
func moduleRoot(t *testing.T) (string, string) {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		f, err := os.Open(filepath.Join(dir, "go.mod"))
		if err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if mod, ok := strings.CutPrefix(sc.Text(), "module "); ok {
					return dir, strings.TrimSpace(mod)
				}
			}
			t.Fatalf("no module line in %s/go.mod", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// moduleImports maps each package in the module, by path relative to
// the module root, to the module packages its non-test files import.
// Files for every platform are included.
func moduleImports(t *testing.T, root, module string) map[string][]string {
	t.Helper()
	deps := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		if _, ok := deps[pkg]; !ok {
			deps[pkg] = nil
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if dep, ok := strings.CutPrefix(p, module+"/"); ok && !slices.Contains(deps[pkg], dep) {
				deps[pkg] = append(deps[pkg], dep)
			}
			if p == module {
				deps[pkg] = append(deps[pkg], ".")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}
