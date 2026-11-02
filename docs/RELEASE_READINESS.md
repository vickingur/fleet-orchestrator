# Public-release readiness

This repository remains private. This checklist records the preparation boundary; it is not authorization to change visibility, publish packages or create a release.

## Prepared

- [x] Reusable implementation separated from the live deployment.
- [x] Fictional inventory, examples and policy fixtures replace personal values.
- [x] Original private Git history excluded; new history starts from the sanitized tree.
- [x] README, local build, deployment configuration, contribution and security guidance provided.
- [x] Ordinary SSH setup documented separately from Tailscale discovery/UI/container integration, with a shared example used by the integration test.
- [x] Git index privacy checks reject tracked deployment files and scan staged blobs rather than trusting working-tree contents.
- [x] Local race tests, formatting, vet, lint and generated-doc checks are defined in `make check`.
- [x] CI is configured for Linux, macOS and Docker SSH integration with read-only repository access and pinned action revisions.
- [x] Top-level dependency notices inventoried; the unverified terminal-definition copy removed.
- [x] Approved MIT project license added.

## Required before changing visibility

- [ ] Owner confirms rights/provenance of the retained project source.
- [ ] Verify all needed source/data notices are present.
- [ ] Select and verify a private security-reporting route and decide the support policy.
- [ ] Inspect the exact proposed public commit and all reachable new history for private data, including commit metadata, workflow logs and artifacts.
- [ ] Confirm all CI jobs passed for that exact commit. Local success or a check on an earlier commit is insufficient.
- [ ] Owner explicitly approves making this repository public.

## Before distributing binaries or images

- [ ] Choose a release version, supported platforms and maintenance expectations.
- [ ] Complete a transitive dependency, data-file, toolchain and container-package licensing review for the artifacts actually distributed.
- [ ] Pin and verify the container build inputs, or explicitly document the remaining mutable inputs and supported scope.
- [ ] Build artifacts from the reviewed source, preserve required notices and provide checksums. Do not attach Fleet deployment bundles: they contain configuration and a controller path.
- [ ] Validate installation and rollback on disposable machines without access to a live fleet.

Record verification against the proposed release commit in its review or release notes. Do not mark CI or ownership decisions complete merely because this checklist exists.
