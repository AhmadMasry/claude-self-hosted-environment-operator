# Releasing

Releases are cut by pushing a `vX.Y.Z` tag on `master`. The `release` workflow builds the multi-arch image, signs it, attaches an SBOM attestation, packages the Helm chart and publishes a GitHub Release.

A run on a `vX.Y.Z` tag ref publishes: a tag push, or a manual dispatch with the tag picked as the ref (the `dry_run` input is then ignored). A manual dispatch from a branch is always a dry run: the workflow refuses a branch dispatch with `dry_run=false` and fails before building anything. The version (from the tag, or the `version` input on a branch dispatch) must be a semantic version, `X.Y.Z` with an optional `-prerelease` suffix, or the workflow fails; `+build` metadata is refused because image tags cannot carry it.

- Image: `ghcr.io/ahmadmasry/claude-self-hosted-environment-operator`, tagged `vX.Y.Z`, plus the floating `vX.Y` tag for a plain `X.Y.Z` version only (a prerelease such as `v1.2.0-rc.1` never moves `v1.2`). There is no `latest` tag; consumers pin `vX.Y.Z` or `vX.Y`.
- Chart: `oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator`, version `X.Y.Z`. `helm install` without `--version` picks the newest stable version; prereleases need `--version` or `--devel`.
- Installer: `install.yaml` on the GitHub Release. `releases/latest/download/install.yaml` resolves to the newest release that is not a prerelease, which is what the README links.
- A prerelease version (`-rc.N` and the like) creates a GitHub Release flagged pre-release, so it never becomes "latest".
- Both GHCR packages (image and chart) are public: packages published from this public repository are public from their first publish.
- Signing: keyless cosign through the GitHub OIDC token; the SBOM (SPDX JSON, from syft) is attached as a cosign attestation and as a release asset.

## Maintainer checklist

1. [ ] Move the changelog's Unreleased section under the new version.
2. [ ] Run `real-e2e` at both levels (see [testing.md](testing.md)).
3. [ ] Run the dry-run release: Actions tab, `release`, Run workflow, pick the branch, set `version` (for example `0.0.0-dryrun`) and leave `dry_run` as `true`. It runs `make test`, builds the image without pushing, generates the SBOM, builds the installer and chart, and uploads `dist/install.yaml`, the chart `.tgz` and `sbom.spdx.json` as an artifact. It does not log in to GHCR, sign, push the chart or create a GitHub Release.
4. [ ] Tag `vX.Y.Z` on `master` with a signed annotated tag (`git tag -s`) and push it; the tag rulesets require the signature and forbid moving or deleting the tag afterwards. Cut a `-rc.N` first when the pipeline changed.
5. [ ] Verify the image signature:

   ```bash
   cosign verify \
     --certificate-identity-regexp 'https://github.com/AhmadMasry/claude-self-hosted-environment-operator/.*' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     ghcr.io/ahmadmasry/claude-self-hosted-environment-operator:vX.Y.Z
   ```

6. [ ] Verify the SBOM attestation with the same identity flags:

   ```bash
   cosign verify-attestation --type spdxjson \
     --certificate-identity-regexp 'https://github.com/AhmadMasry/claude-self-hosted-environment-operator/.*' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     ghcr.io/ahmadmasry/claude-self-hosted-environment-operator:vX.Y.Z
   ```

7. [ ] Verify the chart pulls: `helm pull oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator --version X.Y.Z`
8. [ ] Confirm the GitHub Release has `install.yaml`, the chart `.tgz` and `sbom.spdx.json` attached, is flagged pre-release only for a prerelease version, and that no `latest` image tag exists on the package.
