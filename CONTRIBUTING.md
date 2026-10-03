# Contributing

## Toolchain

Go (version in `go.mod`), Docker, kind, kubectl, Helm 3, kubebuilder v4.
`make` installs controller-gen, kustomize, envtest and golangci-lint into
`bin/` on first use.

## Workflow

1. Branch from `master`.
2. `make manifests generate` after editing `api/` markers or RBAC markers;
   commit the generated files.
3. `make test` (unit + envtest), `make lint`, and the race run must pass. The race run needs the
   envtest binaries on an absolute path (run `make setup-envtest` once; `1.37` is the envtest version
   the Makefile derives from `k8s.io/api`):

       KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.37 --bin-dir $PWD/bin -p path)" go test -race ./internal/... ./cmd/...
4. `make test-e2e` runs the kind suite (creates and deletes its own cluster).
5. Open a pull request; CI runs lint, tests, race, chart lint and contract test, the e2e and the upgrade e2e.

## Test tiers

- Unit: pure functions in `internal/builders`, `internal/hook`, `internal/metrics`, `internal/telemetry`.
- envtest: both controllers against a real API server, `internal/controller`.
- e2e: kind cluster with stub runner and orchestrator images, `test/e2e`.
- Real environment: manual workflow, see `docs/testing.md`.

## Commit messages

Conventional prefixes (`feat`, `fix`, `docs`, `test`, `chore`) with a short
subject. Generated files are committed together with the change that
regenerates them.
