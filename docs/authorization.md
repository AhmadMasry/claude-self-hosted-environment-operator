# Access lists

Anthropic does not restrict who may use a self-hosted environment: "any member of your Anthropic
organization can dispatch a session to any of its environments"
([Harden your deployment](https://code.claude.com/docs/en/self-hosted-environments-deploy#harden-your-deployment)).
An environment that runs on a production cluster is therefore open to every member of the organization.

`spec.authorization` adds the missing check on the cluster side. It names a ConfigMap that lists the
Anthropic account IDs allowed to use the environment. A session created by anyone else gets no pod, and a
running session is stopped when its creator is taken off the list.

**Access lists are a feature of on-demand mode.** They work on environments that set `spec.onDemand` and
keep `onDemand.orchestrator.minIdle` at 0. Fixed fleets and standby runners are not covered, and the API
rejects `spec.authorization` there; [Limits](#limits) says what to use instead.

The creator is the account Anthropic names on the spawn request, which its hook contract describes as the
"tagged ID of the account that enqueued the session".

The check decides what the cluster runs. It does not hide the environment in claude.ai or stop a member
from selecting it: that part belongs to Anthropic.

## Set it up

1. Create the list, one account ID per line:

   ```yaml
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: prod-access
     namespace: claude-prod
   data:
     accounts: |
       # text after a # is a comment
       user_01EXAMPLE0000000000000001   # platform lead
       user_01EXAMPLE0000000000000002   # on-call engineer
   ```

2. Point the environment at it:

   ```yaml
   apiVersion: claude.cloudeng.ps/v1alpha1
   kind: ClaudeEnvironment
   metadata:
     name: prod
     namespace: claude-prod
   spec:
     authorization:
       mode: Enforce            # the default; Audit reports and refuses nobody
       allowedAccountsFrom:
         configMapKeyRef: {name: prod-access, key: accounts}
     onDemand: {}
     # environmentSecretRef, runner ... as before
   ```

3. Check that it is in force:

       kubectl -n claude-prod get cenv prod \
         -o jsonpath='{.status.conditions[?(@.type=="AccessRestricted")]}{"\n"}'

   `status: "True"`, reason `AccessListEnforced` and the message `2 accounts allowed by ConfigMap
   "prod-access" key "accounts"` mean the list is read and enforced.

Without `spec.authorization` nothing changes: every member of the organization can use the environment.

### Finding account IDs

The list holds account IDs, the tagged `user_...` values Anthropic sends with every spawn request. Email
addresses are not accepted: Anthropic's documentation says the email can be missing and tells integrators
to key on the account ID, an address can be reassigned to another person, and the operator never stores or
logs an email. To collect the IDs of the people who use an environment today:

- Start in `mode: Audit`, let people work, then read the runners:

      kubectl -n claude-prod get crun -o wide      # the ACCOUNT column
      kubectl -n claude-prod get events --field-selector reason=UnauthorizedAudit

- Or read the orchestrator's log, which records the account ID of every request, with or without an access
  list:

      kubectl -n claude-prod logs deploy/prod-orchestrator -c orchestrator | grep accountID

- The refusal message (below) carries the account ID of the person who was refused.

## Rules

| Case | `Enforce` | `Audit` |
| :- | :- | :- |
| The creator's account ID is on the list | Runs | Runs |
| The creator's account ID is not on the list | Refused; a running session is stopped | Runs, and is reported |
| The session has no user account ID (a Claude Tag channel session, for example) | Refused | Runs, and is reported |
| The ConfigMap or the key does not exist | Every session is refused and running sessions are stopped; the environment is `Degraded` with `AccessListMissing` | Runs; the environment is `Degraded` with `AccessListMissing` |
| A line is not an account ID (an email address, a typo) | The line is ignored; the environment is `Degraded` with `AccessListInvalid` and the line numbers | The same |
| The list is empty | Nobody is allowed | Runs, and is reported |

A line that is not an account ID neither widens access nor locks out the other accounts. Its text is never
copied into a status, an event or a log, because a mistyped entry is often an email address.

## Where the check runs

The same function runs in two places, and both read the ConfigMap from the API server at that moment, never
from a cache:

1. **The spawn-runner hook**, when Anthropic asks the orchestrator for a runner. A refused creator ends the
   hook with exit code 2 before anything is created: no Secret, no ClaudeRunner, no pod. Exit code 2 tells
   Anthropic not to retry. A session that is resumed asks for a new runner, so it passes through the hook
   again.
2. **The ClaudeRunner controller**, before it creates the pod and on every later pass of a runner whose pod
   is still alive. It is the component that starts pods, so no ClaudeRunner gets a pod without passing the
   check. A refused runner ends as `Failed` with reason `Unauthorized`, and its pod is deleted.

This is enough for on-demand environments because a work order is bound to one session and the runner pod
serves that session only.

A list that cannot be read is never treated as an answer. If the API server does not return the ConfigMap
for any reason other than "it does not exist", the hook ends with the retryable exit code 1 and Anthropic
asks again, and the controller retries without stopping anyone. This also covers the moment after you add
`spec.authorization` or rename the list, when the orchestrator's Role has not been updated yet. Before the
controller stops a runner it reads the ClaudeEnvironment from the API server as well, so a session is not
stopped over a list the environment no longer names.

## What a refused session looks like

The session shows **Failed**. Anthropic keeps the reason as the session's spawn error, taken from the last
512 characters the hook wrote to its standard error, and shows it when the session is expanded in the
environment's **Activity** tab. The hook writes one of these lines:

    error: account user_01ABC... is not on this self-hosted environment's access list; ask the environment's administrators to add this account ID
    error: this session has no user account ID, and this self-hosted environment serves only the accounts on its access list
    error: this self-hosted environment's access list is unavailable, so every session is refused; ask the environment's administrators

These three lines name the account ID, so it can be added to the list, and nothing about the cluster.
Anthropic's documentation does not say whether the person who started the session sees the spawn error in
their own session view, so expect to look it up in the Activity tab or in the orchestrator's log.

A refused session stays blocked until its user sends it a new message or an Owner selects **Retry** in the
Activity tab. Either one sends a new spawn request, which is checked again. The block is per session:
refusals do not slow down or pause the environment for the accounts on the list. Two of the orchestrator's
own metrics count them, `queue_circuit_broken_sessions` and `sessions_given_up_total{cause="hook_declined"}`,
so an alert on blocked sessions also fires for refusals.

## Removing access

Edit the ConfigMap in place and remove the line. The change applies at once:

- a new or resumed session from that account is refused by the hook;
- a session of that account that is running is stopped: the controller marks its ClaudeRunner `Failed` with
  reason `Unauthorized` and deletes the pod. Work the session had not pushed is lost with the pod. The
  runner drains and deregisters, Anthropic asks for a new runner for the session, and the hook refuses that
  request, so the session ends as Failed with the message above. In a test against a real environment the
  runner was marked `Unauthorized` within a second of the edit and its pod was gone three seconds later.

Deleting the ConfigMap refuses everyone and stops every running session of the environment. Change the list
by editing it, not by deleting and recreating it (`kubectl replace --force` does the latter).

Switching `mode` from `Audit` to `Enforce` applies the list to the sessions that are running at that moment
too.

## Rolling it out with Audit mode

`mode: Audit` refuses nobody. It reports each session that `Enforce` would refuse:

- the hook's log line carries `"authorization":"audit_denied"`;
- the ClaudeRunner gets a Warning event `UnauthorizedAudit` with the account ID;
- the environment's `AccessRestricted` condition is `False` with reason `AccessListAudited`.

Audit mode never holds a session back. If the list is missing or cannot be read, the session starts anyway;
a read failure appears as a `warning` in the hook's log line.

Run in Audit mode until the events stop naming people who should have access, then switch to `Enforce`.

## Limits

- **On-demand mode only.** The check relies on the spawn request Anthropic sends for each session, which
  names the session's creator before anything runs. Only on-demand mode has that request.
- **Not on fixed fleets.** A fixed fleet has no spawn request to check, and the API rejects
  `spec.authorization` there. Restrict a fixed fleet with `runner.settings.lockToAccount`, which Anthropic's
  runner enforces: a pre-locked runner executes the sessions of that one account only. Use one fixed fleet
  per person, or an on-demand environment for a shared list.
- **Not with standby runners.** `onDemand.orchestrator.minIdle` above 0 is rejected together with
  `spec.authorization`: a standby runner is started before any session exists and then serves whoever
  dispatches next. A protected environment starts each runner when its session arrives.
- **Sessions without a user account are refused**, since there is no account ID to look up. Claude Tag
  channel sessions are the documented case.
- **Account IDs only.** There are no groups, and no email addresses.

## Who you are trusting

- **The namespace.** Anyone who can edit ConfigMaps or ClaudeEnvironments in the environment's namespace can
  add themselves to the list or remove the check. The built-in `edit` role is enough. This is not a new
  exposure, because the same rights already reach the environment key, but it sets the boundary: keep each
  protected environment in its own namespace, grant write access there sparingly, and change the list
  through reviewed Git commits.
- **The orchestrator.** Its Role gains `get` on that one ConfigMap by name and nothing else. It cannot
  list ConfigMaps or change the list.
- **The account ID.** Anthropic sends it with the spawn request over the connection the orchestrator
  authenticates with the environment key. The hook copies it into `ClaudeRunner.spec.accountID`, which is
  immutable. The orchestrator pod runs no session code. The operator does not verify the work order's
  signature itself; whoever could forge a request already holds the environment key. For the same reason,
  whoever can create ClaudeRunners in the namespace chooses the account ID they carry. The hook is the
  only intended creator, and the [admission policy](on-demand.md#admission-policy) ties each ClaudeRunner the
  orchestrator's identity creates to its own environment and work order.
- **The people on the list.** Inside a session they can do whatever the runner pod can do. The list decides
  who gets a session, not what a session may reach; see [hardening](hardening.md) for that.

## Audit trail

| Where | What it records |
| :- | :- |
| Orchestrator log, one JSON line per hook run | `accountID`, `authorization` (`allowed`, `denied`, `audit_denied`), `outcome` (`denied` for a refusal), `sessionID`, `orderID` |
| ClaudeRunner | `spec.accountID` for every session that ran; `status.reason: Unauthorized` and a Warning event `Unauthorized` when the controller refused or stopped it; Warning event `UnauthorizedAudit` in Audit mode |
| ClaudeEnvironment | Condition `AccessRestricted`; `Degraded` with `AccessListMissing` or `AccessListInvalid` |
| Metrics | `claude_operator_runners_total{outcome="unauthorized"}` counts the runners the controller refused or stopped. Refusals by the hook create no runner and appear in the orchestrator log only |

No email address is read, stored or logged at any point.

    kubectl -n claude-prod logs deploy/prod-orchestrator -c orchestrator | grep '"authorization":"denied"'
    kubectl -n claude-prod get crun -o wide            # ACCOUNT and REASON columns
    kubectl -n claude-prod get events --field-selector reason=Unauthorized
