package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeGoMod(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/modules\n\ngo 1.26.0\n\nrequire "+frameworkModule+" "+version+"\n"), 0o644))
	return root
}

func TestFrameworkRequire(t *testing.T) {
	root := writeGoMod(t, "v0.5.0")
	have, err := frameworkRequire(root)
	require.NoError(t, err)
	require.Equal(t, "v0.5.0", have)
}

func TestAlignGoModNoops(t *testing.T) {
	// Equal versions, a dev build, and a pseudo-version of the wanted commit need no change.
	for _, c := range []struct{ have, want string }{
		{"v0.6.0", "v0.6.0"},
		{"v0.6.0", "dev"},
		{"v0.6.0", ""},
		{"v0.6.1-0.20261008120000-0123456789ab", "0123456789ab"},
	} {
		root := writeGoMod(t, c.have)
		before, _ := os.ReadFile(filepath.Join(root, "go.mod"))
		require.NoError(t, alignGoMod(root, c.want), "%+v", c)
		after, _ := os.ReadFile(filepath.Join(root, "go.mod"))
		require.Equal(t, string(before), string(after), "%+v", c)
	}
}

func TestWantedFrameworkVersion(t *testing.T) {
	t.Setenv("INFRALIB_TEST_VERSION", "v0.7.0")
	require.Equal(t, "v0.7.0", wantedFrameworkVersion(), "a pinned release tag wins over the baked version")
	t.Setenv("INFRALIB_TEST_VERSION", "latest")
	require.Equal(t, version, wantedFrameworkVersion(), "a mutable tag falls back to the baked version")
	t.Setenv("INFRALIB_TEST_VERSION", "")
	require.Equal(t, version, wantedFrameworkVersion())
	require.True(t, isSemver("v1.24.9"))
	require.False(t, isSemver("1.24.9"))
	require.False(t, isSemver("v1.24"))
	require.False(t, isSemver("dev"))
}

func TestSameVersion(t *testing.T) {
	const sha = "14393624e9e632e8096a8d8f78e2e7afbccf78f4"
	for _, c := range []struct {
		have, want string
		same       bool
	}{
		{"v0.9.21", "v0.9.21", true},
		{"v0.9.21", "v0.9.22", false},
		// A commit build knows its version as the full sha; go.mod holds the
		// pseudo-version of that commit.
		{"v0.9.22-0.20261121104433-14393624e9e6", sha, true},
		{"v0.9.22-0.20261121104433-14393624e9e6", "14393624e9e6", true},
		{"v0.9.22-0.20261121104433-aaaaaaaaaaaa", sha, false},
		{"v0.9.21", sha, false},
		// A version is not a commit: no prefix games with it.
		{"v0.9.22-0.20261121104433-14393624e9e6", "v0.9.22-0.2026", true},
		{"v0.9.2", "v0.9.21", false},
	} {
		require.Equalf(t, c.same, sameVersion(c.have, c.want), "have %s want %s", c.have, c.want)
	}
}
