package env

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Two optional companions of environments/<name>.yaml let an environment
// differ from the executor's defaults without becoming anything but a plain
// agent configuration:
//
//	environments/<name>.env                  KEY=VALUE lines, variable overrides
//	environments/ca-certificates/*.crt       extra CA certificates, every environment
//	environments/<name>/ca-certificates/*.crt    extra CA certificates, this one
//
// The .env file has the format of docker's --env-file: one KEY=VALUE per line,
// blank lines and lines starting with # are skipped, the value is taken as
// written (no quoting, no expansion). The variables override the executor's
// for this environment only, in the agent, in the tests (Getenv) and in the
// orchestrator's kubeconfig step: AWS_REGION=us-east-1 puts one environment in
// another region. Nothing secret belongs there, the file is committed.
//
// The certificates reach the agent (agents/<name>/ca-certificates/, which it
// hands to every step) and the in-cluster probe of the route checks.

// EnvFileName is the per-environment variable override file.
func (e *Environment) EnvFileName() string { return e.Name + ".env" }

// CertsDirName is the directory of extra CA certificates, under
// environments/ for all environments or under environments/<name>/ for one.
const CertsDirName = "ca-certificates"

// CACertificate is one extra CA certificate file.
type CACertificate struct {
	// Name is the file name, the one the agent and the probe see.
	Name string
	// Path is the file relative to the repository root.
	Path    string
	Content []byte
}

// Getenv returns the environment's value of a variable: the override from
// environments/<name>.env when the file sets it, otherwise the process's.
func (e *Environment) Getenv(key string) string {
	if v, ok := e.Overrides[key]; ok {
		return v
	}
	return os.Getenv(key)
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnvFile parses KEY=VALUE lines in docker --env-file format.
func ParseEnvFile(data []byte) (map[string]string, error) {
	result := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKey.MatchString(key) {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", n, line)
		}
		result[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// loadOverrides reads environments/<name>.env when it exists.
func (e *Environment) loadOverrides(root string) error {
	path := filepath.Join(root, DirName, e.EnvFileName())
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	overrides, err := ParseEnvFile(data)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Join(DirName, e.EnvFileName()), err)
	}
	e.Overrides = overrides
	e.OverridesPath = filepath.Join(DirName, e.EnvFileName())
	return nil
}

// loadCACertificates reads the shared and the environment's own certificate
// files. A file of the environment's own replaces a shared one of the same
// name. The result is sorted by name.
func (e *Environment) loadCACertificates(root string) error {
	byName := map[string]CACertificate{}
	for _, dir := range []string{filepath.Join(DirName, CertsDirName), filepath.Join(DirName, e.Name, CertsDirName)} {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.crt"))
		if err != nil {
			return err
		}
		for _, file := range files {
			content, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if !bytes.Contains(content, []byte("-----BEGIN CERTIFICATE-----")) {
				return fmt.Errorf("%s: not a PEM certificate", filepath.Join(dir, filepath.Base(file)))
			}
			name := filepath.Base(file)
			byName[name] = CACertificate{Name: name, Path: filepath.Join(dir, name), Content: content}
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	e.CACertificates = e.CACertificates[:0]
	for _, name := range names {
		e.CACertificates = append(e.CACertificates, byName[name])
	}
	return nil
}
