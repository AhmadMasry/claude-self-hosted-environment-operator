# Foundation plan: deferred follow-ups and rulings

Carried out of the Plan 1 execution ledger on 2026-10-02 for Plans 2 and 3.
Spec: docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md

## Rulings made during execution

- Ruling: work on a feature branch in the main checkout, no worktree — repo is new, kubebuilder scaffolds the whole tree, nothing else in flight — cost if wrong: none beyond a branch switch.
- | T2→T11 | sample YAML vs JSON tags | CONFLICT: sample used lifecycleHooks.configMapRef.name; types serialize ConfigMapRef as {name}. Ruling: types win; sample fixed to lifecycleHooks: {name} — cost if wrong: sample edit |
- | T2 (self) | make test after types change | CONFLICT: scaffolded controller test creates empty spec, rejected by CEL. Ruling: delete scaffolded test in Task 2 (moved from Task 7) — cost if wrong: none |
- Task 1: minor (deferred): go.mod/Dockerfile/devcontainer pin go 1.26 (kubebuilder default) while toolchain is 1.27 — Ruling: keep scaffold floor 1.26; no 1.27-only features — cost if wrong: bump one line later — Done (Plan 3, Task 5): floor kept at 1.26; Dockerfile and devcontainer pin `golang:1.26`
- Task 2: Ruling: plan's CEL rules are defective — guard optional-field access with has() and express non-empty as size(...) > 0; add valid-spec test with capacity 4 and no proxy — spec requires rejecting only invalid specs — cost if wrong: one more CEL rule edit
- Task 7: Ruling: fix the ordering defect now (sorted iteration + sorted degradations) and fold in three one-line minors (IgnoreNotFound on status update after delete, Warning event on missing wrapper key, log message style) — cost if wrong: a few lines of rework
- Task 7: minor (deferred): onDemand path leaves a previously created fixed workload running after a fixed->onDemand switch — Ruling: Plan 2 (on-demand) must delete fixed workloads when mode is onDemand — cost if wrong: stale runners after a mode switch — Done (Plan 2): on-demand mode deletes the owned fixed Deployment or StatefulSet
- Task 12 pre-ruling: scaffold e2e suite variable is managerImage (brief says projectImage); scaffold deploys the operator in the Manager Ordered Describe's BeforeAll and deletes its namespace in AfterAll. Ruling: fixed-fleet e2e specs nest inside that Describe as an Ordered Context placed after the existing Its, instead of a separate top-level Describe — otherwise Ginkgo container ordering can run them before the operator exists — cost if wrong: test placement only
- Task 11: implemented fa8e6b8; controller found a plan defect: examples/hooks-configmap.yaml post-session script reads CLAUDE_RUNNER_CHECKOUT_PATH, but the product docs give post-session hooks CLAUDE_RUNNER_WORKSPACE_PATHS (colon-separated; CHECKOUT_PATH is a checkout-hook variable). Ruling: sample must iterate the colon-separated WORKSPACE_PATHS (mirroring the product's own reference script) — cost if wrong: a sample that never snapshots
- Ruling: rewrote the four fix-wave commit trailers from "Claude Opus 5.5" to "Claude Fable 5.1" with git filter-branch (local, unpushed; diff unchanged, new SHAs 08bdb07..541b52f) — the session's attribution instruction names Fable — cost if wrong: trailer text only

## Deferred minors (from task and final reviews)

- Task 1: minor (deferred): go.mod/Dockerfile/devcontainer pin go 1.26 (kubebuilder default) while toolchain is 1.27 — Ruling: keep scaffold floor 1.26; no 1.27-only features — cost if wrong: bump one line later — Done (Plan 3, Task 5): floor kept at 1.26; Dockerfile and devcontainer pin `golang:1.26`
- Task 1: minor (deferred): scaffold README.md has TODO(user) placeholders and stale go version; replaced in Task 11 — Done (Plan 1, Task 11; top section rewritten in Plan 3, Task 1)
- Task 1: minor (deferred): AGENTS.md scaffold rules ("never create files manually") conflict with hand-written code; trim in Task 11 docs — Done (Plan 3, Task 1)
- Task 1: minor (deferred): .custom-gcl.yml logtools version: latest and devcontainer post-install downloads latest (scaffold default) — Done (Plan 3, Task 5): logtools and the devcontainer tools pinned
- Task 2: minor (deferred): no tests for podTemplate forbidden-field absence or Volume exactly-one rule (brief did not request) — Done in part (Plan 3, Task 4: schema-level test that `hostNetwork`, `hostPID`, `privileged` are unknown fields) — Deferred: no envtest yet for the Volume exactly-one rule
- Task 3+4: minor (deferred): args.go hard-codes product defaults 15/5/60 while drain.go has constants for 5/60 — could share constants — Done (Plan 3, Task 4): `builders.Default*` constants
- Task 3+4: minor (deferred): lockToAccount may be an email and lands in pod args by product design; docs must say so and logs must not print rendered args — Done (Plan 3, Tasks 5 and 13): README hardening notes and `docs/hardening.md`; rendered args are not logged
- Task 5: minor (deferred): no tests for WorkspaceFromPVC=true, client-label fieldRef env var, probe port name, or mount collisions — Done (Plan 3, Task 4); mount collisions are now rejected by CEL and tested at the API level
- Task 5: minor (deferred): volume names repeated as bare literals in podtemplate.go; user env var named SELF_HOSTED_RUNNER_CLIENT_LABEL/HOST_CONFIG_DIR would duplicate operator's — Done (Plan 3, Task 4): volume-name constants; CEL rejects the operator-owned env names
- Task 5: minor (deferred): test file uses testSecretKey as a volume name and mid-file const block — Done in part (`testSecretKey` is now only a Secret key) — Deferred: mid-file const block in `podtemplate_test.go` (cosmetic)
- Task 6: minor (deferred): FixedStatefulSet assumes PersistentWorkspace != nil — Task 7 controller gates on it — Deferred: the only caller (the controller) gates on `PersistentWorkspace`; add a nil check if another caller appears
- Task 6: minor (deferred): *_test.go files in internal/builders carry no license header — Done for the files listed (Plan 3, Task 5) — Deferred: `internal/builders/networkpolicy_test.go`, `internal/controller/failedstart_test.go` and `internal/controller/runnerphase_test.go` still lack the header
- Task 7: minor (deferred): configMapRefs map drops the wrapper-key check when hostConfig and wrapperScript share a ConfigMap name — Done (Plan 2): the wrapper key wins when a ConfigMap is shared
- Task 7: minor (deferred): reconcile returns RequeueAfter together with a non-nil error (controller-runtime ignores the Result) — Done (Plan 3, Task 3)
- Task 7: minor (deferred): Ready reason is SecretMissing even for SecretKeyMissing — Done (Plan 1 final fix wave): Ready takes the `SecretFound` condition's reason
- Task 7: minor (deferred): onDemand path leaves a previously created fixed workload running after a fixed->onDemand switch — Ruling: Plan 2 (on-demand) must delete fixed workloads when mode is onDemand — cost if wrong: stale runners after a mode switch — Done (Plan 2)
- Task 7: minor (deferred): cluster-wide Secret/ConfigMap informers (memory + RBAC) — Plan 3 follow-up (metadata-only or label-filtered cache) — Done (Plan 3, Task 6): Pods, ServiceAccounts, Roles, RoleBindings and NetworkPolicies are label-filtered; Secrets and ConfigMaps stay unfiltered by design, with `--watch-namespaces` documented in `docs/hardening.md` (Task 13)
- Task 8: minor (deferred): fallback branch forwards an arbitrary last log line (larger leak surface than fatal-only) — consider fatal/error-only plus kubectl hint — Done (Plan 1 final fix wave): only `[runner:fatal]` or `error:` lines are forwarded, else a `kubectl logs --previous` hint
- Task 8: minor (deferred): ccenvkey_ assertion in the table test checks fixtures only, asserts nothing about behaviour (plan-mandated) — Done (Plan 1 final fix wave): the table test now asserts no key, JWT or email reaches the message
- Task 8: minor (deferred): Warning event re-emitted on every reconcile while Degraded holds (recorder aggregates) — Done (Plan 3, Task 3): Warnings only on transition, per reason
- Task 9: minor (deferred): state label and its values are literals while other labels are constants — Deferred: `state` and `phase` label values are still literals in `internal/metrics` (cosmetic)
- Task 9: minor (deferred): implementer report omitted RED/GREEN/lint output (reviewer verified from diff) — Won't do: process note about a past report, nothing to change in the repository
- Task 10: minor (deferred): Makefile docker-build does not pass --build-arg VERSION, images log "dev" (Plan 3 release work) — Done (Plan 3, Task 5)
- Task 10: minor (deferred): --zap-log-level/--zap-devel remain bound but are overridden by --log-level/--log-format (plan-mandated) — Done (Plan 3, Task 4): the `--zap-*` flags are gone
- Task 10: minor (deferred): cmd/options_test.go lacks license header — Done (Plan 3, Task 5)
- Task 11: minor (deferred): sample test accepts no digest-only images (only rejects :latest) — fine for samples — Won't do: accepted in this entry ("fine for samples"); since Plan 3, Task 4 the CRD's CEL enforces tag or digest on every object
- Task 11: minor (deferred): pod-security enforce-version: latest in sample namespace (standard practice) — Won't do: approved ruling (`enforce-version: latest` stays on sample namespaces)
- Task 12: minor (deferred): e2e swallows the error from kubectl get events (FailedCreate check could pass vacuously) — Done (Plan 3, Task 5)
- Task 12: minor (deferred): e2e applies testdata via CWD-relative path; exact "Running Running" phase assertion has residual flake risk during rollouts; stub image literal duplicated in testdata YAML and suite const — Done (Plan 3, Task 5)
- Final: minor (deferred): Makefile docker-build passes an empty VERSION when git describe yields nothing (source tarball) — guard with $(if ...) — Done (Plan 3, Task 5)
- Final: minor (deferred): on the apply-failed path status.fixed keeps the previous counts and checkRunnerPods is skipped — Done (Plan 3, Task 3): `status.fixed` cleared on the apply-failed path
- Final: minor (deferred): the immutability envtest trigger depends on StatefulSet volumeClaimTemplates staying immutable (KEP-4650) — Done (Plan 3, Task 3): the trigger changes `accessModes`, which stays immutable under KEP-4650
- Final: minor (deferred): README conditions table lacks WorkloadApplyFailed — Done (Plan 3, Task 1)
