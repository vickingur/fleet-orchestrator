# Agent instructions

Fleet is a Go CLI that runs commands on machines over SSH, syncs a shared agent layer (instructions, settings, skills) to them, and manages agent containers. This repository is the tool only; what it deploys lives in a separate fleet config.

## Layout

- `cmd/fleet`, `internal/cli`: the commands. `internal/tui` is the terminal UI.
- `internal/syncer`: applies the shared layer on a node. `internal/bundle` packs what `push` ships. `internal/remote` and `internal/execx` run commands.
- `internal/configcheck` backs `fleet check`. `internal/starter` is what `fleet init` writes. `internal/docs` renders `FLEET.md`.
- `container/`: the agent container recipe.
- `examples/config`: the fictional fleet config the tests use. `examples/ssh`: the ordinary-SSH example the e2e test loads.

## Checks

- Run `make check` before every commit: gofmt, vet, golangci-lint, race tests and the privacy scan. The scan reads staged blobs, so stage first.
- `make e2e` needs a running Docker daemon; CI runs it on every push.
- After changing `internal/docs` or `examples/config/fleet.toml`, regenerate the example guide: `cd examples/config && go run ../../cmd/fleet docs`.

## Privacy

Treat this repository as public. A real fleet config (inventory, `FLEET.md`, `home/` layer, tailnet policy) lives in its owner's private repository, never here. Never put real host names, users, addresses, SSH identity paths, policy, or personal skills and settings into code, tests, docs, commit messages, issues or pull requests. Fixtures use `.invalid` host names and documentation addresses (`192.0.2.0/24`). Commit with a GitHub noreply address.

## Care

`fleet push` builds from whatever checkout it runs in, and on a controller that can be this one. Do not run `push`, `apply`, `spawn`, `rm` or `image` from here unless asked: they change real machines.

Work on a feature branch with Conventional Commits.
