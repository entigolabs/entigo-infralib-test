package env

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestModuleRefForms(t *testing.T) {
	var step Step
	err := yaml.UnmarshalStrict([]byte(`
name: apps
modules:
  - argocd
  - source: crossplane-core
    name: crossplane-system
  - source: aws/vpc
    input: environments/aws_exa/net/vpc.yaml
`), &step)
	require.NoError(t, err)
	require.Len(t, step.Modules, 3)
	if step.Modules[0] != (ModuleRef{Source: "argocd"}) {
		t.Errorf("short form = %+v", step.Modules[0])
	}
	if step.Modules[1] != (ModuleRef{Source: "crossplane-core", Name: "crossplane-system"}) {
		t.Errorf("object form = %+v", step.Modules[1])
	}
	if step.Modules[2].Input != "environments/aws_exa/net/vpc.yaml" {
		t.Errorf("input override = %+v", step.Modules[2])
	}
	out, err := yaml.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "modules:\n- argocd\n- name: crossplane-system\n  source: crossplane-core\n- input: environments/aws_exa/net/vpc.yaml\n  source: aws/vpc\nname: apps\n" {
		t.Errorf("round trip:\n%s", out)
	}
}

func TestValidate(t *testing.T) {
	for name, content := range map[string]string{
		"no cloud":        "environments: {x: {prefix: p, region: r}}",
		"bad cloud":       "environments: {x: {cloud: azure, prefix: p, region: r}}",
		"google project":  "environments: {x: {cloud: google, prefix: p, region: r}}",
		"empty step":      "environments: {x: {cloud: aws, prefix: p, region: r, steps: [{name: net}]}}",
		"duplicate step":  "environments: {x: {cloud: aws, prefix: p, region: r, steps: [{name: net, modules: [a/b]}, {name: net, modules: [a/c]}]}}",
		"bad step type":   "environments: {x: {cloud: aws, prefix: p, region: r, steps: [{name: net, type: helm, modules: [a/b]}]}}",
		"module without":  "environments: {x: {cloud: aws, prefix: p, region: r, steps: [{name: net, modules: [{name: x}]}]}}",
		"unknown field":   "environments: {x: {cloud: aws, prefix: p, region: r, colour: blue}}",
		"no environments": "modules_dir: modules",
	} {
		var config Config
		if err := yaml.UnmarshalStrict([]byte(content), &config); err == nil {
			config.root = t.TempDir()
			if err := validateConfig(&config); err == nil {
				t.Errorf("%s: expected an error", name)
			}
		}
	}
}
