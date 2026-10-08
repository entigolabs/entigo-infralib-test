#!/usr/bin/env bash
# Nukes the test account of one cloud: everything that the repository's
# nuke/<cloud>.yaml does not keep is deleted. The nuke tools run as containers
# from the host (bash + docker, like infralib-test.sh), with the credentials
# and region settings the agent reads from the environment.
#
#   nuke.sh aws    [--dry-run]
#   nuke.sh google [--dry-run]
#   nuke.sh oracle [--dry-run] --prefix biz [--prefix pri ...]
#
# Configuration: nuke/<cloud>.yaml in the working directory (NUKE_CONFIG
# overrides), in the format of the tool of that cloud:
#   aws     ghcr.io/ekristen/aws-nuke      AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, [AWS_SESSION_TOKEN], AWS_REGION
#   google  taivox/gcp-nuke                GOOGLE_PROJECT and GOOGLE_APPLICATION_CREDENTIALS_JSON (or
#                                          GOOGLE_APPLICATION_CREDENTIALS, a file; or a gcloud login)
#   oracle  ghcr.io/entigolabs/oci-nuke    OCI_REGION, OCI_COMPARTMENT_ID, OCI_CONFIG_FILE (default
#                                          ~/.oci/config), and the environment prefixes: the
#                                          tenancy-scoped listers find nothing without one
# AWS_NUKE_IMAGE, GCP_NUKE_IMAGE and OCI_NUKE_IMAGE override the tool images;
# NUKE_ATTEMPTS (default 2) runs the nuke again when it fails, since the first
# pass often leaves dependents behind. --dry-run only lists what would go,
# and skips the AWS preparation (stopping the config recorders, emptying the
# buckets) that has no dry mode.
set -euo pipefail

AWS_NUKE_IMAGE=${AWS_NUKE_IMAGE:-ghcr.io/ekristen/aws-nuke:v3.48.2}
GCP_NUKE_IMAGE=${GCP_NUKE_IMAGE:-taivox/gcp-nuke:v0.0.5}
OCI_NUKE_IMAGE=${OCI_NUKE_IMAGE:-ghcr.io/entigolabs/oci-nuke:0.1.12}
# The aws CLI for the AWS preparation when the host has none.
AWS_CLI_IMAGE=${AWS_CLI_IMAGE:-entigolabs/entigo-infralib-test-aws:${INFRALIB_TEST_VERSION:-latest}}
NUKE_ATTEMPTS=${NUKE_ATTEMPTS:-2}

log()  { echo "$(date +%H:%M:%S) $*" >&2; }
warn() { echo "$(date +%H:%M:%S) WARNING: $*" >&2; }
die()  { echo "nuke.sh: $*" >&2; exit 1; }

[ $# -ge 1 ] || die "usage: nuke.sh aws|google|oracle [--dry-run] [--prefix P]..."
CLOUD=$1; shift
DRY_RUN=false
PREFIXES=()
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=true; shift ;;
    --prefix) [ $# -ge 2 ] || die "--prefix needs a value"; PREFIXES+=("$2"); shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done
case "$CLOUD" in aws|google|oracle) ;; *) die "unknown cloud $CLOUD" ;; esac
CONFIG=$(realpath "${NUKE_CONFIG:-nuke/$CLOUD.yaml}") || die "no nuke configuration ${NUKE_CONFIG:-nuke/$CLOUD.yaml}"
[ -f "$CONFIG" ] || die "no nuke configuration $CONFIG"
[ "$DRY_RUN" = true ] && log "Dry run: nothing is deleted"

# ------------------------------------------------------------------- aws
aws_cli() {
  if command -v aws >/dev/null 2>&1; then
    aws "$@"
  else
    docker run --rm --entrypoint aws \
      -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN -e AWS_REGION -e AWS_DEFAULT_REGION="$AWS_REGION" \
      "$AWS_CLI_IMAGE" "$@"
  fi
}

# aws-nuke cannot delete a bucket with versions or delete markers in it, so
# every bucket is emptied first, including the agent's state buckets: the
# nuked environments are provisioned from scratch afterwards anyway.
empty_buckets() {
  local bucket batch
  for bucket in $(aws_cli s3api list-buckets --query 'Buckets[].Name' --output text); do
    log "Emptying bucket $bucket"
    while :; do
      batch=$(aws_cli s3api list-object-versions --bucket "$bucket" --max-items 500 --output json 2>/dev/null \
        | jq -c '{Objects: [((.Versions // [])[], (.DeleteMarkers // [])[]) | select(.Key != null) | {Key, VersionId}], Quiet: true}') || { warn "could not list $bucket"; break; }
      [ "$(echo "$batch" | jq '.Objects | length')" -gt 0 ] || break
      aws_cli s3api delete-objects --bucket "$bucket" --delete "$batch" >/dev/null || { warn "could not empty $bucket"; break; }
    done
  done
}

# A running configuration recorder recreates its delivery channel and keeps
# aws-nuke from removing the config service resources.
stop_config_recorders() {
  local recorder
  for recorder in $(aws_cli configservice describe-configuration-recorders --query 'ConfigurationRecorders[].name' --output text 2>/dev/null); do
    log "Stopping configuration recorder $recorder"
    aws_cli configservice stop-configuration-recorder --configuration-recorder-name "$recorder" || warn "could not stop $recorder"
  done
}

nuke_aws() {
  : "${AWS_ACCESS_KEY_ID:?}" "${AWS_SECRET_ACCESS_KEY:?}" "${AWS_REGION:?}"
  if [ "$DRY_RUN" = false ]; then
    stop_config_recorders
    empty_buckets
  fi
  local args=(run --config /home/aws-nuke/config.yml --force --max-wait-retries 100
    --access-key-id "$AWS_ACCESS_KEY_ID" --secret-access-key "$AWS_SECRET_ACCESS_KEY")
  [ -n "${AWS_SESSION_TOKEN:-}" ] && args+=(--session-token "$AWS_SESSION_TOKEN")
  [ "$DRY_RUN" = false ] && args+=(--no-dry-run)
  docker run --rm -e AWS_REGION -v "$CONFIG":/home/aws-nuke/config.yml:ro "$AWS_NUKE_IMAGE" "${args[@]}"
}

# ---------------------------------------------------------------- google
nuke_google() {
  : "${GOOGLE_PROJECT:?}"
  local docker_args=(--rm -v "$CONFIG":/google-nuke-config.yaml:ro)
  if [ -n "${GOOGLE_APPLICATION_CREDENTIALS_JSON:-}" ]; then
    docker_args+=(-e GOOGLE_APPLICATION_CREDENTIALS_JSON)
  elif [ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]; then
    docker_args+=(-e GOOGLE_APPLICATION_CREDENTIALS_JSON="$(cat "$GOOGLE_APPLICATION_CREDENTIALS")")
  else
    docker_args+=(-v "$HOME/.config/gcloud":/home/gcp-nuke/.config/gcloud:ro)
  fi
  local args=(run --config /google-nuke-config.yaml --project-id "$GOOGLE_PROJECT" --no-prompt --prompt-delay 3 --quiet)
  [ "$DRY_RUN" = false ] && args+=(--no-dry-run)
  docker run "${docker_args[@]}" "$GCP_NUKE_IMAGE" "${args[@]}"
}

# ---------------------------------------------------------------- oracle
nuke_oracle() {
  : "${OCI_REGION:?}" "${OCI_COMPARTMENT_ID:?}"
  [ ${#PREFIXES[@]} -gt 0 ] || die "oracle needs --prefix: the tenancy-scoped listers (users, groups, policies, keys) find nothing without one"
  local config_file config_dir prefix
  config_file=$(realpath "${OCI_CONFIG_FILE:-$HOME/.oci/config}")
  config_dir=$(dirname "$config_file")
  for prefix in "${PREFIXES[@]}"; do
    log "Nuking prefix $prefix in $OCI_COMPARTMENT_ID"
    local args=(run --config /home/oci-nuke/config.yml --compartment-id "$OCI_COMPARTMENT_ID" --region "$OCI_REGION" --prefix "$prefix" --no-prompt)
    [ "$DRY_RUN" = false ] && args+=(--no-dry-run)
    # The config directory is mounted at its own path too because key_file=
    # in the config is absolute; --user because the config is mode 0600.
    docker run --rm --user "$(id -u):$(id -g)" \
      -v "$config_dir":/home/oci-nuke/.oci:ro -v "$config_dir":"$config_dir":ro \
      -v "$CONFIG":/home/oci-nuke/config.yml:ro \
      -e OCI_CONFIG_FILE="$config_file" -e OCI_REGION \
      "$OCI_NUKE_IMAGE" "${args[@]}"
  done
}

attempt=1
while :; do
  log "Nuking $CLOUD with $CONFIG (attempt $attempt of $NUKE_ATTEMPTS)"
  if "nuke_$CLOUD"; then
    log "Nuke of $CLOUD done"
    exit 0
  fi
  [ $attempt -lt "$NUKE_ATTEMPTS" ] || die "nuke of $CLOUD failed"
  attempt=$((attempt + 1))
  warn "nuke of $CLOUD failed, running it again"
done
