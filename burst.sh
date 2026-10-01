#!/usr/bin/env sh
# One-command on-sale stampede against a running deployment.
#   ./burst.sh <BASE_URL> [ADMIN_TOKEN] [extra flags, e.g. -hot-users 2000 -spread 10000]
set -eu
BASE_URL="${1:?usage: ./burst.sh <BASE_URL> [ADMIN_TOKEN] [extra burst flags]}"
shift 1
# the second argument is the admin token unless it is already a flag
if [ $# -ge 1 ] && [ "${1#-}" = "$1" ]; then ADMIN_TOKEN="$1"; shift 1; fi
: "${ADMIN_TOKEN:?pass ADMIN_TOKEN as the second argument or set it in the environment}"
cd "$(dirname "$0")"
exec go run ./cmd/burst -base-url "$BASE_URL" -admin-token "$ADMIN_TOKEN" "$@"
