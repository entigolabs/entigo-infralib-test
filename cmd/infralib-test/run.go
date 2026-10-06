package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entigolabs/entigo-infralib-test/env"
)

// testEvent is one line of `go test -json`.
type testEvent struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

type testState struct {
	output  []string
	started time.Time
	// parent is true once a subtest of this test was seen; the parent's own
	// pass/fail then only repeats its children's and is not reported.
	parent bool
}

type packageState struct {
	display string
	logFile *os.File
	tests   map[string]*testState
	// hadTests is false for packages that failed to build or had nothing to run.
	hadTests bool
	output   []string
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	root := fs.String("root", "", "repository root (default: INFRALIB_ROOT or the parent holding environments/)")
	var envNames stringList
	fs.Var(&envNames, "env", "environment to test against (repeatable, default: every environment whose steps list a module)")
	timeout := fs.Duration("timeout", 30*time.Minute, "go test timeout per package")
	logDir := fs.String("log-dir", "", "directory for full per-module logs (default <root>/logs)")
	parallel := fs.Int("parallel", 4, "packages compiled and run in parallel (go test -p)")
	runFilter := fs.String("run", "", "regular expression passed to go test -run")
	verbose := fs.Bool("verbose", false, "stream every test's output instead of only failures")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: infralib-test run [flags] [module dir...]\n\nModule dirs are relative to the repository root, e.g. modules/aws/vpc. Without any, every module that has tests and is part of a selected environment runs.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitError{code: 2}
	}
	config, err := loadConfig(*root)
	if err != nil {
		return err
	}
	environments, err := selectEnvironments(config, envNames)
	if err != nil {
		return err
	}
	modules, err := selectModules(config, environments, fs.Args())
	if err != nil {
		return err
	}
	if len(modules) == 0 {
		fmt.Fprintln(os.Stderr, "no module has tests for the selected environments")
		return nil
	}
	if *logDir == "" {
		*logDir = filepath.Join(config.Root(), "logs")
	}
	if err := os.MkdirAll(*logDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(config.Root(), "go.mod")); err != nil {
		return fmt.Errorf("%s has no go.mod; module tests need one that requires github.com/entigolabs/entigo-infralib-test", config.Root())
	}

	names := make([]string, 0, len(environments))
	for _, e := range environments {
		names = append(names, e.Name)
	}
	fmt.Fprintf(os.Stderr, "Testing %d modules against %s\n", len(modules), strings.Join(names, ", "))

	goArgs := []string{"test", "-json", "-count=1", "-timeout", timeout.String(), "-p", fmt.Sprint(*parallel)}
	if *runFilter != "" {
		goArgs = append(goArgs, "-run", *runFilter)
	}
	for _, m := range modules {
		goArgs = append(goArgs, "./"+filepath.ToSlash(m.TestDir()))
	}
	cmd := exec.Command("go", goArgs...)
	cmd.Dir = config.Root()
	cmd.Env = append(os.Environ(),
		env.RootEnv+"="+config.Root(),
		env.SelectedEnv+"="+strings.Join(names, ","),
	)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("go test: %w", err)
	}
	summary := format(stdout, config, *logDir, *verbose)
	waitErr := cmd.Wait()

	fmt.Fprintf(os.Stderr, "\n%d passed, %d failed, %d skipped\n", summary.passed, summary.failed, summary.skipped)
	if summary.failed > 0 || summary.broken > 0 {
		return exitError{code: 1, message: fmt.Sprintf("Failed: %s", strings.Join(summary.failures, ", "))}
	}
	if waitErr != nil {
		return fmt.Errorf("go test: %w", waitErr)
	}
	return nil
}

// selectModules lists the modules to test: the given directories, or every
// module with a *_test.go that a selected environment's steps list.
func selectModules(config *env.Config, environments []*env.Environment, dirs []string) ([]env.Module, error) {
	var candidates []env.Module
	if len(dirs) > 0 {
		for _, dir := range dirs {
			abs := dir
			if !filepath.IsAbs(dir) {
				abs = filepath.Join(config.Root(), dir)
			}
			m, err := config.LoadModule(abs)
			if err != nil {
				return nil, err
			}
			candidates = append(candidates, m)
		}
	} else {
		var err error
		candidates, err = config.LocalModules()
		if err != nil {
			return nil, err
		}
	}
	var modules []env.Module
	for _, m := range candidates {
		tests, _ := filepath.Glob(filepath.Join(config.Root(), m.TestDir(), "*_test.go"))
		if len(tests) == 0 {
			continue
		}
		for _, e := range environments {
			if _, ok := config.Find(e, m.Source); ok {
				modules = append(modules, m)
				break
			}
		}
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Dir < modules[j].Dir })
	return modules, nil
}

type summary struct {
	passed, failed, skipped, broken int
	failures                        []string
}

// format consumes go test -json events, prints one line per finished test and
// a failed test's output, and writes everything to a log file per package.
func format(r io.Reader, config *env.Config, logDir string, verbose bool) summary {
	var s summary
	packages := map[string]*packageState{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	modulePath := modulePath(config.Root())

	pkg := func(name string) *packageState {
		p, ok := packages[name]
		if !ok {
			display := strings.TrimPrefix(name, modulePath+"/")
			display = strings.TrimPrefix(display, config.ModulesDir+"/")
			display = strings.TrimSuffix(display, "/test")
			p = &packageState{display: display, tests: map[string]*testState{}}
			file, err := os.Create(filepath.Join(logDir, strings.ReplaceAll(display, "/", "-")+".log"))
			if err == nil {
				p.logFile = file
			} else {
				fmt.Fprintf(os.Stderr, "cannot create log file: %v\n", err)
			}
			packages[name] = p
		}
		return p
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// go test prints non-JSON lines for some build problems.
			fmt.Fprintln(os.Stderr, string(line))
			continue
		}
		p := pkg(event.Package)
		if event.Output != "" && p.logFile != nil {
			p.logFile.WriteString(event.Output)
		}
		if event.Test == "" {
			switch event.Action {
			case "output":
				p.output = append(p.output, event.Output)
				if verbose {
					fmt.Print(event.Output)
				}
			case "fail":
				if !p.hadTests {
					s.broken++
					s.failures = append(s.failures, p.display)
					fmt.Printf("✗ %s (package failed)\n", p.display)
					printIndented(p.output)
				}
			case "skip":
				if !p.hadTests {
					fmt.Printf("- %s (no tests)\n", p.display)
				}
			}
			continue
		}
		p.hadTests = true
		state, ok := p.tests[event.Test]
		if !ok {
			state = &testState{}
			p.tests[event.Test] = state
		}
		if i := strings.LastIndex(event.Test, "/"); i > 0 {
			if parent, ok := p.tests[event.Test[:i]]; ok {
				parent.parent = true
			}
		}
		if state.parent && (event.Action == "pass" || event.Action == "fail") {
			continue
		}
		switch event.Action {
		case "run":
			state.started = event.Time
			if verbose {
				fmt.Printf("▶ %s %s\n", p.display, event.Test)
			}
		case "output":
			state.output = append(state.output, event.Output)
			if verbose {
				fmt.Print(event.Output)
			}
		case "pass":
			s.passed++
			fmt.Printf("✓ %s %s (%s)\n", p.display, event.Test, duration(event.Elapsed))
		case "skip":
			s.skipped++
			fmt.Printf("- %s %s skipped%s\n", p.display, event.Test, skipReason(state.output))
		case "fail":
			s.failed++
			s.failures = append(s.failures, p.display+" "+event.Test)
			fmt.Printf("✗ %s %s (%s)\n", p.display, event.Test, duration(event.Elapsed))
			if !verbose {
				printIndented(state.output)
			}
		}
	}
	for _, p := range packages {
		if p.logFile != nil {
			p.logFile.Close()
		}
	}
	return s
}

func printIndented(lines []string) {
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\n")
		if strings.HasPrefix(trimmed, "=== ") || strings.HasPrefix(trimmed, "--- ") {
			continue
		}
		fmt.Printf("    %s\n", strings.TrimPrefix(trimmed, "    "))
	}
}

func skipReason(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "===") || strings.HasPrefix(trimmed, "---") {
			continue
		}
		// "    env.go:123: reason" -> reason
		if i := strings.Index(trimmed, ": "); i > 0 && strings.Contains(trimmed[:i], ".go:") {
			trimmed = trimmed[i+2:]
		}
		return ": " + trimmed
	}
	return ""
}

func duration(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(100 * time.Millisecond).String()
}

// modulePath reads the module path of the repository's go.mod.
func modulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
