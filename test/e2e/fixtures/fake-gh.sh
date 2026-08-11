#!/bin/sh
set -eu

if [ "$#" -lt 2 ] || [ "$1" != "pr" ] || [ "$2" != "list" ]; then
  echo "Unsupported gh command: $*" >&2
  exit 2
fi

printf '%s\n' '[{"url":"https://github.com/kelos-dev/kelos/pull/42","state":"OPEN","isDraft":false,"headRepositoryOwner":{"login":"kelos-dev"},"statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""},{"__typename":"StatusContext","state":"SUCCESS"}]}]'
