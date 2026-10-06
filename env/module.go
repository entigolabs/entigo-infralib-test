package env

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// ModuleFileName is the optional per-module test metadata file, kept in the
// module's test directory next to the environment input files.
const ModuleFileName = "module.yaml"

// Module is a module an environment step refers to: either a directory of
// this repository or an external module provided by another agent source.
type Module struct {
	// Dir is the module directory relative to the repository root, e.g.
	// modules/aws/vpc. Empty for external modules.
	Dir string
	// Type is the directory under modules/, e.g. aws, aws-v2, google, k8s.
	Type string
	// Name is the module directory name.
	Name string
	// Source is how an agent config refers to the module: "aws/vpc" for
	// terraform modules, "argocd" for k8s modules.
	Source string
	// External is true when this repository has no directory for the module,
	// so another source of the agent config must provide it.
	External bool
	// Ref is the step entry the module was resolved from, if any.
	Ref  ModuleRef
	Meta ModuleMeta
}

// ModuleMeta is the content of test/module.yaml.
type ModuleMeta struct {
	// PinStep tests the module inside its regular step even when the rest of a
	// pull request run uses a per-branch step (modules the step itself depends
	// on, such as config-rules).
	PinStep bool `json:"pin_step,omitempty"`
}

// IsK8s reports whether the module is a Kubernetes (argocd-apps) module.
func (m Module) IsK8s() bool { return m.Type == "k8s" }

// AgentName is the module's name in the agent config of an environment: the
// step entry's, or the agent's convention of <module> for terraform modules
// and <module>-<prefix> for k8s modules.
func (m Module) AgentName(e *Environment) string {
	if m.Ref.Name != "" {
		return m.Ref.Name
	}
	if !m.IsK8s() {
		return m.Name
	}
	return fmt.Sprintf("%s-%s", m.Name, e.Prefix)
}

// TestDir is the module's test directory relative to the repository root.
// Empty for external modules.
func (m Module) TestDir() string {
	if m.External {
		return ""
	}
	return filepath.Join(m.Dir, "test")
}

// InputPath is the agent input file of a module of this repository for an
// environment, relative to the repository root: test/<env>.yaml. Empty for
// external modules, whose inputs are inline in the step entry.
func (m Module) InputPath(e *Environment) string {
	if m.External {
		return ""
	}
	return filepath.Join(m.TestDir(), e.InputFileName())
}

// Inputs returns the module's agent inputs for the environment: the step
// entry's for an external module; for a module of this repository the parsed
// test/<env>.yaml, nil when there is none or it is empty. A module of this
// repository with inline inputs is an error, so inputs have one home.
func (c *Config) Inputs(m Module, e *Environment) (map[string]any, error) {
	if m.External {
		return m.Ref.Inputs, nil
	}
	if len(m.Ref.Inputs) > 0 {
		return nil, fmt.Errorf("module %s is part of this repository; put its inputs for %s in %s, not in %s", m.Source, e.Name, m.InputPath(e), e.Path)
	}
	data, err := os.ReadFile(filepath.Join(c.root, m.InputPath(e)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var inputs map[string]any
	if err := yaml.Unmarshal(data, &inputs); err != nil {
		return nil, fmt.Errorf("%s: %w", m.InputPath(e), err)
	}
	return inputs, nil
}

// Placement is a module inside a step of an environment.
type Placement struct {
	Step   Step
	Module Module
}

// Modules resolves every module of every step of the environment.
func (c *Config) Modules(e *Environment) ([]Placement, error) {
	var result []Placement
	for _, step := range e.Steps {
		for _, ref := range step.Modules {
			m, err := c.Resolve(ref)
			if err != nil {
				return nil, fmt.Errorf("%s: step %s: %w", e.Path, step.Name, err)
			}
			result = append(result, Placement{Step: step, Module: m})
		}
	}
	return result, nil
}

// Find returns where a module source sits in the environment, by its last
// occurrence (a per-branch step is appended after the regular ones).
func (c *Config) Find(e *Environment, source string) (Placement, bool) {
	var result Placement
	found := false
	placements, err := c.Modules(e)
	if err != nil {
		return result, false
	}
	for _, p := range placements {
		if p.Module.Source == source {
			result, found = p, true
		}
	}
	return result, found
}

// EnvironmentsOf lists the environments whose steps include the module,
// sorted by name.
func (c *Config) EnvironmentsOf(m Module) []*Environment {
	var result []*Environment
	for _, e := range c.All() {
		if _, ok := c.Find(e, m.Source); ok {
			result = append(result, e)
		}
	}
	return result
}

// Well known module sources the framework derives cluster and gateway
// information from, per cloud.
var (
	clusterSources = map[string]string{CloudAWS: "aws/eks", CloudGoogle: "google/gke", CloudOracle: "oracle/oke"}
	gatewaySources = map[string]string{CloudAWS: "aws-alb", CloudGoogle: "google-gateway", CloudOracle: "oracle-gateway"}
	dnsSources     = map[string][]string{CloudAWS: {"aws-v2/route53", "aws/route53"}, CloudGoogle: {"google/dns"}, CloudOracle: {"oracle/dns"}}
)

// ClusterModule returns the placement of the environment's Kubernetes
// cluster module (aws/eks, google/gke or oracle/oke). The cluster is named
// <prefix>-<step>-<module> by the agent.
func (c *Config) ClusterModule(e *Environment) (Placement, bool) {
	return c.Find(e, clusterSources[e.Cloud])
}

// ClusterName is the agent's name for the environment's cluster, or "" when
// the environment has no cluster module.
func (c *Config) ClusterName(e *Environment) string {
	p, ok := c.ClusterModule(e)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s-%s-%s", e.Prefix, p.Step.Name, p.Module.AgentName(e))
}

// GatewayModule returns the placement of the environment's gateway module
// (aws-alb, google-gateway or oracle-gateway).
func (c *Config) GatewayModule(e *Environment) (Placement, bool) {
	return c.Find(e, gatewaySources[e.Cloud])
}

// DNSModule returns the placement of the environment's public DNS module
// (route53 or dns).
func (c *Config) DNSModule(e *Environment) (Placement, bool) {
	for _, source := range dnsSources[e.Cloud] {
		if p, ok := c.Find(e, source); ok {
			return p, true
		}
	}
	return Placement{}, false
}

// LoadModule describes the module whose directory (or test directory) is dir,
// an absolute path inside root.
func (c *Config) LoadModule(dir string) (Module, error) {
	rel, err := filepath.Rel(c.root, dir)
	if err != nil {
		return Module{}, err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 0 && parts[len(parts)-1] == "test" {
		parts = parts[:len(parts)-1]
	}
	modulesDir := filepath.ToSlash(c.ModulesDir)
	if len(parts) != 3 || parts[0] != modulesDir {
		return Module{}, fmt.Errorf("%s is not a module directory (expected %s/<type>/<name>)", rel, modulesDir)
	}
	m, err := c.moduleAt(parts[1], parts[2])
	if err != nil {
		return Module{}, err
	}
	if m.External {
		return Module{}, fmt.Errorf("module directory %s does not exist", rel)
	}
	return m, nil
}

// ModuleBySource resolves an agent module source ("aws/vpc" or "argocd") to a
// module of this repository, or to an external module when no directory for
// it exists.
func (c *Config) ModuleBySource(source string) (Module, error) {
	return c.Resolve(ModuleRef{Source: source})
}

// Resolve is ModuleBySource for a step entry, keeping its name and inputs.
func (c *Config) Resolve(ref ModuleRef) (Module, error) {
	parts := strings.Split(ref.Source, "/")
	var m Module
	var err error
	switch len(parts) {
	case 1:
		m, err = c.moduleAt("k8s", parts[0])
	case 2:
		m, err = c.moduleAt(parts[0], parts[1])
	default:
		return Module{}, fmt.Errorf("invalid module source %q", ref.Source)
	}
	if err != nil {
		return Module{}, err
	}
	m.Ref = ref
	return m, nil
}

// LocalModules lists every module directory of this repository, in path order.
func (c *Config) LocalModules() ([]Module, error) {
	types, err := os.ReadDir(filepath.Join(c.root, c.ModulesDir))
	if err != nil {
		return nil, err
	}
	var modules []Module
	for _, t := range types {
		if !t.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(c.root, c.ModulesDir, t.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			m, err := c.moduleAt(t.Name(), entry.Name())
			if err != nil {
				return nil, err
			}
			modules = append(modules, m)
		}
	}
	return modules, nil
}

func (c *Config) moduleAt(moduleType, name string) (Module, error) {
	if moduleType == "" || name == "" {
		return Module{}, fmt.Errorf("invalid module source %q", moduleType+"/"+name)
	}
	m := Module{
		Type:   moduleType,
		Name:   name,
		Source: moduleType + "/" + name,
	}
	if m.IsK8s() {
		m.Source = name
	}
	dir := filepath.Join(c.ModulesDir, moduleType, name)
	if _, err := os.Stat(filepath.Join(c.root, dir)); err != nil {
		if os.IsNotExist(err) {
			m.External = true
			return m, nil
		}
		return Module{}, fmt.Errorf("module %s: %w", dir, err)
	}
	m.Dir = dir
	data, err := os.ReadFile(filepath.Join(c.root, m.TestDir(), ModuleFileName))
	if err == nil {
		if err := yaml.UnmarshalStrict(data, &m.Meta); err != nil {
			return Module{}, fmt.Errorf("%s: %w", filepath.Join(m.TestDir(), ModuleFileName), err)
		}
	} else if !os.IsNotExist(err) {
		return Module{}, err
	}
	return m, nil
}

// CurrentModule describes the module whose test directory is the working
// directory, which is where `go test` runs a module's tests.
func CurrentModule(t logger.T) Module {
	t.Helper()
	config := MustLoad(t)
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := config.LoadModule(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return m
}
