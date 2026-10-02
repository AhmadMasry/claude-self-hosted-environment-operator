# Foundation plan: deferred follow-ups and rulings

Carried out of the Plan 1 execution ledger on 2026-10-02 for Plans 2 and 3.
Spec: docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md

## Rulings made during execution

- Ruling: work on a feature branch in the main checkout, no worktree — repo is new, kubebuilder scaffolds the whole tree, nothing else in flight — cost if wrong: none beyond a branch switch.
- | T2→T11 | sample YAML vs JSON tags | CONFLICT: sample used lifecycleHooks.configMapRef.name; types serialize ConfigMapRef as {name}. Ruling: types win; sample fixed to lifecycleHooks: {name} — cost if wrong: sample edit |
- | T2 (self) | make test after types change | CONFLICT: scaffolded controller test creates empty spec, rejected by CEL. Ruling: delete scaffolded test in Task 2 (moved from Task 7) — cost if wrong: none |
- Task 1: minor (deferred): go.mod/Dockerfile/devcontainer pin go 1.26 (kubebuilder default) while toolchain is 1.27 — Ruling: keep scaffold floor 1.26; no 1.27-only features — cost if wrong: bump one line later
- Task 2: Ruling: plan's CEL rules are defective — guard optional-field access with has() and express non-empty as size(...) > 0; add valid-spec test with capacity 4 and no proxy — spec requires rejecting only invalid specs — cost if wrong: one more CEL rule edit
- Task 7: Ruling: fix the ordering defect now (sorted iteration + sorted degradations) and fold in three one-line minors (IgnoreNotFound on status update after delete, Warning event on missing wrapper key, log message style) — cost if wrong: a few lines of rework
- Task 7: minor (deferred): onDemand path leaves a previously created fixed workload running after a fixed->onDemand switch — Ruling: Plan 2 (on-demand) must delete fixed workloads when mode is onDemand — cost if wrong: stale runners after a mode switch
- Task 12 pre-ruling: scaffold e2e suite variable is managerImage (brief says projectImage); scaffold deploys the operator in the Manager Ordered Describe's BeforeAll and deletes its namespace in AfterAll. Ruling: fixed-fleet e2e specs nest inside that Describe as an Ordered Context placed after the existing Its, instead of a separate top-level Describe — otherwise Ginkgo container ordering can run them before the operator exists — cost if wrong: test placement only
- Task 11: implemented fa8e6b8; controller found a plan defect: examples/hooks-configmap.yaml post-session script reads CLAUDE_RUNNER_CHECKOUT_PATH, but the product docs give post-session hooks CLAUDE_RUNNER_WORKSPACE_PATHS (colon-separated; CHECKOUT_PATH is a checkout-hook variable). Ruling: sample must iterate the colon-separated WORKSPACE_PATHS (mirroring the product's own reference script) — cost if wrong: a sample that never snapshots
- Ruling: rewrote the four fix-wave commit trailers from "Claude Opus 5.5" to "Claude Fable 5.1" with git filter-branch (local, unpushed; diff unchanged, new SHAs 08bdb07..541b52f) — the session's attribution instruction names Fable — cost if wrong: trailer text only

## Deferred minors (from task and final reviews)

- Task 1: minor (deferred): go.mod/Dockerfile/devcontainer pin go 1.26 (kubebuilder default) while toolchain is 1.27 — Ruling: keep scaffold floor 1.26; no 1.27-only features — cost if wrong: bump one line later
- Task 1: minor (deferred): scaffold README.md has TODO(user) placeholders and stale go version; replaced in Task 11
- Task 1: minor (deferred): AGENTS.md scaffold rules ("never create files manually") conflict with hand-written code; trim in Task 11 docs
- Task 1: minor (deferred): .custom-gcl.yml logtools version: latest and devcontainer post-install downloads latest (scaffold default)
- Task 2: minor (deferred): no tests for podTemplate forbidden-field absence or Volume exactly-one rule (brief did not request)
- Task 3+4: minor (deferred): args.go hard-codes product defaults 15/5/60 while drain.go has constants for 5/60 — could share constants
- Task 3+4: minor (deferred): lockToAccount may be an email and lands in pod args by product design; docs must say so and logs must not print rendered args
- Task 5: minor (deferred): no tests for WorkspaceFromPVC=true, client-label fieldRef env var, probe port name, or mount collisions
- Task 5: minor (deferred): volume names repeated as bare literals in podtemplate.go; user env var named SELF_HOSTED_RUNNER_CLIENT_LABEL/HOST_CONFIG_DIR would duplicate operator's
- Task 5: minor (deferred): test file uses testSecretKey as a volume name and mid-file const block
- Task 6: minor (deferred): FixedStatefulSet assumes PersistentWorkspace != nil — Task 7 controller gates on it
- Task 6: minor (deferred): *_test.go files in internal/builders carry no license header
- Task 7: minor (deferred): configMapRefs map drops the wrapper-key check when hostConfig and wrapperScript share a ConfigMap name
- Task 7: minor (deferred): reconcile returns RequeueAfter together with a non-nil error (controller-runtime ignores the Result)
- Task 7: minor (deferred): Ready reason is SecretMissing even for SecretKeyMissing
- Task 7: minor (deferred): onDemand path leaves a previously created fixed workload running after a fixed->onDemand switch — Ruling: Plan 2 (on-demand) must delete fixed workloads when mode is onDemand — cost if wrong: stale runners after a mode switch
- Task 7: minor (deferred): cluster-wide Secret/ConfigMap informers (memory + RBAC) — Plan 3 follow-up (metadata-only or label-filtered cache)
- Task 8: minor (deferred): fallback branch forwards an arbitrary last log line (larger leak surface than fatal-only) — consider fatal/error-only plus kubectl hint
- Task 8: minor (deferred): ccenvkey_ assertion in the table test checks fixtures only, asserts nothing about behaviour (plan-mandated)
- Task 8: minor (deferred): Warning event re-emitted on every reconcile while Degraded holds (recorder aggregates)
- Task 9: minor (deferred): state label and its values are literals while other labels are constants
- Task 9: minor (deferred): implementer report omitted RED/GREEN/lint output (reviewer verified from diff)
- Task 10: minor (deferred): Makefile docker-build does not pass --build-arg VERSION, images log "dev" (Plan 3 release work)
- Task 10: minor (deferred): --zap-log-level/--zap-devel remain bound but are overridden by --log-level/--log-format (plan-mandated)
- Task 10: minor (deferred): cmd/options_test.go lacks license header
- Task 11: minor (deferred): sample test accepts no digest-only images (only rejects :latest) — fine for samples
- Task 11: minor (deferred): pod-security enforce-version: latest in sample namespace (standard practice)
- Task 12: minor (deferred): e2e swallows the error from kubectl get events (FailedCreate check could pass vacuously)
- Task 12: minor (deferred): e2e applies testdata via CWD-relative path; exact "Running Running" phase assertion has residual flake risk during rollouts; stub image literal duplicated in testdata YAML and suite const
- Final: minor (deferred): Makefile docker-build passes an empty VERSION when git describe yields nothing (source tarball) — guard with $(if ...)
- Final: minor (deferred): on the apply-failed path status.fixed keeps the previous counts and checkRunnerPods is skipped
- Final: minor (deferred): the immutability envtest trigger depends on StatefulSet volumeClaimTemplates staying immutable (KEP-4650)
- Final: minor (deferred): README conditions table lacks WorkloadApplyFailed
