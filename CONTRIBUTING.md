# Contributing

Fleet is licensed under the [MIT License](LICENSE).

Use a feature branch and keep one behavior change per review. Tests use the fictional config in `examples/config`; keep it fictional, and keep a real fleet config in its own repository.

Before sending a change:

1. Add behavior-focused tests for changes to command execution, synchronization, backups or privacy boundaries.
2. Stage only the intended reusable files and review `git diff --cached`.
3. Run `make check`. This checks the staged blobs for private material as well as the working source's formatting, lint and tests.
4. For SSH, push, adopt or restore behavior, also run `make e2e` with Docker available. The test creates temporary keys and a throwaway container, and removes the container afterward. It can leave a reusable test image in Docker.
5. Explain the triggering case, resulting behavior, test results and any limits in the pull request.

Never add real inventory, generated node guides, access-policy details, credentials, logs, sessions, deployment bundles or the old private history. The privacy checker is heuristic; inspect fixtures and documentation as carefully as executable code. Use `.invalid` hostnames and documentation IP addresses for examples.

CI uses read-only repository access, pinned action revisions, Linux/macOS checks, and Docker integration tests. It neither deploys Fleet nor publishes releases. Changes to dependencies must preserve applicable third-party notices; update the provenance inventory when versions change.
