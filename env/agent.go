package env

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// AgentModule is a module entry of a generated agent config.
type AgentModule struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	DefaultModule bool           `json:"default_module,omitempty"`
}

// AgentStep is a step of a generated agent config.
type AgentStep struct {
	Name            string        `json:"name"`
	Type            string        `json:"type,omitempty"`
	ArgocdNamespace string        `json:"argocd_namespace,omitempty"`
	Modules         []AgentModule `json:"modules,omitempty"`
}

// AgentSource is a module source of an agent config. The agent associates a
// module with the first source that includes it: a source with Include lists
// exactly the module sources it provides, one without includes everything
// not excluded.
type AgentSource struct {
	URL          string   `json:"url"`
	Version      string   `json:"version,omitempty"`
	ForceVersion bool     `json:"force_version,omitempty"`
	Include      []string `json:"include,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
}

// AgentConfig is the part of a generated agent config tests read back.
type AgentConfig struct {
	Sources []AgentSource `json:"sources,omitempty"`
	Steps   []AgentStep   `json:"steps"`
}

// AgentsDir is where generated agent configurations live.
func (c *Config) AgentsDir() string {
	if dir := os.Getenv(AgentsDirEnv); dir != "" {
		return dir
	}
	return filepath.Join(c.root, "agents")
}

// AgentConfigPath is the generated config file of an environment.
func (c *Config) AgentConfigPath(e *Environment) string {
	return filepath.Join(c.AgentsDir(), e.Name, "config.yaml")
}

// LoadAgentConfig reads the generated agent config of an environment.
func (c *Config) LoadAgentConfig(e *Environment) (*AgentConfig, error) {
	data, err := os.ReadFile(c.AgentConfigPath(e))
	if err != nil {
		return nil, fmt.Errorf("agent config for %s not found, run the generator first: %w", e.Name, err)
	}
	config := &AgentConfig{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%s: %w", c.AgentConfigPath(e), err)
	}
	return config, nil
}

// AgentPlacement is where a module was deployed in an environment according
// to the generated agent config.
type AgentPlacement struct {
	Step AgentStep
	// Module is the agent module entry; its Name is the ArgoCD application and
	// namespace for k8s modules and the module name for terraform modules.
	Module AgentModule
}

// Find locates a module source in the agent config. When the source appears in
// several steps the last one wins: a per-branch step is appended after the
// regular steps and is the one the agent actually ran.
func (a *AgentConfig) Find(source string) (AgentPlacement, bool) {
	var result AgentPlacement
	found := false
	for _, step := range a.Steps {
		for _, m := range step.Modules {
			if m.Source == source && !m.DefaultModule {
				result = AgentPlacement{Step: step, Module: m}
				found = true
			}
		}
	}
	return result, found
}

// ModulePlacement returns where the calling module test's module was deployed in e.
// INFRALIB_STEP overrides the step name, for running a test by hand against a
// step the generator did not write.
func ModulePlacement(t logger.T, e *Environment) AgentPlacement {
	t.Helper()
	config := MustLoad(t)
	m := CurrentModule(t)
	return config.PlacementOf(t, e, m)
}

// PlacementOf is ModulePlacement for an explicit module.
func (c *Config) PlacementOf(t logger.T, e *Environment, m Module) AgentPlacement {
	t.Helper()
	agent, err := c.LoadAgentConfig(e)
	if err != nil {
		t.Fatalf("%v", err)
	}
	p, ok := agent.Find(m.Source)
	if !ok {
		t.Fatalf("module %s is not part of the generated agent config for %s (%s)", m.Source, e.Name, c.AgentConfigPath(e))
	}
	if step := strings.TrimSpace(os.Getenv("INFRALIB_STEP")); step != "" {
		p.Step.Name = step
	}
	return p
}
