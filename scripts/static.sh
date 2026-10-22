#!/usr/bin/env bash
# Static checks of modules, without a cloud: what a pull request can tell
# before anything is applied.
#
#   static.sh modules/aws/vpc modules/k8s/argocd ...   check these modules
#   static.sh                                           check every module
#
# A terraform module (modules/<type>/<name>, type not k8s) is linted with
# tflint; the terraform_required_providers rule is off, since the agent
# generates the providers. A k8s module (a Helm chart) gets helm lint --strict,
# a helm template render, and a kube-score of the rendered manifests. Both
# helm steps use the chart's test/static_values.yaml when there is one, for
# values the chart cannot default (a hostname, say). kube-score skips the
# checks every chart of the agent's platform fails by design (pull policy,
# read-only root filesystem, user ids, pod disruption budgets, statefulset
# service names) plus the ones the module's test/module.yaml lists under
# kube_score_ignore.
#
# Everything runs in pinned containers, so the host needs docker only. Run
# from the repository root or pass --root <dir>. Exit status is the number of
# modules that failed.
set -euo pipefail

TFLINT_IMAGE="${TFLINT_IMAGE:-ghcr.io/terraform-linters/tflint:v0.64.0}"
HELM_IMAGE="${HELM_IMAGE:-alpine/helm:3.22.1}"
KUBE_SCORE_IMAGE="${KUBE_SCORE_IMAGE:-zegl/kube-score:v1.20.0}"
# pod-probes-identical: upstream charts commonly reuse one probe for liveness
# and readiness; the platform's charts inherit that from their subcharts.
KUBE_SCORE_DEFAULT_IGNORE=(container-image-pull-policy container-security-context-readonlyrootfilesystem
  container-security-context-user-group-id deployment-has-poddisruptionbudget statefulset-has-servicename
  pod-probes-identical)

die() { echo "static.sh: $*" >&2; exit 2; }
log() { printf '\033[1;34m==> %s\033[0m\n' "$*" >&2; }

ROOT=.
MODULES=()
while [ $# -gt 0 ]; do
  case "$1" in
    --root) [ $# -ge 2 ] || die "--root needs a value"; ROOT=$2; shift 2 ;;
    -h|--help) sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) die "unknown option $1" ;;
    *) MODULES+=("${1%/}"); shift ;;
  esac
done
cd "$ROOT"
ROOT=$(pwd -P)

if [ ${#MODULES[@]} -eq 0 ]; then
  for d in modules/*/*/; do MODULES+=("${d%/}"); done
fi

# The user inside the containers is the caller, so that helm's cache files
# and tflint's are theirs; HOME is a writable scratch directory.
run() { # run IMAGE MOUNT-DIR [docker args --] cmd...
  local image=$1 dir=$2; shift 2
  local extra=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do extra+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$dir:/work" -w /work "${extra[@]}" "$image" "$@"
}

# kube_score_ignore MODULE-DIR: the kube_score_ignore list of test/module.yaml,
# one check id per line. Accepts a block list and a flow list.
kube_score_ignore() {
  local file=$1/test/module.yaml
  [ -f "$file" ] || return 0
  awk '
    /^kube_score_ignore:[[:space:]]*\[/ { s=$0; sub(/^[^[]*\[/, "", s); sub(/\].*$/, "", s); n=split(s, a, ","); for (i=1;i<=n;i++) { gsub(/[[:space:]"\x27]/, "", a[i]); if (a[i] != "") print a[i] }; next }
    /^kube_score_ignore:/ { inlist=1; next }
    inlist && /^[[:space:]]*-[[:space:]]*/ { v=$0; sub(/^[[:space:]]*-[[:space:]]*/, "", v); sub(/[[:space:]]*(#.*)?$/, "", v); gsub(/["\x27]/, "", v); if (v != "") print v; next }
    inlist && /^[^[:space:]#]/ { inlist=0 }
  ' "$file"
}

check_terraform() {
  local module=$1
  log "tflint $module"
  # The whole repository is mounted and tflint changes into the module, so
  # a module that includes a sibling (oke and its oke-node-pool, through a
  # symlink or a ../ source) finds it.
  run "$TFLINT_IMAGE" "$ROOT" -- --chdir "$module" --disable-rule terraform_required_providers
}

check_chart() {
  local module=$1 name values=() ignore=()
  name=$(basename "$module")
  [ -f "$module/test/static_values.yaml" ] && values=(-f test/static_values.yaml)
  log "helm lint $module"
  run "$HELM_IMAGE" "$ROOT/$module" -- lint --strict "${values[@]}" .
  log "helm template $module"
  local rendered
  rendered=$(run "$HELM_IMAGE" "$ROOT/$module" -- template "$name" --skip-tests --namespace "$name" "${values[@]}" .)
  log "kube-score $module"
  for check in "${KUBE_SCORE_DEFAULT_IGNORE[@]}"; do ignore+=(--ignore-test "$check"); done
  while read -r check; do [ -n "$check" ] && ignore+=(--ignore-test "$check"); done < <(kube_score_ignore "$module")
  printf '%s\n' "$rendered" | run "$KUBE_SCORE_IMAGE" "$ROOT/$module" -i -- score "${ignore[@]}" -
}

failed=()
for module in "${MODULES[@]}"; do
  case "$module" in
    modules/k8s/*) check_chart "$module" || failed+=("$module") ;;
    modules/*/*)  check_terraform "$module" || failed+=("$module") ;;
    *) die "$module is not a module directory (modules/<type>/<name>)" ;;
  esac
done
if [ ${#failed[@]} -gt 0 ]; then
  printf '\033[1;31m==> static checks failed: %s\033[0m\n' "${failed[*]}" >&2
  exit ${#failed[@]}
fi
log "static checks passed for ${#MODULES[@]} modules"
