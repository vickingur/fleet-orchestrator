# Provenance and licensing

This repository begins with a sanitized source snapshot of an existing private Fleet implementation. Original deployment history, commit metadata, personal configuration, operational notes and runtime data were excluded. The new history contains only the reusable tree and release preparation.

Fleet is licensed under the [MIT License](../LICENSE), copyright (c) 2026 Victor Poputnikov. Third-party components retain their own licenses and notices.

## Retained and excluded material

- Retained: Go CLI and packages, behavior tests, container recipes, generic example policy, build configuration and newly written documentation/examples.
- Replaced: personal module namespace, machine names, usernames, addresses, tailnet strings and deployment-oriented test fixtures.
- Excluded: personal settings and instructions, private skill collections, plugin setup, operations notes, monitoring/usage helpers, original Git history and live state.
- Removed during release preparation: an unused Ghostty terminfo definition whose source snapshot did not include its accompanying license/provenance notice. No third-party licensing assumption was made for that file.

## Dependencies and tools

[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) reproduces the top-level license/notice files found in the cached distributions of all 21 modules declared in `go.mod`, at the recorded versions. The original dependency notices and their copyright statements are preserved. They do not license Fleet itself.

This is a source-preparation inventory, not a complete binary/container distribution inventory. Nested notices, generated Unicode/terminal data, further transitive modules, the Go toolchain, base-image packages and downloaded agent tools require a distribution-specific review before releasing compiled artifacts or container images. No dependency source has been vendored.

The container recipe uses a Debian base image plus upstream installers and agent binaries. Keep their independent terms and notices in scope if images are distributed. CI actions are referenced by pinned upstream commits; they are not copied into this repository.
