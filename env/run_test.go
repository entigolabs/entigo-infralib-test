package env

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// repo writes a repository with two environments, a local module aws/hello
// listed in the given environments, then chdirs into its test directory.
func repo(t *testing.T, listedIn ...string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"aws_biz", "aws_pri"} {
		content := "steps:\n  - name: net\n    modules:\n      - source: aws/vpc\n"
		for _, l := range listedIn {
			if l == name {
				content += "      - source: aws/hello\n"
			}
		}
		writeEnv(t, root, name, content)
	}
	test := filepath.Join(root, "modules", "aws", "hello", "test")
	require.NoError(t, os.MkdirAll(test, 0o755))
	t.Setenv(RootEnv, root)
	t.Setenv(SelectedEnv, "")
	t.Chdir(test)
}

func TestRunEachRunsInParallel(t *testing.T) {
	repo(t, "aws_biz", "aws_pri")
	// Each subtest waits for the other to have started; serial execution
	// would never get past the barrier.
	var arrived atomic.Int32
	barrier := func(t *testing.T, e *Environment) {
		arrived.Add(1)
		deadline := time.Now().Add(5 * time.Second)
		for arrived.Load() < 2 {
			if time.Now().After(deadline) {
				t.Fatalf("%s ran alone: subtests are not parallel", e.Name)
			}
			time.Sleep(time.Millisecond)
		}
	}
	RunEach(t, map[string]TestFunc{"aws_biz": barrier, "aws_pri": barrier})
}

func TestRunEachSelection(t *testing.T) {
	repo(t, "aws_biz", "aws_pri")
	t.Setenv(SelectedEnv, "aws_pri")
	var ran []string
	t.Run("inner", func(t *testing.T) {
		RunEach(t, map[string]TestFunc{
			"aws_biz": func(t *testing.T, e *Environment) { ran = append(ran, e.Name) },
			"aws_pri": func(t *testing.T, e *Environment) { ran = append(ran, e.Name) },
		})
	})
	require.Equal(t, []string{"aws_pri"}, ran)
}

func TestRunEachDrift(t *testing.T) {
	repo(t, "aws_biz")
	config := MustLoad(t)
	m := CurrentModule(t)
	none := func(*testing.T, *Environment) {}
	// A listed environment without a test, a test for an environment that does
	// not list the module, and a test for an unknown environment are all reported.
	problems := coverageProblems(config, m, map[string]TestFunc{"aws_pri": none, "google_biz": none})
	require.Len(t, problems, 3)
	require.Contains(t, problems[0], `unknown environment "google_biz"`)
	require.Contains(t, problems[1], "no test for environments aws_biz")
	require.Contains(t, problems[2], "tests for environments aws_pri")
	require.Empty(t, coverageProblems(config, m, map[string]TestFunc{"aws_biz": none}))
}

func TestRunSameBody(t *testing.T) {
	repo(t, "aws_biz", "aws_pri")
	var seen []string
	t.Run("inner", func(t *testing.T) {
		Run(t, func(t *testing.T, e *Environment) { seen = append(seen, e.Prefix) })
	})
	require.ElementsMatch(t, []string{"biz", "pri"}, seen)
}

func TestSelectedSkipsUnlisted(t *testing.T) {
	repo(t) // hello is listed nowhere
	ok := t.Run("inner", func(t *testing.T) {
		Selected(t)
		t.Fatal("must have skipped")
	})
	require.True(t, ok, "a skipped subtest passes")
}
