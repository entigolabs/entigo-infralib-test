#!/usr/bin/env bash
# Reports the updates available to what a module repository depends on, the
# way entigo-infralib's weekly report has, one kind at a time so that each
# kind can be posted as a message of its own:
#
#   report.sh k8s           Helm chart dependencies of modules/k8s/*/Chart.yaml
#                           and the Crossplane provider packages referenced in
#                           the modules' provider.yaml files
#   report.sh <cloud>       OpenTofu providers and registry modules used by
#                           modules/<cloud>*/ (modules/aws and modules/aws-v2
#                           for aws)
#   report.sh --list-kinds  the kinds this repository has, one per line
#
# One line per finding on stdout, "<name> newer version <latest>, current
# <version> (<file>)", or a line saying what could not be checked; nothing when
# everything is current. After its own checks the script runs the repository's
# report/<kind>.sh, if there is one, for checks the framework knows nothing
# about (entigo-infralib's EKS AMI release versions, say), and appends its
# output. A kind that only has such a hook is a kind too.
#
# Needs curl, jq and yq; helm for k8s. GITHUB_TOKEN, when set, authenticates
# the GitHub API calls made for vendored charts. Run from the repository root
# or pass --root <dir>.
set -euo pipefail

die() { echo "report.sh: $*" >&2; exit 2; }

ROOT=.
KIND=""
LIST=false
while [ $# -gt 0 ]; do
  case "$1" in
    --root) [ $# -ge 2 ] || die "--root needs a value"; ROOT=$2; shift 2 ;;
    --list-kinds) LIST=true; shift ;;
    -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) die "unknown option $1" ;;
    *) [ -z "$KIND" ] || die "one kind at a time"; KIND=$1; shift ;;
  esac
done
cd "$ROOT"

# The kinds: k8s when modules/k8s exists, every other modules/<dir> with a
# -v<N> suffix folded into its base name, and every report/<kind>.sh hook.
list_kinds() {
  {
    [ ! -d modules/k8s ] || echo k8s
    for d in modules/*/; do
      [ -d "$d" ] || continue
      n=$(basename "$d")
      [ "$n" != k8s ] || continue
      echo "$n" | sed -E 's/-v[0-9]+$//'
    done
    for h in report/*.sh; do
      [ -f "$h" ] || continue
      basename "${h%.sh}"
    done
  } | sort -u
}

if [ "$LIST" = true ]; then
  list_kinds
  exit 0
fi
[ -n "$KIND" ] || die "usage: report.sh k8s|<cloud> [--root DIR], or report.sh --list-kinds"

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need curl; need jq

# Only major.minor.patch is compared: a chart's or provider's prerelease or
# build suffix is noise for the question "is there a newer version".
semver3() { echo "$1" | sed 's/^v//' | cut -d. -f1-3; }

# Highest X.Y.Z among the lines on stdin, prereleases and odd tags dropped.
highest() { sed 's/^v//' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1; }

github_api() {
  local args=(-s -H "Accept: application/vnd.github+json")
  [ -z "${GITHUB_TOKEN:-}" ] || args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  curl "${args[@]}" "$1"
}

# ---------------------------------------------------------------- k8s ------

# A deprecated module keeps shipping so platforms can finish migrating off it,
# but its upstream chart and providers are no longer followed.
deprecated_module() {
  [ -f "$1/Chart.yaml" ] && [ "$(yq -r '.deprecated // false' "$1/Chart.yaml")" = true ]
}

# Highest version tag of a chart in an OCI registry: anonymous pull token
# where the registry hands one out, then the tags list with its pagination.
latest_oci_chart() {
  local name=$1 url=$2 ref host path token="" next tags="" response body
  ref="${url#oci://}/$name"
  host=${ref%%/*}
  path=${ref#*/}
  case "$host" in
    ghcr.io) token=$(curl -s "https://ghcr.io/token?scope=repository:${path}:pull" | jq -r '.token // ""') ;;
    # ECR Public redirects the token endpoint without the trailing slash.
    public.ecr.aws) token=$(curl -s "https://public.ecr.aws/token/?scope=repository:${path}:pull&service=public.ecr.aws" | jq -r '.token // ""') ;;
    docker.io|registry-1.docker.io) host=registry-1.docker.io
      token=$(curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${path}:pull" | jq -r '.token // ""') ;;
  esac
  next="https://${host}/v2/${path}/tags/list?n=1000"
  while [ -n "$next" ]; do
    response=$(curl -s -D - -H "Authorization: Bearer ${token}" "$next")
    body=${response#*$'\r\n\r\n'}
    tags="$tags $(echo "$body" | jq -r '.tags[]?' 2>/dev/null || true)"
    next=$(echo "$response" | grep -i '^link:' | sed 's/.*<\(.*\)>.*/\1/' || true)
    case "$next" in /v2/*) next="https://${host}${next}" ;; esac
  done
  echo "$tags" | tr ' ' '\n' | highest
}

# Highest version in a classic chart repository. The repository is added
# under a name derived from its URL, so two charts that share a dependency
# name but not a repository do not fight over the alias.
latest_repo_chart() {
  local name=$1 url=$2 alias
  alias="r$(echo -n "$url" | md5sum | cut -c1-10)"
  helm repo add "$alias" "$url" --force-update >/dev/null 2>&1 || { echo ""; return; }
  helm search repo -r "\v${alias}/${name}\v" --output json 2>/dev/null | jq -r '.[0].version // ""'
}

report_k8s_charts() {
  local chart name version url latest upstream
  for chart in $(find modules/k8s -name Chart.yaml 2>/dev/null | sort); do
    deprecated_module "$(dirname "$chart")" && continue
    while read -r name version url; do
      [ -n "$name" ] || continue
      case "$url" in
        oci://*) latest=$(latest_oci_chart "$name" "$url") ;;
        file://*)
          # A vendored chart has no registry to ask, so the upstream
          # repository's releases are followed instead; which one is an
          # annotation on the module's own Chart.yaml.
          upstream=$(yq -r '.annotations."infralib.entigo.io/upstream-repo" // ""' "$chart")
          if [ -z "$upstream" ]; then
            echo "$name is vendored from $url, but $chart has no infralib.entigo.io/upstream-repo annotation to check against"
            continue
          fi
          latest=$(github_api "https://api.github.com/repos/$upstream/releases/latest" | jq -r '.tag_name // ""' | sed 's/^v//') ;;
        *) latest=$(latest_repo_chart "$name" "$url") ;;
      esac
      if [ -z "$latest" ] || [ "$latest" = null ]; then
        echo "$name Chart not found in repo $url ($chart)"
      elif [ "$(semver3 "$latest")" != "$(semver3 "$version")" ]; then
        echo "$name newer version $latest, current $version ($chart)"
      fi
    done < <(yq -r '.dependencies[]? | "\(.name) \(.version) \(.repository)"' "$chart")
  done
}

# Crossplane provider packages: the Upbound marketplace redirects a provider's
# page to its latest version, which is the fifth path element of the redirect.
report_k8s_providers() {
  local file provider path current latest
  for file in $(find modules/k8s -name provider.yaml 2>/dev/null | sort); do
    # provider.yaml sits at varying depths under templates/, so the module
    # is modules/k8s/<module>, not the file's own directory.
    deprecated_module "$(echo "$file" | cut -d/ -f1-3)" && continue
    while read -r provider; do
      [ -n "$provider" ] || continue
      path=${provider%%:*}
      current=${provider#*:}
      latest=$(curl -s "https://marketplace.upbound.io/providers/$path" | cut -d/ -f5)
      if [ -z "$latest" ]; then
        echo "$path crossplane provider package latest version https://marketplace.upbound.io/providers/$path not found"
      elif [ "$current" != "$latest" ]; then
        echo "$path newer version $latest, current $current ($file)"
      fi
    done < <(grep xpkg.upbound.io "$file" | grep -v '\$provider' | cut -d/ -f2- || true)
  done
}

report_k8s() {
  need yq; need helm
  report_k8s_charts
  report_k8s_providers
}

# ----------------------------------------------------------- OpenTofu ------

# Every "source = ..." block of the given files with the version it pins, as
# "source=version" lines: the required_providers entries and the module
# blocks both have that shape.
sources_with_versions() {
  cat "$@" 2>/dev/null | awk '
    $0 ~ " source" { source=$3; gsub(" ", "", source)
      while ($1 != "}") {
        if ($1 == "version") { v=""; for (i=3; i<=NF; i++) v=v$i; print source "=" v }
        if ((getline) <= 0) break
      }
    }' | tr -d '"' | sort -u
}

# A version that is a constraint rather than a pin (">= 5.0", "~> 1.2") is
# satisfied by whatever is current and has nothing to report.
exact_version() { [[ "$1" =~ ^[0-9]+(\.[0-9]+)*$ ]]; }

report_tofu() {
  local kind=$1 dirs tf_files line source current latest name
  dirs=$(ls -d "modules/$kind" modules/"$kind"-v* 2>/dev/null || true)
  [ -n "$dirs" ] || { [ -f "report/$kind.sh" ] && return 0 || die "no modules/$kind directory"; }
  tf_files=$(find $dirs -name '*.tf' | sort)
  [ -n "$tf_files" ] || return 0
  # Two modules may use different submodules of the same registry module,
  # which is one line once the //submodule path is dropped; hence sort -u.
  while read -r line; do
    source=${line%%=*}; current=${line#*=}
    exact_version "$current" || continue
    # A provider source is namespace/name; a registry module source is
    # namespace/name/provider, with any //submodule path dropped. The
    # OpenTofu registry returns every version of either, so the highest
    # stable one is picked here.
    name=${source%%//*}
    case "$name" in
      */*/*)
        latest=$(curl -s "https://registry.opentofu.org/v1/modules/$name/versions" | jq -r '.modules[0].versions[]?.version' 2>/dev/null | highest || true)
        if [ -z "$latest" ]; then
          echo "$name module not found in https://registry.opentofu.org/v1/modules/$name/versions"
        elif [ "$current" != "$latest" ]; then
          echo "$name newer version $latest, current $current"
        fi ;;
      */*)
        latest=$(curl -s "https://registry.opentofu.org/v1/providers/$name/versions" | jq -r '.versions[]?.version' 2>/dev/null | highest || true)
        if [ -z "$latest" ]; then
          echo "$name provider not found in https://registry.opentofu.org/v1/providers/$name/versions"
        elif [ "$current" != "$latest" ]; then
          echo "$name newer version $latest, current $current"
        fi ;;
    esac
  done < <(sources_with_versions $tf_files) | sort -u
}

case "$KIND" in
  k8s) report_k8s ;;
  *) report_tofu "$KIND" ;;
esac

# The repository's own checks for this kind, if it has any.
if [ -f "report/$KIND.sh" ]; then
  bash "report/$KIND.sh"
fi
