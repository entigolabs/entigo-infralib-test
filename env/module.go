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
	// Name overrides the agent module (and so ArgoCD app and namespace) name,
	// which defaults to <module>-<prefix>. Charts that must live in a fixed
	// namespace set it, e.g. crossplane-core -> crossplane-system.
	Name string `json:"name,omitempty"`
	// NoPrefix keeps the agent module name equal to the module name.
	NoPrefix bool `json:"no_prefix,omitempty"`
	// PinStep tests the module inside its regular step even when the rest of a
	// pull request run uses a per-branch step (modules the step itself depends
	// on, such as config-rules).
	PinStep bool `json:"pin_step,omitempty"`
}

// IsK8s reports whether the module is a Kubernetes (argocd-apps) module.
func (m Module) IsK8s() bool { return m.Type == "k8s" }

// AgentName is the module name used in the agent config for an environment.
func (m Module) AgentName(e *Environment) string {
	if m.Ref.Name != "" {
		return m.Ref.Name
	}
	if m.Meta.Name != "" {
		return m.Meta.Name
	}
	if !m.IsK8s() || m.Meta.NoPrefix {
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

// InputPath is the agent input file of the module for an environment and
// step, relative to the repository root. A module of this repository keeps it
// in its test directory as <env>.yaml; an external module in
// environments/<env>/<step>/<module>.yaml unless the step entry names one.
func (m Module) InputPath(e *Environment, step string) string {
	if m.Ref.Input != "" {
		return m.Ref.Input
	}
	if m.External {
		return filepath.Join(InputsDir, e.Name, step, m.Name+".yaml")
	}
	return filepath.Join(m.TestDir(), e.InputFileName())
}

// HasInput reports whether the module's input file for the environment and
// step exists in the repository.
func (c *Config) HasInput(m Module, e *Environment, step string) bool {
	_, err := os.Stat(filepath.Join(c.root, m.InputPath(e, step)))
	return err == nil
}

// IsMember reports whether the module takes part in the environment's step:
// external modules always do, modules of this repository when they have an
// input file for the environment.
func (c *Config) IsMember(m Module, e *Environment, step string) bool {
	return m.External || c.HasInput(m, e, step)
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

// Resolve is ModuleBySource for a step entry, keeping its overrides.
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

// ModulesIn lists the modules of this repository under a directory relative
// to root, in name order.
func (c *Config) ModulesIn(dir string) ([]Module, error) {
	entries, err := os.ReadDir(filepath.Join(c.root, dir))
	if err != nil {
		return nil, err
	}
	var modules []Module
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		m, err := c.LoadModule(filepath.Join(c.root, dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		modules = append(modules, m)
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
