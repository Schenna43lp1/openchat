#!/usr/bin/env bash
# Checks that an image keeps accounts and sessions in a fresh volume, stops cleanly and refuses unwritable storage.
set -euo pipefail

# Git Bash rewrites container paths, but curl still needs that for /dev/null.
docker() { MSYS_NO_PATHCONV=1 command docker "$@"; }

image="${1:?usage: container-smoke.sh IMAGE}"
name="openchat-smoke-$$"
volume="$name-data"
password="smoke-test-password"

cleanup() {
  docker rm --force "$name" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  docker logs "$name" >&2 2>/dev/null || true
  exit 1
}

start() {
  docker run --detach --name "$name" --publish 127.0.0.1::8080 --volume "$volume:/app/data" "$@" "$image" >/dev/null
  local address
  address="$(docker port "$name" 8080/tcp)" || fail "container exited during startup"
  base="http://$address"
  for _ in $(seq 1 60); do
    if curl --silent --fail "$base/healthz" >/dev/null; then return; fi
    sleep 0.5
  done
  fail "server did not become healthy"
}

# sign_in prints the response headers.
sign_in() {
  curl --silent --output /dev/null --dump-header - \
    --data "mode=$1&username=smoke&password=$password" "$base/login"
}

status_of() {
  awk 'NR == 1 { print $2 }'
}

start --env OPENCHAT_ADMIN_USERNAME=smoke --env "OPENCHAT_ADMIN_PASSWORD=$password"
[ "$(sign_in register | status_of)" = 403 ] || fail "registration is open by default"
headers="$(sign_in login)"
[ "$(status_of <<<"$headers")" = 303 ] || fail "admin was not created"
cookie="$(tr -d '\r' <<<"$headers" | sed -n 's/^[Ss]et-[Cc]ookie: \([^;]*\).*/\1/p')"
[ -n "$cookie" ] || fail "login set no session cookie"

docker stop "$name" >/dev/null
[ "$(docker inspect --format '{{.State.ExitCode}}' "$name")" = 0 ] || fail "shutdown was not clean"
docker rm "$name" >/dev/null

start
[ "$(sign_in login | status_of)" = 303 ] || fail "account did not survive a new container"
code="$(curl --silent --output /dev/null --write-out '%{http_code}' --header "Cookie: $cookie" "$base/session")"
[ "$code" = 204 ] || fail "session did not survive a new container"

cleanup
status=0
MSYS_NO_PATHCONV=1 timeout 20 docker run --rm --name "$name" --tmpfs /app/data:mode=0555 "$image" >/dev/null 2>&1 || status=$?
case "$status" in
  0 | 124) fail "server accepted unwritable storage" ;;
  12[5-7]) fail "container could not be run (exit $status)" ;;
esac

echo "container smoke test passed"
