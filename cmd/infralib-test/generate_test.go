package main

import (
	"os"
	"path/filepath"
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
	// regular deployment; the gateway module rides along with its inputs as a
	// default that is not applied, under its regular name.
	require.Equal(t, "mart-foo-hello-world-exa=hello-world aws-alb-exa=aws-alb", strings.Join(moduleNames(last), " "))
	require.True(t, last.Modules[1].DefaultModule)
	require.Equal(t, "public", last.Modules[1].Inputs["global"].(map[string]any)["externalGateway"])
	p, ok := agent.Find("hello-world")
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
	require.Equal(t, []string{"net"}, result.Steps["aws_exa"])
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	require.Equal(t, "net,infra,apps", strings.Join(stepNames(agent), ","), "in-place adds no step")
	require.Equal(t, "main", agent.Sources[0].Version)
	require.True(t, agent.Sources[0].ForceVersion)
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
