# On-demand plan: deferred follow-ups and rulings

Carried out of the Plan 2 execution ledger on 2026-10-02 for Plan 3.
Spec: docs/superpowers/specs/2026-10-02-claude-selfhosted-operator-design.md

## Rulings made during execution

- Ruling: feature branch in the main checkout, no worktree — same reasoning as Plan 1 (single checkout, e2e needs the Makefile's kind cluster) — cost if wrong: branch switch.
- Task 4: Ruling: fix ordering now (Get runner first); delete the work-order Secret when the runner create fails non-retryably (orchestrator Role gains delete on secrets; Task 3 test updated) — a live JWT must not sit orphaned — cost if wrong: one Role verb
- Task 4: Ruling: hand-off uses a raw JSON merge patch with no Get, Role stays create/patch/delete on secrets — least privilege — cost if wrong: one verb added later
- Task 6: implemented 624e938; controller found make lint failing (unparam on truncate's constant parameter, triggered by the new callers). Ruling: drop the parameter in failedstart.go (existing-file edit allowed) — cost if wrong: none. Fix round 1 dispatched before review.
- Task 10: Ruling: hook traces sampled at ratio 1 (one span per spawn, low volume) while the manager's --tracing-sample-ratio governs its own root spans; spec 8.4's "sampled, not full rate" applies to reconcile spans — cost if wrong: a flag on the hook later
- Task 12: BLOCKED on a plan defect in Task 3 code: the hook init container sets Args only, so the operator image's /manager entrypoint runs ("flag provided but not defined: -install") and the orchestrator never starts (reproduced on kind; e2e 4 passed, 1 failed, 1 skipped). Ruling: add Command: [HookBinaryPath] to the init container in internal/builders/orchestrator.go with a test assertion, done by the Task 12 implementer as part of its task — cost if wrong: one builder line
- Ruling: M1 (keep old JWT on redelivery), M4 (fixed 45s hook deadline vs low hookTimeoutSeconds), M5-M10 deferred to Plan 3 — cost if wrong: Plan 3 scope

## Deferred minors (from task and final reviews)

- Task 1: minor (deferred): scaffolded clauderunner sample has an empty spec and is in config/samples/kustomization.yaml — Task 11 deletes it
- Task 1: minor (deferred): ClaudeRunner spec/status fields lack doc comments (kubectl explain); negative orderID test asserts only NotTo(Succeed()); test file lacks license header
- Task 2+3: minor (deferred): probe path literal "/spawn-runner" duplicates unused HookBinaryPath; probe test checks only Command[0:2]; literals instead of volume-name constants in tests; podTemplate slices aliased not copied (matches Plan 1)
- Task 4: minor (deferred): maxConcurrentRunners is a soft cap across concurrent hooks — document in Task 11
- Task 4: minor (deferred): order/session IDs longer than 63 chars fail label validation (exit 2); product IDs are short in practice
- Task 4: minor (deferred): JWT header without a dot segment is not redacted; shared labels map between Secret and runner
- Task 4: minor (deferred): hand-off ref sets blockOwnerDeletion=true; on clusters with OwnerReferencesPermissionEnforcement (OpenShift) the patch needs update on clauderunners/finalizers and would only warn — consider blockOwnerDeletion unset
- Task 4: minor (deferred): hand-off patch replaces the Secret's owner list (drops the environment owner); fine since the runner is env-owned
- Task 5: minor (deferred): Install leaves a temp file on copy/chmod/rename failure; install test compares length only; probe test mutates the handler body without sync (race detector could flag)
- Task 6: minor (deferred): no tests for Failed-without-terminated-state, waiting-vs-Unschedulable precedence, truncation limit; redaction case does not assert [redacted]; double redact/truncate on the Failed message is harmless
- Task 7: minor (deferred): nowFunc is a mutable package global read by the manager goroutine (make test has no -race); TTL spec asserts pod only (no GC in envtest); spawn-timeout spec's +2min shift cannot distinguish 30s from the 120s default
- Task 8: minor (deferred): applyFailed message says "runner <Kind>" for orchestrator objects; stale Status.OnDemand counts on the HookImageUnset path; duplicate FleetAvailable event on a status-update conflict; new Owns on SA/Role/RoleBinding cache those cluster-wide (label-filter in CacheByObject as follow-up); ReasonUnsupportedMode now dead; ondemand test file lacks license header
- Task 9: minor (deferred): CountRunner(created) also fires on an AlreadyExists race; fail() paths (EnvironmentMissing/WorkOrderMissing) never count runners_total{failed}; histogram test checks series count only; pending/running phase literals; commented cert-manager replacements entries at column 0 under the new replacements key (indentation trap if uncommented)
- Task 10: minor (deferred): two vacuous telemetry test assertions (TraceparentFrom on a bare context; Extract never returns nil); os.Exit paths after Init skip the trace flush
- Task 11: minor (deferred): ReasonUnsupportedMode constant is dead code
- Task 12: minor (deferred): e2e GC check passes on any kubectl error (match NotFound); FailedCreate query discards its error; logs read from deploy/ picks one pod; stub handler mode branches could be split
- Final fix wave: minor (deferred): ownerMismatch matches by name not UID; clauderunner_controller_test.go lacks a license header; nowFunc global race under -race (pre-existing)
- Final: minor (deferred): createPod on AlreadyExists re-counts created and re-emits PodCreated for one pass and derives Pending from the local pod; a status conflict after a spawn-timeout delete records PodLost instead of SpawnTimeout; "rejects an empty orderID" test no longer isolates MinLength; I2 "lands after" test sleeps 2s rather than asserting the waiting state first; orderID up to 253 chars makes the Secret name too long (unreachable via hook)

## Final-review minors deferred to Plan 3 (M1, M4 to M10)

- M1: redelivery after a crash between the two hook creates keeps the old JWT; merge-patch data.jwt when no ClaudeRunner exists yet.
- M4: hook deadline is fixed at 45 s while hookTimeoutSeconds allows lower values; derive the deadline from an env var or raise the CRD minimum.
- M5: trace continued on every reconcile for the runner's whole life; stop after a terminal phase.
- M6: ClaudeRunner controller writes no log lines (no Created Pod line with orderID/sessionID).
- M7: spawn duration measured at observation time, not from the container's startedAt.
- M8: environment reconciles churn on every runner/pod transition (4 SSA patches + a List); add a phase-change predicate.
- M9: orphaned work-order Secrets after a hook crash with no redelivery; sweep *-work-order Secrets with no ClaudeRunner older than expectedSpawnSeconds.
- M10: HookImageUnset test mutates envReconciler.HookImage while the manager runs (same pattern as nowFunc).
- Plan 3: label-filtered cache for ServiceAccounts, Roles, RoleBindings (must land before 1.0); ValidatingAdmissionPolicy limiting the orchestrator ServiceAccount to *-work-order Secrets; OpenShift or quota'd-namespace e2e variant.
