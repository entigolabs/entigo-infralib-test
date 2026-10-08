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
    module.yaml                      optional settings of the module's tests, see below
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

A module of this repository is part of an environment when a step lists it. Its `test/<env>.yaml` holds only what that scenario changes: never restate a default, or a changed default goes unnoticed by the tests and reaches a release. An empty input file (comments only) is the normal case for a module tested with its defaults. A module the repository does not contain is external: the agent fetches it from the first of `sources:` that provides it. That is how a repository with one chart gets a whole platform to test it on.

What the agent reads from its environment, the framework reads from the same place: `AWS_REGION`; `GOOGLE_PROJECT`, `GOOGLE_REGION`, `GOOGLE_ZONE`; `OCI_REGION`, `OCI_COMPARTMENT_ID`. Nothing is defaulted. The cluster to connect to is the environment's `aws/eks`, `google/gke` or `oracle/oke` module, named `<prefix>-<step>-<module>` by the agent. `k8s.Connect` finds the context the cloud CLI created for it in the executor's kubeconfig (`aws eks update-kubeconfig` names it after the cluster ARN, `gcloud container clusters get-credentials` as `gke_<project>_<location>_<name>`); OKE contexts carry no cluster name, so an Oracle test imports the `oracle` package, which resolves it from the cluster id output. A route check reads the HTTPRoute and its Gateway, so no gateway or DNS configuration is needed.

### test/module.yaml

Optional, one file per module, read with strict keys. It holds what the framework needs to know about a module beyond its inputs:

```yaml
pin_step: true
```

`pin_step` is for modules that cannot be installed twice on one environment, or whose second installation would hide whether the branch's one works: external-dns, argocd, a gateway controller, an aws-alb, a karpenter. A pull request normally tests a module in a per-branch step under a branch-prefixed name next to the regular installation. A pinned module keeps its regular name instead, so the pull request replaces the shared installation for its test, and the installation keeps that version until the module itself is merged, or the next stable or release run puts everything back to its release. A pinned k8s module still runs in a step of its own, so nothing else of its regular step is applied. A pinned terraform module runs inside its regular step together with the other modules of that step.

Because a pinned pull request touches the shared installation, `module-pull-request.yaml` makes such runs wait for every other run of the workflow, on any branch, before they start. Pull requests without a pinned module only wait for runs of their own branch, since per-branch steps cannot collide. The main workflow is a separate workflow and is not part of that queue.

### Writing a module test

A module test runs once per environment the module is part of, as parallel subtests named after the environment. A module can have as many test functions as it needs; each one decides whether it is the same everywhere (`env.Run`) or differs per environment (`env.RunEach`).

```go
package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/k8s"
)

// Same expectation everywhere.
func TestDeployment(t *testing.T) {
	env.Run(t, func(t *testing.T, e *env.Environment) {
		c := k8s.Connect(t, e)                                          // the environment's cluster, in the module's namespace
		k8s.WaitUntilDeploymentAvailable(t, c, c.Namespace, 60, 6*time.Second)
	})
}

// Distinct expectations per environment. The map must cover exactly the
// environments the module is part of; a gap either way fails the run.
func TestRoute(t *testing.T) {
	env.RunEach(t, map[string]env.TestFunc{
		"aws_biz": testRoutePublic,
		"aws_pri": testRouteAbsent,
	})
}

func testRoutePublic(t *testing.T, e *env.Environment) {
	c := k8s.Connect(t, e)
	// Hostname, scheme, port and the load balancer address all come from the
	// HTTPRoute and its Gateway; the check runs an in-cluster curl Job pinned
	// to the address with the Host header set, so internal load balancers
	// work and DNS propagation does not matter. 200 at / is the default.
	route := k8s.WaitUntilRouteReachable(t, c, c.Namespace, 100, 6*time.Second)
	require.False(t, route.Internal(), "%s must be public", route.URL())
}

func testRouteAbsent(t *testing.T, e *env.Environment) { /* assert no HTTPRoute exists */ }
```

A terraform module test reads its step's OpenTofu outputs with `tf.Get(t, e)`, which needs the cloud package imported for its output reader (`import _ "github.com/entigolabs/entigo-infralib-test/aws"`); k8s tests need no cloud package.

Tests never hold cloud account ids, cluster names or hostnames: the cluster is derived from the environment's cluster module, routes and gateways are read from the cluster. Where a module was applied comes from the generated agent config (`env.ModulePlacement`), so the same test works in a regular step and in a per-branch step. Credentials and the kubeconfig are the executor's; the framework only picks the context.

**Running against one environment.** `./test.sh test --env aws_biz` (or `INFRALIB_ENVIRONMENTS=aws_biz` for a bare `go test`) selects the environments; `env.Run` and `env.RunEach` run only those and skip the test when none is selected. Subtests are named after the environment, so `go test -run 'TestRoute/aws_biz'` works as well.

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

With module arguments each module is applied in a step named `<step-prefix>-<module>` (prefix from user and branch, `main` on the main branch in CI), a k8s module under a branch-prefixed application name, so a branch never touches the shared installation. `--in-place` applies the modules under their regular names instead, the post-merge use: a k8s module still gets a step of its own, named the same way, so that the other modules of its regular step are left as they are; a terraform module runs inside its regular step, together with the other modules of that step, since a second step would be a second state. The regular steps stay in the generated config in both cases, unapplied, so every template reference resolves. `test/module.yaml` with `pin_step: true` makes a module behave in place in pull requests too.

The agent runs from the test image itself, so agent, tofu, kubectl and the tests always come from one image. `INFRALIB_AGENT_IMAGE` overrides that for pre-release agents.

## One version pin

`INFRALIB_TEST_VERSION` in a repository's `test.sh` is the only framework pin. `infralib-test run` aligns the repository's `go.mod` to that tag before testing (or, for `dev`, `main` and `<sha>` images, to the commit baked into the image, see `infralib-test version`), with `go mod edit -require` and `go mod tidy`, so tests compile against the framework in the image and the image's warmed Go cache always matches. The change lands in the working tree: after bumping `test.sh`, run the tests once and commit `go.mod` and `go.sum` with it. The pull-request workflow fails when that commit is missing. `go.mod` and `go.sum` stay in the repository for editors, `go vet` and scanners. `-no-align` turns the alignment off.

## Images

`images/cli/Dockerfile` builds the 20 MB `entigolabs/entigo-infralib-test-cli` from alpine with the command, `scripts/` and `templates/`. `images/<cloud>/Dockerfile` builds `entigolabs/entigo-infralib-test-<cloud>` from the repository root on top of `entigolabs/entigo-infralib-<cloud>:latest`, adding Go, the `infralib-test` command, `scripts/`, `templates/` and this module's source under `/opt/infralib-test`, and warms the Go module and build caches with the packages a test of that cloud imports. A dependency the cache lacks is downloaded at run time (`GOFLAGS=-mod=mod`); the cache catches up on the next build. `GOTOOLCHAIN=local` means a Go bump is a `GO_VERSION` bump here, never a download in a test run.

Tags: pull request `dev`; main `<sha>` and `main`. Versions are made by the `Release` workflow (manual dispatch): it requires green `Go` and `Images` runs for the main commit, re-tags that commit's `<sha>` images as `vX.Y.Z` and `latest` without rebuilding, then creates the git tag and the GitHub release. A version tag therefore never exists without its images, and `latest` only moves on a release. The version is `release_version.txt` plus `.0` when its major.minor moved, else the latest patch plus one. Both workflows need `DOCKER_USERNAME` and `DOCKER_PASSWORD`.

## Workflows for module repositories

Five reusable workflows, called with `secrets: inherit`. Credentials and regions come from the caller's secrets: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`; `GOOGLE_CREDENTIALS`, `GOOGLE_PROJECT`, `GOOGLE_REGION`, `GOOGLE_ZONE`; `OCI_CONFIG`, `OCI_PRIVATE_KEY`, `OCI_REGION`, `OCI_COMPARTMENT_ID`. The clouds whose secrets are set are the ones whose environments run. Each builds the kubeconfig for EKS and GKE from the environments' cluster modules (`.github/actions/kubeconfig`) and uploads `logs/`.

- `module-pull-request.yaml`: tests the modules a pull request changed, one at a time, each in a per-branch step on the shared environments (k8s applications get a branch-prefixed name); a module with `pin_step` is applied to its regular step instead and such a run waits for every other run of the workflow first (see `test/module.yaml`). The steps stay: recreating a cluster on every push would be wasteful, and test environments are nuked daily. `./test.sh --destroy modules/x` tears one down by hand.
- `module-main.yaml`: after a merge to main, one job per environment applies the modules the push changed under their regular names (`--in-place`) and runs their tests. Only those modules are applied: a module that did not change is left as it is, so a pinned module someone is still working on in a pull request is not put back to main by an unrelated merge. When the push changed no module (an environment file, say) every step is applied and every module is tested. The modules come from the repository's `main` branch over git, as in the release workflow, never from the checkout: a mounted path becomes a `file://` source in the ArgoCD Applications that only lives as long as the repo-server pod that received it. Paths are for pull requests and local runs. Nothing is tagged.
- `module-stable.yaml`: one job per environment that provisions the latest release of the repository and runs the tests of that release; a final `Stable` job gates on all of them. The modules come from `source` when given (for example an `oci://` registry), else from `release_repo`, else from the repository itself.
- `module-release.yaml`: one job per environment that applies `main` from the repository's git URL and tests it, then a `Release` job that tags main and creates the GitHub release. The version is `release_version.txt` plus `.0` when its major.minor moved, else the latest patch plus one. Two optional destinations:
  - `oci_registries`: charts (`<registry>/k8s/<chart>:<version>`), OpenTofu modules (`<registry>/<type>/<module>:<version>`), an SBOM attached to each, and a cosign-signed `index:<version>` holding the released tree, `manifest.json` and an index SBOM. Pushed to the first registry and mirrored byte-identically to the rest; ghcr.io logs in with the run's token, public.ecr.aws with the AWS secrets and creates missing repositories. The release gets `manifest.json` and the index zip as assets, which also mark completion. The agent consumes it as `oci://<registry>`. Note that OpenTofu cannot download modules from ghcr.io anonymously (ghcr denies its token probe), so an agent without source credentials should be pointed at the ECR Public mirror, as entigo-infralib's own release README does for AWS.
  - `release_repo`: `modules/` without tests pushed over SSH (`SSH_PRIVATE_KEY`, a deploy key with write access), its tag marking completion. This is how entigo-infralib-release is made; it predates OCI and is kept for compatibility.

  An interrupted run resumes whatever destination is missing on the next run.

## Nuking the test accounts

Test environments are thrown away daily and provisioned again by the stable run, so that nothing accumulates and a fresh install is proven every day. `scripts/nuke.sh <cloud>` deletes everything in a cloud's test account that `nuke/<cloud>.yaml` in the repository does not keep, running the nuke tool of the cloud as a container: aws-nuke, gcp-nuke or oci-nuke, versions pinned in the script. The configuration files are in the tools' own formats: which account or project, which regions, and the filters for what stays (the CI user and its keys, the DNS zones, the state keys). For AWS the script first stops the configuration recorders and empties every bucket, since aws-nuke cannot delete a bucket with versions in it; Oracle needs the environment prefixes, since its tenancy-scoped listers find nothing without one. `--dry-run` only lists.

`module-nuke.yaml` is the reusable workflow: one job per cloud with credentials and a `nuke/<cloud>.yaml`, so that one cloud can be nuked by hand with the `clouds` input; `dry_run` to rehearse. It posts failures to Slack when a `SLACK_WEBHOOK_URL` secret exists, and announces runs somebody dispatched. The workflow checks out the framework at its own version for the script, so a repository needs no `test.sh` to nuke.

## Roadmap

In order:

1. Publish the images and run the example repository end to end on AWS; fix what that finds.
2. Nuke of test accounts, shipped with the framework.
4. Google and Oracle environments in the example.
5. Version report (`report.sh`) for consumers.
6. Migrate `entigo-infralib` on a branch.

## Development

```
go test ./...
docker buildx build -f images/aws/Dockerfile -t entigolabs/entigo-infralib-test-aws:local .
```

The Go version of `go.mod` and of the images must agree with the Go version of the test image base; see `GO_VERSION` in the Dockerfiles.
