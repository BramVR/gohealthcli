---
title: Release
summary: "GitHub Release artifacts, Homebrew tap handoff, and release verification."
read_when:
  - "Cutting a gohealthcli release."
  - "Debugging GoReleaser artifacts or Homebrew tap updates."
---

# Release

Releases are tag-driven. The release workflow builds GitHub Release archives
with GoReleaser, then dispatches the Homebrew tap updater when the tap token is
configured.

## Cut a Release

```bash
git tag -a vX.Y.Z -m "Release X.Y.Z"
git push origin main vX.Y.Z
```

To re-run a release for an existing tag, start the `release` workflow manually
and pass `tag: vX.Y.Z`.

## Release Artifacts

Expected archive names:

- `gohealthcli_<version>_darwin_amd64.tar.gz`
- `gohealthcli_<version>_darwin_arm64.tar.gz`
- `gohealthcli_<version>_linux_amd64.tar.gz`
- `gohealthcli_<version>_linux_arm64.tar.gz`
- `checksums.txt`

GoReleaser stamps the binary with:

- `main.version={{ .Version }}`
- `main.commit={{ .Commit }}`
- `main.built={{ .Date }}`

## Homebrew Tap

The release workflow dispatches `update-formula.yml` in
`BramVR/homebrew-tap`. That tap workflow owns the formula-editing logic and
updates the target-specific archive URLs and SHA256 values in
`Formula/gohealthcli.rb`.

Repository secret:

- `HOMEBREW_TAP_TOKEN`: token allowed to run workflows in `BramVR/homebrew-tap`

If the secret is missing, GitHub Release artifacts are still published and the
tap update is skipped with a warning.

## Verify

```bash
gh release view vX.Y.Z --repo BramVR/gohealthcli
brew update
brew install BramVR/tap/gohealthcli
gohealthcli --version --json
brew test BramVR/tap/gohealthcli
```
