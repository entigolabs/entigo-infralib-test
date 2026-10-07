#!/usr/bin/env bash
# Publish a module repository's charts and OpenTofu modules as OCI packages,
# with an SBOM attached to each, plus a signed index of everything. Adapted
# from entigo-infralib's release workflow; the layout is what the agent
# consumes as an oci:// source:
#
#   <registry>/k8s/<chart>:<version>            Helm chart
#   <registry>/<type>/<module>:<version>        OpenTofu module (zip)
#   <registry>/index:<version>                  zip of the released tree, manifest.json, SBOM
#
# Environment:
#   REGISTRIES   comma separated registry paths; the first is pushed to, the
#                rest receive byte-identical copies (oras cp)
#   VERSION      bare semver of the release
#   SUPPLIER     organisation for the SBOMs
#   OUT          directory for manifest.json and the index zip (release assets)
set -euo pipefail
cd "${GITHUB_WORKSPACE:-.}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=retry.sh
source "$HERE/retry.sh"

IFS=, read -r -a ALL <<<"${REGISTRIES}"
PRIMARY="${ALL[0]%/}"
MIRRORS=("${ALL[@]:1}")
export SBOM_REGISTRY="$PRIMARY" SBOM_SUPPLIER="${SUPPLIER:-Entigolabs}"
WORK=$(mktemp -d)
mkdir -p "$WORK/entries" "$WORK/charts" "$OUT"

# label HOST -> short name used as key in manifest.json (ghcr, ecr, or the host)
label() {
  case "${1%%/*}" in
    ghcr.io) echo ghcr ;;
    public.ecr.aws) echo ecr ;;
    *) echo "${1%%/*}" ;;
  esac
}

# record NAME TYPE PKG_DIGEST SBOM_DIGEST: one manifest entry with every registry.
record() {
  local name=$1 type=$2 pkg=$3 sbom=$4 regs='{}' r
  for r in "$PRIMARY" "${MIRRORS[@]}"; do
    regs=$(jq -c --arg k "$(label "$r")" --arg repo "$r/$type/$name" --arg pkg "$pkg" --arg sbom "$sbom" \
      '. + {($k): {repository: $repo, package: $pkg, sbom: $sbom}}' <<<"$regs")
  done
  jq -nc --arg name "$name" --arg type "$type" --arg tag "$VERSION" --argjson regs "$regs" \
    '{name: $name, type: $type, tag: $tag, registries: $regs}' > "$WORK/entries/${type}__${name}.json"
}

# mirror REF: copy a tag (with referrers) from the primary to every mirror.
mirror() {
  local ref=$1 m
  for m in "${MIRRORS[@]}"; do
    retry oras cp -r --no-tty "$PRIMARY/$ref" "$m/$ref" >/dev/null
  done
}

publish_chart() {
  set -euo pipefail
  local dir=$1 name; name=$(basename "$dir")
  [ -f "$dir/Chart.yaml" ] || return 0
  trap 'rc=$?; echo "ERROR: k8s/'"$name"' failed (exit ${rc}) in: ${BASH_COMMAND}" >&2' ERR
  echo "Publishing k8s/$name"
  # SBOM first, while test/ (static values) still exists; then package without it.
  python3 "$HERE/sbom.py" helm "$dir" "$name" "$VERSION" > "$WORK/$name-sbom.spdx.json"
  rm -rf "$dir/test" "$dir/test.sh"
  helm package "$dir" --version "$VERSION" --destination "$WORK/charts" >/dev/null
  local log="$WORK/$name-push.log" digest sbom
  if ! retry helm push "$WORK/charts/$name-$VERSION.tgz" "oci://$PRIMARY/k8s" > "$log" 2>&1; then
    sed "s/^/  [$name] /" "$log" >&2; return 1
  fi
  grep '^WARN' "$log" >&2 || true
  digest=$(awk '/Digest:/ {print $2}' "$log" | tail -n1)
  [ -n "$digest" ] || { echo "ERROR: no digest for k8s/$name:" >&2; sed "s/^/  [$name] /" "$log" >&2; return 1; }
  sbom=$(retry oras attach --artifact-type application/vnd.syft.sbom --disable-path-validation \
    --format go-template --template '{{.digest}}' "$PRIMARY/k8s/$name:$VERSION" "$WORK/$name-sbom.spdx.json:application/spdx+json")
  [ -n "$sbom" ] || { echo "ERROR: failed to attach SBOM for k8s/$name" >&2; return 1; }
  mirror "k8s/$name:$VERSION"
  record "$name" k8s "$digest" "$sbom"
  echo "Published  k8s/$name ($digest)"
}

publish_module() {
  set -euo pipefail
  local dir=$1 type name; type=$(basename "$(dirname "$dir")"); name=$(basename "$dir")
  [ -f "$dir/main.tf" ] || return 0
  trap 'rc=$?; echo "ERROR: '"$type/$name"' failed (exit ${rc}) in: ${BASH_COMMAND}" >&2' ERR
  echo "Publishing $type/$name"
  rm -rf "$dir/test" "$dir/test.sh"
  local zip="$WORK/$type-$name-$VERSION.zip" digest sbom
  ( cd "$dir" && zip -r "$zip" . >/dev/null )
  digest=$(retry oras push --artifact-type=application/vnd.opentofu.modulepkg --disable-path-validation \
    --format go-template --template '{{.digest}}' "$PRIMARY/$type/$name:$VERSION" "$zip:archive/zip")
  [ -n "$digest" ] || { echo "ERROR: no digest for $type/$name" >&2; return 1; }
  python3 "$HERE/sbom.py" tofu "$dir" "$name" "$VERSION" "$type" > "$WORK/$type-$name-sbom.spdx.json"
  sbom=$(retry oras attach --artifact-type application/vnd.syft.sbom --disable-path-validation \
    --format go-template --template '{{.digest}}' "$PRIMARY/$type/$name:$VERSION" "$WORK/$type-$name-sbom.spdx.json:application/spdx+json")
  [ -n "$sbom" ] || { echo "ERROR: failed to attach SBOM for $type/$name" >&2; return 1; }
  mirror "$type/$name:$VERSION"
  record "$name" "$type" "$digest" "$sbom"
  echo "Published  $type/$name ($digest)"
}

export -f publish_chart publish_module record mirror label retry
export PRIMARY VERSION WORK HERE
export MIRRORS_STR="${MIRRORS[*]:-}"
# Arrays do not export; workers rebuild MIRRORS from the string.
worker() { IFS=' ' read -r -a MIRRORS <<<"$MIRRORS_STR"; export MIRRORS; "$@"; }
export -f worker

# Charts, then modules, up to 6 at a time. xargs only says "123" on failure,
# so the missing-entry check below names what did not get published.
charts=(modules/k8s/*/); modules=()
for d in modules/*/; do [ "$(basename "$d")" = k8s ] || modules+=("$d"*/); done
rc=0
{ printf '%s\n' "${charts[@]}" | xargs -P 6 -I{} bash -c 'worker publish_chart "$@"' _ {}; } || rc=$?
{ printf '%s\n' "${modules[@]}" | xargs -P 6 -I{} bash -c 'worker publish_module "$@"' _ {}; } || rc=$?

expected=0; missing=""
for d in "${charts[@]}"; do [ -f "$d/Chart.yaml" ] && { expected=$((expected+1)); [ -f "$WORK/entries/k8s__$(basename "$d").json" ] || missing="$missing  k8s/$(basename "$d")"$'\n'; }; done
for d in "${modules[@]}"; do [ -f "$d/main.tf" ] && { expected=$((expected+1)); t=$(basename "$(dirname "$d")"); [ -f "$WORK/entries/${t}__$(basename "$d").json" ] || missing="$missing  $t/$(basename "$d")"$'\n'; }; done
if [ "$rc" -ne 0 ] || [ -n "$missing" ]; then
  echo "ERROR: publishing failed (xargs exit $rc). Without a published package:" >&2
  printf '%s' "${missing:-  (none missing, a worker failed after recording its entry)}" >&2
  exit 1
fi
cat "$WORK"/entries/*.json > "$WORK/entries.jsonl"
echo "Published $expected packages to $PRIMARY${MIRRORS[*]:+ and mirrored to ${MIRRORS[*]}}"

# Every mirror must resolve every tag to the recorded digest before the index is built.
for m in "${MIRRORS[@]}"; do
  fail=0
  while read -r entry; do
    repo=$(jq -r --arg k "$(label "$m")" '.registries[$k].repository' <<<"$entry")
    pkg=$(jq -r --arg k "$(label "$m")" '.registries[$k].package' <<<"$entry")
    got=$(oras resolve "$repo:$VERSION" 2>&1 | grep -oE 'sha256:[0-9a-f]{64}' | head -n1 || true)
    [ "$got" = "$pkg" ] || { echo "ERROR: $repo:$VERSION -> '${got:-<missing>}', expected '$pkg'" >&2; fail=1; }
  done < "$WORK/entries.jsonl"
  [ "$fail" -eq 0 ] || exit 1
  echo "Verified $expected packages in $m"
done

# Index: the released tree (no charts/, tests, READMEs), manifest.json and an SBOM, signed.
stage=$(mktemp -d)
tree=(modules); [ -d providers ] && tree+=(providers)
rsync -a --exclude 'charts/' --exclude 'test/' --exclude 'test.sh' --exclude 'README.md' "${tree[@]}" "$stage/"
jq -s --arg version "$VERSION" '{version: $version, modules: .}' "$WORK/entries.jsonl" > "$stage/manifest.json"
python3 "$HERE/sbom.py" agent "$WORK/entries.jsonl" "$VERSION" > "$stage/index-sbom.spdx.json"
cp "$stage/manifest.json" "$OUT/manifest.json"
( cd "$stage" && zip -r "index-$VERSION.zip" "${tree[@]}" manifest.json index-sbom.spdx.json >/dev/null )
cp "$stage/index-$VERSION.zip" "$OUT/"
cd "$stage"
digest=$(retry oras push --artifact-type=application/vnd.entigo.agentpkg --format go-template --template '{{.digest}}' \
  "$PRIMARY/index:$VERSION" "index-$VERSION.zip:archive/zip")
[ -n "$digest" ] || { echo "ERROR: no index digest" >&2; exit 1; }
cosign sign --yes "$PRIMARY/index@$digest"
sbom=$(retry oras attach --artifact-type application/vnd.syft.sbom --format go-template --template '{{.digest}}' \
  "$PRIMARY/index:$VERSION" index-sbom.spdx.json:application/spdx+json)
cosign sign --yes "$PRIMARY/index@$sbom"
for m in "${MIRRORS[@]}"; do
  retry oras cp -r --no-tty "$PRIMARY/index:$VERSION" "$m/index:$VERSION" >/dev/null
  # Copy the signatures instead of re-signing, so the mirror is byte-identical.
  retry oras cp "$PRIMARY/index:sha256-${digest#sha256:}.sig" "$m/index:sha256-${digest#sha256:}.sig"
  retry oras cp "$PRIMARY/index:sha256-${sbom#sha256:}.sig" "$m/index:sha256-${sbom#sha256:}.sig"
done

cat <<SUMMARY

Package index published: $PRIMARY/index:$VERSION ($digest)
Agent source:            oci://$PRIMARY${MIRRORS[*]:+ (mirrors: ${MIRRORS[*]})}

  oras pull $PRIMARY/index:$VERSION -o index-out && unzip index-out/index-$VERSION.zip -d index-$VERSION
  cat index-$VERSION/manifest.json      # every package with its digest

  cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com \\
    --certificate-identity-regexp '^https://github\\.com/${GITHUB_REPOSITORY:-<org>/<repo>}/' \\
    $PRIMARY/index@$digest
SUMMARY
echo "index_digest=$digest" >> "${GITHUB_OUTPUT:-/dev/null}"
