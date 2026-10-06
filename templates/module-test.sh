#!/usr/bin/env bash
# Test this module. Copy of entigo-infralib-test/templates/module-test.sh,
# kept in every module directory so a module can be tested from its own
# directory. It hands over to the repository's test.sh with this module as
# the module to test, so every option of test.sh applies:
#
#   ./test.sh                      provision this module in a step of its own on
#                                  every environment it has an input for, then test it
#   ./test.sh --env aws_pri        one environment only
#   ./test.sh test                 only the tests, the module is already provisioned
#   ./test.sh --destroy            tear the per-branch step down afterwards (CI default)
#   ./test.sh destroy              only tear it down
#   ./test.sh --help               everything else
set -euo pipefail

MODULE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$MODULE"
while [ ! -f "$ROOT/environments.yaml" ]; do
  [ "$ROOT" = / ] && { echo "no environments.yaml found above $MODULE" >&2; exit 1; }
  ROOT="$(dirname "$ROOT")"
done
exec "$ROOT/test.sh" "$@" "${MODULE#"$ROOT"/}"
