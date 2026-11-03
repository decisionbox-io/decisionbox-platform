# Plan — Discovery run resume (v4): operator Pause (cooperative stop → resume)

Issue: #508. Depends on **v1** (#438, merged as `51030d3`). Independent of v2/v3.

---

## 1. The problem, restated against the code

v1 made a discovery run resumable from whatever checkpoint happened to land, with
**no cooperation from the dead run** — it is crash recovery. The resume read-path
and the whole fencing apparatus already exist: `discovery_checkpoints`,
`BeginResume`, `--resume --attempt N`, attempt fences on every write, an
`Ownership` probe at every phase boundary.

What does not exist is a way for an operator to stop a *live* run and keep it.
The only stop today is `CancelRun`
(`services/api/internal/handler/discoveries.go:1261`), which is a terminal hard
kill: `agentRunner.Cancel` kills the subprocess / stops the container / deletes
the Job, `runRepo.Cancel` writes `cancelled`, and `checkpointRepo.DeleteByRun`
**purges the checkpoints**. Cancel cannot double as Pause — a hard-killed
"pause" is exactly the crash case v1 already handles, minus the checkpoints it
just deleted.

So v4 adds the write side: a **cooperative stop**. Signal the live run, let it
finish the step it is in, let its checkpoint land, and let it exit on its own
with a new non-terminal status **`paused`** that `ResumeRun` accepts.

---

## 2. The one design decision everything else follows from

**The pause signal travels through the run document the agent already polls. It
is not a process signal and it does not touch the runner layer.**

The agent reads its own run document at every phase boundary —
`StatusReporter.OwnsRun` → `RunRepository.Ownership`
(`services/agent/internal/database/run_repo.go:431`), one projected read on
`_id`, already called before exploration, before analysis, before each analysis
area, before validation, before recommendations and before the save. v1 built
that probe so a superseded or cancelled attempt stops spending money. It is
already the exact seam a cooperative stop needs: a point where the agent asks
"may I carry on?" and is prepared to stop.

So Pause is: the API stamps `pause_requested_at` on the run document; the agent's
existing probe learns about it on its next gate and stops there.

What this buys, stated plainly because the alternatives all cost something:

- **Nothing in `services/api/internal/runner/` changes.** No new signal path, no
  SIGTERM handling in the agent (which does not exist — `docker.go:132` is
  explicit that the agent does not yet honour SIGTERM, tracked separately as
  #270), no K8s `terminationGracePeriodSeconds` tuning, and identical behaviour
  across all three runner modes. A SIGTERM design would have needed agent signal
  handling *plus* three runner changes *plus* a grace period longer than one
  exploration step (LLM call at `LLM_TIMEOUT`=300s × retries, plus a warehouse
  query) — and would then be indistinguishable from a pod eviction.
- **The stop point is already correct.** The checkpoint write in the exploration
  loop is where "the current step is finished and durable" is true. Pausing
  immediately after it is free by construction.
- **No API→agent network path.** There is none today (the API talks to agents
  only through Mongo and the runner), and inventing one for this would be the
  largest change in the PR.

Rejected alternatives are in §8.

---

## 3. State model

### 3.1 `paused` is a new status; "pausing" is NOT

A pause takes effect at the agent's next gate, which can be minutes away. That
draining window needs representing, and it is deliberately **not** a status.

While draining, the run stays `running` and carries `pause_requested_at`. The
reason is sharp: during the drain an agent **is alive and querying the
warehouse**, so every predicate that means "this run is active" must keep saying
yes. Those predicates are spelled as explicit status lists —
`GetRunningByProject`, `GetOtherRunningByProject`, `GetLatestByProject`'s active
pass, `CleanupStaleRuns`, `FailAttempt`, `Fail` — and a `pausing` status would
have to be added to every one of them, with each miss a real bug (a `pausing`
run invisible to the concurrency gate means two agents on one project). The
marker is purely additive and changes none of them.

`paused` *is* a status, and it is **non-terminal and resumable**. The existing
status lists already give the right answers for free, which was checked one by
one rather than assumed:

| Predicate | Lists | `paused` behaviour | Correct? |
|---|---|---|---|
| `GetRunningByProject` / `GetOtherRunningByProject` | `{pending, running}` | not active | yes — no agent is alive |
| `GetLatestByProject` active pass | `{pending, running}` | falls to the `started_at` / `last_resumed_at` passes | yes — it is still the project's current run unless a newer one exists |
| `CleanupStaleRuns` (startup sweep) | `{pending, running}` | not swept | yes — nothing to sweep, and sweeping would un-resume it |
| `ListTerminalWithoutCompletionHook` | `{completed, failed, cancelled}` | hooks do **not** fire | yes — the run's outcome is still to come |
| `ListTerminalWithReservation` | `{completed, failed, cancelled}` | reservation stays open | yes — see §3.4 |
| `ResumableOrActiveIDs` (agent boot sweep) | `$nin {cancelled, completed}` | step index **kept** | yes — it is resumable |
| `ListActiveRecent` | `{pending, running}` | not listed | yes — covered by the clause above |

### 3.2 New fields on the run document

Added to **both** copies of the model — `services/api/models/run.go` and
`services/agent/internal/models/run.go` — which the codebase keeps mirrored on
purpose (see the existing `CompletionHooksFiredAt` comment: the agent neither
reads nor writes it but carries it so the document shape stays consistent).

| Field | Written by | Meaning |
|---|---|---|
| `pause_requested_at` | API (`PauseRun`) | when an operator asked. Its presence is the signal. |
| `pause_requested_attempt` | API | the attempt the request was made against. `json:"-"`. |
| `pause_deadline_at` | API | `pause_requested_at + DISCOVERY_PAUSE_DRAIN_TIMEOUT`. Past it the request has **lapsed** and the agent ignores it. |
| `last_paused_at` | agent | when the run actually entered `paused`. This is the `last_paused_at` the issue asks for. |

All `omitempty`, all absent on every existing run.

Why an absolute `pause_deadline_at` rather than the agent deriving the deadline
from its own env: it keeps the timeout a **single-reader** config value (the API),
so the two services cannot disagree about it the way `DISCOVERY_MAX_DURATION` and
`AGENT_JOB_TIMEOUT_HOURS` can — and the dashboard can render the lapse, which it
could not if the number lived only in the agent's environment.

### 3.3 Why the request lapses at all

An un-expiring pause request is a landmine. If the agent is wedged inside a
multi-hour warehouse scan and the operator gives up and walks away, a request
that never expires makes the run pause hours later, unattended, out of nowhere —
and until then the dashboard says "Pausing…" forever with nothing to act on.

A lapse is the whole mechanism: past the deadline `Ownership` simply does not
report the request, so the semantics are "a pause takes effect at the next step
or phase boundary within the drain window, otherwise it lapses and the run
carries on". No cleanup job is needed — a lapsed marker is inert, `PauseRun`
overwrites it on a re-request, and the terminal write `$unset`s it.

Default `30m`, generous on purpose: one exploration step is an LLM call bounded
by `LLM_TIMEOUT` (300s default) times `LLM_RETRY_MAX_ATTEMPTS` plus a warehouse
query, so a legitimately slow step must not trip it. `0` disables the deadline
(honour the request whenever the agent next gets to a gate).

### 3.4 Plan reservations, and what stays out of scope

A paused run keeps its `policy_reservation_id` open, because it is not terminal
so the background confirmer does not list it. That is the conservative answer —
the resumed attempt opens no reservation of its own (v1's documented design), so
closing it on pause would leave the resume unreserved with nothing to reopen.
The consequence is that a paused run still holds a concurrent-run slot on a
metered deployment.

That is precisely the **concurrency-at-resume** question the issue puts out of
scope, and `ResumeRun`'s existing comment already says it is "to be revisited
with the v4 (operator Pause) increment". This plan does **not** build it: no
`CheckResumeDiscoveryRun` seam, no pricing decision. What it does do is avoid
making it worse, and close the one new leak it would otherwise open — see
`DiscardRun` in §5.2.

---

## 4. The pause window — where a pause is honoured, and why not everywhere

v1 resumes at **exploration-step** granularity. So the cost of a pause depends
entirely on where it lands, and at the tail it would be absurd: pausing just
before Phase 7 throws away a complete analysis, validation and recommendation
pass that the resume would re-pay in full, seconds before the run would have
finished anyway.

**Rule: a pause is honoured at every seam where a checkpoint has just landed,
and declined everywhere else. Concretely: anywhere in exploration, at the
Phase 4 entry gate, and at each per-area analysis checkpoint. Past the last
area's checkpoint it is declined and the run finishes.**

The per-area half of that rule exists because **v2 (#510) is ready to merge**,
so v4 is built on top of it: v2 makes each completed analysis area durable, and
a pause right after one lands costs nothing. The window is therefore a property
of the seam — "a checkpoint just landed here" — rather than a phase boundary,
which is what makes it one rule instead of two. §11 has the dependency detail.

| Gate (file:line; the per-area row is on #510's branch) | Honours pause? | Why |
|---|---|---|
| exploration step, after `checkpointStep` (`ai/exploration.go:836`) | **yes** | the step is finished and durable; nothing is lost |
| `complete_rejected` step checkpoint (`ai/exploration.go:790`) | **yes** | same seam, same guarantee |
| replayed step (`fenceReplayedStep`, `exploration_resume.go:346`) | **yes** | the rows already exist; stopping costs nothing |
| "rebuilding the step index" (`orchestrator.go:1273`) | **yes, if a resume point exists** | pre-exploration on a resume; nothing spent yet |
| Phase 4 entry, `ownershipLost(ctx, "analysis")` (`orchestrator.go:1393`) | **yes** | exploration's summary is checkpointed, so the resume skips Phase 3 entirely — a genuinely free pause point, and the common "let exploration finish, then stop" case |
| per-area, after `checkpointAnalysisArea` (v2, `analysis_resume.go:69`) | **yes** | the area and its validation are durable, so a resume restores them; the exact twin of the exploration-step seam |
| per-area ENTRY and `"validating area X"` gates | **no** | mid-area: this area's analysis call has happened and is not yet checkpointed. Its own checkpoint seam, a few lines later, is the free point — and catches the LAST area too, which a next-iteration gate would miss |
| recommendations, "saving the result" | **no** | Phases 5–7 are not resumable until v3; pausing there discards them for nothing |

Two further preconditions on honouring, both load-bearing:

1. **There must be something to resume from.** `paused` with no checkpoint would
   be a dead end: `ResumeRun` refuses `prefixLen == 0 && !explorationComplete`
   with a 409. So a gate only pauses when
   `o.resume.prefixLen() > 0 || o.resume.explorationComplete() || a checkpoint
   has landed this attempt` — which is exactly what `o.keepStepIndex` already
   tracks (set at the first checkpoint, at the exploration summary, and at
   construction on a resume; cleared only on discard). Exposed as
   `hasResumePoint()` returning that field, documented as one answer to two
   questions, rather than a parallel flag that could drift. A pause requested
   before step 1 completes therefore waits for step 1 — which is literally
   "finish the current step, then stop".
2. **Supersession and cancellation outrank a pause.** Checked in that order in
   `Ownership`.

The operator is told about the window **synchronously**, not by silence:
`PauseRun` refuses with 409 when the run's `phase` is already past analysis,
naming the phase and pointing at Cancel. The run document's `phase` is set only
by `SetPhase`, so it reads `analysis` for the whole of Phase 4 — per-insight
validation included, since `setLLMPhase` is layer attribution and does not touch
it. So the API's gate and the agent's seams agree without either having to know
about the other's internals. The residual race — the run moves to
analysis between that check and the agent's next gate — ends with the agent
declining the request and logging why; the marker is then cleared by the terminal
write.

### Races, and what each one does

| Race | Outcome |
|---|---|
| pause requested, step in flight | step finishes, its checkpoint lands, then `paused`. The intended path. |
| pause requested on the step that **completes** exploration | the step's checkpoint lands and the pause is honoured there; exploration reads as complete, so the resume skips Phase 3. Nothing lost. |
| pause requested after the agent's `Complete` write | `PauseRun`'s filter (`status ∈ {pending, running}`) matches nothing → 409 "run is not active (status: completed)". |
| pause requested, then the run is **cancelled** | cancel wins. `Ownership` checks `cancelled` first and regardless of attempt (v1's rule); the agent's `Pause` write is also fenced on `status: running`, so it cannot resurrect a cancelled run. |
| pause requested against attempt 1, a resume starts attempt 2 | `BeginResume` `$unset`s the request, and `pause_requested_attempt` fences it even if that `$unset` were lost. |
| pause requested, agent dies before any gate | the run is a `running` zombie — the pre-existing crash case. The startup sweep marks it `failed`, checkpoints survive, it is resumable. The request lapses at its deadline. |
| two Pause clicks | the second gets 409 "a pause is already in progress", unless the first has lapsed (the filter allows a re-request past the deadline). |
| agent exits 0 on pause | all three runners treat a clean exit as "the agent's own terminal write is authoritative" and call no `OnFailure` (`kubernetes.go:664`, `docker.go` `streamWaitRemove` status 0, `subprocess.go` `cmd.Wait()` nil). So nothing overwrites `paused`. |

---

## 5. Implementation, by area

### 5.1 Agent — the cooperative stop

**`services/agent/internal/models/run.go`**
- `RunStatusPaused = "paused"`, with a comment saying it is written by the agent
  and is non-terminal.
- The four fields from §3.2.
- Extend the `RunLifecycleEvent.Status` doc comment to list `paused`.

**`services/agent/internal/database/run_repo.go`**
- `RunOwnership` gains `RunPauseRequested`: *owned by this attempt, and an
  operator has asked for a cooperative stop*. Documented as **not** a loss of
  ownership — that distinction is the one thing that must not be got wrong (see
  the `OwnsRun` note below).
- `Ownership` projects `pause_requested_at`, `pause_deadline_at`,
  `pause_requested_attempt` and returns `RunPauseRequested` **after** the
  cancelled / discarded / taken-over checks, and only when the request is
  honourable: non-nil, deadline unset or in the future, and
  `pause_requested_attempt` is 0 or equal to this attempt.
- New `Pause(ctx, runID, attempt) (bool, error)`, modelled on `Fail`:
  - filter: `_id`, `status: "running"`, `discarded_at: nil`, `attemptFilter`;
  - `$set`: `status: paused`, `last_paused_at: now`, `phase_detail`, `updated_at`;
  - `$unset`: the three pause-request fields;
  - `$push`: one `lifecycle` event `{status: "paused", at, attempt, reason}` — in
    the same update as the status, for the reason `FailAttempt` gives: this is
    the record that survives a resume, and `BeginResume` clears the scalar
    fields;
  - **no `completed_at`** — a paused run has not completed, and
    `GetLatestByProject` and the dashboard both read that field.
  - returns applied-ness, so a run that moved on (cancelled, swept to `failed`,
    taken over) is reported rather than silently claimed.

**`services/agent/internal/discovery/status.go`**
- `runStanding` (3 values: `standingCarryOn`, `standingPauseRequested`,
  `standingLost`) and `Standing(ctx) runStanding`, one read, replacing the body
  of `OwnsRun`.
- `OwnsRun` becomes `Standing(ctx) != standingLost`. **A pause request must read
  as still-owned here**, or every non-pausable gate past the pause window would
  abort a working run as superseded.
- `classifyUnappliedTerminal` must handle `RunPauseRequested` in the same arm as
  `RunOwnedByThisAttempt`. Its `default:` arm returns `terminalSuperseded`,
  which **deletes this attempt's result** — so a missing case here destroys a
  complete discovery. This gets its own test.
- `Pause(ctx, reason) bool` wrapping `repo.Pause`, plus an `AddStep` of an
  `info` row into the live feed so the operator's log explains itself.
- Add `Pause` to the `runDocWriter` interface.

**`services/agent/internal/ai/exploration.go` / `exploration_resume.go`**
- `ErrPauseRequested` next to `ErrAttemptSuperseded`, documented as a *clean
  stop*, not a failure.
- `ExplorationEngine.checkpoint` returns `error` (nil / `ErrAttemptSuperseded` /
  `ErrPauseRequested`) instead of `superseded bool`; ordinary write failures stay
  logged-and-swallowed. Both call sites in the loop propagate it.
- `replayOutcome` gains `Paused`; `replayPrefix` sets it when `fenceReplay`
  returns `ErrPauseRequested`; `Explore` returns `(result, ErrPauseRequested)`.
- Doc comments on `StepPersister` / `StepFence` gain the new return.

**`services/agent/internal/discovery/orchestrator_resume.go`**
- `checkpointStep`: the pre-write probe becomes `Standing`. A pause does **not**
  skip the write — it is remembered, `SaveStep` and `MarkExplorationCheckpoint`
  run as normal, and only then does it return `ErrPauseRequested`. That ordering
  *is* the acceptance criterion ("stops after the current step with a clean
  checkpoint"). Supersession still short-circuits before the write, unchanged.
- `fenceReplayedStep`: same, minus any write.
- `checkpointAnalysisArea` (v2's, in `analysis_resume.go`): the same change as
  `checkpointStep`, and it is the same five lines — its pre-write probe becomes
  `Standing`, the area checkpoint is written regardless, and it returns
  `ErrPauseRequested` afterwards. The symmetry is not a coincidence: v2 built it
  as v1's twin, with the same `OwnsRun` probe, the same write-before-shared-writes
  ordering and the same `ErrAttemptSuperseded` return.
- `pauseAttempt(ctx) error` — the single owner of the transition: book this
  attempt's active time (`AddActiveTime`, so elapsed stays honest across the
  pause), call `statusReporter.Pause`, and return `ErrPauseRequested` when it
  applied or `ErrAttemptSuperseded` when it did not, with a precise log line at
  the call site either way.
- `hasResumePoint() bool` → `o.keepStepIndex`, documented.
- `stopBefore(ctx, what) error` — the pausable gate: `Standing`, then either nil,
  `ErrAttemptSuperseded`, or `pauseAttempt` (when `hasResumePoint()`; otherwise
  carry on and log once). `ownershipLost` is left exactly as it is for the gates
  outside the pause window.

**`services/agent/internal/discovery/orchestrator.go`**
- `o.attemptStartedAt` field, set from the existing `startTime`, so
  `pauseAttempt` can book active time.
- Two gates convert from `ownershipLost` to `stopBefore`: `"rebuilding the step
  index"` (1273) and `"analysis"` (1393). Every other gate is untouched.
- The `Explore` error branch gains `errors.Is(err, ai.ErrPauseRequested)` →
  `return nil, o.pauseAttempt(ctx)`. Returning **before** the persist tail is
  deliberate: a pause saves no discovery, so there is no partial result for a
  later resume to retire, and `discardCheckpoints` (gated on `terminalClaimed`)
  never runs, so the checkpoints and the per-run vector index survive — which is
  what makes the run resumable.

**`services/agent/agentserver/agentserver.go`**
- `runDiscovery`'s error branch: `errors.Is(err, ai.ErrPauseRequested)` → log and
  `return nil` (exit 0), before the failure notification and
  `TrackDiscoveryFailed`. A pause is not a failure and must not be announced or
  banked as one.

### 5.2 API — request, resume, cancel, discard

**`services/api/models/run.go`**
- The four fields (JSON for all but `pause_requested_attempt`).
- `RunPhaseAcceptsPause(phase string) bool` over `{"", "init",
  "schema_discovery", "exploration", "analysis"}`, with a keep-in-sync note
  pointing at the agent's `models.Phase*` constants (the same pattern as
  `agentDefaultDiscoveryMaxDuration`) and a test pinning the strings.
  `analysis` is in the set because v2 makes each area durable — see §4.

**`services/api/database/run_repo.go`**
- `RequestPause(ctx, runID, attempt, deadline) (*models.DiscoveryRun, error)` —
  `FindOneAndUpdate`, filter `_id` + `status ∈ {pending, running}` +
  `discarded_at: nil` + `attemptFence(attempt)` + `$or: [{pause_requested_at:
  nil}, {pause_deadline_at: {$lt: now}}]` (so a duplicate request is refused but
  a lapsed one can be re-made). `ErrNoPausableRun` on no match.
- `BeginResume`: filter status becomes `{$in: ["failed", "paused"]}`; `$unset`
  gains the three pause-request fields (a stale request must not pause the new
  attempt the moment it starts). `last_paused_at` is **kept** — it is history.
- `Cancel`, `Fail`, `FailAttempt`, `CleanupStaleRuns`: `$unset` the three
  request fields so no document carries a request nobody can honour.
- `ClearExplorationCheckpointMarker`: its `status: "failed"` precondition becomes
  `{$in: ["failed", "paused"]}`, or a paused run whose checkpoints expired keeps
  offering a Resume that can only 409.
- `Discard`'s status filter is left at `{failed, cancelled}` — see below.

**`services/api/database/interfaces.go`** — `RequestPause` on `RunRepo` (and the
handler's `mock_test.go` mock).

**`services/api/internal/handler/discoveries.go`**
- `PauseRun` (`POST /api/v1/runs/{runId}/pause`), shaped like `ResumeRun`:
  canonicalise `runID = run.ID` (the uppercase-hex/lowercase-string trap the
  other three run routes each document), project ACL check (this route is outside
  `ProjectACLMiddleware`), status gate, `RunPhaseAcceptsPause` gate, then
  `RequestPause`. 202 with `{status: "pause_requested", run_id, attempt,
  deadline}`. No runner call, no policy call, no checkpoint call.
  - 404 run / project unknown; 403 no project access; 409 not active, past
    exploration, or already pausing.
- `ResumeRun`: accept `paused` as well as `failed`; reword the 409.
- `CancelRun`: accept `paused` in the active-status gate. Cancel on a paused run
  has no workload to kill (the `agentRunner.Cancel` call is a harmless no-op that
  still defends a superseded agent being alive) but must still write `cancelled`,
  purge the checkpoints and confirm the reservation — which is exactly what the
  existing body already does.
- `DiscardRun`: **refuse** `paused`, with "a paused run is not finished — resume
  it, or cancel it to throw it away". This is not tidiness: Discard leaves the
  status alone, so a discarded-while-paused run would never appear in
  `ListTerminalWithReservation` and would leak its concurrent-run slot forever.
  Routing it through Cancel closes that.

**`services/api/internal/server/server.go`** — `POST /api/v1/runs/{runId}/pause`
with `withRole(member)`, matching resume: pause preserves work rather than
destroying it. Cancel and discard stay `admin`.

**Config** — `DISCOVERY_PAUSE_DRAIN_TIMEOUT`, read in the handler via the
existing `getEnvOrDefault`, parsed as a Go duration, default `30m`; invalid
falls back to the default with a warning, `0` means no deadline. API process
only — nothing is added to `agentForwardedEnvKeys`.

### 5.3 Dashboard

**`src/lib/api.ts`** — `pause_requested_at?`, `pause_deadline_at?`,
`last_paused_at?` on `DiscoveryRunStatus`; `pauseRun(runId)` with the same
"409 is a real answer, surface the server's message" note the other run calls
carry.

**`src/app/projects/[id]/page.tsx`**
- `showRunPanel` must include `paused`, or the panel vanishes the moment the run
  pauses and the Resume button with it. Likewise the step-poll's `isTerminal`
  and `LiveRunPanel`'s elapsed `isTerminal` must count `paused` as settled — the
  elapsed calculation adds `since(last_resumed_at)` to `active_ms` for anything
  not terminal, which would double-count a paused run's compute.
- `justFinished` / `isDone` stay as they are: a paused run is not finished, so it
  gets no Dismiss and no Discard.
- Buttons: live → **Pause** + **Cancel**; paused → **Resume from step N** +
  **Cancel**. Cancel becomes red + modal-confirmed (the dashboard's destructive
  tier, same weight as Discard's existing modal), saying plainly that it discards
  the run and its checkpoints and cannot be undone — which is the whole point of
  putting it next to a Pause that does not.
- The `onCancel` prop currently does double duty as **Dismiss** on a finished
  run. Split it into `onCancel` and `onDismiss` so the local per-tab hide does
  not inherit a confirmation dialog.
- Header state: `paused` → amber "Discovery paused"; `running` with a live
  `pause_requested_at` → "Pausing — finishing the current step"; with a lapsed
  one → "Pause request timed out", which is the only thing the deadline is
  rendered for.
- `onPause` handler: optimistic marker install so the 2s poll keeps running, and
  the same refuse-then-`getRun`-reconcile pattern `onResume` uses.

**`src/components/projects/ProjectRunStatus.tsx`** — an explicit `paused` branch
(amber "Paused") ahead of the forward-compatible fallback that would otherwise
render a lowercase grey "paused".

**Not in this repo:** `app/projects/[id]/page.tsx` is one of the dashboard files
a deployment overlay replaces *wholesale* rather than patching, so the same
change must land there too or those deployments get no Pause CTA. **A paired
plan PR is open on an identically-named branch** (`jale/issue-508`); the API and
agent halves are shared and need nothing, and `src/lib/api.ts` and
`ProjectRunStatus.tsx` are not overlaid, so the overlay work is one file.

That pairing needs nothing from this plan beyond the tests §6 already specifies.
The composed build is tested with the overlay applied, and the suite it runs is
**this repo's** — `ProjectPage.test.tsx` imports `@/app/projects/[id]/page` and
renders it, so with the overlay in place it asserts the overlay's copy. The test
file is not itself overlaid, which is what keeps that true. So the Pause,
Cancel-confirm and paused-Resume assertions written here are also what catches a
missed sync over there, and nothing extra is owed.

Worth knowing while writing them: a button that only the overlay could get wrong
is still asserted by a test that lives here, so §6's run-panel cases should
assert visible text and behaviour rather than internals — which is how they are
already written.

### 5.4 Docs (Rule 4)

- `docs/concepts/discovery-lifecycle.md` — a **Pausing a run** subsection under
  "Checkpointing and resume": the two CTAs, the pause window and why it closes
  after Phase 4 entry, the drain timeout and lapse, and what resuming a paused
  run re-uses. Amend "Cancel stays terminal" to contrast the two, and the
  "Across attempts" field list.
- `docs/reference/api.md` — `POST /api/v1/runs/{runId}/pause` with its response
  and every 409; update resume's resumable statuses, the `GET /runs/{runId}`
  field table, and the cancel section.
- `docs/reference/data-models.md` — `status` enum gains `paused`; four rows in
  the `DiscoveryRun` table; `paused` in the `RunLifecycleEvent` note.
- `docs/reference/configuration.md` — `DISCOVERY_PAUSE_DRAIN_TIMEOUT` in the API
  table, sized against `LLM_TIMEOUT`.
- `helm-charts/decisionbox-api/values.yaml` — commented example next to
  `DISCOVERY_CHECKPOINT_RETENTION`; `docs/reference/helm-values.md` to match.
- `CHANGELOG.md` — one Unreleased entry.
- `services/api/models/project.go` — the `LastRunStatus` comment lists the
  statuses; add `paused`.

---

## 6. Test strategy (Rule 9 — failure and edge cases first)

### Agent unit
- `Ownership` precedence: cancelled > discarded > taken-over > pause-requested >
  owned; a pause request for a **different** attempt is ignored; a **lapsed**
  request is ignored; `pause_deadline_at` absent means no lapse.
- `OwnsRun` returns **true** under `RunPauseRequested` (the regression that would
  abort every working run past the pause window).
- `classifyUnappliedTerminal` under `RunPauseRequested` returns
  `terminalUnknown`, not `terminalSuperseded` — i.e. it does **not** delete the
  result. Asserted on both `Complete` and `Fail`.
- `StatusReporter.Pause`: applied / not-applied, and disabled-reporter no-op.
- Exploration engine: a pause at the step seam returns `ErrPauseRequested`
  **after** `PersistStep` was called (ordering asserted explicitly, since it is
  the AC); a pause during replay stops without writing checkpoint rows; a pause
  on the `complete_rejected` path; supersession still beats a pause.
- `stopBefore`: pauses at a pausable gate, carries on when `hasResumePoint()` is
  false, returns `ErrAttemptSuperseded` when the pause write does not apply.
- The per-area seam: a pause returns `ErrPauseRequested` **after**
  `checkpointAnalysisArea` has written the area (the same ordering assertion as
  the exploration seam); a pause on the LAST area is honoured rather than
  falling through to Phase 5; the mid-area `"validating area X"` gate declines
  it; and the run then resumes restoring the K completed areas and analysing
  only the remainder — v2's own acceptance, re-asserted through a pause instead
  of a kill.
- `pauseAttempt` books active time exactly once.

### Agent integration (testcontainers, Mongo)
- `RunRepository.Pause` fences: wrong attempt → not applied; `cancelled` run →
  not applied; `discarded_at` set → not applied; applied write sets
  `last_paused_at`, pushes the lifecycle event, `$unset`s the request, and leaves
  `completed_at` **unset**.
- `Ownership` against really-written documents for each precedence case.
- Checkpoints and the per-run vector index survive a pause (the run is still in
  `ResumableOrActiveIDs`) — **both** collections, exploration and per-area, since
  v2 is underneath.

### API unit (handler)
- `PauseRun`: 202 happy path; 404 unknown run / missing project; 403 without
  project access; 409 on `completed`/`failed`/`cancelled`; 409 past exploration
  naming the phase; 409 on a duplicate request; 202 when the previous request has
  lapsed; no runner and no policy call is made (counting fakes, matching how
  `ResumeRun`'s "no metering" is asserted today).
- `ResumeRun` from `paused`; its 409 text for a `cancelled` run still reads
  correctly.
- `CancelRun` on a `paused` run: writes `cancelled`, purges checkpoints,
  confirms the reservation.
- `DiscardRun` refuses `paused`.

### API integration (testcontainers, Mongo)
- `RequestPause` filter matrix, including the lapsed-re-request `$or` and the
  attempt fence.
- `BeginResume` from `paused`, and that it `$unset`s the request.
- `Fail` / `FailAttempt` / `Cancel` / `CleanupStaleRuns` clear the request.
- `ClearExplorationCheckpointMarker` works on a `paused` run.
- The §3.1 predicate table, asserted against real documents — in particular that
  a `paused` run does not block a fresh trigger and does not fire completion
  hooks.

### Dashboard (`src/__tests__/ProjectPage.test.tsx`, `api.test.ts`)
- The panel stays up on `paused`; Pause and Cancel are rendered while live;
  Resume and Cancel while paused; Dismiss/Discard are not offered on `paused`.
- The Cancel confirmation modal gates the call; Dismiss does not.
- "Pausing…" while a request is live, "timed out" past the deadline, and neither
  on a completed run that carries a stale marker.
- Elapsed does not double-count on a paused run.
- Step polling stops on `paused`.

### Live acceptance (the part composition cannot prove)
ACs 1 and 2 are claims about a **live run**, and the decision points sit inside
`RunDiscovery` and the exploration loop — the region v1's report identified as
having no test seam and as the home of five of that epic's defects. So they get
run for real on the `dbx438-*` dev stack, against images rebuilt from this
branch's tip and a **fresh project** (prompts are snapshotted at project
creation), extending `verify-ac.sh` with a `pause` mode:

1. start a run, wait for N checkpoints, `POST .../pause`;
2. snapshot `discovery_checkpoints` **while the run is still live** — a terminal
   run deletes its own, so a post-hoc query is structurally empty;
3. assert the run lands `paused` with `last_paused_at`, the request cleared, and
   the highest checkpoint ≥ the step that was in flight;
4. resume, and assert from the agent log that `Exploration step starting` /
   `Executing exploration action` fire **only** for steps after the prefix — the
   absence of those lines over the prefix is the "no re-paid exploration" claim
   itself;
5. a second pass pausing at the Phase 4 entry gate, asserting
   `skipping Phase 3 entirely` on the resume;
6. a third pass pausing MID-ANALYSIS, asserting `Analysis area restored from a
   previous attempt — not re-run` for the areas that were already checkpointed
   and an analysis call for only the rest — the v2 seam, which is the one a
   composition test cannot prove either;
7. cancel a paused run and assert it is terminal, both checkpoint sets purged,
   and a re-resume 409s.

Capture the agent log **before** cancelling — cancel reaps the container
immediately and the log is gone for good.

### Local gates before the PR
`make build`, `make test-go`, `make lint-go`, `make test-ui`, `make lint-ui`,
plus `go vet -tags=integration ./...` and the discovery/database integration
targets (CI skips Integration Tests on PRs). All prefixed `GOTOOLCHAIN=auto`.

---

## 7. Risks

| Risk | Mitigation |
|---|---|
| `classifyUnappliedTerminal`'s `default:` arm deletes a result — a missing `RunPauseRequested` case would destroy a complete discovery | explicit case + a dedicated test on both `Complete` and `Fail`; this is called out as the single highest-consequence line in the change |
| `OwnsRun` reading a pause request as "lost" would abort working runs at every non-pausable gate | `OwnsRun` is defined as `Standing != standingLost`, with a test |
| A `paused` run with no checkpoint would be unresumable | the `hasResumePoint()` precondition, plus the exploration-step seam where a checkpoint has just landed by construction |
| A status list somewhere treats `paused` as active (or as terminal) | the §3.1 table is enumerated from the code and each row is asserted in integration tests |
| The dashboard panel disappearing on `paused` (it is gated on `isRunning || justFinished`) | explicit test |
| The overlaid dashboard page silently missing the Pause CTA | flagged for a parallel PR before merge |
| Clock skew between API and agent around `pause_deadline_at` | the granularity is tens of minutes; a skewed agent either pauses slightly early or lapses slightly late, both harmless |
| Scope creep into the concurrency-at-resume decision | explicitly not built; the one new leak it would open (discard-while-paused) is closed by refusing that discard |

---

## 8. Alternatives considered

- **SIGTERM as the pause signal.** Rejected: the agent has no signal handling at
  all today (#270), so this is a prerequisite feature, not a detail; it needs a
  grace window longer than one exploration step in all three runners; and it
  makes an operator pause indistinguishable from a pod eviction, which is exactly
  the crash case v1 already covers.
- **A `pausing` status instead of a marker.** Rejected: during the drain an agent
  is alive, so every "is this run active" predicate must keep saying yes — six of
  them, each a correctness bug if missed, in exchange for nothing the marker does
  not give.
- **Reusing Cancel with a `graceful=true` flag.** Rejected: cancel's handler
  purges checkpoints and writes a terminal status; the overlap is the HTTP verb
  and nothing else, and conflating them is how an operator ends up hard-killing a
  run they meant to keep.
- **Auto-escalating a drained-out pause to a hard kill.** Rejected: it would
  destroy the checkpoints the operator explicitly asked to keep, and could land
  during the persist tail and lose a finished discovery. The lapse-and-say-so
  design leaves the escalation to the operator, who has Cancel.
- **Honouring a pause at every gate.** Rejected: pausing before Phase 7 discards
  a complete analysis + validation + recommendation pass that the resume re-pays
  in full, seconds before the run would have finished.
- **Releasing the plan reservation on pause.** Rejected: a resume opens none, so
  there would be nothing to reopen. This is the out-of-scope concurrency
  decision, left where v1 left it.

---

## 9. Out of scope / follow-ups

- Per-**insight** (v3) resume granularity. v2 is a *dependency* now rather than
  a follow-up — see §11 — so a pause mid-analysis keeps the areas already done;
  what it still re-pays is the single area in flight, and Phases 5–7 in full.
  v3 would shrink both, and needs nothing from v4 to do so.
- **Concurrency-at-resume policy** — a paused run holds its concurrent-run
  reservation, and a fresh run can start alongside it (then blocking the resume),
  which is today's behaviour for a `failed` run. Flagged in the issue as a
  follow-up decision; a GitHub issue will be opened for it rather than a TODO
  left in the code (Rule 3).
- A paused run can still be hidden by a *newer* run in `GetLatestByProject`,
  exactly as a failed one can. Documented, not widened.
- Automatic pause (budget caps, schedules). Nothing pauses a run except an
  operator.

---

## 10. Acceptance criteria → evidence

| AC | Evidence |
|---|---|
| An operator can Pause a running discovery; it stops after the current step with a clean checkpoint and lands in `paused` | live `verify-ac.sh pause`: `paused` + `last_paused_at` + highest checkpoint ≥ the in-flight step; unit test pinning that `PersistStep` is called **before** `ErrPauseRequested` is returned |
| A `paused` run can be resumed and completes, re-using work up to the pause point (no re-paid exploration) | live resume: zero `Exploration step starting` / `Executing exploration action` over the replayed prefix; a second pass pausing at Phase 4 entry logs `skipping Phase 3 entirely`; a third pausing mid-analysis logs `Analysis area restored from a previous attempt` for the areas already done |
| Cancel remains a terminal hard-kill, now with a confirmation dialog; Pause and Cancel are distinct CTAs | handler tests (cancel on running and on paused); dashboard tests for both CTAs and the modal; live cancel of a paused run → terminal, checkpoints purged, re-resume 409 |
| Same `runID` across pause/resume; lifecycle metadata records the pause/resume events | integration test on the `lifecycle` array (`paused` by the agent, `running` by `BeginResume`) plus `last_paused_at` / `last_resumed_at`; live run asserts one `run_id` throughout |

---

## 11. v2 (#506 / PR #510) is a dependency, not a neighbour

The issue says v4 is independent of v2, and as a *dependency* statement that is
still true — v4 needs only v1's plumbing. But **#510 is ready to merge**, so in
practice v4 is built on top of it, and that changes one v4 decision rather than
none. Checked against the branch, not the issue text.

### What v2 does not disturb

#510 touches none of the machinery v4 extends: `RunOwnership`, `Ownership`,
`OwnsRun` and `classifyUnappliedTerminal` are untouched, and both v2 and v3 add
no run control and no new status — so nothing contends for the status field, the
pause marker or the ownership probe. Two things come out free:

- v2 extends `CheckpointRepo.DeleteByRun` to purge the per-area rows alongside
  the exploration rows, so v4's cancel-a-paused-run path picks that up with no
  change — the handler already calls `DeleteByRun`.
- v2's `discardAnalysisCheckpoints` is the twin of `discardCheckpoints`, and
  v4's pause path returns before the terminal section, so neither runs. A paused
  run keeps both sets, which is what makes it resumable, and both are bounded by
  the same retention.

`ResumeState` is also unchanged: v2's API-side use of the analysis collection is
purge-only, so resumability is still decided by the exploration prefix or
summary — which a run paused mid-analysis has.

### What v2 changes — the pause window, now in scope

An earlier draft of this plan closed the window after the Phase 4 entry gate,
**because under v1 alone a mid-analysis pause discards every area already
analysed**. v2 removes that reason: `checkpointAnalysisArea` writes a durable
per-area checkpoint — the area, its insights and its validation — at the end of
each area's iteration, before the shared display writes. It is the exact
structural twin of v1's `checkpointStep`: same `OwnsRun` probe, same ordering,
same `ErrAttemptSuperseded` return.

So the window includes the per-area seam, and §4 and §5.1 now say so. It is five
lines against that twin, and leaving it out would have Pause decline
mid-analysis for a reason that was no longer true — which is why it is in scope
rather than a follow-up: the pause window reflecting what resume can actually
re-use *is* the feature.

What stays declined is everything with no checkpoint behind it: the mid-area
gates, and Phases 5–7. v3 would make those cheap too, one rung finer, and the
same per-seam rule absorbs it with no v4 redesign. v3 is not started.

### Sequencing

v4's build step starts from a `main` that has #510, and rebases if it lands
mid-flight. The conflict surface is textual, not semantic: #510 inserts ~20
lines immediately after the `ownershipLost(ctx, "analysis")` gate that v4
converts to `stopBefore`, and ~25 lines at the head of the per-area loop; both
add helpers to `orchestrator_resume.go` and `status.go`; both edit
`models/checkpoint.go`, `agentserver.go` and the same sections of
`discovery-lifecycle.md`, `api.md`, `data-models.md`, `configuration.md`, the
API chart values and `CHANGELOG.md`. Neither touches `models/run.go`, where
v4's new fields live.

If #510 is somehow reverted or held, v4 still ships — the per-area rows in §4's
table simply have no seam to attach to, and the window reverts to closing at the
Phase 4 entry gate. Nothing else in the plan depends on it.

---

**This is a PLAN for review — no implementation is included in this PR.
Implementation follows after approval.**

Closes #508

— Co-coded with Jale 🤖
