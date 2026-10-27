#!/bin/sh
# Runs an isolated, portless proxy/backend pair using the production image.
set -eu
image=${1:-reverse-proxy-demo:local}
name="reverse-proxy-smoke-$$"
work=$(mktemp -d)
cleanup() {
  docker rm -f "$name" "$name-backend" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM
cat > "$work/config.yaml" <<YAML
port: 8080
management:
  address: "127.0.0.1:9091"
health_check: {path: /health, interval: 100ms, timeout: 1s}
routes:
  - path: /api
    strip_prefix: /api
    timeout: 2s
    backends: [{url: "http://$name-backend:9001"}]
YAML
chmod 755 "$work"
chmod 644 "$work/config.yaml"
docker network create "$name" >/dev/null
docker run -d --name "$name-backend" --network "$name" hashicorp/http-echo:1.0.0 -listen=:9001 -text=runtime-ok >/dev/null
docker run --rm --network "$name" -v "$work/config.yaml:/app/config.yaml:ro" "$image" --check-config
docker run -d --name "$name" --network "$name" -v "$work/config.yaml:/app/config.yaml:ro" "$image" >/dev/null
fetch() { docker exec "$name" wget -q -T 2 -O - "http://127.0.0.1:$1$2"; }
wait_status() {
  expected=$1
  i=0
  while [ "$i" -lt 50 ]; do
    if fetch 9091 /readyz >/dev/null 2>&1; then actual=ready; else actual=unready; fi
    if [ "$actual" = "$expected" ]; then return; fi
    i=$((i+1)); sleep 0.2
  done
  echo "readiness did not become $expected" >&2; docker logs "$name"; exit 1
}
wait_status ready
fetch 9091 /livez | grep -q alive
fetch 8080 /api/test | grep -q runtime-ok
fetch 9091 /metrics | grep -q 'backend_attempts_total.*outcome="success".* 1'
# The application listener must not expose the proxy's management endpoints.
if fetch 8080 /metrics >/dev/null 2>&1; then echo 'metrics exposed on application listener' >&2; exit 1; fi
# Another container cannot reach a loopback-bound management listener.
if docker run --rm --network "$name" --entrypoint wget "$image" -q -T 2 -O /dev/null "http://$name:9091/livez" 2>/dev/null; then
  echo 'loopback management listener accessible from another container' >&2; exit 1
fi
# Verify the production process runs as the image's unprivileged user.
test "$(docker exec "$name" id -u)" != 0
docker stop -t 2 "$name-backend" >/dev/null
wait_status unready
fetch 9091 /livez | grep -q alive
docker start "$name-backend" >/dev/null
wait_status ready
docker logs "$name" 2>/dev/null | grep -q '"msg":"request_completed"'
docker stop -t 12 "$name" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$name")" = 0
echo 'Container smoke passed: startup, forwarding, metrics, isolation, non-root, readiness recovery, liveness, logs, SIGTERM.'
