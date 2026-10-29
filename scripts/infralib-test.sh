#!/usr/bin/env bash
# Host orchestrator of the entigo-infralib-test framework.
#
# Runs the agent and the module tests of a module repository inside the
# per-cloud test images (entigolabs/entigo-infralib-test-<cloud>). The host
# needs bash and docker; credentials and the kubeconfig are the executor's and
# are passed through read-only. The host parses nothing: environments and
# their clouds are read through the infralib-test command in the cli image,
# and the agent and the tests run in the per-cloud images.
#
# This file ships inside the images under /opt/infralib-test/scripts and is
# extracted by the consumer repository's test.sh (see templates/test.sh).
set -euo pipefail

usage() {
  cat <<USAGE
usage: test.sh [command] [options] [module-dir ...]

commands:
  run        (default) provision with the agent, then run the module tests
  agent      only provision with the agent
  test       only run the module tests of what is provisioned: the modules
             in their regular steps (use run for per-branch steps)
  generate   only write the agent configurations under agents/
  destroy    destroy the per-module steps of this branch (module-dir args) or,
             with --all, every step of the selected environments
  envs       list the environments of the repository (--tsv for scripts); with
             module directories only those whose steps list one of the modules
  shell      open a shell in the test image of an environment (test.sh shell ENV)
             or run one command there (test.sh shell ENV -- kubectl get nodes):
             the image has the cloud's CLI and the kubectl credential plugin
             of its clusters, the repository is mounted at /conf, the working
             directory, and the environment's .env overrides are applied
  static     static checks of the given modules (all without arguments), no
             cloud needed: tflint for terraform modules, helm lint, helm
             template and kube-score for charts (scripts/static.sh)
  kubeconfig contexts for the selected environments' clusters, written by the
             cloud CLI of each environment's image to .infralib-test/kubeconfig,
             which the tests then use (KUBECONFIG in the shell still wins)
  nuke       the nuke script with its arguments (test.sh nuke aws --dry-run):
             everything in the cloud's account that nuke/<cloud>.yaml does not
             keep goes (scripts/nuke.sh)
  publish    the repository's charts and modules as OCI packages with a signed
             index, from the aws image: --registries REG[,REG...] (the first is
             pushed to, the rest mirrored) --release X.Y.Z; OCI_SIGN=false
             skips the signatures (scripts/oci)

options:
  -e, --env NAME        environment to use (repeatable). Default: every
                        environment whose cloud has credentials and region
                        settings in this shell
      --source URL      agent source of this repository instead of the mounted
                        checkout (e.g. https://github.com/org/repo)
      --version VER     version of that source (sets force_version)
      --in-place        apply the given modules under their regular names (k8s
                        modules in a step of their own, terraform modules inside
                        their regular step) instead of as branch copies
                        instead of per-module steps of this branch
      --step-prefix P   name prefix of per-module steps (default: derived from
                        the user and branch, "main" on the main branch in CI)
      --steps LIST      only run these agent steps (comma separated) in a full run
      --destroy         after the tests, destroy the per-module steps created
                        by this run (or INFRALIB_DESTROY=true); off by default,
                        shared test environments are usually nuked on a schedule
      --timeout D       go test timeout per module (default 45m)
      --run REGEX       only run tests matching REGEX
      --tag TAG         image tag (default: INFRALIB_TEST_VERSION, else latest)
  INFRALIB_TEST_EXTRA_ENV  names of further variables to pass into the test
                        containers (space or comma separated), for tests that
                        need credentials beyond the cloud's own
  ROLE_ARN              a role in the environments' AWS account to assume for
                        everything (agent, tests, kubeconfig, nuke, publish),
                        with this shell's credentials: a web identity
                        (AWS_ROLE_ARN and AWS_WEB_IDENTITY_TOKEN_FILE, a pod
                        with IRSA), access keys, or the ~/.aws profile. Every
                        container gets a profile that chains the role and
                        refreshes it, since a chained session lasts an hour
      --no-pull         do not pull the images before running
  -q, --quiet           write agent logs to logs/<env>/agent.log instead of the terminal
  -v, --verbose         stream every test's output, not only failures
  -h, --help

Without module-dir arguments every step of the selected environments is
applied and every module with tests is tested. With them, each module gets a
step of its own (so a branch can be tested on a shared environment) and only
those modules are tested.

An optional environments/<env>.env (KEY=VALUE lines, docker --env-file format)
overrides variables for that environment alone, in its agent container and in
its tests: AWS_REGION=us-east-1 puts one environment in another region. Extra
CA certificates in environments/ca-certificates/*.crt (every environment) or
environments/<env>/ca-certificates/*.crt (one) are handed to the agent and to
the route probes; step include files in environments/config/<step>/include/
(every environment) or environments/<env>/config/<step>/include/ (one) reach
the agent as its config/<step>/include/.

environment variables:
  INFRALIB_TEST_VERSION       image tag, normally pinned by test.sh
  INFRALIB_TEST_IMAGE_PREFIX  image name prefix (default entigolabs/entigo-infralib-test-)
  INFRALIB_AGENT_IMAGE        run the agent from this image instead of the test image
  INFRALIB_DESTROY            true = --destroy
  AWS_REGION, AWS_*           aws region (required for aws environments) and
                              credentials/profile; ~/.aws is mounted when present
  GOOGLE_PROJECT, GOOGLE_REGION, GOOGLE_ZONE
                              required for google environments
  CLOUDSDK_CONFIG             gcloud configuration directory (default ~/.config/gcloud)
  GOOGLE_APPLICATION_CREDENTIALS  google service account key file
  OCI_REGION, OCI_COMPARTMENT_ID  required for oracle environments
  OCI_CONFIG_FILE             oracle config (default ~/.oci/config); its directory is mounted
  OCI_KUBE_ENDPOINT           OKE endpoint the agent connects to (default PUBLIC_ENDPOINT)
  AZURE_SUBSCRIPTION_ID, AZURE_LOCATION
                              required for azure environments
  AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET
                              azure service principal; AZURE_CONFIG_DIR (default ~/.azure)
                              is mounted when present, for az logins
  AZURE_RESOURCE_GROUP        pre-made resource group of the agent (default <prefix>-<location>)
  KUBECONFIG                  kubeconfig holding the environments' cluster contexts
                              (aws eks update-kubeconfig / gcloud container clusters
                              get-credentials / oci ce cluster create-kubeconfig /
                              az aks get-credentials + kubelogin convert-kubeconfig)
USAGE
}

log()  { printf '\033[1;34m==> %s\033[0m\n' "$*" >&2; }
warn() { printf '\033[1;33m==> %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[1;31m==> %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- arguments
COMMAND=run
ENVS=()
MODULES=()
SELF_SOURCE=""
SELF_VERSION=""
IN_PLACE=false
STEP_PREFIX=""
ONLY_STEPS=""
DESTROY="${INFRALIB_DESTROY:-false}"
DESTROY_ALL=false
TIMEOUT=45m
RUN_FILTER=""
TAG="${INFRALIB_TEST_VERSION:-latest}"
TSV=false
PULL=true
QUIET=false
VERBOSE=false

NUKE_ARGS=()
REGISTRIES=""
RELEASE=""
case "${1:-}" in
  run|agent|test|generate|destroy|envs|shell|static|kubeconfig|publish) COMMAND=$1; shift ;;
  nuke) COMMAND=nuke; shift; NUKE_ARGS=("$@"); set -- ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    -e|--env) ENVS+=("$2"); shift 2 ;;
    --registries) REGISTRIES=$2; shift 2 ;;
    --release) RELEASE=$2; shift 2 ;;
    --source) SELF_SOURCE=$2; shift 2 ;;
    --version) SELF_VERSION=$2; shift 2 ;;
    --in-place) IN_PLACE=true; shift ;;
    --step-prefix) STEP_PREFIX=$2; shift 2 ;;
    --steps) ONLY_STEPS=$2; shift 2 ;;
    --destroy) DESTROY=true; shift ;;
    --all) DESTROY_ALL=true; shift ;;
    --timeout) TIMEOUT=$2; shift 2 ;;
    --run) RUN_FILTER=$2; shift 2 ;;
    --tag) TAG=$2; shift 2 ;;
    --no-pull) PULL=false; shift ;;
    --tsv) TSV=true; shift ;;
    -q|--quiet) QUIET=true; shift ;;
    -v|--verbose) VERBOSE=true; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; MODULES+=("$@"); break ;;
    -*) die "unknown option $1 (see --help)" ;;
    *) MODULES+=("${1%/}"); shift ;;
  esac
done

# ---------------------------------------------------------------- repository
ROOT=$(pwd -P)
while ! compgen -G "$ROOT/environments/*.yaml" >/dev/null; do
  [ "$ROOT" = / ] && die "no environments/*.yaml found in $(pwd) or its parents"
  ROOT=$(dirname "$ROOT")
done
cd "$ROOT"
mkdir -p agents logs
# Where the kubeconfig command writes, relative to the root; the containers
# see it under /conf.
KUBECONFIG_FILE=.infralib-test/kubeconfig
IMAGE_PREFIX="${INFRALIB_TEST_IMAGE_PREFIX:-entigolabs/entigo-infralib-test-}"
image_for() { echo "${IMAGE_PREFIX}$1:${TAG}"; }
# shellcheck source=aws-role.sh
source "$(dirname "${BASH_SOURCE[0]}")/aws-role.sh"

PULLED=()
pull() {
  local image=$1
  [ "$PULL" = true ] || return 0
  for p in "${PULLED[@]:-}"; do [ "$p" = "$image" ] && return 0; done
  log "Pulling $image"
  # A locally built image that no registry holds is fine too.
  docker pull -q "$image" >/dev/null 2>&1 || docker image inspect "$image" >/dev/null 2>&1 || die "cannot pull $image"
  PULLED+=("$image")
}

# ---------------------------------------------------------------- credentials
# A cloud is usable when credentials and the region settings the agent and
# the tests read are present. Regions are never defaulted here.
have_aws()    { { [ -n "${AWS_ACCESS_KEY_ID:-}" ] || [ -n "${AWS_PROFILE:-}" ] || [ -f "$HOME/.aws/credentials" ] || [ -n "${ROLE_ARN:-}" ] || [ -n "${AWS_WEB_IDENTITY_TOKEN_FILE:-}" ]; } && [ -n "${AWS_REGION:-}" ]; }
have_google() { { [ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ] || [ -d "${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}" ]; } && [ -n "${GOOGLE_PROJECT:-}" ] && [ -n "${GOOGLE_REGION:-}" ] && [ -n "${GOOGLE_ZONE:-}" ]; }
have_oracle() { [ -f "${OCI_CONFIG_FILE:-$HOME/.oci/config}" ] && [ -n "${OCI_REGION:-}" ] && [ -n "${OCI_COMPARTMENT_ID:-}" ]; }
have_azure()  { { [ -n "${AZURE_CLIENT_SECRET:-}" ] || [ -d "${AZURE_CONFIG_DIR:-$HOME/.azure}" ]; } && [ -n "${AZURE_SUBSCRIPTION_ID:-}" ] && [ -n "${AZURE_LOCATION:-}" ]; }
have_cloud()  { "have_$1"; }
require_cloud() {
  case $1 in
    aws)    [ -n "${AWS_REGION:-}" ] || die "AWS_REGION is not set" ;;
    google) for v in GOOGLE_PROJECT GOOGLE_REGION GOOGLE_ZONE; do [ -n "${!v:-}" ] || die "$v is not set"; done ;;
    oracle) for v in OCI_REGION OCI_COMPARTMENT_ID; do [ -n "${!v:-}" ] || die "$v is not set"; done ;;
    azure)  for v in AZURE_SUBSCRIPTION_ID AZURE_LOCATION; do [ -n "${!v:-}" ] || die "$v is not set"; done ;;
  esac
}

# base_args CLOUD ROLE: docker run arguments shared by every container: the
# checkout at /conf, the executor's uid so written files are theirs, and the
# credentials of a cloud. Credentials are passed by environment variable and
# read-only mount; the paths inside the container are fixed and announced
# through the variables the tools read, so HOME inside the container does not
# matter. ROLE is "test" or "agent": only test containers get the executor's
# kubeconfig; the agent builds its own with the cloud CLI (aws eks
# update-kubeconfig and friends) and must not be pointed at a read-only one.
base_args() {
  local cloud=$1 role=${2:-test}
  ARGS=(--rm -v "$ROOT:/conf" -w /conf --user "$(id -u):$(id -g)" -e HOME=/tmp
        -e INFRALIB_ROOT=/conf -e "INFRALIB_TEST_VERSION=$TAG" -e TF_PLUGIN_CACHE_DIR=/conf/.infralib-test/plugin-cache)
  mkdir -p .infralib-test/plugin-cache
  # The kubeconfig the tests use: KUBECONFIG, else the one the kubeconfig
  # command wrote into the checkout, else the executor's own.
  local kubeconfig="${KUBECONFIG:-}"
  [ -n "$kubeconfig" ] || { [ -f "$ROOT/$KUBECONFIG_FILE" ] && kubeconfig="$ROOT/$KUBECONFIG_FILE"; } || kubeconfig="$HOME/.kube/config"
  if [ "$role" = test ] && [ -f "$kubeconfig" ]; then
    ARGS+=(-v "$kubeconfig:/creds/kubeconfig:ro" -e KUBECONFIG=/creds/kubeconfig)
  fi
  # INFRALIB_TEST_EXTRA_ENV names further variables (space or comma separated)
  # the tests read, passed into the test containers from this shell: extra
  # credentials a test logs in with, say. Secrets have no place in the
  # committed environments/<env>.env files, so this is how they travel.
  if [ "$role" = test ] && [ -n "${INFRALIB_TEST_EXTRA_ENV:-}" ]; then
    local v
    for v in ${INFRALIB_TEST_EXTRA_ENV//,/ }; do
      if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi
    done
  fi
  case $cloud in
    cli)
      # No credentials, but the region and account settings of every cloud:
      # infralib-test envs reports them per environment (with the
      # environments/<env>.env overrides applied) for the kubeconfig step.
      for v in AWS_REGION GOOGLE_PROJECT GOOGLE_REGION GOOGLE_ZONE OCI_REGION OCI_COMPARTMENT_ID AZURE_LOCATION AZURE_SUBSCRIPTION_ID; do
        if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi
      done
      ;;
    aws)
      if [ -n "${ROLE_ARN:-}" ]; then
        # A role in the environments' account, assumed inside every
        # container through a profile (see aws-role.sh); the shell's own
        # credentials are the ones to assume it with.
        aws_role_setup "$ROOT/.infralib-test/aws-role" || die "cannot set up ROLE_ARN"
        ARGS+=("${AWS_ROLE_ARGS[@]}")
        if [ "$AWS_ROLE_SOURCE" = profile ] && [ -d "$HOME/.aws" ]; then
          ARGS+=(-v "$HOME/.aws:/creds/aws:ro" -e AWS_SHARED_CREDENTIALS_FILE=/creds/aws/credentials)
        fi
      else
        for v in AWS_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_ROLE_ARN AWS_WEB_IDENTITY_TOKEN_FILE; do
          if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi
        done
        if [ -d "$HOME/.aws" ]; then
          ARGS+=(-v "$HOME/.aws:/creds/aws:ro" -e AWS_SHARED_CREDENTIALS_FILE=/creds/aws/credentials -e AWS_CONFIG_FILE=/creds/aws/config)
        fi
      fi
      ;;
    google)
      # GOOGLE_* for the tests, the agent's own names alongside.
      for v in GOOGLE_PROJECT GOOGLE_REGION GOOGLE_ZONE; do if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi; done
      ARGS+=(-e "PROJECT_ID=${GOOGLE_PROJECT:-}" -e "LOCATION=${GOOGLE_REGION:-}" -e "ZONE=${GOOGLE_ZONE:-}")
      local gcloud="${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}"
      # gcloud writes logs and token caches into its config dir, so this one is writable.
      if [ -d "$gcloud" ]; then ARGS+=(-v "$gcloud:/creds/gcloud" -e CLOUDSDK_CONFIG=/creds/gcloud); fi
      if [ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]; then
        ARGS+=(-v "$GOOGLE_APPLICATION_CREDENTIALS:/creds/google.json:ro" -e GOOGLE_APPLICATION_CREDENTIALS=/creds/google.json)
      fi
      ;;
    oracle)
      for v in OCI_REGION OCI_COMPARTMENT_ID OCI_PROFILE; do if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi; done
      # The agent's pipeline scripts write their OKE kubeconfig for the private
      # endpoint unless told otherwise; a run from outside the VCN (a runner, a
      # workstation) needs the public one, like the kubeconfig the tests use.
      ARGS+=(-e "OCI_KUBE_ENDPOINT=${OCI_KUBE_ENDPOINT:-PUBLIC_ENDPOINT}")
      local config="${OCI_CONFIG_FILE:-$HOME/.oci/config}"
      # The config file names its key_file by absolute path, so its directory
      # is mounted at the same path inside the container.
      ARGS+=(-v "$(dirname "$config"):$(dirname "$config"):ro" -e "OCI_CONFIG_FILE=$config")
      ;;
    azure)
      # The agent and the tests read the same AZURE_* variables; kubelogin,
      # which an AKS kubeconfig execs, knows the service principal by its
      # own names.
      for v in AZURE_SUBSCRIPTION_ID AZURE_LOCATION AZURE_TENANT_ID AZURE_CLIENT_ID AZURE_CLIENT_SECRET AZURE_RESOURCE_GROUP; do
        if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi
      done
      [ -z "${AZURE_CLIENT_SECRET:-}" ] || ARGS+=(-e "AAD_SERVICE_PRINCIPAL_CLIENT_ID=${AZURE_CLIENT_ID:-}" -e "AAD_SERVICE_PRINCIPAL_CLIENT_SECRET=${AZURE_CLIENT_SECRET}")
      local azure="${AZURE_CONFIG_DIR:-$HOME/.azure}"
      # az writes token caches into its config dir, so this one is writable.
      # An if, not a && list: as the last command of the function a false
      # test would be its exit status, and set -e would end the caller (a
      # runner without an az login has no such directory).
      if [ -d "$azure" ]; then ARGS+=(-v "$azure:/creds/azure" -e AZURE_CONFIG_DIR=/creds/azure); fi
      ;;
  esac
}

# run_in CLOUD [docker args --] command...: run a command in the cloud's test image.
run_in() {
  local cloud=$1; shift
  base_args "$cloud"
  local extra=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do extra+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  docker run "${ARGS[@]}" "${extra[@]}" --entrypoint "$1" "$(image_for "$cloud")" "${@:2}"
}

# ---------------------------------------------------------------- environments
# ENV_ROWS holds "name cloud prefix region zone project compartment cluster"
# for every environment of the repository, read through the cli image, which
# is also where the orchestrator itself came from.
load_environments() {
  pull "$(image_for cli)"
  mapfile -t ENV_ROWS < <(run_in cli -- infralib-test envs -tsv)
  [ ${#ENV_ROWS[@]} -gt 0 ] || die "infralib-test envs returned nothing"
}
env_field() { # env_field NAME INDEX
  # read treats a run of tabs as one separator, which would shift the
  # columns after an empty one (zone, project, compartment of an aws
  # environment), so the separator becomes one it does not collapse.
  local row
  for row in "${ENV_ROWS[@]}"; do
    IFS='|' read -r -a f <<<"${row//$'\t'/|}"
    if [ "${f[0]}" = "$1" ]; then echo "${f[$2]:-}"; return 0; fi
  done
  die "environment $1 is not defined in environments.yaml"
}
env_cloud()  { env_field "$1" 1; }
env_prefix() { env_field "$1" 2; }

select_environments() {
  load_environments
  if [ ${#ENVS[@]} -gt 0 ]; then
    for e in "${ENVS[@]}"; do require_cloud "$(env_cloud "$e")"; done
    return
  fi
  local row
  for row in "${ENV_ROWS[@]}"; do
    IFS=$'\t' read -r -a f <<<"$row"
    if have_cloud "${f[1]}"; then
      ENVS+=("${f[0]}")
    else
      warn "Skipping ${f[0]}: no ${f[1]} credentials or region settings in this shell (see --help)"
    fi
  done
  [ ${#ENVS[@]} -gt 0 ] || die "no environment selected: pass --env or provide cloud credentials and region settings"
}

clouds_of_selected() {
  local e
  for e in "${ENVS[@]}"; do env_cloud "$e"; done | sort -u
}

# ---------------------------------------------------------------- step prefix
# Per-module steps are named <prefix>-<module>; the prefix identifies who is
# testing what so several branches can share one environment. Mirrors the
# previous convention: 4 letters of the user, 7 of the branch, lower case.
step_prefix() {
  if [ -n "$STEP_PREFIX" ]; then echo "$STEP_PREFIX"; return; fi
  local branch user
  branch="${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo local)}}"
  if [ -n "${GITHUB_ACTIONS:-}" ] && [ "$branch" = main ]; then echo main; return; fi
  branch=$(echo "$branch" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-\n' '-' | cut -d- -f1-2 | cut -c1-7 | sed 's/-*$//')
  user=$(whoami | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9\n' '-' | cut -c1-4 | sed 's/-*$//')
  echo "${user}-${branch}"
}

# ---------------------------------------------------------------- generate
# Writes agents/<env>/config.yaml for the selected environments and fills
# STEPS_<env> with the steps the agent should run ("all", or a list).
generate() {
  local e args=()
  for e in "${ENVS[@]}"; do args+=(-env "$e"); done
  if [ -n "$SELF_SOURCE" ]; then
    args+=(-source "$SELF_SOURCE")
    [ -n "$SELF_VERSION" ] && args+=(-version "$SELF_VERSION" -force-version)
  fi
  if [ ${#MODULES[@]} -gt 0 ]; then
    for m in "${MODULES[@]}"; do args+=(-module "$(module_source "$m")"); done
    args+=(-step-prefix "$(step_prefix)")
    [ "$IN_PLACE" = true ] && args+=(-in-place)
  fi
  log "Generating agent configurations for ${ENVS[*]}"
  local line
  SELECTED=()
  while read -r line; do
    set -- $line
    case "$2" in
      skip) warn "Skipping $1: its steps list none of the requested modules" ;;
      *) steps=$2; [ "$steps" = all ] && [ -n "$ONLY_STEPS" ] && steps=$ONLY_STEPS
         declare -g "STEPS_${1//-/_}=$steps"; SELECTED+=("$1"); echo "    $1: steps $steps" >&2 ;;
    esac
  done < <(run_in cli -- infralib-test generate "${args[@]}")
  ENVS=("${SELECTED[@]}")
  show_configs
}

# show_configs prints the agent configuration of every selected environment
# before anything runs it: a collapsible group per environment in GitHub
# Actions, a plain block elsewhere.
show_configs() {
  local e file
  for e in "${ENVS[@]}"; do
    file="agents/$e/config.yaml"
    [ -f "$file" ] || continue
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
      echo "::group::Agent configuration $file"
      cat "$file"
      echo "::endgroup::"
    else
      log "Agent configuration $file"
      sed 's/^/    /' "$file" >&2
    fi
  done
}

# module_source modules/aws/vpc -> aws/vpc, modules/k8s/argocd -> argocd
module_source() {
  local rel=${1#"$ROOT"/}; rel=${rel#./}
  rel=${rel#modules/}
  case $rel in
    k8s/*) echo "${rel#k8s/}" ;;
    */*) echo "$rel" ;;
    *) die "$1 is not a module directory (expected modules/<type>/<name>)" ;;
  esac
}

# ---------------------------------------------------------------- agent
agent_image() { echo "${INFRALIB_AGENT_IMAGE:-$(image_for "$1")}"; }

# apply_overrides ENV: export the variables of environments/ENV.env into this
# shell, so that base_args passes them (and, for google, the agent's own names
# derived from them) instead of the executor's. Call it in a subshell.
apply_overrides() {
  local file="environments/$1.env" line key value
  [ -f "$file" ] || return 0
  log "Applying variable overrides of $file to $1"
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in ''|'#'*) continue ;; esac
    key="${line%%=*}"; value="${line#*=}"
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] && [ "$key" != "$line" ] || die "$file: expected KEY=VALUE, got '$line'"
    export "$key=$value"
  done < "$file"
}

# run_agent SUBCOMMAND ENV: ei-agent run|destroy for one environment. Region,
# project and compartment reach the agent through the same variables base_args
# passes to every container, with the environment's overrides applied. The
# body is a subshell so the overrides stay with this environment.
run_agent() (
  local sub=$1 e=$2 cloud prefix steps
  cloud=$(env_cloud "$e"); prefix=$(env_prefix "$e")
  local var="STEPS_${e//-/_}"; steps="${!var:-all}"
  apply_overrides "$e"
  base_args "$cloud" agent
  if [ "$cloud" = google ] && [ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]; then
    ARGS+=(-e "GOOGLE_APPLICATION_CREDENTIALS_JSON=$(cat "$GOOGLE_APPLICATION_CREDENTIALS")")
  fi
  # LOCAL_MODE makes the agent's entrypoint scripts hand the plan to the
  # apply stage through the container's filesystem instead of a CodeBuild
  # or Cloud Build artifact; the old all-in-one test image baked it in.
  ARGS+=(-e LOCAL_MODE=true)
  local cmd=(ei-agent "$sub" -c "/conf/agents/$e/config.yaml" --prefix "$prefix" --pipeline-type=local)
  case $sub in
    run)     cmd+=(--allow-parallel=false) ;;
    destroy) cmd+=(--yes) ;;  # nobody is there to confirm
  esac
  [ "$steps" != all ] && cmd+=(--steps "$steps")
  mkdir -p "logs/$e"
  log "Agent $sub for $e (steps: $steps)"
  if [ "$QUIET" = true ]; then
    docker run "${ARGS[@]}" --entrypoint "${cmd[0]}" "$(agent_image "$cloud")" "${cmd[@]:1}" >"logs/$e/agent-$sub.log" 2>&1
  else
    # sed -u: unbuffered, so the live log in CI moves line by line instead of
    # in 4 KB blocks (the apply phase is quiet enough to sit in a buffer).
    docker run "${ARGS[@]}" --entrypoint "${cmd[0]}" "$(agent_image "$cloud")" "${cmd[@]:1}" 2>&1 | tee "logs/$e/agent-$sub.log" | sed -u "s/^/[$e] /"
    return "${PIPESTATUS[0]}"
  fi
)

# run_agents SUBCOMMAND: every selected environment in parallel. Each is
# reported the moment it finishes, in whatever order that happens: each
# background run leaves its exit status in logs/<env>/agent-<sub>.rc, and
# the loop below watches those files rather than waiting on the processes
# one by one, which would hold a finished environment's line until every
# environment started before it was done too.
run_agents() {
  local sub=$1 e pids=() names=() failed=()
  for e in "${ENVS[@]}"; do
    pull "$(agent_image "$(env_cloud "$e")")"
    mkdir -p "logs/$e"; rm -f "logs/$e/agent-$sub.rc"
    ( if run_agent "$sub" "$e"; then rc=0; else rc=$?; fi; echo "$rc" > "logs/$e/agent-$sub.rc"; exit "$rc" ) &
    pids+=($!); names+=("$e")
  done
  local i remaining=${#pids[@]}
  while [ "$remaining" -gt 0 ]; do
    for i in "${!pids[@]}"; do
      [ -n "${pids[$i]}" ] && [ -f "logs/${names[$i]}/agent-$sub.rc" ] || continue
      wait "${pids[$i]}" || true
      if [ "$(cat "logs/${names[$i]}/agent-$sub.rc")" = 0 ]; then
        log "Agent $sub for ${names[$i]} done"
      else
        failed+=("${names[$i]}")
      fi
      pids[$i]=""; remaining=$((remaining - 1))
    done
    [ "$remaining" -gt 0 ] && sleep 1
  done
  if [ ${#failed[@]} -gt 0 ]; then
    for e in "${failed[@]}"; do
      if [ -f "logs/$e/agent-$sub.log" ]; then
        warn "Agent $sub for $e failed, last lines of logs/$e/agent-$sub.log:"
        tail -n 40 "logs/$e/agent-$sub.log" >&2 || true
      else
        warn "Agent $sub for $e failed before the agent container started (no logs/$e/agent-$sub.log); see the messages above"
      fi
    done
    die "agent $sub failed for ${failed[*]}"
  fi
}

# ---------------------------------------------------------------- tests
run_tests() {
  local cloud rc=0
  for cloud in $(clouds_of_selected); do
    local args=(-timeout "$TIMEOUT" -log-dir /conf/logs)
    [ -n "$RUN_FILTER" ] && args+=(-run "$RUN_FILTER")
    [ "$VERBOSE" = true ] && args+=(-verbose)
    local e
    for e in "${ENVS[@]}"; do [ "$(env_cloud "$e")" = "$cloud" ] && args+=(-env "$e"); done
    pull "$(image_for "$cloud")"
    log "Running module tests on $cloud"
    run_in "$cloud" -- infralib-test run "${args[@]}" "${MODULES[@]}" || rc=$?
  done
  return $rc
}

# ---------------------------------------------------------------- kubeconfig
# write_kubeconfig ENV: the cloud CLI of the environment's image adds the
# context of the environment's cluster to .infralib-test/kubeconfig, the file
# the test containers then get. A cluster that does not exist (yet) is a
# warning: terraform module tests need no cluster, and a k8s test that does
# fails on its own. Region, project and compartment are the environment's
# own (the shell's variables with environments/<env>.env applied). The OKE
# context is renamed to the cluster name, since oci names it after the
# cluster OCID; the AKS kubeconfig is converted to exec kubelogin as the
# service principal, after an az login of its own.
write_kubeconfig() (
  local e=$1 cloud region project compartment cluster prefix
  cloud=$(env_cloud "$e"); prefix=$(env_prefix "$e")
  region=$(env_field "$e" 3); project=$(env_field "$e" 5); compartment=$(env_field "$e" 6); cluster=$(env_field "$e" 7)
  [ -n "$cluster" ] || { log "No cluster module in $e, no kubeconfig"; return 0; }
  apply_overrides "$e"
  base_args "$cloud" agent
  ARGS+=(-e "KUBECONFIG=/conf/$KUBECONFIG_FILE")
  local image; image=$(image_for "$cloud")
  local missing="no kubeconfig for $e: cluster $cluster is not available"
  log "kubeconfig for $e: cluster $cluster in ${region:-?}"
  case $cloud in
    aws)
      docker run "${ARGS[@]}" --entrypoint aws "$image" eks update-kubeconfig --region "$region" --name "$cluster" --kubeconfig "/conf/$KUBECONFIG_FILE" \
        || warn "$missing" ;;
    google)
      docker run "${ARGS[@]}" --entrypoint gcloud "$image" container clusters get-credentials "$cluster" --region "$region" --project "$project" \
        || warn "$missing" ;;
    oracle)
      local id
      id=$(docker run "${ARGS[@]}" --entrypoint oci "$image" ce cluster list --compartment-id "$compartment" --region "$region" --name "$cluster" --lifecycle-state ACTIVE --query 'data[0].id' --raw-output 2>/dev/null || true)
      if [ -z "$id" ] || [ "$id" = null ]; then warn "$missing"; return 0; fi
      docker run "${ARGS[@]}" --entrypoint sh "$image" -c "oci ce cluster create-kubeconfig --cluster-id '$id' --region '$region' --file \"\$KUBECONFIG\" --token-version 2.0.0 --kube-endpoint ${OCI_KUBE_ENDPOINT:-PUBLIC_ENDPOINT} && kubectl config rename-context \"\$(kubectl config current-context)\" '$cluster'" \
        || warn "$missing" ;;
    azure)
      docker run "${ARGS[@]}" --entrypoint sh "$image" -c "az login --service-principal -u \"\$AZURE_CLIENT_ID\" -p \"\$AZURE_CLIENT_SECRET\" --tenant \"\$AZURE_TENANT_ID\" --output none && az account set --subscription \"\$AZURE_SUBSCRIPTION_ID\" && az aks get-credentials --resource-group '${AZURE_RESOURCE_GROUP:-${prefix}-${region}}' --name '$cluster' --file \"\$KUBECONFIG\" --overwrite-existing && kubelogin convert-kubeconfig -l spn --client-id \"\$AZURE_CLIENT_ID\" --tenant-id \"\$AZURE_TENANT_ID\" --kubeconfig \"\$KUBECONFIG\"" \
        || warn "$missing" ;;
  esac
)

# ---------------------------------------------------------------- commands
case $COMMAND in
  kubeconfig)
    # Contexts for the selected environments' clusters, into
    # .infralib-test/kubeconfig, where the test containers pick them up.
    select_environments
    mkdir -p "$(dirname "$KUBECONFIG_FILE")"
    for e in "${ENVS[@]}"; do pull "$(image_for "$(env_cloud "$e")")"; write_kubeconfig "$e"; done
    log "Kubeconfig in $KUBECONFIG_FILE"
    ;;
  nuke)
    # The nuke script with its own arguments (nuke.sh CLOUD [--dry-run] ...),
    # from the repository root, where nuke/<cloud>.yaml is.
    exec "$(dirname "${BASH_SOURCE[0]}")/nuke.sh" "${NUKE_ARGS[@]}"
    ;;
  publish)
    # The repository's charts and modules as OCI packages (scripts/oci),
    # from the aws test image: --registries for the registries (the first
    # is pushed to, the rest mirrored), --release for the bare version.
    [ -n "$REGISTRIES" ] && [ -n "$RELEASE" ] || die "usage: test.sh publish --registries REG[,REG...] --release X.Y.Z"
    pull "$(image_for aws)"
    base_args aws agent
    for v in GH_TOKEN GH_ACTOR SUPPLIER OCI_SIGN SIGSTORE_ID_TOKEN COSIGN_EXPERIMENTAL RETRY_ATTEMPTS RETRY_DELAY; do
      if [ -n "${!v:-}" ]; then ARGS+=(-e "$v"); fi
    done
    mkdir -p .infralib-test/oci-publish
    docker run "${ARGS[@]}" -e "REGISTRIES=$REGISTRIES" -e "VERSION=$RELEASE" -e OUT=/conf/.infralib-test/oci-publish \
      --entrypoint bash "$(image_for aws)" -c '/opt/infralib-test/scripts/oci/login.sh && /opt/infralib-test/scripts/oci/publish.sh'
    log "manifest.json and the index zip are in .infralib-test/oci-publish"
    ;;
  static)
    exec "$(dirname "${BASH_SOURCE[0]}")/static.sh" --root "$ROOT" "${MODULES[@]}"
    ;;
  envs)
    # With module directories only the environments whose steps list one of
    # them: the ones a change to the modules can affect.
    args=()
    for m in "${MODULES[@]}"; do args+=(-module "$(module_source "$m")"); done
    [ "$TSV" = true ] && args+=(-tsv)
    pull "$(image_for cli)"
    run_in cli -- infralib-test envs "${args[@]}"
    ;;
  shell)
    # shell ENV opens an interactive sh; shell ENV -- CMD... runs the command
    # instead, for scripts that need the image's cloud CLI or kubectl
    # credential plugin (the OKE context execs oci, the AKS one kubelogin).
    [ ${#MODULES[@]} -ge 1 ] || die "usage: test.sh shell ENV [-- command...]"
    load_environments
    cloud=$(env_cloud "${MODULES[0]}")
    pull "$(image_for "$cloud")"
    apply_overrides "${MODULES[0]}"
    base_args "$cloud"
    if [ ${#MODULES[@]} -eq 1 ]; then
      exec docker run "${ARGS[@]}" -it --entrypoint sh "$(image_for "$cloud")"
    fi
    exec docker run "${ARGS[@]}" --entrypoint "${MODULES[1]}" "$(image_for "$cloud")" "${MODULES[@]:2}"
    ;;
  generate)
    select_environments
    generate
    ;;
  agent)
    select_environments
    generate
    run_agents run
    ;;
  test)
    select_environments
    # Tests of what is deployed: the modules sit in their regular steps, a
    # module argument only narrows which tests run.
    IN_PLACE=true
    generate
    run_tests
    ;;
  destroy)
    select_environments
    if [ "$DESTROY_ALL" = true ]; then
      [ ${#MODULES[@]} -eq 0 ] || die "--all destroys whole environments; do not pass modules"
      warn "Destroying EVERY step of ${ENVS[*]} in 10 seconds, ctrl-c to abort"
      sleep 10
    else
      [ ${#MODULES[@]} -gt 0 ] || die "destroy needs module directories (per-module steps) or --all"
    fi
    generate
    run_agents destroy
    ;;
  run)
    select_environments
    generate
    run_agents run
    rc=0
    run_tests || rc=$?
    if [ "$DESTROY" = true ] && [ ${#MODULES[@]} -gt 0 ] && [ "$IN_PLACE" = false ]; then
      run_agents destroy || { warn "destroy failed"; [ $rc -eq 0 ] && rc=1; }
    fi
    exit $rc
    ;;
esac
