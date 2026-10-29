#!/usr/bin/env bash
# Sourced by infralib-test.sh and nuke.sh: AWS credentials through an assumed
# role. ROLE_ARN names a role in the account the environments live in, the
# way the agent's own ROLE_ARN does: a CI job in another account (a GitLab
# runner on EKS with IRSA, say) assumes it for everything the orchestrator
# runs there, the agent, the tests, the kubeconfig and the nuke.
#
# The assumed role is not passed on as keys: a session chained from a role
# lasts an hour at most, and a run is longer. Instead every container gets an
# AWS config file with a profile that names the role and the credentials to
# assume it with, and the SDKs in the containers (the aws CLI, OpenTofu, the
# agent, the Go tests, aws-nuke) assume and refresh it themselves:
#
#   [profile infralib-test]            role_arn = $ROLE_ARN, assumed with
#   [profile infralib-test-source]     the pod's web identity token (IRSA,
#                                      AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE),
#                                      or credential_source = Environment
#                                      (AWS_ACCESS_KEY_ID in this shell), or
#                                      source_profile = the shell's profile
#                                      (~/.aws).
#
# The token is copied next to the config, since the pod's token file is not
# visible to containers, and the directory is mounted read-only.
#
# aws_role_setup DIR: writes DIR/config (paths as the containers see them,
# the directory mounted at /creds/aws-role) and DIR/config.host (paths on
# this host, for tools run here); sets AWS_ROLE_ARGS, the docker run
# arguments, and AWS_ROLE_SOURCE (web, env or profile); exports
# AWS_CONFIG_FILE, AWS_PROFILE and AWS_SDK_LOAD_CONFIG for host tools. Does
# nothing without ROLE_ARN.
aws_role_setup() {
  local dir=$1 mount=/creds/aws-role
  AWS_ROLE_ARGS=()
  AWS_ROLE_SOURCE=""
  [ -n "${ROLE_ARN:-}" ] || return 0
  [ -n "${AWS_REGION:-}" ] || { echo "ROLE_ARN needs AWS_REGION" >&2; return 1; }
  mkdir -p "$dir" && chmod 700 "$dir"
  if [ -n "${AWS_WEB_IDENTITY_TOKEN_FILE:-}" ] && [ -n "${AWS_ROLE_ARN:-}" ]; then
    AWS_ROLE_SOURCE="web"
    cp "$AWS_WEB_IDENTITY_TOKEN_FILE" "$dir/token" && chmod 600 "$dir/token"
  elif [ -n "${AWS_ACCESS_KEY_ID:-}" ]; then
    AWS_ROLE_SOURCE="env"
  else
    AWS_ROLE_SOURCE="profile"
  fi
  local where
  for where in "$mount" "$dir"; do
    local file="$dir/config"; [ "$where" = "$dir" ] && file="$dir/config.host"
    {
      echo "[profile infralib-test]"
      echo "role_arn = $ROLE_ARN"
      echo "role_session_name = infralib-test"
      echo "region = $AWS_REGION"
      case $AWS_ROLE_SOURCE in
        web)     echo "source_profile = infralib-test-source" ;;
        env)     echo "credential_source = Environment" ;;
        profile) echo "source_profile = ${AWS_PROFILE:-default}" ;;
      esac
      if [ "$AWS_ROLE_SOURCE" = web ]; then
        echo
        echo "[profile infralib-test-source]"
        echo "role_arn = $AWS_ROLE_ARN"
        echo "web_identity_token_file = $where/token"
        echo "region = $AWS_REGION"
      fi
    } > "$file"
    chmod 600 "$file"
  done
  AWS_ROLE_ARGS=(-v "$dir:$mount:ro" -e "AWS_CONFIG_FILE=$mount/config" -e AWS_PROFILE=infralib-test -e AWS_SDK_LOAD_CONFIG=1 -e AWS_REGION)
  if [ "$AWS_ROLE_SOURCE" = env ]; then
    AWS_ROLE_ARGS+=(-e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY)
    [ -z "${AWS_SESSION_TOKEN:-}" ] || AWS_ROLE_ARGS+=(-e AWS_SESSION_TOKEN)
  fi
  export AWS_CONFIG_FILE="$dir/config.host" AWS_PROFILE=infralib-test AWS_SDK_LOAD_CONFIG=1
}
