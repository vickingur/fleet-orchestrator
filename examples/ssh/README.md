# Ordinary SSH example

This configuration demonstrates a controller and an SSH worker without Tailscale. The `.invalid` hosts and identity path are placeholders; they are not working endpoints. Use a separate private deployment checkout, copy this example to its `fleet.toml`, and replace the host, port, user, OS and identity fields for your machines. The example checks for `fish`; replace the tool section with commands appropriate to your worker.

From that private checkout, after `make build`:

```sh
export FLEET_SELF=controller
./dist/fleet docs
./dist/fleet exec worker -- 'uname -sm'
./dist/fleet doctor worker
./dist/fleet drift worker
```

`FLEET_SELF=controller` means this process runs on the node named `controller`; it does not select the remote target. Commands targeted at `controller` execute locally. Run on the worker with `FLEET_SELF=worker`, not with the controller's value. `doctor` can report missing or outdated Fleet before the first deployment, and `drift` exits 4 while changes are pending. Review both before choosing to run `fleet push worker`.

The Docker integration test loads this exact example file, substitutes a loopback SSH address, ephemeral port and temporary key, and sets `FLEET_SELF=controller`. A failing Tailscale shim records any discovery attempt; the test proves ordinary execution, drift, deployment, health checking and adoption did not invoke it. The disposable SSH node has no Tailscale executable. The test invokes OpenSSH with `-F /dev/null` and a temporary identity so developer SSH configuration does not affect it. A separate `ls` assertion verifies that unavailable Tailscale produces unknown status without failing the inventory listing.

Run the complete disposable demonstration with `make e2e` and a running Docker daemon. It does not use your SSH keys, inventory, Tailscale account or live machines. The terminal UI and bundled agent-container proxy still have the limitations described in [configuration](../../docs/CONFIGURATION.md#transport-support-and-limits).
