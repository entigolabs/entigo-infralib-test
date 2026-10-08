package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMissingPackages(t *testing.T) {
	mod := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(mod, "language"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mod, "language", "language.go"), []byte("package language\n"), 0o644))
	pruned := []prunedModule{{Module: "golang.org/x/text@v0.42.0", Dir: mod}}
	listJSON := []byte(`{"ImportPath":"golang.org/x/text/language","Dir":"` + filepath.Join(mod, "language") + `","GoFiles":["language.go"]}
{"ImportPath":"golang.org/x/text/collate","Dir":"` + filepath.Join(mod, "collate") + `","GoFiles":["collate.go","index.go"]}
{"ImportPath":"fmt","Dir":"/usr/local/go/src/fmt","GoFiles":["print.go"]}
{"ImportPath":"m [m.test]","Dir":"/tmp/m"}
`)
	missing, err := missingPackages(pruned, listJSON)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{mod: {"golang.org/x/text/collate"}}, missing, "only the package whose files are gone, and only under a pruned module")
}

func TestReadPrunedModules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pruned")
	require.NoError(t, os.WriteFile(path, []byte("golang.org/x/text@v0.42.0 /go/pkg/mod/golang.org/x/text@v0.42.0\nbroken line\n"), 0o644))
	t.Setenv(prunedListEnv, path)
	modules, err := readPrunedModules()
	require.NoError(t, err)
	require.Len(t, modules, 1)
	require.Equal(t, "/go/pkg/mod/golang.org/x/text@v0.42.0", modules[0].Dir)
	t.Setenv(prunedListEnv, filepath.Join(t.TempDir(), "missing"))
	modules, err = readPrunedModules()
	require.NoError(t, err)
	require.Nil(t, modules, "no list means nothing was pruned")
}
