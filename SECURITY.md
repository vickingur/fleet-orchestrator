# Security

Fleet executes trusted commands and synchronizes configuration under the user's account. Inventory tool commands, manifest checks/fixes, SSH proxy configuration and managed instructions are code or operational input. Do not run a checkout or bundle you do not trust.

## Boundaries

`FLEET_SELF` identifies the machine running Fleet. Commands targeting that inventory name execute locally. Verify it before operating; do not set it to a remote destination merely to avoid discovery. DNS/IPv4 SSH access does not require Tailscale, but the discovery UI and default container proxy retain Tailscale-specific behavior.

- SSH uses a Fleet-owned known-hosts file with first-use acceptance (`accept-new`). Changed keys are rejected, but first contact still requires trust in the network and target identity.
- Ordinary SSH operations disable agent/X11 forwarding and clear configured port forwards. The Codex attachment command intentionally opens a dedicated local tunnel and uses a token handshake.
- Agent containers use a restricted runtime configuration. For containers joined to Tailscale, tailnet reachability also depends on the deployed policy. Containers spawned without tailnet enrollment still have their ordinary container networking. The example policy is not automatically installed and is not evidence of the state of any live network.
- Drift can run configured ensure checks. A dry-run label does not make arbitrary manifest commands safe.
- Generated deployment bundles include inventory and the controller source path. Backups, registry, credentials and audit logs are private data.
- Container recipes fetch upstream installers and a latest tool release. A reproducible, version-pinned container supply chain remains release work.

## Reporting

Before a public release, the maintainer must choose and verify a private security-reporting channel. No dedicated reporting endpoint or response SLA is established by this repository. While the repository is private, use an existing private channel to its maintainer. Do not disclose credentials or exploit details in public issues or pull requests.

If a credential is accidentally committed, treat deletion of the latest file as insufficient: it may remain in Git history, logs or caches. Contact the maintainer privately so they can coordinate credential replacement and cleanup. The source privacy scanner is a useful gate, not a comprehensive secret detector or security audit.

No stable release/support window has been declared yet. CI runs isolated tests and does not access a live fleet.
