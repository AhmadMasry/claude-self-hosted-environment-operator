# Releasing

Releases are cut by pushing a `vX.Y.Z` tag on `master`. The `release` workflow builds the multi-arch image, signs it, attaches an SBOM attestation, packages the Helm chart and publishes a GitHub Release.

Only a `vX.Y.Z` tag push publishes. A manual dispatch is always a dry run: the workflow refuses a dispatch with `dry_run=false` and fails before building anything. The version (from the tag, or the `version` input on a dispatch) must be a semantic version, `X.Y.Z` with an optional `-prerelease` or `+build` suffix, or the workflow fails.

- Image: `ghcr.io/ahmadmasry/claude-self-hosted-environment-operator`, tagged `vX.Y.Z`, plus the floating `vX.Y` tag for a plain `X.Y.Z` version only (a prerelease such as `v1.2.0-rc.1` never moves `v1.2`). There is no `latest` tag; consumers pin `vX.Y.Z` or `vX.Y`.
- Chart: `oci://ghcr.io/ahmadmasry/charts/claude-selfhosted-operator`, version `X.Y.Z`.
- Signing: keyless cosign through the GitHub OIDC token; the SBOM (SPDX JSON, from syft) is attached as a cosign attestation and as a release asset.

## Maintainer checklist

1. [ ] Confirm the API group decision is settled before the first tag. See the base spec: [docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md](superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md). Changing the group after release breaks installed CRDs.
2. [ ] Run `real-e2e` at both levels (see [testing.md](testing.md)).
3. [ ] Run the dry-run release: Actions tab, `release`, Run workflow, pick the branch, set `version` (for example `0.0.0-dryrun`) and leave `dry_run` as `true`. It runs `make test`, builds the image without pushing, generates the SBOM, builds the installer and chart, and uploads `dist/install.yaml`, the chart `.tgz` and `sbom.spdx.json` as an artifact. It does not log in to GHCR, sign, push the chart or create a GitHub Release.
4. [ ] Tag `vX.Y.Z` on `master` and push the tag.
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
8. [ ] After the first real push only: set the GHCR packages (the image and the chart) to public once, under the repository's Packages settings (Settings, Packages). Until then anonymous pulls and the verification commands above fail.
9. [ ] Confirm no `latest` image tag exists on the package.
