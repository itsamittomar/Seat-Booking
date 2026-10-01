#!/usr/bin/env sh
# One-command on-sale stampede against a running deployment.
#   ./burst.sh <BASE_URL> [ADMIN_TOKEN] [extra flags, e.g. -hot-users 2000 -spread 10000]
set -eu
BASE_URL="${1:?usage: ./burst.sh <BASE_URL> [ADMIN_TOKEN] [extra burst flags]}"
if [ $# -ge 2 ]; then ADMIN_TOKEN="$2"; shift 2; else shift 1; fi
: "${ADMIN_TOKEN:?pass ADMIN_TOKEN as the second argument or set it in the environment}"
cd "$(dirname "$0")"
exec go run ./cmd/burst -base-url "$BASE_URL" -admin-token "$ADMIN_TOKEN" "$@"
