# AGENTS.md

Guidance for AI agents and humans working in this repository.

- Generated files are never edited by hand: `config/crd/bases/*.yaml`,
  `config/rbac/role.yaml`, `**/zz_generated.*.go`, `PROJECT`, `dist/`.
  Run `make manifests generate` after editing `api/` or RBAC markers and
  commit the result.
- Keep the kubebuilder layout: `api/v1alpha1`, `internal/controller`,
  `internal/builders` (pure functions that turn CRs into objects),
  `internal/hook` (the spawn-runner hook), `cmd/` (manager) and
  `cmd/spawn-runner`.
- Do not remove `// +kubebuilder:scaffold:*` markers.
- Tests: unit (`go test ./internal/...`), envtest (`make test`), kind e2e
  (`make test-e2e`, isolated cluster), race (`go test -race`). Add a test
  with every behaviour change; see CONTRIBUTING.md.
- Logging follows the Kubernetes style guide: capitalised, no trailing
  period, key-value pairs. Never log secrets, JWTs, or emails.
- Every pod the operator builds must satisfy the Restricted Pod Security
  Standard; the builders set the fields explicitly.
