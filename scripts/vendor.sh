#!/usr/bin/env bash
# Vendors the community OpenTofu modules a module calls into the module
# itself, so that it carries them the way a chart carries charts/*.tgz and
# nothing is downloaded from the registry or GitHub for them at init time.
# Two files in the module directory, after Chart.yaml and Chart.lock, and the
# modules themselves under <module-dir>/vendors, after charts/:
#
#   vendor.yaml   written by hand (or by the update automation): what is
#                 wanted, a source and a version per module
#                   modules:
#                     - source: terraform-aws-modules/efs/aws
#                       version: 2.2.1
#                 source is written the way OpenTofu writes a module source,
#                 without a //submodule path: the whole module is vendored
#                 and the call picks the submodule. Without a prefix it is a
#                 registry address (<namespace>/<name>/<provider>), the only
#                 kind resolved today. A prefixed one (git::https://...) is
#                 reserved for modules outside the registry: only resolve()
#                 has to learn it, the fetch and the lock already work for
#                 any git host.
#   vendor.lock   written by this script: what that resolved to, the
#                 directory name under vendors/ (the name of the module's
#                 repository), the repository, the commit and a checksum of
#                 the vendored tree; and, marked required_by, every
#                 module that one of them calls, which the script found
#                 and vendored by itself
#
#   vendor.sh <module-dir>...
#       Like "helm dependency update": downloads every module of vendor.yaml
#       in the version the registry serves, prunes what a module call never
#       reads, writes it to <module-dir>/vendors/<repository-name> and writes
#       <module-dir>/vendor.lock. The module calls it with
#       source = "./vendors/<repository-name>" (or "./vendors/<repository-name>/modules/<sub>" for a submodule)
#       and no version. A version bump is an edit of vendor.yaml and this.
#   vendor.sh --check [<module-dir>...]
#       Offline: vendor.lock is what vendor.yaml asks for and every vendored
#       tree still matches its checksum (nobody edited it by hand). Without
#       arguments, every module of the repository. The static checks run it
#       for every terraform module that vendors something.
#
# A vendored module that calls a registry module of its own (terraform-aws-eks
# and its kms module) gets that module vendored too, without a vendor.yaml
# entry: next to it, in a directory named after both
# (vendors/terraform-aws-eks--terraform-aws-kms), in the highest version its
# constraint allows, and the call rewritten to that path. vendor.lock lists it
# as required_by the module that calls it. Only the calls that can be reached
# from what the module uses are followed: a submodule nobody calls keeps its
# registry calls, which OpenTofu never loads. Those rewritten call lines are
# the only difference between a vendored tree and its upstream commit. --check
# fails when a module with a vendor.yaml can still reach a module call that is
# downloaded at init time.
#
# Needs curl, git and sha256sum; --check only sha256sum. Module directories
# are relative to the repository root: run from there or pass --root <dir>.
# In a module repository: ./test.sh vendor [--check] [<module-dir>...].
set -euo pipefail
ROOT=.
CHECK=false
DIRS=()
die() { echo "vendor.sh: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --root) [ $# -ge 2 ] || die "--root needs a value"; ROOT=$2; shift 2 ;;
    --check) CHECK=true; shift ;;
    -h|--help) sed -n '2,55p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) die "unknown option $1" ;;
    *) DIRS+=("${1%/}"); shift ;;
  esac
done
cd "$ROOT"
REGISTRY="${VENDOR_REGISTRY:-https://registry.opentofu.org}"

# tree_checksum DIR: one sha256 over the names and contents of every file.
tree_checksum() {
  ( cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1 )
}

# wanted FILE: "source version" per entry of a vendor.yaml or vendor.lock. The
# lock's required_by entries are the modules the script found by itself,
# which vendor.yaml does not list.
wanted() {
  awk '{ sub(/[[:space:]]+#.*$/, "") }
       $1 == "-" { $1 = ""; $0 = $0; nested = 0 }
       $1 == "required_by:" { nested = 1 }
       $1 == "source:"  { source = $2; gsub(/["\x27]/, "", source) }
       $1 == "version:" { version = $2; gsub(/["\x27]/, "", version); if (!nested) print source, version }' "$1"
}

# module_calls FILE...: "file<TAB>source<TAB>version" per module block, the
# version empty when the call has none. Only the block's own attributes count,
# not a source or version key of one of its arguments.
module_calls() {
  awk '
    function quoted(line) { sub(/^[^"]*"/, "", line); sub(/".*$/, "", line); return line }
    FNR == 1 { depth = 0 }
    depth == 0 && /^module[ \t]+"[^"]+"[ \t]*\{/ { depth = 1; src = ""; ver = ""; next }
    depth == 0 { next }
    depth == 1 && src == "" && /^[ \t]*source[ \t]*=[ \t]*"/ { src = quoted($0) }
    depth == 1 && ver == "" && /^[ \t]*version[ \t]*=[ \t]*"/ { ver = quoted($0) }
    { line = $0; depth += gsub(/\{/, "", line) - gsub(/\}/, "", line)
      if (depth <= 0) { depth = 0; if (src != "") print FILENAME "\t" src "\t" ver } }
  ' "$@"
}

# walk DIR: follows the module calls of DIR's .tf files through local sources
# and appends every other call it reaches to REACHED, as module_calls prints
# it. SEEN and REACHED are the caller's.
walk() {
  local dir file source version
  dir=$(cd "$1" 2>/dev/null && pwd -P) || return 0
  [ -z "${SEEN[$dir]:-}" ] || return 0
  SEEN[$dir]=1
  compgen -G "$dir/*.tf" >/dev/null || return 0
  while IFS=$'\t' read -r file source version; do
    case "$source" in
      ./*|../*) walk "$dir/$source" ;;
      *) REACHED+=("$file"$'\t'"$source"$'\t'"$version") ;;
    esac
  done < <(module_calls "$dir"/*.tf)
}

# locked FILE: "name checksum" per entry of a vendor.lock.
locked() {
  awk '$1 == "-" && $2 == "name:" { name = $3 } $1 == "checksum:" { sub(/^sha256:/, "", $2); print name, $2 }' "$1"
}

check() {
  local rc=0 dir vendors name want got d line file source here REACHED=()
  local -A SEEN=()
  here=$(pwd -P)
  [ $# -gt 0 ] || set -- modules/*/*/
  for dir in "$@"; do
    dir="${dir%/}"; vendors="$dir/vendors"
    [ -d "$vendors" ] || [ -f "$dir/vendor.yaml" ] || [ -f "$dir/vendor.lock" ] || continue
    [ -f "$dir/vendor.yaml" ] || { echo "FAIL $dir has vendored modules but no vendor.yaml" >&2; rc=1; continue; }
    [ -f "$dir/vendor.lock" ] || { echo "FAIL $dir has no vendor.lock: run ./test.sh vendor $dir" >&2; rc=1; continue; }
    if [ "$(wanted "$dir/vendor.yaml" | LC_ALL=C sort)" != "$(wanted "$dir/vendor.lock" | LC_ALL=C sort)" ]; then
      echo "FAIL $dir/vendor.lock is not what vendor.yaml asks for: run ./test.sh vendor $dir" >&2; rc=1; continue
    fi
    while read -r name want; do
      got=$(tree_checksum "$vendors/$name" 2>/dev/null || true)
      if [ "$got" = "$want" ]; then echo "ok   $vendors/$name"
      else echo "FAIL $vendors/$name does not match vendor.lock (edited by hand?)" >&2; rc=1; fi
    done < <(locked "$dir/vendor.lock")
    # A directory the lock does not know is as wrong as a changed one.
    for d in "$vendors"/*/; do
      [ -d "$d" ] || continue
      locked "$dir/vendor.lock" | grep -q "^$(basename "$d") " || { echo "FAIL ${d%/} is not in vendor.lock" >&2; rc=1; }
    done
    # Whatever the module can still reach that is not a local path is
    # downloaded at init time, which is what vendoring is there to prevent.
    SEEN=(); REACHED=(); walk "$dir"
    for line in "${REACHED[@]}"; do
      IFS=$'\t' read -r file source _ <<<"$line"
      echo "FAIL ${file#"$here"/} calls $source, which is downloaded at init time: run ./test.sh vendor $dir (a call in the module's own files has to be in vendor.yaml first)" >&2; rc=1
    done
  done
  return $rc
}

# resolve SOURCE VERSION: sets REPO (git URL) and COMMIT. The one place that
# knows where a kind of source lives; everything after it is plain git.
resolve() {
  local source=$1 version=$2 answer get ref
  case "$source" in
    *::*) die "$source: only registry sources (<namespace>/<name>/<provider>) are supported so far, not ${source%%::*}:: ones" ;;
  esac
  [[ "$source" =~ ^[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$ ]] || die "$source is not a registry source (<namespace>/<name>/<provider>, without //submodule)"

  # The registry answers a download request with where the version lives, in
  # the body ({"location": "git::https://<host>/<org>/<repo>?ref=<commit>"})
  # or, on registries that follow the older protocol, in X-Terraform-Get.
  answer=$(curl -sSfL -D - "$REGISTRY/v1/modules/$source/$version/download" | tr -d '\r') || die "$source $version is not in $REGISTRY"
  get=$(grep -oE 'git::https://[^"[:space:]]+' <<<"$answer" | tail -n1 || true)
  [[ "$get" =~ ^git::(https://[^?]+)\?ref=([^\&]+)$ ]] || die "unsupported download location for $source $version: '${get}'"
  REPO="${BASH_REMATCH[1]%.git}"; ref="${BASH_REMATCH[2]}"

  # The registry pins a commit; a tag is resolved to one, so that the record
  # survives a moved tag.
  if [[ "$ref" =~ ^[0-9a-f]{40}$ ]]; then COMMIT="$ref"
  else COMMIT=$(git ls-remote "$REPO" "refs/tags/$ref^{}" "refs/tags/$ref" | sort -k2 -r | head -n1 | cut -f1); fi
  [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || die "cannot resolve $ref in $REPO"
}

# pick SOURCE CONSTRAINT: the highest released version of a registry module
# that a version constraint allows (any version without one), as OpenTofu
# picks it.
pick() {
  local source=$1 constraint=$2 versions picked
  versions=$(curl -sSfL "$REGISTRY/v1/modules/$source/versions" | grep -oE '"version":[[:space:]]*"[^"]+"' | sed -E 's/.*"([^"]+)"$/\1/') || die "$source is not in $REGISTRY"
  picked=$(awk -v constraint="$constraint" '
    function cmp(a, b,    x, y, i, n) { n = split(a, x, "."); i = split(b, y, "."); if (i > n) n = i
      for (i = 1; i <= n; i++) { if (x[i] + 0 < y[i] + 0) return -1; if (x[i] + 0 > y[i] + 0) return 1 } return 0 }
    function allowed(v,    parts, n, i, c, op, want, p, m, upper, j) {
      n = split(constraint, parts, ",")
      for (i = 1; i <= n; i++) {
        c = parts[i]; gsub(/[[:space:]]/, "", c); if (c == "") continue
        op = "="; if (match(c, /^(>=|<=|!=|~>|=|>|<)/)) { op = substr(c, 1, RLENGTH); c = substr(c, RLENGTH + 1) }
        want = c; sub(/^v/, "", want)
        if (op == "=" && cmp(v, want) != 0) return 0
        if (op == "!=" && cmp(v, want) == 0) return 0
        if (op == ">" && cmp(v, want) <= 0) return 0
        if (op == ">=" && cmp(v, want) < 0) return 0
        if (op == "<" && cmp(v, want) >= 0) return 0
        if (op == "<=" && cmp(v, want) > 0) return 0
        if (op == "~>") {
          if (cmp(v, want) < 0) return 0
          m = split(want, p, ".")
          if (m > 1) { upper = ""; for (j = 1; j < m - 1; j++) upper = upper p[j] "."; upper = upper (p[m - 1] + 1); if (cmp(v, upper) >= 0) return 0 }
        }
      }
      return 1 }
    { v = $0; sub(/^v/, "", v) }
    v ~ /^[0-9]+(\.[0-9]+)*$/ && allowed(v) && (best == "" || cmp(v, best) > 0) { best = v }
    END { print best }' <<<"$versions")
  [ -n "$picked" ] || die "no version of $source in $REGISTRY satisfies \"$constraint\""
  echo "$picked"
}

# fetch_tree REPO COMMIT DEST: that one commit, pruned, as DEST.
fetch_tree() {
  local repo=$1 commit=$2 src=$3
  # Over git: no host has to offer an archive download.
  git init -q "$src"
  git -C "$src" -c advice.detachedHead=false fetch -q --depth 1 "$repo" "$commit" || die "cannot fetch $commit from $repo"
  git -C "$src" -c advice.detachedHead=false checkout -q FETCH_HEAD

  # Pruned by exclusion, not by a list of wanted extensions: a module may read
  # any file it ships (templates, scripts, JSON), but never these.
  ( cd "$src"
    rm -rf .git .github examples test tests wrappers docs build autogen helpers
    rm -f CODEOWNERS Makefile Gemfile Gemfile.lock kitchen.yml metadata.yaml metadata.display.yaml
    find . -mindepth 2 -type f \( -name 'metadata.yaml' -o -name 'metadata.display.yaml' \) -delete
    find . -maxdepth 1 -type f -name '.*' -delete
    find . -type f -name '*.md' -delete
    find . -type l -delete
    find . -type d -empty -delete )
  [ -n "$(find "$src" -name '*.tf' -print -quit)" ] || die "$repo@$commit has no module files after pruning"
  [ -n "$(find "$src" -maxdepth 1 -type f -iname 'licen[cs]e*' -print -quit)" ] || die "$repo@$commit ships no licence file; it cannot be redistributed as is"
}

# nested TREE LOCK ENTRY...: vendors the registry modules that the vendored
# module in TREE calls, as far as they can be reached from its ENTRY
# directories (the ones the caller uses: "." or modules/<sub>), next to TREE
# as <TREE's name>--<repository-name>, rewrites those calls to that path, and
# does the same for each of them. Their lock entries, marked required_by, are
# appended to LOCK, each followed by the entries of what it requires itself.
nested() {
  local tree lock=$2 parent entry line file source version base sub name picked dest prefix rel map REPO COMMIT REACHED=() entries=()
  local -A SEEN=() NAME=() CONSTRAINT=() SUBS=() FILES=() PICKED=() REPOS=() COMMITS=()
  tree=$(cd "$1" && pwd -P); parent=$(basename "$tree"); shift 2
  for entry in "$@"; do
    [ -d "$tree/$entry" ] || die "$parent has no directory $entry"
    walk "$tree/$entry"
  done
  [ ${#REACHED[@]} -gt 0 ] || return 0

  for line in "${REACHED[@]}"; do
    IFS=$'\t' read -r file source version <<<"$line"
    base=${source%%//*}; sub=.
    [[ "$source" != *//* ]] || sub=${source#*//}
    [[ "$base" =~ ^[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$ ]] || die "$parent calls $source in ${file#"$tree"/}: only registry modules can be vendored so far"
    if [ -z "${NAME[$base]:-}" ]; then
      picked=$(pick "$base" "$version")
      resolve "$base" "$picked"
      name="$parent--$(basename "$REPO")"; dest="$(dirname "$tree")/$name"
      [ ! -e "$dest" ] || die "$parent requires $base, which would be vendored as $name, but that directory is already taken"
      fetch_tree "$REPO" "$COMMIT" "$dest"
      NAME[$base]=$name; CONSTRAINT[$base]=$version; PICKED[$base]=$picked; REPOS[$base]=$REPO; COMMITS[$base]=$COMMIT
    elif [ "${CONSTRAINT[$base]}" != "$version" ]; then
      die "$parent calls $base with two version constraints (\"${CONSTRAINT[$base]}\" and \"$version\"), which cannot be vendored into one directory"
    fi
    SUBS[$base]+="$sub"$'\n'; FILES[$file]=1
  done

  # The calls, rewritten: a path from the file's own directory to the
  # directory next to the module, and no version, which a local source
  # cannot have.
  map=""; for base in "${!NAME[@]}"; do map+="$base=${NAME[$base]};"; done
  for file in "${!FILES[@]}"; do
    rel=$(dirname "${file#"$tree"/}"); prefix=../
    [ "$rel" = . ] || prefix=../$(sed -E 's#[^/]+#..#g' <<<"$rel")/
    awk -v map="$map" -v prefix="$prefix" '
      function flush(    i, n, parts, kv, base, subdir, target, indent) {
        target = ""; base = src; subdir = ""
        if (index(src, "//")) { base = substr(src, 1, index(src, "//") - 1); subdir = substr(src, index(src, "//") + 2) }
        n = split(map, parts, ";")
        for (i = 1; i <= n; i++) { split(parts[i], kv, "="); if (kv[1] == base) target = prefix kv[2] (subdir != "" ? "/" subdir : "") }
        for (i = 1; i <= count; i++) {
          if (target != "" && i == verline) continue
          if (target != "" && i == srcline) { indent = block[i]; sub(/source.*$/, "", indent); print indent "source = \"" target "\""; continue }
          print block[i]
        }
        count = 0 }
      function quoted(line) { sub(/^[^"]*"/, "", line); sub(/".*$/, "", line); return line }
      depth == 0 && /^module[ \t]+"[^"]+"[ \t]*\{/ { depth = 1; src = ""; srcline = 0; verline = 0; count = 1; block[1] = $0; next }
      depth == 0 { print; next }
      { block[++count] = $0 }
      depth == 1 && srcline == 0 && /^[ \t]*source[ \t]*=[ \t]*"/ { src = quoted($0); srcline = count }
      depth == 1 && verline == 0 && /^[ \t]*version[ \t]*=[ \t]*"/ { verline = count }
      { line = $0; depth += gsub(/\{/, "", line) - gsub(/\}/, "", line); if (depth <= 0) { depth = 0; flush() } }
      END { if (count) flush() }' "$file" > "$file.rewritten"
    mv "$file.rewritten" "$file"
  done

  # Each one's own requirements first: its checksum is of the tree with its
  # calls rewritten.
  while read -r base; do
    name=${NAME[$base]}; dest="$(dirname "$tree")/$name"
    mapfile -t entries < <(printf '%s' "${SUBS[$base]}" | LC_ALL=C sort -u)
    : > "$lock.$name"
    nested "$dest" "$lock.$name" "${entries[@]}"
    {
      echo "  - name: $name"
      echo "    required_by: $parent"
      echo "    source: $base"
      echo "    version: ${PICKED[$base]}"
      echo "    repository: ${REPOS[$base]}"
      echo "    commit: ${COMMITS[$base]}"
      echo "    checksum: sha256:$(tree_checksum "$dest")"
      cat "$lock.$name"
    } >> "$lock"
    rm -f "$lock.$name"
    echo "  required by $parent: $base ${PICKED[$base]} (${COMMITS[$base]:0:12}) as $name"
  done < <(printf '%s\n' "${!NAME[@]}" | LC_ALL=C sort)
}

# fetch SOURCE VERSION STAGE DIR: one module of DIR's vendor.yaml into
# STAGE/<repository-name>, with the modules it requires next to it, their
# lock entries appended to STAGE/vendor.lock.
fetch() {
  local source=$1 version=$2 stage=$3 dir=$4 REPO COMMIT repo commit name src file call entries=()
  resolve "$source" "$version"
  repo=$REPO; commit=$COMMIT; name="$(basename "$repo")"

  src="$stage/$name"
  [ ! -e "$src" ] || die "vendor.yaml lists $name twice"
  fetch_tree "$repo" "$commit" "$src"

  # What the module uses of it: the root, a submodule, or several. Before
  # the module's calls are written, the root.
  while IFS=$'\t' read -r file call _; do
    case "$call" in
      "./vendors/$name") entries+=(.) ;;
      "./vendors/$name/"*) entries+=("${call#"./vendors/$name/"}") ;;
    esac
  done < <(module_calls "$dir"/*.tf)
  [ ${#entries[@]} -gt 0 ] || { entries=(.); echo "NOTE: no module call in $dir uses ./vendors/$name yet: its own module calls were followed from its root. Run this again after writing a call to one of its submodules." >&2; }
  : > "$stage/.nested.lock"
  nested "$src" "$stage/.nested.lock" "${entries[@]}"

  {
    echo "  - name: $name"
    echo "    source: $source"
    echo "    version: $version"
    echo "    repository: $repo"
    echo "    commit: $commit"
    echo "    checksum: sha256:$(tree_checksum "$src")"
    cat "$stage/.nested.lock"
  } >> "$stage/vendor.lock"
  rm -f "$stage/.nested.lock"
  echo "Vendored $source $version (${commit:0:12}) as $name: $(find "$src" -type f | wc -l) files, $(du -sk "$src" | cut -f1)K"
}

update() {
  local dir=${1%/} vendors stage source version d
  vendors="$dir/vendors"
  [ -f "$dir/main.tf" ] || die "$dir is not a module directory"
  [ -f "$dir/vendor.yaml" ] || die "$dir/vendor.yaml does not exist: list the modules to vendor there"
  [ -n "$(wanted "$dir/vendor.yaml")" ] || die "$dir/vendor.yaml lists no module (source and version)"
  mkdir -p "$vendors"

  # Everything is fetched next to the target first: a failed download leaves
  # the vendored trees and the lock as they were.
  stage=$(mktemp -d "$vendors/.stage.XXXXXX"); STAGE=$stage
  {
    echo "# Written by ./test.sh vendor from vendor.yaml: what each module resolved to."
    echo "# Never edit this file or the directories under vendors/ by hand."
    echo "# An entry with required_by is a dependency the script discovered by itself:"
    echo "# a module that the named vendored module calls. It is not in vendor.yaml."
    echo "modules:"
  } > "$stage/vendor.lock"
  while read -r source version; do fetch "$source" "$version" "$stage" "$dir"; done < <(wanted "$dir/vendor.yaml")

  for d in "$vendors"/*/; do [ ! -d "$d" ] || rm -rf "$d"; done
  mv "$stage"/*/ "$vendors/"
  mv "$stage/vendor.lock" "$dir/vendor.lock"
  rm -rf "$stage"
}

# A failed update exits from inside a function; its stage must not stay behind.
STAGE=""; trap '[ -z "$STAGE" ] || rm -rf "$STAGE"' EXIT
if [ "$CHECK" = true ]; then check "${DIRS[@]}"; exit $?; fi
[ ${#DIRS[@]} -gt 0 ] || die "usage: vendor.sh [--root DIR] <module-dir>..., or vendor.sh --check [<module-dir>...] (see --help)"
for dir in "${DIRS[@]}"; do update "$dir"; done
