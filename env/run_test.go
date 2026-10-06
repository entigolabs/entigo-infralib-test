package env

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// repo writes a repository with two environments and a module whose test
// directory holds an input for the given environments, then chdirs into it.
func repo(t *testing.T, inputs ...string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, FileName), []byte(`
environments:
  aws_biz: {cloud: aws, prefix: biz, region: eu-north-1}
  aws_pri: {cloud: aws, prefix: pri, region: eu-north-1}
`), 0o644))
	test := filepath.Join(root, "modules", "aws", "hello", "test")
	require.NoError(t, os.MkdirAll(test, 0o755))
	for _, e := range inputs {
		require.NoError(t, os.WriteFile(filepath.Join(test, e+".yaml"), nil, 0o644))
	}
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
	dir, err := os.Getwd()
	require.NoError(t, err)
	none := func(*testing.T, *Environment) {}
	// An input without a test, a test without an input, and a test for an
	// environment that does not exist are all reported.
	problems := coverageProblems(config, dir, map[string]TestFunc{"aws_pri": none, "google_biz": none})
	require.Len(t, problems, 3)
	require.Contains(t, problems[0], `unknown environment "google_biz"`)
	require.Contains(t, problems[1], "no test for environments aws_biz")
	require.Contains(t, problems[2], "tests for environments aws_pri")
	require.Empty(t, coverageProblems(config, dir, map[string]TestFunc{"aws_biz": none}))
}

func TestRunSameBody(t *testing.T) {
	repo(t, "aws_biz", "aws_pri")
	var seen []string
	t.Run("inner", func(t *testing.T) {
		Run(t, func(t *testing.T, e *Environment) { seen = append(seen, e.Prefix) })
	})
	require.ElementsMatch(t, []string{"biz", "pri"}, seen)
}
