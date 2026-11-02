#!/bin/sh
# Bring an agent container up: apply the shared layer, optionally join the
# tailnet, then stay up for exec and SSH sessions.
set -eu

# Inside a container the shared layer always wins (--force, with backups in the
# state volume): agent homes are volumes that outlive images, so files changed
# in an older image or by the agent must not block start-up. A failed apply is
# reported in the container log but never keeps the agent from starting.
if ! fleet apply --force; then
  echo "fleet apply failed; the agent starts with the layer it already has" >&2
fi

key=/run/secrets/ts-authkey
if [ -f "$key" ]; then
  state=/tmp/tailscale
  mkdir -p "$state"
  # tailscaled never sees the key, and `tailscale up` reads it from the secret
  # file, so it is neither in a process environment nor in any argv.
  env -u TS_AUTHKEY tailscaled --tun=userspace-networking --statedir="$state" \
    --socket="$state/tailscaled.sock" --socks5-server=localhost:1055 >"$state/tailscaled.log" 2>&1 &
  tailscale --socket="$state/tailscaled.sock" up --auth-key="file:$key" \
    --hostname="agent-$FLEET_AGENT" --advertise-tags=tag:agent --ssh
  echo "joined the tailnet as agent-$FLEET_AGENT"
fi

echo "agent $FLEET_AGENT ready"
exec sleep infinity
