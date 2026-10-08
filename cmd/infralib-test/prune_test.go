package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrunedModuleOf(t *testing.T) {
	pruned := []prunedModule{
		{Module: "golang.org/x/text@v0.42.0", Dir: "/go/pkg/mod/golang.org/x/text@v0.42.0"},
		{Module: "github.com/oracle/oci-go-sdk/v65@v65.126.1", Dir: "/go/pkg/mod/github.com/oracle/oci-go-sdk/v65@v65.126.1"},
	}
	m, ok := prunedModuleOf(pruned, "golang.org/x/text/language")
	require.True(t, ok)
	require.Equal(t, "golang.org/x/text@v0.42.0", m.Module)
	m, ok = prunedModuleOf(pruned, "github.com/oracle/oci-go-sdk/v65/containerengine")
	require.True(t, ok)
	require.Contains(t, m.Dir, "oci-go-sdk")
	_, ok = prunedModuleOf(pruned, "golang.org/x/textual")
	require.False(t, ok, "a path prefix must end at a path element")
	_, ok = prunedModuleOf(pruned, "k8s.io/api/core/v1")
	require.False(t, ok)
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
