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
	// environment file follow it and provide the external modules.
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
	// Skipped lists environments no requested module is part of.
	Skipped []string
}

func generateCommand(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	root := fs.String("root", "", "repository root (default: INFRALIB_ROOT or the parent holding environments/)")
	out := fs.String("out", "", "output directory (default: <root>/agents or INFRALIB_AGENTS_DIR)")
	var envNames, modules stringList
	fs.Var(&envNames, "env", "environment to generate for (repeatable, default all)")
	source := fs.String("source", "/conf", "agent source url or path of this repository; the sources of the environment file follow it")
	version := fs.String("version", "", "version of this repository's source")
	forceVersion := fs.Bool("force-version", false, "set force_version on this repository's source")
	fs.Var(&modules, "module", "only test this module source, in a step of its own (repeatable)")
	stepPrefix := fs.String("step-prefix", "", "name prefix of per-module steps (required with -module unless -in-place)")
	inPlace := fs.Bool("in-place", false, "with -module: apply the modules inside their regular steps instead of per-module steps")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: infralib-test generate [flags]\n\nWrites agents/<env>/config.yaml for every selected environment: the environment's own agent configuration with this repository as first source, module names and the inputs of this repository's modules filled in, and per-module steps appended. Prints one line per environment: '<env> all', '<env> <step>,<step>' or '<env> skip'.\n\n")
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
		if len(opts.Modules) > 0 && !anyModuleIsMember(opts, e) {
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

func anyModuleIsMember(opts generateOptions, e *env.Environment) bool {
	for _, m := range opts.Modules {
		if _, ok := opts.Config.Find(e, m.Source); ok {
			return true
		}
	}
	return false
}

// generateEnvironment patches a copy of the environment's agent config and
// writes it to agents/<env>/config.yaml. It returns the steps to run (nil
// for all). Fields the framework does not model pass through untouched;
// the approval fields default to never, since nobody is there to answer.
func generateEnvironment(opts generateOptions, e *env.Environment) ([]string, error) {
	config := opts.Config
	raw := e.Raw()
	rawSteps, _ := raw["steps"].([]any)
	if len(rawSteps) != len(e.Steps) {
		return nil, fmt.Errorf("%s: steps could not be read", e.Path)
	}

	var local, external []string // module sources by origin, for the source include list
	for i, step := range e.Steps {
		rawStep, _ := rawSteps[i].(map[string]any)
		// A test run is unattended: approve everything the agent would ask
		// about unless the step says otherwise.
		for _, key := range []string{"manual_approve_run", "manual_approve_update"} {
			if _, set := rawStep[key]; !set {
				rawStep[key] = "never"
			}
		}
		rawModules, _ := rawStep["modules"].([]any)
		for j, ref := range step.Modules {
			m, err := config.Resolve(ref)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", step.Name, err)
			}
			if m.External {
				external = appendUnique(external, m.Source)
			} else {
				local = appendUnique(local, m.Source)
			}
			patched, err := agentModule(config, e, m)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", step.Name, err)
			}
			rawModule, _ := rawModules[j].(map[string]any)
			rawModule["name"] = patched.Name
			if patched.Inputs != nil {
				rawModule["inputs"] = patched.Inputs
			}
		}
	}
	if len(external) > 0 && len(e.Sources) == 0 {
		return nil, fmt.Errorf("modules %s are not part of this repository; add the source that provides them under sources: in %s", strings.Join(external, ", "), e.Path)
	}

	// This repository is the first source. When other sources provide
	// external modules, restrict it to the modules it actually holds so the
	// agent looks the rest up in the later sources.
	self := opts.Self
	if len(external) > 0 {
		sort.Strings(local)
		self.Include = local
	}
	rawSources, _ := raw["sources"].([]any)
	raw["sources"] = append([]any{toRaw(self)}, rawSources...)

	// Per-module steps of a restricted run.
	var runSteps []string
	for _, m := range opts.Modules {
		home, ok := config.Find(e, m.Source)
		if !ok {
			continue
		}
		if m.Meta.PinStep || opts.InPlace {
			runSteps = appendUnique(runSteps, home.Step.Name)
			continue
		}
		name := fmt.Sprintf("%s-%s", opts.StepPrefix, m.Name)
		homeIndex := stepIndex(e, home.Step.Name)
		rawHome, _ := rawSteps[homeIndex].(map[string]any)
		rawStep := deepCopyMap(rawHome)
		rawStep["name"] = name
		own, err := agentModule(config, e, home.Module)
		if err != nil {
			return nil, err
		}
		// A k8s module gets a branch-prefixed application name so it lives
		// next to the regular step's deployment instead of replacing it.
		own.Name = home.Module.BranchName(e, opts.StepPrefix)
		modules := []any{toRaw(own)}
		// The gateway module a chart chains inputs from must be present in
		// the step for templating to resolve, as a default that is not applied.
		if gw, ok := config.GatewayModule(e); ok && gw.Step.Name == home.Step.Name && gw.Module.Source != m.Source && home.Step.Type == env.StepTypeArgoCD {
			def, err := agentModule(config, e, gw.Module)
			if err != nil {
				return nil, err
			}
			def.DefaultModule = true
			modules = append(modules, toRaw(def))
		}
		rawStep["modules"] = modules
		rawSteps = append(rawSteps, rawStep)
		runSteps = appendUnique(runSteps, name)
	}
	raw["steps"] = rawSteps

	data, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(opts.OutDir, e.Name)
	if err := os.RemoveAll(dir); err != nil {
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

// agentModule is the agent config entry of a module in an environment, inputs included.
func agentModule(config *env.Config, e *env.Environment, m env.Module) (env.AgentModule, error) {
	inputs, err := config.Inputs(m, e)
	if err != nil {
		return env.AgentModule{}, err
	}
	return env.AgentModule{Name: m.AgentName(e), Source: m.Source, Inputs: inputs}, nil
}

func stepIndex(e *env.Environment, name string) int {
	for i, s := range e.Steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// toRaw converts a typed value into the generic map form through YAML.
func toRaw(v any) map[string]any {
	data, err := yaml.Marshal(v)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		panic(err)
	}
	return m
}

func deepCopyMap(m map[string]any) map[string]any {
	return toRaw(m)
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
