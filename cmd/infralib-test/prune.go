package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		if len(fields) != 2 || !strings.Contains(fields[0], "@") {
			continue
		}
		modules = append(modules, prunedModule{Module: fields[0], Dir: fields[1]})
	}
	return modules, s.Err()
}

// restorePrunedModules checks which packages the tests in dirs need and
// restores every pruned module that lacks the files of one of them. Go
// indexes a module cache directory once and trusts the index afterwards, so
// `go list` still reports the pruned packages as present; the files on disk
// are what counts.
func restorePrunedModules(root string, dirs []string) error {
	pruned, err := readPrunedModules()
	if err != nil || len(pruned) == 0 {
		return err
	}
	args := append([]string{"list", "-e", "-deps", "-test", "-json=ImportPath,Dir,GoFiles"}, dirs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// A broken test package is reported by go test itself, with context.
		return nil
	}
	missing, err := missingPackages(pruned, out)
	if err != nil {
		return err
	}
	for dir, pkgs := range missing {
		fmt.Fprintf(os.Stderr, "Restoring %s: the image pruned it and the tests import %s\n", filepath.Base(dir), strings.Join(pkgs, ", "))
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("restore pruned module %s: %w", dir, err)
		}
	}
	return nil
}

// missingPackages reads `go list -json` output and returns, per pruned
// module directory, the packages under it whose Go files are not on disk.
func missingPackages(pruned []prunedModule, listJSON []byte) (map[string][]string, error) {
	missing := map[string][]string{}
	dec := json.NewDecoder(bytes.NewReader(listJSON))
	for {
		var p struct {
			ImportPath string
			Dir        string
			GoFiles    []string
		}
		if err := dec.Decode(&p); err != nil {
			if err == io.EOF {
				return missing, nil
			}
			return nil, fmt.Errorf("go list output: %w", err)
		}
		if len(p.GoFiles) == 0 {
			continue
		}
		for _, m := range pruned {
			if p.Dir != m.Dir && !strings.HasPrefix(p.Dir, m.Dir+string(filepath.Separator)) {
				continue
			}
			if _, err := os.Stat(filepath.Join(p.Dir, p.GoFiles[0])); err != nil {
				missing[m.Dir] = append(missing[m.Dir], p.ImportPath)
			}
			break
		}
	}
}
