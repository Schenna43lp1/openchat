#!/usr/bin/env bash
# Checks that compose.yaml starts healthy with an image, keeps the data and runs the operator command.
# It uses its own project, volume and port and never reads .env.
set -euo pipefail

export OPENCHAT_IMAGE="${1:?usage: compose-smoke.sh IMAGE}"
export OPENCHAT_VOLUME="openchat-compose-smoke-$$-data"
export OPENCHAT_BIND=127.0.0.1
export OPENCHAT_HOST_PORT=0
export OPENCHAT_ADMIN_USERNAME=smoke
export OPENCHAT_ADMIN_PASSWORD=smoke-test-password
export COMPOSE_PROGRESS=quiet

# Relative, because Git Bash would rewrite absolute paths and the arguments for the container.
cd "$(dirname "$0")/.."
compose() {
  MSYS_NO_PATHCONV=1 docker compose --project-name "openchat-compose-smoke-$$" --env-file scripts/compose-smoke.env \
    --file compose.yaml "$@"
}

cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  compose ps --all >&2 || true
  compose logs >&2 || true
  exit 1
}

start() {
  compose up --detach --wait --wait-timeout 60 --pull never >/dev/null || fail "did not become healthy"
  base="http://$(compose port openchat 8080)"
}

sign_in() {
  curl --silent --output /dev/null --write-out '%{http_code}' \
    --data "username=smoke&password=$1" "$base/login"
}

start
case "$base" in
  http://127.0.0.1:*) ;;
  *) fail "not bound to localhost: $base" ;;
esac
[ "$(sign_in "$OPENCHAT_ADMIN_PASSWORD")" = 303 ] || fail "admin was not created"

compose down >/dev/null
start
[ "$(sign_in "$OPENCHAT_ADMIN_PASSWORD")" = 303 ] || fail "account did not survive a new container"

compose stop >/dev/null
issued="$(compose run --rm --no-deps -T openchat reset-password smoke 2>/dev/null)" || fail "operator reset failed"
[[ "$issued" =~ ^[A-Z2-7]{26}$ ]] || fail "reset printed more or less than the password"
start
[ "$(sign_in "$issued")" = 303 ] || fail "issued password does not work"
if compose logs 2>&1 | grep --quiet --fixed-strings "$issued"; then
  fail "issued password is in the log"
fi

echo "compose smoke test passed"
