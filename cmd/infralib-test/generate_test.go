package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/env"
)

const awsExa = `
# a comment the agent never sees; the generator rewrites the file
agent_version: v1.14.4
sources:
  - url: https://github.com/entigolabs/entigo-infralib-release
callback:
  url: http://localhost
steps:
  - name: net
    type: terraform
    approve: force
    modules:
      - name: vpc
        source: aws/vpc
        inputs:
          vpc_cidr: "10.0.0.0/16"
      - source: aws-v2/route53
      - source: aws/hello-world
  - name: infra
    type: terraform
    manual_approve_run: changes
    vpc:
      attach: true
    modules:
      - source: aws/eks
  - name: apps
    type: argocd-apps
    argocd_namespace: argocd-exa
    modules:
      - source: argocd
        inputs:
          argocd: {}
      - source: aws-alb
        inputs:
          global: {externalGateway: public}
      - source: crossplane-core
        name: crossplane-system
      - source: hello-world
`

const googleExa = `
sources:
  - url: https://github.com/entigolabs/entigo-infralib-release
steps:
  - name: net
    modules:
      - source: google/vpc
`

// fixture writes a repository with two environments and the local modules
// aws/hello-world (with an input for aws_exa) and hello-world (without).
func fixture(t *testing.T) *env.Config {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	write("environments/aws_exa.yaml", awsExa)
	write("environments/google_exa.yaml", googleExa)
	write("go.mod", "module example.com/modules\n")
	write("modules/aws/hello-world/main.tf", "")
	write("modules/aws/hello-world/test/aws_exa.yaml", "greeting: hello\n")
	write("modules/aws/hello-world/test/hello_test.go", "package test\n")
	write("modules/k8s/hello-world/Chart.yaml", "name: hello-world\n")
	write("modules/k8s/hello-world/test/hello_test.go", "package test\n")
	config, err := env.Load(root)
	require.NoError(t, err)
	return config
}

func readRaw(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	return raw
}

func readAgent(t *testing.T, path string) env.AgentConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var agent env.AgentConfig
	require.NoError(t, yaml.Unmarshal(data, &agent))
	return agent
}

func stepNames(agent env.AgentConfig) []string {
	var names []string
	for _, s := range agent.Steps {
		names = append(names, s.Name)
	}
	return names
}

func moduleNames(step env.AgentStep) []string {
	var names []string
	for _, m := range step.Modules {
		names = append(names, m.Name+"="+m.Source)
	}
	return names
}

func TestGenerateFull(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
	})
	require.NoError(t, err)
	require.Empty(t, result.Skipped)
	require.Empty(t, result.Steps["aws_exa"])

	path := filepath.Join(out, "aws_exa", "config.yaml")
	agent := readAgent(t, path)
	require.Equal(t, "net,infra,apps", strings.Join(stepNames(agent), ","))
	require.Equal(t, "vpc=aws/vpc route53=aws-v2/route53 hello-world=aws/hello-world", strings.Join(moduleNames(agent.Steps[0]), " "))
	require.Equal(t, "argocd-exa=argocd aws-alb-exa=aws-alb crossplane-system=crossplane-core hello-world-exa=hello-world", strings.Join(moduleNames(agent.Steps[2]), " "))
	// Inputs: the step entry's for external modules, the parsed test/<env>.yaml
	// for local ones, none when there is nothing to pass.
	net := agent.Steps[0].Modules
	require.Equal(t, "10.0.0.0/16", net[0].Inputs["vpc_cidr"])
	require.Nil(t, net[1].Inputs)
	require.Equal(t, "hello", net[2].Inputs["greeting"])
	require.Nil(t, agent.Steps[2].Modules[3].Inputs, "k8s hello-world has no input file for aws_exa")
	// The repository source comes first and lists only its own modules.
	require.Equal(t, "/conf", agent.Sources[0].URL)
	require.Equal(t, []string{"aws/hello-world", "hello-world"}, agent.Sources[0].Include)
	require.Equal(t, "https://github.com/entigolabs/entigo-infralib-release", agent.Sources[1].URL)
	require.Empty(t, agent.Sources[1].Include)
	// Everything the framework does not model passes through.
	raw := readRaw(t, path)
	require.Equal(t, "v1.14.4", raw["agent_version"])
	require.Equal(t, map[string]any{"url": "http://localhost"}, raw["callback"])
	steps := raw["steps"].([]any)
	require.Equal(t, "force", steps[0].(map[string]any)["approve"])
	require.Equal(t, map[string]any{"attach": true}, steps[1].(map[string]any)["vpc"])
	// Unattended runs: approvals default to never, a step's own setting wins.
	require.Equal(t, "never", steps[0].(map[string]any)["manual_approve_run"])
	require.Equal(t, "never", steps[0].(map[string]any)["manual_approve_update"])
	require.Equal(t, "changes", steps[1].(map[string]any)["manual_approve_run"])
	require.Equal(t, "never", steps[1].(map[string]any)["manual_approve_update"])
	entries, _ := os.ReadDir(filepath.Join(out, "aws_exa"))
	require.Len(t, entries, 1, "only config.yaml")

	google := readAgent(t, filepath.Join(out, "google_exa", "config.yaml"))
	require.Equal(t, "vpc=google/vpc", strings.Join(moduleNames(google.Steps[0]), " "))
}

func TestGenerateModuleStep(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	hello, err := config.ModuleBySource("hello-world")
	require.NoError(t, err)
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
		Modules:      []env.Module{hello},
		StepPrefix:   "mart-foo",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"google_exa"}, result.Skipped, "google_exa does not list hello-world")
	require.Equal(t, []string{"mart-foo-hello-world"}, result.Steps["aws_exa"])

	path := filepath.Join(out, "aws_exa", "config.yaml")
	agent := readAgent(t, path)
	last := agent.Steps[len(agent.Steps)-1]
	require.Equal(t, "mart-foo-hello-world", last.Name)
	require.Equal(t, env.StepTypeArgoCD, last.Type)
	require.Equal(t, "argocd-exa", last.ArgocdNamespace)
	// The k8s module gets a branch-prefixed name so it does not replace the
	// regular deployment; the gateway module rides along with its inputs,
	// under its regular name. Both are marked default: the config holds
	// each source twice now, and the agent's config-wide typed lookup fails
	// on a type with several modules and no default.
	require.Equal(t, "mart-foo-hello-world-exa=hello-world aws-alb-exa=aws-alb", strings.Join(moduleNames(last), " "))
	require.True(t, last.Modules[0].DefaultModule)
	require.True(t, last.Modules[1].DefaultModule)
	for _, step := range agent.Steps[:3] {
		for _, m := range step.Modules {
			require.False(t, m.DefaultModule, "%s in %s", m.Source, step.Name)
		}
	}
	require.Equal(t, "public", last.Modules[1].Inputs["global"].(map[string]any)["externalGateway"])
	p, ok := agent.Find("hello-world", regularStep)
	require.True(t, ok)
	require.Equal(t, "mart-foo-hello-world", p.Step.Name, "Find prefers the per-module step")
	require.Equal(t, "mart-foo-hello-world-exa", p.Module.Name, "and reports the branch-prefixed application")
	// Step fields the framework does not model are copied from the home step.
	raw := readRaw(t, path)
	steps := raw["steps"].([]any)
	require.Len(t, steps, 4)
}

func TestGenerateInPlace(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	hello, err := config.ModuleBySource("aws/hello-world")
	require.NoError(t, err)
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "https://github.com/example/modules", Version: "main", ForceVersion: true},
		Modules:      []env.Module{hello},
		InPlace:      true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"net"}, result.Steps["aws_exa"], "a terraform module is applied inside its regular step")
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	require.Equal(t, "net,infra,apps", strings.Join(stepNames(agent), ","), "in-place adds no step for terraform")
	require.Equal(t, "main", agent.Sources[0].Version)
	require.True(t, agent.Sources[0].ForceVersion)
}

func TestGenerateInPlaceK8s(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	hello, err := config.ModuleBySource("hello-world")
	require.NoError(t, err)
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
		Modules:      []env.Module{hello},
		StepPrefix:   "main",
		InPlace:      true,
	})
	require.NoError(t, err)
	// A k8s module in place still gets a step of its own, so the regular
	// apps step (and the other modules in it) is not applied, but it keeps
	// its regular application name and so replaces the regular deployment.
	require.Equal(t, []string{"main-hello-world"}, result.Steps["aws_exa"])
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	require.Equal(t, "net,infra,apps,main-hello-world", strings.Join(stepNames(agent), ","))
	last := agent.Steps[len(agent.Steps)-1]
	require.Equal(t, "hello-world-exa=hello-world aws-alb-exa=aws-alb", strings.Join(moduleNames(last), " "))
	p, ok := agent.Find("hello-world", regularStep)
	require.True(t, ok)
	require.Equal(t, "main-hello-world", p.Step.Name)
	require.Equal(t, "hello-world-exa", p.Module.Name)
}

func TestGenerateTwoModulesOneDefaultGateway(t *testing.T) {
	// A second k8s module next to hello-world in the same apps step.
	root := fixture(t).Root()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	write("modules/k8s/hello-other/Chart.yaml", "name: hello-other\n")
	write("modules/k8s/hello-other/test/hello_test.go", "package test\n")
	write("environments/aws_exa.yaml", awsExa+"      - source: hello-other\n")
	config, err := env.Load(root)
	require.NoError(t, err)
	hello, err := config.ModuleBySource("hello-world")
	require.NoError(t, err)
	other, err := config.ModuleBySource("hello-other")
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "agents")
	_, err = generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
		Modules:      []env.Module{hello, other},
		StepPrefix:   "main",
		InPlace:      true,
	})
	require.NoError(t, err)
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	require.Equal(t, "net,infra,apps,main-hello-world,main-hello-other", strings.Join(stepNames(agent), ","))
	// Every module of the own steps is marked default (the two under test
	// and the gateway copy each carries), nothing in the regular steps is;
	// the agent takes the first default of a type in config order.
	require.Equal(t, []string{"aws-alb", "aws-alb", "hello-other", "hello-world"}, defaultSources(agent))
	for _, step := range agent.Steps[3:] {
		for _, m := range step.Modules {
			require.True(t, m.DefaultModule, "%s in %s", m.Source, step.Name)
		}
	}
	// Find skips the gateway copies of the own steps: the gateway was not
	// tested, so it is found in its regular step.
	p, ok := agent.Find("aws-alb", regularStep)
	require.True(t, ok)
	require.Equal(t, "apps", p.Step.Name)
	// Both per-module steps still carry the gateway for step-scoped templating.
	for _, name := range []string{"main-hello-world", "main-hello-other"} {
		for _, step := range agent.Steps {
			if step.Name == name {
				require.Equal(t, "aws-alb", step.Modules[1].Source, name)
			}
		}
	}
}

func TestGeneratePinnedModule(t *testing.T) {
	config := fixture(t)
	hello, err := config.ModuleBySource("hello-world")
	require.NoError(t, err)
	hello.Meta.PinStep = true
	out := filepath.Join(t.TempDir(), "agents")
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
		Modules:      []env.Module{hello},
		StepPrefix:   "mart-foo",
	})
	require.NoError(t, err)
	// pin_step makes a pull request behave like an in-place run for the module.
	require.Equal(t, []string{"mart-foo-hello-world"}, result.Steps["aws_exa"])
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	last := agent.Steps[len(agent.Steps)-1]
	require.Equal(t, "mart-foo-hello-world", last.Name)
	require.Equal(t, "hello-world-exa", last.Modules[0].Name, "the regular application, not a branch copy")
}

func TestGenerateExternalNeedsSource(t *testing.T) {
	config := fixture(t)
	config.Environments["aws_exa"].Sources = nil
	_, err := generate(generateOptions{
		Config:       config,
		OutDir:       filepath.Join(t.TempDir(), "agents"),
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
	})
	require.ErrorContains(t, err, "sources:")
}

func TestGenerateLocalInlineInputsRejected(t *testing.T) {
	config := fixture(t)
	root := config.Root()
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "aws_exa.yaml"), []byte(`
sources: [{url: https://example.com/release}]
steps:
  - name: net
    modules:
      - source: aws/hello-world
        inputs: {greeting: inline}
`), 0o644))
	config, err := env.Load(root)
	require.NoError(t, err)
	_, err = generate(generateOptions{Config: config, OutDir: filepath.Join(t.TempDir(), "agents"), Environments: config.All(), Self: env.AgentSource{URL: "/conf"}})
	require.ErrorContains(t, err, "modules/aws/hello-world/test/aws_exa.yaml")
}

func TestGenerateCopiesCACertificates(t *testing.T) {
	config := fixture(t)
	root := config.Root()
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "environments", "ca-certificates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "ca-certificates", "root.crt"), []byte(pem), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "environments", "aws_exa", "ca-certificates"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "aws_exa", "ca-certificates", "aws.crt"), []byte(pem), 0o644))
	config, err := env.Load(root)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "agents")
	_, err = generate(generateOptions{Config: config, OutDir: out, Environments: config.All(), Self: env.AgentSource{URL: "/conf"}})
	require.NoError(t, err)
	aws, err := os.ReadDir(filepath.Join(out, "aws_exa", "ca-certificates"))
	require.NoError(t, err)
	require.Len(t, aws, 2, "the shared certificate and aws_exa's own")
	google, err := os.ReadDir(filepath.Join(out, "google_exa", "ca-certificates"))
	require.NoError(t, err)
	require.Len(t, google, 1, "only the shared certificate")
	content, err := os.ReadFile(filepath.Join(out, "google_exa", "ca-certificates", "root.crt"))
	require.NoError(t, err)
	require.Equal(t, pem, string(content))
}

func TestGenerateCopiesStepFiles(t *testing.T) {
	config := fixture(t)
	root := config.Root()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "environments", "config", "net", "include"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "config", "net", "include", "shared.tf"), []byte("shared"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "environments", "aws_exa", "config", "net", "include"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "aws_exa", "config", "net", "include", "zone.tf"), []byte("zone"), 0o644))
	config, err := env.Load(root)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "agents")
	_, err = generate(generateOptions{Config: config, OutDir: out, Environments: config.All(), Self: env.AgentSource{URL: "/conf"}})
	require.NoError(t, err)
	aws, err := os.ReadDir(filepath.Join(out, "aws_exa", "config", "net", "include"))
	require.NoError(t, err)
	require.Len(t, aws, 2, "the shared file and aws_exa's own")
	google, err := os.ReadDir(filepath.Join(out, "google_exa", "config", "net", "include"))
	require.NoError(t, err)
	require.Len(t, google, 1, "only the shared file")
	content, err := os.ReadFile(filepath.Join(out, "aws_exa", "config", "net", "include", "zone.tf"))
	require.NoError(t, err)
	require.Equal(t, "zone", string(content))
}

// defaultSources lists the sources of the modules marked default_module, each
// with the step holding it, sorted; the regular steps come first in a config.
func defaultSources(agent env.AgentConfig) []string {
	var result []string
	for _, step := range agent.Steps {
		for _, m := range step.Modules {
			if m.DefaultModule {
				result = append(result, m.Source)
			}
		}
	}
	sort.Strings(result)
	return result
}

func TestGenerateKeepsEnvironmentDefault(t *testing.T) {
	// An environment that marks a module default itself keeps that choice.
	root := fixture(t).Root()
	data, err := os.ReadFile(filepath.Join(root, "environments", "aws_exa.yaml"))
	require.NoError(t, err)
	patched := strings.Replace(string(data), "      - source: aws-alb\n", "      - source: aws-alb\n        default_module: true\n", 1)
	require.NotEqual(t, string(data), patched)
	require.NoError(t, os.WriteFile(filepath.Join(root, "environments", "aws_exa.yaml"), []byte(patched), 0o644))
	config, err := env.Load(root)
	require.NoError(t, err)
	hello, err := config.ModuleBySource("hello-world")
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "agents")
	_, err = generate(generateOptions{Config: config, OutDir: out, Environments: config.All(), Self: env.AgentSource{URL: "/conf"}, Modules: []env.Module{hello}, StepPrefix: "mart-foo"})
	require.NoError(t, err)
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	require.Equal(t, []string{"aws-alb", "aws-alb", "hello-world"}, defaultSources(agent))
	for _, m := range agent.Steps[2].Modules {
		if m.Source == "aws-alb" {
			require.True(t, m.DefaultModule, "the environment's own default stays, and comes first in config order")
		}
	}
	p, ok := agent.Find("aws-alb", regularStep)
	require.True(t, ok)
	require.Equal(t, "apps", p.Step.Name, "the environment's own default is not a gateway copy")
}

// regularStep tells the fixture environments' own steps from per-module ones.
func regularStep(name string) bool { return name == "net" || name == "infra" || name == "apps" }
