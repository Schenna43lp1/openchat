#!/usr/bin/env bash
# Checks that an image keeps accounts in a fresh volume and refuses unwritable storage.
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
  docker run --detach --name "$name" --publish 127.0.0.1::8080 --volume "$volume:/app/data"     --env "OPENCHAT_USERS_FILE=$users_file" "$image" >/dev/null
  local address
  address="$(docker port "$name" 8080/tcp)" || fail "container exited during startup"
  base="http://$address"
  for _ in $(seq 1 60); do
    if curl --silent --fail --output /dev/null "$base/login"; then return; fi
    sleep 0.5
  done
  fail "server did not start"
}

sign_in() {
  curl --silent --output /dev/null --write-out '%{http_code}'     --data "mode=$1&username=smoke&password=$password" "$base/login"
}

for users_file in data/users.json data/users.sqlite; do
  cleanup
  start
  [ "$(sign_in register)" = 303 ] || fail "account was not created ($users_file)"
  docker rm --force "$name" >/dev/null

  start
  [ "$(sign_in login)" = 303 ] || fail "account did not survive a new container ($users_file)"
done

cleanup
status=0
MSYS_NO_PATHCONV=1 timeout 20 docker run --rm --name "$name" --tmpfs /app/data:mode=0555 "$image" >/dev/null 2>&1 || status=$?
case "$status" in
  0 | 124) fail "server accepted unwritable storage" ;;
  12[5-7]) fail "container could not be run (exit $status)" ;;
esac

echo "container smoke test passed"
