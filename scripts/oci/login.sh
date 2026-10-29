#!/usr/bin/env bash
# Logs helm and oras in to every registry of REGISTRIES (comma separated
# registry paths) and creates the repositories a push needs where the
# registry wants them made first:
#   ghcr.io                      GH_TOKEN and GH_ACTOR
#   public.ecr.aws               AWS credentials (ECR Public, us-east-1);
#                                repositories created, one per package
#   *.dkr.ecr.<region>.amazonaws.com  AWS credentials; repositories created,
#                                one per package, under the path after the host
# Run from the repository root, where modules/ is.
set -euo pipefail
IFS=, read -r -a regs <<<"${REGISTRIES:?}"

# packages PREFIX: the repository names a publish creates under PREFIX.
packages() {
  local prefix=$1 d t
  for d in modules/k8s/*/; do [ -f "${d}Chart.yaml" ] && echo "${prefix}/k8s/$(basename "$d")"; done
  for d in modules/*/*/; do t=$(basename "$(dirname "$d")"); [ "$t" != k8s ] && [ -f "${d}main.tf" ] && echo "${prefix}/${t}/$(basename "$d")"; done
  echo "${prefix}/index"
}

for r in "${regs[@]}"; do
  host=${r%%/*}
  case "$host" in
    ghcr.io)
      [ -n "${GH_TOKEN:-}" ] && [ -n "${GH_ACTOR:-}" ] || { echo "ERROR: $r needs GH_TOKEN and GH_ACTOR" >&2; exit 1; }
      echo "$GH_TOKEN" | helm registry login ghcr.io -u "$GH_ACTOR" --password-stdin
      echo "$GH_TOKEN" | oras login ghcr.io -u "$GH_ACTOR" --password-stdin ;;
    public.ecr.aws)
      aws ecr-public get-login-password --region us-east-1 | helm registry login public.ecr.aws -u AWS --password-stdin
      aws ecr-public get-login-password --region us-east-1 | oras login public.ecr.aws -u AWS --password-stdin
      prefix=${r#public.ecr.aws/*/}
      existing=$(aws ecr-public describe-repositories --region us-east-1 --query 'repositories[].repositoryName' --output text | tr '\t' '\n')
      comm -23 <(packages "$prefix" | sort -u) <(echo "$existing" | sort -u) | while read -r repo; do
        [ -n "$repo" ] && echo "Creating ECR Public repository $repo" && aws ecr-public create-repository --region us-east-1 --repository-name "$repo" >/dev/null
      done ;;
    *.dkr.ecr.*.amazonaws.com)
      region=$(echo "$host" | sed -E 's/^[0-9]+\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$/\1/')
      aws ecr get-login-password --region "$region" | helm registry login "$host" -u AWS --password-stdin
      aws ecr get-login-password --region "$region" | oras login "$host" -u AWS --password-stdin
      prefix=${r#"$host"/}
      existing=$(aws ecr describe-repositories --region "$region" --query 'repositories[].repositoryName' --output text | tr '\t' '\n')
      comm -23 <(packages "$prefix" | sort -u) <(echo "$existing" | sort -u) | while read -r repo; do
        [ -n "$repo" ] && echo "Creating ECR repository $repo" && aws ecr create-repository --region "$region" --repository-name "$repo" >/dev/null
      done ;;
    *) echo "ERROR: no login method for registry host $host (ghcr.io, public.ecr.aws and private ECR are supported)" >&2; exit 1 ;;
  esac
done
