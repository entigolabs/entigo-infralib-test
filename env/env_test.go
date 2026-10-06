package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeEnv(t *testing.T, root, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, DirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, DirName, name+".yaml"), []byte(content), 0o644))
}

const minimal = "steps:\n  - name: net\n    modules:\n      - source: aws/vpc\n"

func TestFileNameGivesCloudAndPrefix(t *testing.T) {
	root := t.TempDir()
	writeEnv(t, root, "aws_biz", minimal)
	writeEnv(t, root, "google_pri-2", "prefix: pri-2\n"+minimal)
	t.Setenv("AWS_REGION", "eu-north-1")
	t.Setenv("GOOGLE_PROJECT", "proj")
	config, err := Load(root)
	require.NoError(t, err)
	aws := config.Environments["aws_biz"]
	require.Equal(t, "aws", aws.Cloud)
	require.Equal(t, "biz", aws.Prefix)
	require.Equal(t, "eu-north-1", aws.Region)
	require.Equal(t, filepath.Join("environments", "aws_biz.yaml"), aws.Path)
	google := config.Environments["google_pri-2"]
	require.Equal(t, "google", google.Cloud)
	require.Equal(t, "pri-2", google.Prefix)
	require.Equal(t, "proj", google.Project)
	require.Equal(t, "", google.Region, "GOOGLE_REGION unset is reported where needed, not at load")
}

func TestRawPassesThroughAndCopies(t *testing.T) {
	root := t.TempDir()
	writeEnv(t, root, "aws_biz", "agent_version: v1\nsteps:\n  - name: net\n    approve: force\n    modules:\n      - source: aws/vpc\n")
	config, err := Load(root)
	require.NoError(t, err)
	e := config.Environments["aws_biz"]
	raw := e.Raw()
	require.Equal(t, "v1", raw["agent_version"])
	raw["agent_version"] = "changed"
	raw["steps"].([]any)[0].(map[string]any)["approve"] = "never"
	again := e.Raw()
	require.Equal(t, "v1", again["agent_version"], "Raw returns a copy")
	require.Equal(t, "force", again["steps"].([]any)[0].(map[string]any)["approve"])
}

func TestLoadErrors(t *testing.T) {
	for name, file := range map[string]struct{ name, content string }{
		"no underscore":   {"aws", minimal},
		"empty prefix":    {"aws_", minimal},
		"bad cloud":       {"azure_p", minimal},
		"prefix mismatch": {"aws_p", "prefix: q\n" + minimal},
		"no steps":        {"aws_p", "sources: []\n"},
		"unnamed step":    {"aws_p", "steps:\n  - modules: [{source: a/b}]\n"},
		"duplicate step":  {"aws_p", "steps:\n  - {name: net, modules: [{source: a/b}]}\n  - {name: net, modules: [{source: a/c}]}\n"},
		"bad step type":   {"aws_p", "steps:\n  - {name: net, type: helm, modules: [{source: a/b}]}\n"},
		"module without":  {"aws_p", "steps:\n  - {name: net, modules: [{name: x}]}\n"},
		"deep source":     {"aws_p", "steps:\n  - {name: net, modules: [{source: a/b/c}]}\n"},
		"string module":   {"aws_p", "steps:\n  - {name: net, modules: [a/b]}\n"},
		"source no url":   {"aws_p", "sources: [{version: v1}]\n" + minimal},
	} {
		root := t.TempDir()
		writeEnv(t, root, file.name, file.content)
		_, err := Load(root)
		require.Error(t, err, name)
	}
}

func TestWellKnownModules(t *testing.T) {
	root := t.TempDir()
	writeEnv(t, root, "aws_biz", `
steps:
  - name: net
    modules:
      - source: aws-v2/route53
  - name: infra
    modules:
      - source: aws/eks
  - name: apps
    type: argocd-apps
    modules:
      - source: aws-alb
`)
	config, err := Load(root)
	require.NoError(t, err)
	e := config.Environments["aws_biz"]
	require.Equal(t, "biz-infra-eks", config.ClusterName(e))
	gw, ok := config.GatewayModule(e)
	require.True(t, ok)
	require.Equal(t, "aws-alb-biz", gw.Module.AgentName(e))
	dns, ok := config.DNSModule(e)
	require.True(t, ok)
	require.Equal(t, "net", dns.Step.Name)
	require.Equal(t, "route53", dns.Module.AgentName(e))
}
