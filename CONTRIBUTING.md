# Contributing

Public contribution terms are pending selection of a project license. Until that decision, this document describes the private development workflow and grants no additional rights.

Use a feature branch and keep one behavior change per review. Start from a clean checkout and run `make example`. Keep the development inventory fictional; use another checkout for deployment configuration.

Before sending a change:

1. Add behavior-focused tests for changes to command execution, synchronization, backups or privacy boundaries.
2. Stage only the intended reusable files and review `git diff --cached`.
3. Run `make check`. This checks the staged blobs for private material as well as the working source's formatting, lint and tests.
4. For SSH, push, adopt or restore behavior, also run `make e2e` with Docker available. The test creates temporary keys and a throwaway container, and removes the container afterward. It can leave a reusable test image in Docker.
5. Explain the triggering case, resulting behavior, test results and any limits in the pull request.

Never add real inventory, generated node guides, access-policy details, credentials, logs, sessions, deployment bundles or the old private history. The privacy checker is heuristic; inspect fixtures and documentation as carefully as executable code. Use `.invalid` hostnames and documentation IP addresses for examples.

CI uses read-only repository access, pinned action revisions, Linux/macOS checks, and Docker integration tests. It neither deploys Fleet nor publishes releases. Changes to dependencies must preserve applicable third-party notices; update the provenance inventory when versions change.
