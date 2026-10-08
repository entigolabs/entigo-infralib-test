#!/usr/bin/env bash
# Shrinks the Go caches of a test image after they have been warmed. Run as
# root at image build time from the framework source directory, with the
# package patterns the image warmed as arguments:
#
#   prune-go-caches.sh ./env/... ./k8s/... ./aws/...
#
# What goes:
# - tests and test fixtures of the toolchain and of every cached module:
#   nothing builds them, and `go mod tidy` does not load tests of
#   dependencies, so go.sum comes out the same (verified);
# - the downloaded module zips, except those of pruned modules (below);
# - in modules where it saves enough, the packages that none of the warmed
#   packages or their tests import. The module keeps its zip, and the pruned
#   modules are listed in $PRUNED_LIST: when a module test imports one of
#   the missing packages, `infralib-test run` deletes the module directory
#   and Go extracts it again from the zip, without network.
set -euo pipefail

PRUNED_LIST=${PRUNED_LIST:-/opt/infralib-test/pruned-modules}
# A module is pruned when its unused packages hold at least this many files
# or bytes; smaller gains are not worth a re-extraction later.
MIN_FILES=${PRUNE_MIN_FILES:-200}
MIN_BYTES=${PRUNE_MIN_BYTES:-$((5 * 1024 * 1024))}

[ $# -gt 0 ] || { echo "usage: prune-go-caches.sh <package patterns the image warmed>" >&2; exit 1; }
modcache=$(go env GOMODCACHE)
goroot=$(go env GOROOT)

size_of() { du -sb "$1" | cut -f1; }
count_files() { find "$1" -type f | wc -l; }
report() { printf '%-28s %6d MiB %7d files\n' "$1" "$(( $(size_of "$2") / 1048576 ))" "$(count_files "$2")" >&2; }

report "before: GOROOT" "$goroot"
report "before: GOMODCACHE" "$modcache"

# 1. Tests and fixtures of the toolchain and the modules.
find "$goroot/src" "$modcache" -path "$modcache/cache" -prune -o -type f -name '*_test.go' -exec rm -f {} +
find "$goroot/src" "$modcache" -path "$modcache/cache" -prune -o -type d -name testdata -prune -exec rm -rf {} +

# 2. Unused packages of the modules worth it. The closure is what the warmed
# packages and their tests import; a module's directories that hold Go files
# and are not in it are packages nobody compiles.
closure=$(go list -deps -test "$@" | sort -u)
: > "$PRUNED_LIST"
while read -r mod dir; do
  [ -n "$dir" ] && [ -d "$dir" ] || continue
  unused=$(find "$dir" -type f -name '*.go' | xargs -r -n1 dirname | sort -u | while read -r pkgdir; do
    rel=${pkgdir#"$dir"}; rel=${rel#/}
    pkg=$mod${rel:+/$rel}
    grep -qx -- "$pkg" <<<"$closure" || echo "$pkgdir"
  done)
  [ -n "$unused" ] || continue
  files=0; bytes=0
  while read -r pkgdir; do
    files=$((files + $(find "$pkgdir" -maxdepth 1 -type f | wc -l)))
    bytes=$((bytes + $(find "$pkgdir" -maxdepth 1 -type f -exec stat -c %s {} + | awk '{s+=$1} END {print s+0}')))
  done <<<"$unused"
  if [ "$files" -ge "$MIN_FILES" ] || [ "$bytes" -ge "$MIN_BYTES" ]; then
    # Only the files of the package itself: a subdirectory may be a package
    # that is used, or assets a used package embeds.
    while read -r pkgdir; do find "$pkgdir" -maxdepth 1 -type f -exec rm -f {} +; done <<<"$unused"
    find "$dir" -depth -type d -empty -delete
    mkdir -p "$dir"
    printf '%s %s\n' "$mod" "$dir" >> "$PRUNED_LIST"
    printf 'pruned %-50s %5d MiB %6d files\n' "$mod" "$((bytes / 1048576))" "$files" >&2
  fi
done < <(go list -m -f '{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}' all 2>/dev/null)

# 3. Module zips, except for the pruned modules that may need re-extraction.
find "$modcache/cache/download" -type f -name '*.zip' | while read -r zip; do
  # cache/download/<module path>/@v/<version>.zip -> <module path>@<version>
  rel=${zip#"$modcache/cache/download/"}
  mod=${rel%%/@v/*}; ver=$(basename "$zip" .zip)
  grep -q "^$mod@$ver " "$PRUNED_LIST" || rm -f "$zip"
done

report "after: GOROOT" "$goroot"
report "after: GOMODCACHE" "$modcache"
