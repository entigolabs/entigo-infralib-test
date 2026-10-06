package env

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

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
// after the environment. The map must cover exactly the environments whose
// steps list the module: an environment without a test, or a test for an
// environment the module is not part of, fails the test, so the two cannot
// drift apart silently. Tests for environments that are not selected
// (INFRALIB_ENVIRONMENTS) do not run.
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
	for _, problem := range coverageProblems(config, CurrentModule(t), tests) {
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

// coverageProblems compares the environments whose steps list the module
// with the ones tests covers.
func coverageProblems(config *Config, m Module, tests map[string]TestFunc) []string {
	var problems, missing, orphan []string
	for _, e := range config.All() {
		_, hasTest := tests[e.Name]
		_, isMember := config.Find(e, m.Source)
		switch {
		case isMember && !hasTest:
			missing = append(missing, e.Name)
		case hasTest && !isMember:
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
		problems = append(problems, fmt.Sprintf("test for unknown environment %q; %s/ defines %s", name, DirName, strings.Join(environmentNames(config), ", ")))
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("no test for environments %s although their steps list the module", strings.Join(missing, ", ")))
	}
	if len(orphan) > 0 {
		problems = append(problems, fmt.Sprintf("tests for environments %s although their steps do not list the module", strings.Join(orphan, ", ")))
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
