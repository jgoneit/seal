package releasegate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The tag gate only runs at release time; this keeps checked-in Plugin
// manifests aligned with the Core version on every change.
func TestRepositoryPluginManifestsMatchCoreVersion(t *testing.T) {
	root := filepath.Join("..", "..")
	version := repositoryCoreVersion(t, filepath.Join(root, "cmd", "seal", "main.go"))
	for _, manifest := range []string{".codex-plugin/plugin.json", ".claude-plugin/plugin.json"} {
		t.Run(manifest, func(t *testing.T) {
			contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := normalizePluginVersion(contents, version); err != nil {
				t.Fatalf("normalizePluginVersion(%s) error = %v", manifest, err)
			}
		})
	}
}

func repositoryCoreVersion(t *testing.T, path string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if name.Name != "version" || index >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s version constant is not a string literal", path)
				}
				version, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				return version
			}
		}
	}
	t.Fatalf("%s has no version constant", path)
	return ""
}
