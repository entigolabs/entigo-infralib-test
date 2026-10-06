package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/env"
)

// generateOptions are the inputs of one generation.
type generateOptions struct {
	Config       *env.Config
	OutDir       string
	Environments []*env.Environment
	// Self is the agent source of this repository; the sources of the
	// environments file follow it and provide the external modules.
	Self env.AgentSource
	// Modules restricts the run to these module sources. Each gets a step of
	// its own named <StepPrefix>-<module> unless it pins its regular step.
	Modules    []env.Module
	StepPrefix string
	// InPlace applies the restricted Modules inside their regular steps, the
	// way a post-merge run updates a stable environment.
	InPlace bool
}

// generateResult is what the orchestrator needs to run the agent.
type generateResult struct {
	// Steps lists, per environment, the steps the agent should run; empty means all.
	Steps map[string][]string
	// Skipped lists environments no requested module has an input for.
	Skipped []string
}

func generateCommand(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	root := fs.String("root", "", "repository root (default: INFRALIB_ROOT or the parent holding environments.yaml)")
	out := fs.String("out", "", "output directory (default: <root>/agents or INFRALIB_AGENTS_DIR)")
	var envNames, modules stringList
	fs.Var(&envNames, "env", "environment to generate for (repeatable, default all)")
	source := fs.String("source", "/conf", "agent source url or path of this repository; the sources of "+env.FileName+" follow it")
	version := fs.String("version", "", "version of this repository's source")
	forceVersion := fs.Bool("force-version", false, "set force_version on this repository's source")
	fs.Var(&modules, "module", "only test this module source, in a step of its own (repeatable)")
	stepPrefix := fs.String("step-prefix", "", "name prefix of per-module steps (required with -module unless -in-place)")
	inPlace := fs.Bool("in-place", false, "with -module: apply the modules inside their regular steps instead of per-module steps")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: infralib-test generate [flags]\n\nWrites agents/<env>/config.yaml and the module inputs under agents/<env>/config/<step>/ for every selected environment, then prints one line per environment: '<env> all', '<env> <step>,<step>' or '<env> skip'.\n\n")
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
	opts := generateOptions{
		Config:       config,
		OutDir:       *out,
		Environments: environments,
		StepPrefix:   *stepPrefix,
		InPlace:      *inPlace,
		Self:         env.AgentSource{URL: *source, Version: *version, ForceVersion: *forceVersion},
	}
	if opts.OutDir == "" {
		opts.OutDir = config.AgentsDir()
	}
	for _, source := range modules {
		m, err := config.ModuleBySource(source)
		if err != nil {
			return err
		}
		if m.External {
			return fmt.Errorf("module %s is not part of this repository and cannot be tested here", source)
		}
		opts.Modules = append(opts.Modules, m)
	}
	if len(opts.Modules) > 0 && opts.StepPrefix == "" && !opts.InPlace {
		return fmt.Errorf("-step-prefix is required with -module")
	}
	result, err := generate(opts)
	if err != nil {
		return err
	}
	// Machine readable summary for the orchestrator: one line per environment.
	for _, e := range environments {
		if contains(result.Skipped, e.Name) {
			fmt.Printf("%s skip\n", e.Name)
			continue
		}
		steps := result.Steps[e.Name]
		if len(steps) == 0 {
			fmt.Printf("%s all\n", e.Name)
		} else {
			fmt.Printf("%s %s\n", e.Name, strings.Join(steps, ","))
		}
	}
	return nil
}

// generate writes one agent configuration per environment.
func generate(opts generateOptions) (*generateResult, error) {
	result := &generateResult{Steps: map[string][]string{}}
	for _, e := range opts.Environments {
		if len(opts.Modules) > 0 && !anyModuleHasInput(opts, e) {
			result.Skipped = append(result.Skipped, e.Name)
			continue
		}
		steps, err := generateEnvironment(opts, e)
		if err != nil {
			return nil, fmt.Errorf("environment %s: %w", e.Name, err)
		}
		result.Steps[e.Name] = steps
	}
	return result, nil
}

func anyModuleHasInput(opts generateOptions, e *env.Environment) bool {
	for _, m := range opts.Modules {
		if e.HasModuleInput(filepath.Join(opts.Config.Root(), m.TestDir())) {
			return true
		}
	}
	return false
}

// generateEnvironment writes agents/<env>/config.yaml and the module inputs
// under agents/<env>/config/<step>/<module>.yaml, and returns the steps to run
// (nil for all).
func generateEnvironment(opts generateOptions, e *env.Environment) ([]string, error) {
	config := opts.Config
	dir := filepath.Join(opts.OutDir, e.Name)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	agent := env.AgentConfig{EnableOpenTofu: true}
	homes := map[string]env.Step{} // module source -> regular step
	var local, external []string   // module sources by origin, for the source include lists
	for _, step := range e.Steps {
		members, err := stepModules(config, e, step)
		if err != nil {
			return nil, err
		}
		agentStep := env.AgentStep{
			Name:                  step.Name,
			Type:                  step.Type,
			ManualApproveUpdate:   "never",
			ManualApproveRun:      "never",
			Vpc:                   step.Vpc,
			KubernetesClusterName: step.KubernetesClusterName,
			ArgocdNamespace:       step.ArgocdNamespace,
		}
		for _, m := range members {
			homes[m.Source] = step
			if m.External {
				external = appendUnique(external, m.Source)
			} else {
				local = appendUnique(local, m.Source)
			}
			agentStep.Modules = append(agentStep.Modules, env.AgentModule{Name: m.AgentName(e), Source: m.Source})
			if err := copyInput(config, e, m, dir, step.Name, step.Name, m.AgentName(e)); err != nil {
				return nil, err
			}
		}
		if len(agentStep.Modules) > 0 {
			agent.Steps = append(agent.Steps, agentStep)
		}
	}
	if len(external) > 0 && len(config.Sources) == 0 {
		return nil, fmt.Errorf("modules %s are not part of this repository; add the source that provides them under sources: in %s", strings.Join(external, ", "), env.FileName)
	}

	var runSteps []string
	for _, m := range opts.Modules {
		if !e.HasModuleInput(filepath.Join(config.Root(), m.TestDir())) {
			continue
		}
		home, ok := homes[m.Source]
		if !ok {
			return nil, fmt.Errorf("module %s belongs to no step of the environment; add it to a step in %s", m.Source, env.FileName)
		}
		if m.Meta.PinStep || opts.InPlace {
			runSteps = appendUnique(runSteps, home.Name)
			continue
		}
		name := fmt.Sprintf("%s-%s", opts.StepPrefix, m.Name)
		step := env.AgentStep{
			Name:                  name,
			Type:                  home.Type,
			ManualApproveUpdate:   "never",
			ManualApproveRun:      "never",
			Vpc:                   home.Vpc,
			KubernetesClusterName: home.KubernetesClusterName,
			ArgocdNamespace:       home.ArgocdNamespace,
			Modules:               []env.AgentModule{{Name: m.AgentName(e), Source: m.Source}},
		}
		if err := copyInput(config, e, m, dir, home.Name, name, m.AgentName(e)); err != nil {
			return nil, err
		}
		// Modules the chart chains inputs from (the gateway) must be present in
		// the step for templating to resolve, as defaults that are not applied.
		for _, source := range home.DefaultModules {
			d, err := config.ModuleBySource(source)
			if err != nil {
				return nil, err
			}
			if d.Source == m.Source || !config.IsMember(d, e, home.Name) {
				continue
			}
			step.Modules = append(step.Modules, env.AgentModule{Name: d.AgentName(e), Source: d.Source, DefaultModule: true})
			if err := copyInput(config, e, d, dir, home.Name, name, d.AgentName(e)); err != nil {
				return nil, err
			}
		}
		agent.Steps = append(agent.Steps, step)
		runSteps = appendUnique(runSteps, name)
	}

	// This repository is the first source. When other sources provide
	// external modules, restrict it to the modules it actually holds so the
	// agent looks the rest up in the later sources.
	self := opts.Self
	if len(external) > 0 {
		sort.Strings(local)
		self.Include = local
	}
	agent.Sources = append([]env.AgentSource{self}, config.Sources...)

	data, err := yaml.Marshal(agent)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644); err != nil {
		return nil, err
	}
	return runSteps, nil
}

// stepModules resolves the modules of a step that take part in the environment.
func stepModules(config *env.Config, e *env.Environment, step env.Step) ([]env.Module, error) {
	var candidates []env.Module
	for _, ref := range step.Modules {
		m, err := config.Resolve(ref)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", step.Name, err)
		}
		candidates = append(candidates, m)
	}
	if step.ModulesDir != "" {
		listed, err := config.ModulesIn(step.ModulesDir)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", step.Name, err)
		}
		sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
		candidates = append(candidates, listed...)
	}
	var members []env.Module
	seen := map[string]bool{}
	for _, m := range candidates {
		if seen[m.Source] {
			continue
		}
		seen[m.Source] = true
		if config.IsMember(m, e, step.Name) {
			members = append(members, m)
		}
	}
	return members, nil
}

// copyInput copies the module's input for the environment into the generated
// config directory of targetStep. homeStep is the step the input is looked up
// under (external modules keep inputs per step); a missing external input is
// fine, the module then runs with its defaults.
func copyInput(config *env.Config, e *env.Environment, m env.Module, dir, homeStep, targetStep, name string) error {
	src := filepath.Join(config.Root(), m.InputPath(e, homeStep))
	data, err := os.ReadFile(src)
	if err != nil {
		if m.External && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	target := filepath.Join(dir, "config", targetStep)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, name+".yaml"), data, 0o644)
}

func appendUnique(list []string, value string) []string {
	if contains(list, value) {
		return list
	}
	return append(list, value)
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
