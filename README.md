# entigo-infralib-test

Test and release framework for repositories of [Entigo Infralib](https://github.com/entigolabs/entigo-infralib) modules: the OpenTofu modules and Helm charts the [agent](https://github.com/entigolabs/entigo-infralib-agent) provisions a platform from.

A module repository keeps its tests as ordinary Go tests next to each module. This repository provides

- the Go packages those tests import (`env`, `tf`, `k8s`, `aws`, `google`, `oracle`, `retry`, `random`, `logger`),
- `infralib-test`, the command that turns the agent configurations under `environments/` into runnable ones and runs the module tests with readable output,
- per-cloud container images `entigolabs/entigo-infralib-test-{aws,google,oracle}` that hold the agent, the cloud tooling, a Go toolchain and warmed caches, plus the small `entigolabs/entigo-infralib-test-cli` image with the command and the scripts that a module repository bootstraps from,
- a host orchestrator (`scripts/infralib-test.sh`) that runs the agent and the tests from those images with nothing but bash and docker on the host,
- reusable GitHub workflows a module repository calls.

[entigo-infralib-example-source](https://github.com/entigolabs/entigo-infralib-example-source) is the smallest complete consumer and the place to start.

## Status

Scaffolding. Nothing here has run against a cloud yet. See [Roadmap](#roadmap).

## How a module repository is laid out

```
environments/<cloud>_<prefix>.yaml   one agent configuration per environment
go.mod                               requires github.com/entigolabs/entigo-infralib-test
test.sh                              copy of templates/test.sh, pins the framework version
modules/<type>/<name>/               a module: aws/vpc, aws-v2/route53, google/gke, k8s/argocd ...
modules/<type>/<name>/test.sh        copy of templates/module-test.sh: tests this module from its directory
modules/<type>/<name>/test/
    <env>.yaml                       agent inputs of the module in that environment (optional)
    *_test.go                        the module's tests
    module.yaml                      optional: pin_step
```

### environments/

Each file is an ordinary [entigo-infralib-agent](https://github.com/entigolabs/entigo-infralib-agent) `config.yaml`. The file name carries what the agent takes from its flags: `aws_biz.yaml` is cloud `aws`, prefix `biz`. The framework adds nothing to the format. Before a run it patches a copy:

- the repository itself becomes the first source, restricted with `include:` to the modules it holds, so the agent takes everything else from the file's own `sources:`;
- modules without a `name` get the agent's conventional one, `<module>` for terraform modules and `<module>-<prefix>` for k8s modules;
- modules of this repository get the inputs of their `test/<env>.yaml`;
- in a pull request, the modules under test are appended as steps of their own;
- `manual_approve_run` and `manual_approve_update` default to `never`, because a test run has nobody to answer the agent's prompt; a step that sets them keeps its value.

Everything else passes through untouched, so a new agent field needs no framework change.

```yaml
sources:
  - url: https://github.com/entigolabs/entigo-infralib-release
steps:
  - name: net
    type: terraform
    modules:
      - name: vpc                       # not in this repository: comes from the sources above
        source: aws/vpc
        inputs:
          vpc_cidr: "10.0.0.0/16"
      - source: aws-v2/route53
      - source: aws/hello-world         # of this repository: inputs from modules/aws/hello-world/test/aws_biz.yaml
  - name: infra
    type: terraform
    vpc: {attach: true}
    modules:
      - source: aws/eks
  - name: apps
    type: argocd-apps
    argocd_namespace: argocd-biz
    modules:
      - source: argocd                  # name defaults to argocd-biz
      - source: aws-alb
      - source: hello-world
```

A module of this repository is part of an environment when a step lists it. A module the repository does not contain is external: the agent fetches it from the first of `sources:` that provides it. That is how a repository with one chart gets a whole platform to test it on.

What the agent reads from its environment, the framework reads from the same place: `AWS_REGION`; `GOOGLE_PROJECT`, `GOOGLE_REGION`, `GOOGLE_ZONE`; `OCI_REGION`, `OCI_COMPARTMENT_ID`. Nothing is defaulted. The cluster to connect to is the environment's `aws/eks`, `google/gke` or `oracle/oke` module, named `<prefix>-<step>-<module>` by the agent. `k8s.Connect` finds the context the cloud CLI created for it in the executor's kubeconfig (`aws eks update-kubeconfig` names it after the cluster ARN, `gcloud container clusters get-credentials` as `gke_<project>_<location>_<name>`); OKE contexts carry no cluster name, so an Oracle test imports the `oracle` package, which resolves it from the cluster id output. The gateway to publish through is the `aws-alb`, `google-gateway` or `oracle-gateway` module: its agent name is the namespace, its `global.externalGateway` input (or the chart default) the gateway name, and the `pub_domain` output of the `route53` or `dns` module the domain.

### Writing a module test

A module test runs once per environment the module has an input for, as parallel subtests named after the environment. Two shapes:

```go
package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/entigolabs/entigo-infralib-test/aws" // output reader of each cloud the module runs on; tf.Get and k8s.Gateway need it
	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/k8s"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

// Same expectations everywhere: env.Run, branch on e where needed.
func TestHelloWorld(t *testing.T) {
	env.Run(t, func(t *testing.T, e *env.Environment) {
		outputs := tf.Get(t, e)               // OpenTofu outputs of the step the module was applied in
		require.NotEmpty(t, outputs.String(t, "hello-world__hello_world"))
	})
}

// Distinct expectations per environment: env.RunEach with one function each.
// The map must cover exactly the environments the module has inputs for;
// an input without a test or a test without an input fails the run.
func TestHelloWorldExposure(t *testing.T) {
	env.RunEach(t, map[string]env.TestFunc{
		"aws_biz": testPublic,
		"aws_pri": testInternal,
	})
}

func testPublic(t *testing.T, e *env.Environment) {
	c := k8s.Connect(t, e)                    // the environment's cluster, in the module's namespace
	k8s.WaitUntilDeploymentAvailable(t, c, c.Namespace, 20, 6*time.Second)
	gateway := k8s.Gateway(t, e, "external")   // derived from the aws-alb and route53 modules; needs the aws import above
	require.NoError(t, k8s.WaitUntilHostnameAvailable(t, c, gateway, "https://"+gateway.Hostname(c.Namespace), "200", gateway.Retries, 6*time.Second))
}

func testInternal(t *testing.T, e *env.Environment) { /* ... */ }
```

Tests never hold cloud account ids, cluster names or hostnames: those are derived from the environment's modules. Where a module was applied comes from the generated agent config (`env.ModulePlacement`), so the same test works in a regular step and in a per-branch step. Credentials and the kubeconfig are the executor's; the framework only picks the context.

Parallelism, from the outside in: the orchestrator runs the agent for every environment at once; `infralib-test run` lets `go test` run several modules' test packages at once (`-parallel`, default 4); within a module, `env.Run` and `env.RunEach` run the environments as parallel subtests. Environments of different clouds run in different containers, one per cloud image.

## Running

```
./test.sh                              provision every environment you have credentials for, then test every module
./test.sh modules/k8s/hello-world      test one module in a step of its own on the shared environments
modules/k8s/hello-world/test.sh        the same, from the module's directory (templates/module-test.sh)
./test.sh --env aws_biz test           only the tests, environments already provisioned
./test.sh --destroy modules/aws/foo    tear the per-module step down afterwards
./test.sh envs | generate | agent | shell ENV
./test.sh --help
```

`test.sh` is a 20-line bootstrap committed in the module repository (`templates/test.sh`), and `modules/<type>/<name>/test.sh` a 7-line hand-over to it (`templates/module-test.sh`). Neither knows anything about clouds or environments: the bootstrap pins `INFRALIB_TEST_VERSION`, extracts `scripts/` from the `entigo-infralib-test-cli` image of that version into the git-ignored `.infralib-test/` and runs the orchestrator, which

1. reads the environments and their clouds through `infralib-test envs` in the cli image,
2. selects the environments: the ones given with `--env`, otherwise every environment whose cloud has credentials and region settings in the shell,
3. writes `agents/<env>/config.yaml` with `infralib-test generate`,
4. runs `ei-agent run --pipeline-type=local` once per environment in parallel from the per-cloud image, with the checkout mounted at `/conf` as the repository's own source,
5. runs `infralib-test run`, which executes `go test -json` for the selected modules and prints one line per test, a failed test's full output, and a summary; full logs go to `logs/`.

With module arguments each module is applied in a step named `<step-prefix>-<module>` (prefix from user and branch, `main` on the main branch in CI) so a branch never touches the shared steps; `--in-place` applies them inside their regular steps instead, the post-merge use. `module.yaml` with `pin_step: true` keeps a module in its regular step always.

The agent runs from the test image itself, so agent, tofu, kubectl and the tests always come from one image. `INFRALIB_AGENT_IMAGE` overrides that for pre-release agents.

## Images

`images/cli/Dockerfile` builds the 20 MB `entigolabs/entigo-infralib-test-cli` from alpine with the command, `scripts/` and `templates/`. `images/<cloud>/Dockerfile` builds `entigolabs/entigo-infralib-test-<cloud>` from the repository root on top of `entigolabs/entigo-infralib-<cloud>:latest`, adding Go, the `infralib-test` command, `scripts/`, `templates/` and this module's source under `/opt/infralib-test`, and warms the Go module and build caches with the packages a test of that cloud imports. A dependency the cache lacks is downloaded at run time (`GOFLAGS=-mod=mod`); the cache catches up on the next build. `GOTOOLCHAIN=local` means a Go bump is a `GO_VERSION` bump here, never a download in a test run.

Tags: pull request `dev`, main `latest`, git tag `vX.Y.Z` plus `latest`. The `Images` workflow needs `DOCKER_USERNAME` and `DOCKER_PASSWORD`.

## Workflows for module repositories

Three reusable workflows, called with `secrets: inherit`. Credentials and regions come from the caller's secrets: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`; `GOOGLE_CREDENTIALS`, `GOOGLE_PROJECT`, `GOOGLE_REGION`, `GOOGLE_ZONE`; `OCI_CONFIG`, `OCI_PRIVATE_KEY`, `OCI_REGION`, `OCI_COMPARTMENT_ID`. The clouds whose secrets are set are the ones whose environments run. Each builds the kubeconfig for EKS and GKE from the environments' cluster modules (`.github/actions/kubeconfig`) and uploads `logs/`.

- `module-pull-request.yaml`: tests the modules a pull request changed, one at a time, each in a per-branch step on the shared environments (k8s applications get a branch-prefixed name). The steps stay: recreating a cluster on every push would be wasteful, and test environments are nuked daily. `./test.sh --destroy modules/x` tears one down by hand.
- `module-stable.yaml`: one job per environment that provisions the latest release of the repository and runs the tests of that release; a final `Stable` job gates on all of them. The modules come from `release_repo` when given, otherwise from the repository itself.
- `module-release.yaml`: one job per environment that applies `main` from the repository's git URL and tests it, then a `Release` job that tags main and creates the GitHub release. With the optional `release_repo` it also publishes `modules/` without tests there over SSH (`SSH_PRIVATE_KEY`, a deploy key with write access), and that repository's tag marks completion so an interrupted publish resumes. The version is `release_version.txt` plus `.0` when its major.minor moved, else the latest patch plus one.

A release repository such as entigo-infralib-release predates OCI publishing and is optional; OCI publishing (next on the roadmap) will be the primary distribution, and most repositories will release to OCI and their own git tags only.

## Roadmap

In order:

1. Publish the images and run the example repository end to end on AWS; fix what that finds.
2. OCI publishing of charts and modules in the release workflow, as entigo-infralib does today.
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
