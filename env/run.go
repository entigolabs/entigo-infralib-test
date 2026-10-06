package env

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

var osGetwd = os.Getwd

// TestFunc is the body of a module test for one environment.
type TestFunc func(t *testing.T, e *Environment)

// Run runs body once per selected environment, as parallel subtests named
// after the environment. It is the shape for a test that is the same
// everywhere and branches on e where needed:
//
//	func TestVpc(t *testing.T) {
//		env.Run(t, func(t *testing.T, e *env.Environment) {
//			outputs := tf.Get(t, e)
//			...
//		})
//	}
func Run(t *testing.T, body TestFunc) {
	t.Helper()
	for _, e := range Selected(t) {
		t.Run(e.Name, func(t *testing.T) {
			t.Parallel()
			body(t, e)
		})
	}
}

// RunEach runs a distinct test per environment, as parallel subtests named
// after the environment. The map must cover exactly the environments the
// module has an input file for: an input without a test, or a test without
// an input, fails the test, so the two cannot drift apart silently. Tests
// for environments that are not selected (INFRALIB_ENVIRONMENTS) do not run.
//
//	func TestHelloWorld(t *testing.T) {
//		env.RunEach(t, map[string]env.TestFunc{
//			"aws_biz": testBiz,
//			"aws_pri": testPri,
//		})
//	}
func RunEach(t *testing.T, tests map[string]TestFunc) {
	t.Helper()
	config := MustLoad(t)
	for _, problem := range coverageProblems(config, workDir(t), tests) {
		t.Error(problem)
	}
	if t.Failed() {
		return
	}
	for _, e := range Selected(t) {
		body := tests[e.Name]
		t.Run(e.Name, func(t *testing.T) {
			t.Parallel()
			body(t, e)
		})
	}
}

// coverageProblems compares the environments a module has inputs for in dir
// with the ones tests covers.
func coverageProblems(config *Config, dir string, tests map[string]TestFunc) []string {
	var problems, missing, orphan []string
	for _, e := range config.All() {
		_, hasTest := tests[e.Name]
		hasInput := e.HasModuleInput(dir)
		switch {
		case hasInput && !hasTest:
			missing = append(missing, e.Name)
		case hasTest && !hasInput:
			orphan = append(orphan, e.Name)
		}
	}
	var unknown []string
	for name := range tests {
		if _, ok := config.Environments[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		problems = append(problems, fmt.Sprintf("test for unknown environment %q; %s defines %s", name, FileName, strings.Join(environmentNames(config), ", ")))
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("no test for environments %s although the module has an input file for them", strings.Join(missing, ", ")))
	}
	if len(orphan) > 0 {
		problems = append(problems, fmt.Sprintf("tests for environments %s although the module has no input file for them", strings.Join(orphan, ", ")))
	}
	return problems
}

func environmentNames(config *Config) []string {
	names := make([]string, 0, len(config.Environments))
	for _, e := range config.All() {
		names = append(names, e.Name)
	}
	return names
}

func workDir(t *testing.T) string {
	t.Helper()
	dir, err := osGetwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

// String renders the environment for log lines.
func (e *Environment) String() string {
	return fmt.Sprintf("%s (%s %s %s)", e.Name, e.Cloud, e.Prefix, e.Region)
}
