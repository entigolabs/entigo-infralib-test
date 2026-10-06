// Package env describes the test environments a module repository provisions
// and lets a module test discover which of them it runs against.
//
// A module repository keeps one environments.yaml at its root:
//
//	sources:
//	  - url: https://github.com/entigolabs/entigo-infralib-release
//	environments:
//	  aws_biz:
//	    cloud: aws
//	    prefix: biz
//	    region: eu-north-1
//	    kube_context: arn:aws:eks:eu-north-1:123456789012:cluster/biz-infra-eks
//	    gateways:
//	      external: {name: external, namespace: aws-alb-biz, domain: biz-net-route53.example.com}
//	    steps:
//	      - name: net
//	        modules: [aws/vpc, aws/kms]
//	      - name: apps
//	        type: argocd-apps
//	        argocd_namespace: argocd-biz
//	        modules_dir: modules/k8s
//
// A module of this repository takes part in an environment when its test
// directory holds a file named after that environment, e.g.
// modules/aws/vpc/test/aws_biz.yaml, which is also the agent input for the
// module in that environment.
//
// A step may also list modules this repository does not contain. They come
// from another agent source (typically a released entigo-infralib) and build
// the platform the repository's own modules are tested on. Such an external
// module is always part of the environment; its optional agent input lives in
// environments/<env>/<step>/<module>.yaml.
//
// The executor (CI or a person) owns credentials and the kubeconfig; this
// package only picks names out of the file.
package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

const (
	// FileName is the environments file looked up from the working directory upwards.
	FileName = "environments.yaml"
	// RootEnv overrides the repository root lookup.
	RootEnv = "INFRALIB_ROOT"
	// SelectedEnv is a comma separated list of environment names to run against.
	// Unset means every environment the module has an input file for.
	SelectedEnv = "INFRALIB_ENVIRONMENTS"
	// AgentsDirEnv overrides where the generated agent configurations live
	// (default <root>/agents).
	AgentsDirEnv = "INFRALIB_AGENTS_DIR"
	// InputsDir holds the agent inputs of external modules:
	// <InputsDir>/<env>/<step>/<module>.yaml.
	InputsDir = "environments"

	CloudAWS    = "aws"
	CloudGoogle = "google"
	CloudOracle = "oracle"

	StepTypeTerraform = "terraform"
	StepTypeArgoCD    = "argocd-apps"
)

// Gateway names the shared ingress gateway tests publish hostnames through.
type Gateway struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Domain is the DNS zone hostnames are created in; Hostname() joins it with a namespace.
	Domain string `json:"domain"`
	// Retries overrides how many 6 second polls hostname checks allow (0 = cloud default).
	Retries int `json:"retries,omitempty"`
}

// Hostname returns the FQDN a module's namespace gets under this gateway.
func (g Gateway) Hostname(namespace string) string {
	return fmt.Sprintf("%s.%s", namespace, g.Domain)
}

// Step describes one agent step of an environment. Field names follow the
// agent's own config.yaml so a reader of both files sees the same words.
type Step struct {
	Name string `json:"name"`
	// Type is terraform (default) or argocd-apps.
	Type            string `json:"type,omitempty"`
	ArgocdNamespace string `json:"argocd_namespace,omitempty"`
	// KubernetesClusterName is passed through to the agent for argocd-apps steps.
	KubernetesClusterName string         `json:"kubernetes_cluster_name,omitempty"`
	Vpc                   map[string]any `json:"vpc,omitempty"`
	// Modules lists the modules of the step, see ModuleRef. A module of this
	// repository is included only when it has an input file for the
	// environment; an external module is always included.
	Modules []ModuleRef `json:"modules,omitempty"`
	// ModulesDir includes every module directory under it (e.g. modules/k8s)
	// that has an input file for the environment, in name order.
	ModulesDir string `json:"modules_dir,omitempty"`
	// DefaultModules are k8s modules other modules chain inputs from (the
	// gateway module). They are copied into a per-module step as default_module
	// so templating resolves there too.
	DefaultModules []string `json:"default_modules,omitempty"`
}

// ModuleRef names a module of a step. In YAML it is either a plain source
// string or an object:
//
//	modules:
//	  - aws/vpc                               # source only
//	  - source: crossplane-core
//	    name: crossplane-system               # agent module name override
//	    input: environments/aws_biz/apps/crossplane.yaml  # input file override
//
// Source is "aws/vpc" for terraform modules (relative to modules/) and
// "argocd" for k8s modules, the way an agent config refers to them.
type ModuleRef struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	Input  string `json:"input,omitempty"`
}

// UnmarshalJSON accepts a bare source string or the object form.
func (r *ModuleRef) UnmarshalJSON(data []byte) error {
	var source string
	if err := json.Unmarshal(data, &source); err == nil {
		r.Source = source
		return nil
	}
	type plain ModuleRef
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = ModuleRef(p)
	return nil
}

// MarshalJSON writes the short form when only Source is set.
func (r ModuleRef) MarshalJSON() ([]byte, error) {
	if r.Name == "" && r.Input == "" {
		return json.Marshal(r.Source)
	}
	type plain ModuleRef
	return json.Marshal(plain(r))
}

// Environment is one provisioned target: a cloud account/project/compartment
// region pair with a prefix, plus the cluster and gateways tests reach.
type Environment struct {
	Name   string `json:"-"`
	Cloud  string `json:"cloud"`
	Prefix string `json:"prefix"`
	Region string `json:"region"`
	// Zone is used by Google.
	Zone string `json:"zone,omitempty"`
	// Project is the Google project id.
	Project string `json:"project,omitempty"`
	// Account is the AWS account id. Optional: resolved through STS when empty.
	Account string `json:"account,omitempty"`
	// CompartmentID is the Oracle compartment OCID.
	CompartmentID string `json:"compartment_id,omitempty"`
	// KubeContext is the kubeconfig context name of the environment's cluster.
	KubeContext string             `json:"kube_context,omitempty"`
	Gateways    map[string]Gateway `json:"gateways,omitempty"`
	Steps       []Step             `json:"steps,omitempty"`
	// Extra carries free-form values tests may read with Value().
	Extra map[string]string `json:"extra,omitempty"`
}

// Config is the parsed environments file.
type Config struct {
	// ModulesDir is where modules live, default "modules".
	ModulesDir string `json:"modules_dir,omitempty"`
	// Sources are the agent sources that provide the external modules, in the
	// order the agent should consult them. The repository itself is always the
	// first source of a generated agent config and is not listed here.
	Sources      []AgentSource           `json:"sources,omitempty"`
	Environments map[string]*Environment `json:"environments"`
	root         string
}

// Root returns the module repository root: INFRALIB_ROOT when set, otherwise the
// closest parent of the working directory that holds environments.yaml.
func Root() (string, error) {
	if root := os.Getenv(RootEnv); root != "" {
		return filepath.Abs(root)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in the working directory or its parents; set %s", FileName, RootEnv)
		}
		dir = parent
	}
}

// Load reads and validates the environments file at root.
func Load(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		return nil, err
	}
	config := &Config{}
	if err := yaml.UnmarshalStrict(data, config); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	config.root = root
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}

func validateConfig(config *Config) error {
	if config.ModulesDir == "" {
		config.ModulesDir = "modules"
	}
	if len(config.Environments) == 0 {
		return fmt.Errorf("%s: no environments defined", FileName)
	}
	for i, s := range config.Sources {
		if s.URL == "" {
			return fmt.Errorf("%s: source %d has no url", FileName, i)
		}
	}
	for name, e := range config.Environments {
		if e == nil {
			return fmt.Errorf("%s: environment %s is empty", FileName, name)
		}
		e.Name = name
		if err := e.validate(); err != nil {
			return fmt.Errorf("%s: environment %s: %w", FileName, name, err)
		}
	}
	return nil
}

func (e *Environment) validate() error {
	switch e.Cloud {
	case CloudAWS, CloudGoogle, CloudOracle:
	case "":
		return fmt.Errorf("cloud is required")
	default:
		return fmt.Errorf("unknown cloud %q", e.Cloud)
	}
	if e.Prefix == "" {
		return fmt.Errorf("prefix is required")
	}
	if e.Region == "" {
		return fmt.Errorf("region is required")
	}
	if e.Cloud == CloudGoogle && e.Project == "" {
		return fmt.Errorf("project is required for google")
	}
	if e.Cloud == CloudOracle && e.CompartmentID == "" {
		return fmt.Errorf("compartment_id is required for oracle")
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
		if len(s.Modules) == 0 && s.ModulesDir == "" {
			return fmt.Errorf("step %s: modules or modules_dir is required", s.Name)
		}
		for j, m := range s.Modules {
			if m.Source == "" {
				return fmt.Errorf("step %s: module %d has no source", s.Name, j)
			}
		}
	}
	return nil
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
		return nil, fmt.Errorf("environment %q is not defined in %s", name, FileName)
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
// ones named in INFRALIB_ENVIRONMENTS (all when unset) that the module has an
// input file for in the working directory. The test is skipped when none match,
// so a module without, say, oracle inputs simply reports skipped on oracle.
//
// Typical use:
//
//	for _, e := range env.Selected(t) {
//		t.Run(e.Name, func(t *testing.T) {
//			t.Parallel()
//			...
//		})
//	}
func Selected(t logger.T) []*Environment {
	t.Helper()
	config := MustLoad(t)
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	selected := SelectedNames()
	var result []*Environment
	for _, e := range config.All() {
		if selected != nil && !contains(selected, e.Name) {
			continue
		}
		if !e.HasModuleInput(dir) {
			continue
		}
		result = append(result, e)
	}
	for _, name := range selected {
		if _, ok := config.Environments[name]; !ok {
			t.Fatalf("%s names unknown environment %q", SelectedEnv, name)
		}
	}
	if len(result) == 0 {
		skip(t, fmt.Sprintf("no selected environment has an input file in %s", dir))
	}
	return result
}

// MustLoad loads the environments file of the repository the test lives in.
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

// HasModuleInput reports whether testDir holds the environment's input file.
func (e *Environment) HasModuleInput(testDir string) bool {
	_, err := os.Stat(filepath.Join(testDir, e.InputFileName()))
	return err == nil
}

// InputFileName is the agent input file a module carries for this environment.
func (e *Environment) InputFileName() string { return e.Name + ".yaml" }

// Gateway returns the named gateway of the environment, failing the test when
// it is not defined.
func (e *Environment) Gateway(t logger.T, name string) Gateway {
	t.Helper()
	g, ok := e.Gateways[name]
	if !ok {
		t.Fatalf("environment %s defines no gateway %q", e.Name, name)
	}
	if g.Retries == 0 {
		g.Retries = 100
		if e.Cloud == CloudGoogle {
			g.Retries = 400
		}
	}
	return g
}

// Value returns an extra value of the environment, or def when unset.
func (e *Environment) Value(key, def string) string {
	if v, ok := e.Extra[key]; ok {
		return v
	}
	return def
}

// Is reports whether the environment has one of the given names. Handy for
// per-environment expectations inside a shared test body.
func (e *Environment) Is(names ...string) bool { return contains(names, e.Name) }

// Step returns the environment's step definition by name.
func (e *Environment) Step(name string) (*Step, bool) {
	for i := range e.Steps {
		if e.Steps[i].Name == name {
			return &e.Steps[i], true
		}
	}
	return nil, false
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
