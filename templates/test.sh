#!/usr/bin/env bash
# Entry point of this repository's module tests. Copy of
# entigo-infralib-test/templates/test.sh: the real orchestrator ships inside
# the entigolabs/entigo-infralib-test-<cloud> images and is extracted from the
# pinned version on first use into .infralib-test/ (git-ignored). The only
# line to edit here is the version.
#
#   ./test.sh                          provision and test everything you have credentials for
#   ./test.sh modules/aws/hello-world  test one module in a step of its own
#   ./test.sh --help                   every command and option
set -euo pipefail

INFRALIB_TEST_VERSION="${INFRALIB_TEST_VERSION:-latest}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PREFIX="${INFRALIB_TEST_IMAGE_PREFIX:-entigolabs/entigo-infralib-test-}"
# Any cloud image carries the scripts; take the first cloud the repository
# uses, from the environment file names (environments/<cloud>_<prefix>.yaml).
CLOUD=""
for f in "$ROOT"/environments/*.yaml; do
  b=$(basename "$f" .yaml)
  case $b in aws_*|google_*|oracle_*) CLOUD=${b%%_*}; break ;; esac
done
[ -n "$CLOUD" ] || { echo "environments/ holds no <cloud>_<prefix>.yaml (cloud aws|google|oracle)" >&2; exit 1; }
IMAGE="${PREFIX}${CLOUD}:${INFRALIB_TEST_VERSION}"
CACHE="$ROOT/.infralib-test/scripts-${INFRALIB_TEST_VERSION}"

# Mutable tags are refreshed every run, pinned versions only once.
case "$INFRALIB_TEST_VERSION" in latest|dev) rm -rf "$CACHE" ;; esac
if [ ! -x "$CACHE/infralib-test.sh" ]; then
  echo "Extracting the test orchestrator from $IMAGE" >&2
  # A locally built image (no registry copy) is fine too.
  docker pull -q "$IMAGE" >/dev/null 2>&1 || docker image inspect "$IMAGE" >/dev/null 2>&1 || { echo "cannot pull $IMAGE" >&2; exit 1; }
  id=$(docker create "$IMAGE")
  trap 'docker rm -f "$id" >/dev/null 2>&1 || true' EXIT
  mkdir -p "$ROOT/.infralib-test"
  rm -rf "$CACHE.tmp"
  docker cp "$id:/opt/infralib-test/scripts" "$CACHE.tmp"
  docker rm "$id" >/dev/null
  trap - EXIT
  mv "$CACHE.tmp" "$CACHE"
fi

export INFRALIB_TEST_VERSION
exec "$CACHE/infralib-test.sh" "$@"
