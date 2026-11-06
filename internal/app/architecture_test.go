package app

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Find sources from the package directory (go test) or repository directory
// (cross-built test binaries executed under WSL); do not rely on Windows source
// paths recorded in runtime.Caller when executing a Linux test binary.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("run architecture tests from this repository")
		}
		dir = parent
	}
}
func TestThinRootEntrypoint(t *testing.T) {
	root := repositoryRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "main.go" {
		t.Fatalf("root should contain only the thin main.go launcher: %v", files)
	}
	f, err := parser.ParseFile(token.NewFileSet(), files[0], nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name.Name != "main" || len(f.Imports) != 1 || f.Imports[0].Path.Value != `"folderbridge/internal/app"` {
		t.Fatal("root launcher must delegate to internal/app")
	}
}
func TestModuleDependencyDirections(t *testing.T) {
	root := repositoryRoot(t)
	allowed := map[string]map[string]bool{
		"app":        {"folderbridge/internal/syncengine": true, "folderbridge/internal/fileops": true, "folderbridge/internal/update": true},
		"syncengine": {"folderbridge/internal/fileops": true},
		"fileops":    {},
		"update":     {},
	}
	for module, imports := range allowed {
		t.Run(module, func(t *testing.T) {
			files, err := filepath.Glob(filepath.Join(root, "internal", module, "*.go"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 0 {
				t.Fatal("missing module", module)
			}
			for _, name := range files {
				if strings.HasSuffix(name, "_test.go") {
					continue
				}
				// Parse every platform variant, even variants excluded by this OS build.
				f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
				if err != nil {
					t.Fatal(err)
				}
				for _, spec := range f.Imports {
					path, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(path, "folderbridge/") && !imports[path] {
						t.Errorf("%s imports %s against the module dependency direction", filepath.Base(name), path)
					}
				}
			}
		})
	}
}
