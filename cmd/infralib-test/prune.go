package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The test images prune the packages of large modules that the framework
// does not import (images/prune-go-caches.sh) and list those modules here.
// A module test may import one of the pruned packages; restorePrunedModules
// then deletes the module directory so that Go extracts the module again
// from its zip, which the image kept, without network.
const prunedListEnv = "INFRALIB_PRUNED_MODULES"

const defaultPrunedList = "/opt/infralib-test/pruned-modules"

// prunedModule is one line of the list: module path@version and its directory.
type prunedModule struct {
	Module string
	Dir    string
}

func readPrunedModules() ([]prunedModule, error) {
	path := os.Getenv(prunedListEnv)
	if path == "" {
		path = defaultPrunedList
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var modules []prunedModule
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		modules = append(modules, prunedModule{Module: fields[0], Dir: fields[1]})
	}
	return modules, s.Err()
}

// restorePrunedModules checks which packages the tests in dirs need and
// restores every pruned module that lacks one of them.
func restorePrunedModules(root string, dirs []string) error {
	pruned, err := readPrunedModules()
	if err != nil || len(pruned) == 0 {
		return err
	}
	args := append([]string{"list", "-e", "-deps", "-test", "-f", "{{.ImportPath}}\t{{if .Error}}{{.Error.Err}}{{end}}"}, dirs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// A broken test package is reported by go test itself, with context.
		return nil
	}
	missing := map[string][]string{} // module dir -> packages
	for _, line := range strings.Split(string(out), "\n") {
		pkg, problem, ok := strings.Cut(line, "\t")
		if !ok || problem == "" {
			continue
		}
		if m, ok := prunedModuleOf(pruned, pkg); ok {
			missing[m.Dir] = append(missing[m.Dir], pkg)
		}
	}
	for dir, pkgs := range missing {
		fmt.Fprintf(os.Stderr, "Restoring %s: the image pruned it and the tests import %s\n", filepath.Base(dir), strings.Join(pkgs, ", "))
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("restore pruned module %s: %w", dir, err)
		}
	}
	return nil
}

// prunedModuleOf returns the pruned module that provides pkg: the longest
// module path that is a path prefix of pkg.
func prunedModuleOf(pruned []prunedModule, pkg string) (prunedModule, bool) {
	var best prunedModule
	found := false
	for _, m := range pruned {
		path, _, _ := strings.Cut(m.Module, "@")
		if pkg == path || strings.HasPrefix(pkg, path+"/") {
			if !found || len(path) > len(strings.SplitN(best.Module, "@", 2)[0]) {
				best, found = m, true
			}
		}
	}
	return best, found
}
