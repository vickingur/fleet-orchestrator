# Fleet config

The configuration [fleet](https://github.com/vickingur/fleet-orchestrator) deploys: the inventory (`fleet.toml`), the node guide generated from it (`FLEET.md`), and the shared layer every node gets (`home/`, mapped by `home/manifest.toml`).

**Keep this repository private.** It names your machines, accounts and SSH identities, and your tailnet policy once you add one, and its shared layer is what your agents run with. Push it only to a private remote. Never copy it into the fleet tool's repository or a fork of it, and never paste it into a public issue or pull request.

```sh
fleet docs        # after editing fleet.toml
fleet check       # before every push
fleet drift all   # what a push would change
fleet push all
```
