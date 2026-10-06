package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/entigolabs/entigo-infralib-test/env"
)

const fixtureEnvironments = `
sources:
  - url: https://github.com/entigolabs/entigo-infralib-release
environments:
  aws_exa:
    cloud: aws
    prefix: exa
    region: eu-north-1
    kube_context: arn:aws:eks:eu-north-1:123456789012:cluster/exa-infra-eks
    steps:
      - name: net
        modules:
          - aws/vpc
          - aws-v2/route53
          - aws/hello-world
      - name: infra
        vpc: {attach: true}
        modules: [aws/eks]
      - name: apps
        type: argocd-apps
        argocd_namespace: argocd-exa
        default_modules: [aws-alb]
        modules:
          - argocd
          - aws-alb
          - source: crossplane-core
            name: crossplane-system
        modules_dir: modules/k8s
  google_exa:
    cloud: google
    prefix: exa
    region: europe-north1
    project: example
    steps:
      - name: net
        modules: [google/vpc, aws/hello-world]
`

// fixture writes a repository with one local terraform module (aws/hello-world,
// inputs for aws_exa only) and one local k8s module (hello-world, inputs for
// aws_exa), plus external inputs for vpc and argocd.
func fixture(t *testing.T) *env.Config {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(env.FileName, fixtureEnvironments)
	write("go.mod", "module example.com/modules\n")
	write("modules/aws/hello-world/main.tf", "")
	write("modules/aws/hello-world/test/aws_exa.yaml", "greeting: hello\n")
	write("modules/aws/hello-world/test/hello_test.go", "package test\n")
	write("modules/k8s/hello-world/Chart.yaml", "name: hello-world\n")
	write("modules/k8s/hello-world/test/aws_exa.yaml", "replicas: 1\n")
	write("modules/k8s/hello-world/test/hello_test.go", "package test\n")
	write("environments/aws_exa/net/vpc.yaml", "vpc_cidr: 10.0.0.0/16\n")
	write("environments/aws_exa/apps/argocd.yaml", "argocd: {}\n")
	config, err := env.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func readAgent(t *testing.T, path string) env.AgentConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var agent env.AgentConfig
	if err := yaml.Unmarshal(data, &agent); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 0 || len(result.Steps["aws_exa"]) != 0 {
		t.Fatalf("unexpected result %+v", result)
	}

	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	if got := strings.Join(stepNames(agent), ","); got != "net,infra,apps" {
		t.Fatalf("steps = %s", got)
	}
	if got := strings.Join(moduleNames(agent.Steps[0]), " "); got != "vpc=aws/vpc route53=aws-v2/route53 hello-world=aws/hello-world" {
		t.Fatalf("net modules = %s", got)
	}
	if got := strings.Join(moduleNames(agent.Steps[2]), " "); got != "argocd-exa=argocd aws-alb-exa=aws-alb crossplane-system=crossplane-core hello-world-exa=hello-world" {
		t.Fatalf("apps modules = %s", got)
	}
	if agent.Steps[2].ArgocdNamespace != "argocd-exa" || agent.Steps[1].Vpc["attach"] != true {
		t.Fatalf("step attributes not carried over: %+v", agent.Steps)
	}
	// The repository source lists only its own modules so the release provides the rest.
	if got := strings.Join(agent.Sources[0].Include, ","); got != "aws/hello-world,hello-world" {
		t.Fatalf("first source include = %s", got)
	}
	if len(agent.Sources[1].Include) != 0 {
		t.Fatalf("second source must not be restricted: %+v", agent.Sources[1])
	}
	for _, file := range []string{"net/vpc.yaml", "net/hello-world.yaml", "apps/argocd-exa.yaml", "apps/hello-world-exa.yaml"} {
		if _, err := os.Stat(filepath.Join(out, "aws_exa", "config", file)); err != nil {
			t.Errorf("input %s not written: %v", file, err)
		}
	}
	// External modules without an input file get none, and are still in the step.
	if _, err := os.Stat(filepath.Join(out, "aws_exa", "config", "net", "route53.yaml")); err == nil {
		t.Errorf("route53 has no input in the fixture but one was written")
	}

	// google_exa: hello-world has no google input, so net holds only the external vpc.
	google := readAgent(t, filepath.Join(out, "google_exa", "config.yaml"))
	if got := strings.Join(moduleNames(google.Steps[0]), " "); got != "vpc=google/vpc" {
		t.Fatalf("google net modules = %s", got)
	}
}

func TestGenerateModuleStep(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	hello, err := config.ModuleBySource("hello-world")
	if err != nil {
		t.Fatal(err)
	}
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
		Modules:      []env.Module{hello},
		StepPrefix:   "mart-foo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Skipped, ","); got != "google_exa" {
		t.Fatalf("skipped = %s", got)
	}
	if got := strings.Join(result.Steps["aws_exa"], ","); got != "mart-foo-hello-world" {
		t.Fatalf("steps to run = %s", got)
	}
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	last := agent.Steps[len(agent.Steps)-1]
	if last.Name != "mart-foo-hello-world" || last.Type != env.StepTypeArgoCD || last.ArgocdNamespace != "argocd-exa" {
		t.Fatalf("per-module step = %+v", last)
	}
	// The gateway default module rides along, external, as a default that is not applied.
	if got := strings.Join(moduleNames(last), " "); got != "hello-world-exa=hello-world aws-alb-exa=aws-alb" || !last.Modules[1].DefaultModule {
		t.Fatalf("per-module step modules = %s (%+v)", got, last.Modules)
	}
	if _, ok := agent.Find("hello-world"); !ok {
		t.Fatal("Find must locate the module")
	}
	if p, _ := agent.Find("hello-world"); p.Step.Name != "mart-foo-hello-world" {
		t.Fatalf("Find must prefer the per-module step, got %s", p.Step.Name)
	}
}

func TestGenerateExternalNeedsSource(t *testing.T) {
	config := fixture(t)
	config.Sources = nil
	_, err := generate(generateOptions{
		Config:       config,
		OutDir:       filepath.Join(t.TempDir(), "agents"),
		Environments: config.All(),
		Self:         env.AgentSource{URL: "/conf"},
	})
	if err == nil || !strings.Contains(err.Error(), "sources:") {
		t.Fatalf("expected a missing-source error, got %v", err)
	}
}

func TestGenerateInPlace(t *testing.T) {
	config := fixture(t)
	out := filepath.Join(t.TempDir(), "agents")
	hello, err := config.ModuleBySource("aws/hello-world")
	if err != nil {
		t.Fatal(err)
	}
	result, err := generate(generateOptions{
		Config:       config,
		OutDir:       out,
		Environments: config.All(),
		Self:         env.AgentSource{URL: "https://github.com/example/modules", Version: "main", ForceVersion: true},
		Modules:      []env.Module{hello},
		InPlace:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Steps["aws_exa"], ","); got != "net" {
		t.Fatalf("steps to run = %s", got)
	}
	agent := readAgent(t, filepath.Join(out, "aws_exa", "config.yaml"))
	if got := strings.Join(stepNames(agent), ","); got != "net,infra,apps" {
		t.Fatalf("in-place must add no step, got %s", got)
	}
	if agent.Sources[0].Version != "main" || !agent.Sources[0].ForceVersion || agent.Sources[1].URL != "https://github.com/entigolabs/entigo-infralib-release" {
		t.Fatalf("sources = %+v", agent.Sources)
	}
}
