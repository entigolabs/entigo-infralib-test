// Package env describes the test environments of a module repository and
// lets a module test discover which of them it runs against.
//
// A module repository keeps one agent configuration per environment under
// environments/, named <cloud>_<prefix>.yaml:
//
//	environments/aws_biz.yaml
//	environments/aws_pri.yaml
//	environments/google_biz.yaml
//
// Each file is an ordinary entigo-infralib-agent config.yaml. The framework
// does not extend its format; it patches a copy before handing it to the
// agent: the repository itself becomes the first source (restricted to the
// modules it holds), modules without a name get the agent's conventional
// one, modules of this repository get the inputs of their
// test/<environment>.yaml, and in a pull request the modules under test are
// appended as steps of their own.
//
//	sources:
//	  - url: https://github.com/entigolabs/entigo-infralib-release
//	steps:
//	  - name: net
//	    type: terraform
//	    modules:
//	      - name: vpc                      # not in this repository: comes from the sources above
//	        source: aws/vpc
//	        inputs:
//	          vpc_cidr: "10.0.0.0/16"
//	      - source: aws/kms                # of this repository: inputs from modules/aws/kms/test/aws_biz.yaml
//	  - name: apps
//	    type: argocd-apps
//	    argocd_namespace: argocd-biz
//	    modules:
//	      - source: argocd                 # name defaults to argocd-biz for k8s modules
//
// A module of this repository takes part in an environment when a step of
// that environment lists it. A module this repository does not contain is
// external: the agent fetches it from the first of sources: that provides it.
//
// Where the agent takes a value from its flags or environment, so does the
// framework: AWS_REGION; GOOGLE_PROJECT, GOOGLE_REGION, GOOGLE_ZONE;
// OCI_REGION, OCI_COMPARTMENT_ID. The cluster to connect to and the gateway
// to publish through are derived from the modules of the environment, see
// KubeContext and the k8s package.
//
// The executor (CI or a person) owns credentials and the kubeconfig; this
// package only picks names out of the files.
package env

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

const (
	// DirName holds the agent configurations, one <cloud>_<prefix>.yaml each.
	DirName = "environments"
	// RootEnv overrides the repository root lookup.
	RootEnv = "INFRALIB_ROOT"
	// SelectedEnv is a comma separated list of environment names to run against.
	// Unset means every environment the module is part of.
	SelectedEnv = "INFRALIB_ENVIRONMENTS"
	// AgentsDirEnv overrides where the generated agent configurations live
	// (default <root>/agents).
	AgentsDirEnv = "INFRALIB_AGENTS_DIR"

	CloudAWS    = "aws"
	CloudGoogle = "google"
	CloudOracle = "oracle"

	StepTypeTerraform = "terraform"
	StepTypeArgoCD    = "argocd-apps"
)

// Step is an agent step, the fields the framework reads of it.
type Step struct {
	Name string `json:"name"`
	// Type is terraform (the agent's default) or argocd-apps.
	Type            string      `json:"type,omitempty"`
	ArgocdNamespace string      `json:"argocd_namespace,omitempty"`
	Modules         []ModuleRef `json:"modules,omitempty"`
}

// ModuleRef is an agent module entry. Source is "aws/vpc" for terraform
// modules (relative to modules/) and "argocd" for k8s modules. Name defaults
// to the module name, or <module>-<prefix> for k8s modules. Inputs belong to
// external modules; a module of this repository takes its inputs from
// test/<environment>.yaml instead.
type ModuleRef struct {
	Name   string         `json:"name,omitempty"`
	Source string         `json:"source"`
	Inputs map[string]any `json:"inputs,omitempty"`
}

// Environment is one agent configuration: a cloud, a prefix and the steps,
// plus the cloud settings the executor's environment variables provide.
type Environment struct {
	// Name is the file name without .yaml: <cloud>_<prefix>.
	Name   string
	Cloud  string
	Prefix string
	// Path is the configuration file relative to the repository root.
	Path    string
	Sources []AgentSource
	Steps   []Step

	// Region, Zone, Project and CompartmentID come from the environment
	// variables the agent reads too: AWS_REGION; GOOGLE_REGION, GOOGLE_ZONE,
	// GOOGLE_PROJECT; OCI_REGION, OCI_COMPARTMENT_ID.
	Region        string
	Zone          string
	Project       string
	CompartmentID string

	raw map[string]any
}

// agentFile is the part of an agent config the framework needs to read.
type agentFile struct {
	Prefix  string        `json:"prefix,omitempty"`
	Sources []AgentSource `json:"sources,omitempty"`
	Steps   []Step        `json:"steps"`
}

// Config is the set of environments of a repository.
type Config struct {
	// ModulesDir is where modules live: "modules".
	ModulesDir   string
	Environments map[string]*Environment
	root         string
}

// Root returns the module repository root: INFRALIB_ROOT when set, otherwise
// the closest parent of the working directory that holds an environments
// directory with configurations in it.
func Root() (string, error) {
	if root := os.Getenv(RootEnv); root != "" {
		return filepath.Abs(root)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if files, _ := filepath.Glob(filepath.Join(dir, DirName, "*.yaml")); len(files) > 0 {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s/*.yaml found in the working directory or its parents; set %s", DirName, RootEnv)
		}
		dir = parent
	}
}

// Load reads and validates every environment configuration under root.
func Load(root string) (*Config, error) {
	files, err := filepath.Glob(filepath.Join(root, DirName, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no environment configurations (<cloud>_<prefix>.yaml)", filepath.Join(root, DirName))
	}
	config := &Config{ModulesDir: "modules", Environments: map[string]*Environment{}, root: root}
	for _, file := range files {
		e, err := loadEnvironment(root, file)
		if err != nil {
			return nil, err
		}
		config.Environments[e.Name] = e
	}
	return config, nil
}

func loadEnvironment(root, file string) (*Environment, error) {
	rel, _ := filepath.Rel(root, file)
	name := strings.TrimSuffix(filepath.Base(file), ".yaml")
	cloud, prefix, ok := strings.Cut(name, "_")
	if !ok || prefix == "" {
		return nil, fmt.Errorf("%s: file name must be <cloud>_<prefix>.yaml", rel)
	}
	switch cloud {
	case CloudAWS, CloudGoogle, CloudOracle:
	default:
		return nil, fmt.Errorf("%s: unknown cloud %q in the file name (aws, google or oracle)", rel, cloud)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var parsed agentFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if parsed.Prefix != "" && parsed.Prefix != prefix {
		return nil, fmt.Errorf("%s: prefix %q in the file differs from the file name", rel, parsed.Prefix)
	}
	e := &Environment{Name: name, Cloud: cloud, Prefix: prefix, Path: rel, Sources: parsed.Sources, Steps: parsed.Steps, raw: raw}
	if err := e.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	e.loadCloudSettings()
	return e, nil
}

func (e *Environment) validate() error {
	for i, s := range e.Sources {
		if s.URL == "" {
			return fmt.Errorf("source %d has no url", i)
		}
	}
	if len(e.Steps) == 0 {
		return fmt.Errorf("no steps")
	}
	seen := map[string]bool{}
	for i := range e.Steps {
		s := &e.Steps[i]
		if s.Name == "" {
			return fmt.Errorf("step %d has no name", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("step %s defined twice", s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case "":
			s.Type = StepTypeTerraform
		case StepTypeTerraform, StepTypeArgoCD:
		default:
			return fmt.Errorf("step %s: unknown type %q", s.Name, s.Type)
		}
		for j, m := range s.Modules {
			if m.Source == "" {
				return fmt.Errorf("step %s: module %d has no source", s.Name, j)
			}
			if strings.Count(m.Source, "/") > 1 || strings.HasPrefix(m.Source, "/") || strings.HasSuffix(m.Source, "/") {
				return fmt.Errorf("step %s: invalid module source %q", s.Name, m.Source)
			}
		}
	}
	return nil
}

// loadCloudSettings reads the cloud's region and account-like settings from
// the variables the agent reads as well. Missing ones are reported where they
// are needed, so listing environments never requires credentials.
func (e *Environment) loadCloudSettings() {
	switch e.Cloud {
	case CloudAWS:
		e.Region = os.Getenv("AWS_REGION")
	case CloudGoogle:
		e.Project = os.Getenv("GOOGLE_PROJECT")
		e.Region = os.Getenv("GOOGLE_REGION")
		e.Zone = os.Getenv("GOOGLE_ZONE")
	case CloudOracle:
		e.Region = os.Getenv("OCI_REGION")
		e.CompartmentID = os.Getenv("OCI_COMPARTMENT_ID")
	}
}

// MustRegion returns the region, failing the test with the variable to set.
func (e *Environment) MustRegion(t logger.T) string {
	t.Helper()
	if e.Region == "" {
		t.Fatalf("environment %s: %s is not set", e.Name, e.regionVar())
	}
	return e.Region
}

// MustProject returns the Google project, failing the test when unset.
func (e *Environment) MustProject(t logger.T) string {
	t.Helper()
	if e.Project == "" {
		t.Fatalf("environment %s: GOOGLE_PROJECT is not set", e.Name)
	}
	return e.Project
}

// MustCompartmentID returns the Oracle compartment, failing the test when unset.
func (e *Environment) MustCompartmentID(t logger.T) string {
	t.Helper()
	if e.CompartmentID == "" {
		t.Fatalf("environment %s: OCI_COMPARTMENT_ID is not set", e.Name)
	}
	return e.CompartmentID
}

func (e *Environment) regionVar() string {
	switch e.Cloud {
	case CloudGoogle:
		return "GOOGLE_REGION"
	case CloudOracle:
		return "OCI_REGION"
	}
	return "AWS_REGION"
}

// Raw returns a deep copy of the configuration file as parsed, for the
// generator to patch without losing fields the framework does not model.
func (e *Environment) Raw() map[string]any {
	return deepCopy(e.raw).(map[string]any)
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[k] = deepCopy(val)
		}
		return m
	case []any:
		l := make([]any, len(v))
		for i, val := range v {
			l[i] = deepCopy(val)
		}
		return l
	}
	return v
}

// Root is the repository root the config was loaded from.
func (c *Config) Root() string { return c.root }

// All returns the environments sorted by name.
func (c *Config) All() []*Environment {
	names := make([]string, 0, len(c.Environments))
	for name := range c.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]*Environment, 0, len(names))
	for _, name := range names {
		result = append(result, c.Environments[name])
	}
	return result
}

// Get returns one environment by name.
func (c *Config) Get(name string) (*Environment, error) {
	e, ok := c.Environments[name]
	if !ok {
		return nil, fmt.Errorf("environment %q has no %s/%s.yaml", name, DirName, name)
	}
	return e, nil
}

// SelectedNames parses INFRALIB_ENVIRONMENTS; nil means no restriction.
func SelectedNames() []string {
	raw := strings.TrimSpace(os.Getenv(SelectedEnv))
	if raw == "" {
		return nil
	}
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	return names
}

// Selected returns the environments the calling module test runs against: the
// ones named in INFRALIB_ENVIRONMENTS (all when unset) whose steps list the
// module. The test is skipped when none match, so a module that is not part
// of, say, any oracle environment simply reports skipped there.
//
// Typical use is through Run or RunEach.
func Selected(t logger.T) []*Environment {
	t.Helper()
	config := MustLoad(t)
	m := CurrentModule(t)
	selected := SelectedNames()
	for _, name := range selected {
		if _, ok := config.Environments[name]; !ok {
			t.Fatalf("%s names unknown environment %q", SelectedEnv, name)
		}
	}
	var result []*Environment
	for _, e := range config.EnvironmentsOf(m) {
		if selected == nil || contains(selected, e.Name) {
			result = append(result, e)
		}
	}
	if len(result) == 0 {
		skip(t, fmt.Sprintf("no selected environment lists module %s", m.Source))
	}
	return result
}

// MustLoad loads the environments of the repository the test lives in.
func MustLoad(t logger.T) *Config {
	t.Helper()
	root, err := Root()
	if err != nil {
		t.Fatalf("%v", err)
	}
	config, err := Load(root)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return config
}

// InputFileName is the agent input file a module of this repository carries
// for the environment.
func (e *Environment) InputFileName() string { return e.Name + ".yaml" }

// Step returns the environment's step definition by name.
func (e *Environment) Step(name string) (*Step, bool) {
	for i := range e.Steps {
		if e.Steps[i].Name == name {
			return &e.Steps[i], true
		}
	}
	return nil, false
}

// Is reports whether the environment has one of the given names. Handy for
// per-environment expectations inside a shared test body.
func (e *Environment) Is(names ...string) bool { return contains(names, e.Name) }

// String renders the environment for log lines.
func (e *Environment) String() string {
	return fmt.Sprintf("%s (%s %s %s)", e.Name, e.Cloud, e.Prefix, e.Region)
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

type skipper interface{ Skip(args ...any) }

func skip(t logger.T, msg string) {
	t.Helper()
	if s, ok := t.(skipper); ok {
		s.Skip(msg)
		return
	}
	t.Fatalf("%s", msg)
}
