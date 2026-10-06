# entigo-infralib-test

Test and release framework for repositories of [Entigo Infralib](https://github.com/entigolabs/entigo-infralib) modules: the OpenTofu modules and Helm charts the [agent](https://github.com/entigolabs/entigo-infralib-agent) provisions a platform from.

A module repository keeps its tests as ordinary Go tests next to each module. This repository provides

- the Go packages those tests import (`env`, `tf`, `k8s`, `aws`, `google`, `oracle`, `retry`, `random`, `logger`),
- `infralib-test`, the command that turns `environments.yaml` into agent configurations and runs the module tests with readable output,
- per-cloud container images `entigolabs/entigo-infralib-test-{aws,google,oracle}` that hold the agent, the cloud tooling, a Go toolchain and warmed caches,
- a host orchestrator (`scripts/infralib-test.sh`) that runs the agent and the tests from those images with nothing but bash and docker on the host,
- reusable GitHub workflows a module repository calls.

[entigo-infralib-example-source](https://github.com/entigolabs/entigo-infralib-example-source) is the smallest complete consumer and the place to start.

## Status

Scaffolding. Nothing here has run against a cloud yet. See [Roadmap](#roadmap).

## How a module repository is laid out

```
environments.yaml                 the environments the repository provisions and tests on
environments/<env>/<step>/<m>.yaml agent inputs of modules that come from another source
go.mod                            requires github.com/entigolabs/entigo-infralib-test
test.sh                           copy of templates/test.sh, pins the framework version
modules/<type>/<name>/            a module: aws/vpc, aws-v2/route53, google/gke, k8s/argocd ...
modules/<type>/<name>/test/
    <env>.yaml                    agent input of the module for that environment; its presence
                                  is what puts the module into the environment
    *_test.go                     the module's tests
    module.yaml                   optional: name override, no_prefix, pin_step
```

### environments.yaml

```yaml
sources:                                   # agent sources that provide the modules this repository lacks
  - url: https://github.com/entigolabs/entigo-infralib-release
environments:
  aws_demo:
    cloud: aws                             # aws | google | oracle
    prefix: demo                           # agent prefix; names the state bucket and every resource
    region: eu-north-1
    kube_context: arn:aws:eks:eu-north-1:123456789012:cluster/demo-infra-eks
    gateways:
      external: {name: external, namespace: aws-alb-demo, domain: demo-net-route53.example.com}
    steps:
      - name: net
        modules: [aws/vpc, aws-v2/route53, aws/hello-world]
      - name: infra
        vpc: {attach: true}
        modules: [aws/eks]
      - name: apps
        type: argocd-apps
        argocd_namespace: argocd-demo
        default_modules: [aws-alb]         # charts chain inputs from these; copied into per-module steps as defaults
        modules:
          - argocd
          - aws-alb
          - source: crossplane-core
            name: crossplane-system        # agent module name override (object form)
        modules_dir: modules/k8s           # plus every local k8s module with an input for the environment
```

A step lists modules by their agent source. A module **of this repository** (a directory under `modules/`) is part of the environment when `modules/<type>/<name>/test/<env>.yaml` exists. A module **this repository does not have** is external: it is always part of the step, the agent fetches it from the first of `sources:` that provides it, and its optional input lives in `environments/<env>/<step>/<name>.yaml`. That is how a repository with one chart gets a whole platform to test it on.

The generated agent config lists this repository as the first source, restricted with `include:` to its own modules, followed by `sources:`.

### Writing a module test

```go
package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/entigolabs/entigo-infralib-test/aws" // registers the output reader of the clouds the module runs on
	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/k8s"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

func TestHelloWorld(t *testing.T) {
	for _, e := range env.Selected(t) {       // every environment the module has an input for (or INFRALIB_ENVIRONMENTS)
		t.Run(e.Name, func(t *testing.T) {
			t.Parallel()
			outputs := tf.Get(t, e)               // OpenTofu outputs of the step the module was applied in
			require.NotEmpty(t, outputs.String(t, "hello-world__hello_world"))

			c := k8s.Connect(t, e)                // the environment's cluster, in the module's namespace
			k8s.WaitUntilDeploymentAvailable(t, c, c.Namespace, 20, 6*time.Second)
		})
	}
}
```

Tests never hold cloud account ids, cluster names or hostnames: those come from `environments.yaml` through `e`. Where a module was applied comes from the generated agent config (`env.ModulePlacement`), so the same test works in a regular step and in a per-branch step. Credentials and the kubeconfig are the executor's; the framework only picks `kube_context`.

## Running

```
./test.sh                              provision every environment you have credentials for, then test every module
./test.sh modules/k8s/hello-world      test one module in a step of its own on the shared environments
./test.sh --env aws_demo test          only the tests, environments already provisioned
./test.sh --destroy modules/aws/foo    tear the per-module step down afterwards (CI default)
./test.sh envs | generate | agent | shell ENV
./test.sh --help
```

`test.sh` is a 30-line bootstrap committed in the module repository (`templates/test.sh`). It pins `INFRALIB_TEST_VERSION`, extracts `scripts/` from the matching image into the git-ignored `.infralib-test/` and runs the orchestrator, which

1. reads the environments through `infralib-test envs` in the image,
2. selects the environments: the ones given with `--env`, otherwise every environment whose cloud has credentials in the shell,
3. writes `agents/<env>/config.yaml` with `infralib-test generate`,
4. runs `ei-agent run --pipeline-type=local` once per environment in parallel from the per-cloud image, with the checkout mounted at `/conf` as the repository's own source,
5. runs `infralib-test run`, which executes `go test -json` for the selected modules and prints one line per test, a failed test's full output, and a summary; full logs go to `logs/`.

With module arguments each module is applied in a step named `<step-prefix>-<module>` (prefix from user and branch, `main` on the main branch in CI) so a branch never touches the shared steps; `--in-place` applies them inside their regular steps instead, the post-merge use. `module.yaml` with `pin_step: true` keeps a module in its regular step always.

The agent runs from the test image itself, so agent, tofu, kubectl and the tests always come from one image. `INFRALIB_AGENT_IMAGE` overrides that for pre-release agents.

## Images

`images/<cloud>/Dockerfile` builds `entigolabs/entigo-infralib-test-<cloud>` from the repository root on top of `entigolabs/entigo-infralib-<cloud>:latest`, adding Go, the `infralib-test` command, `scripts/`, `templates/` and this module's source under `/opt/infralib-test`, and warms the Go module and build caches with the packages a test of that cloud imports. A dependency the cache lacks is downloaded at run time (`GOFLAGS=-mod=mod`); the cache catches up on the next build. `GOTOOLCHAIN=local` means a Go bump is a `GO_VERSION` bump here, never a download in a test run.

Tags: pull request `dev`, main `latest`, git tag `vX.Y.Z` plus `latest`. The `Images` workflow needs `DOCKER_USERNAME` and `DOCKER_PASSWORD`.

## Workflows for module repositories

- `module-pull-request.yaml`: tests the modules a pull request changed, one at a time in per-branch steps, builds the kubeconfig for EKS and GKE from `kube_context`, destroys the steps afterwards, uploads `logs/`. Credentials via `secrets: inherit`.

## Roadmap

In order:

1. Publish the images and run the example repository end to end on AWS; fix what that finds.
2. Reusable `stable` (provision the latest release from scratch, then test) and `release` (test main, tag, publish OCI + release repository) workflows, one agent run per environment covering every step, release depending on every environment job.
3. Nuke of test accounts, shipped with the framework.
4. Google and Oracle environments in the example.
5. Version report (`report.sh`) for consumers.
6. Migrate `entigo-infralib` on a branch.

## Development

```
go test ./...
docker buildx build -f images/aws/Dockerfile -t entigolabs/entigo-infralib-test-aws:local .
```

The Go version of `go.mod` and of the images must agree with the Go version of the test image base; see `GO_VERSION` in the Dockerfiles.
