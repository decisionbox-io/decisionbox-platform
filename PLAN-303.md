# Plan — Issue #303: Chat-client API (discovery + per-user sessions + rename), chat-app bridge

> **Status:** PLAN for review — implementation follows after approval.
> **PR base branch:** `chat-app` (the issue mandates *all* PRs target `chat-app`, not `main`).
> This branch (`jale/issue-303`) was cut from `origin/main`; at time of writing
> `chat-app` and `main` point at the same commit (`4c4cf63`), so no rebase is
> needed for the plan PR. The build PR will rebase onto `origin/chat-app` if it
> has advanced.

## 1. Problem restatement

The **DecisionBox Chat** mobile client (`decisionbox-io/decisionbox-chat-app#1`)
is a thin, request/response-only client that talks 100% to a deployment's API.
A code-grounded review established that the client **reuses the existing Ask
endpoints** (`POST /ask`, `GET/DELETE /ask/sessions[/{sid}]`, `GET /projects`,
`GET /me`) and needs only **three genuine server-side deltas**:

1. **Discovery** — a raw, **unauthenticated** `GET /.well-known/decisionbox` so the
   client can learn the API version, feature set, and auth mode (OIDC vs none)
   *before* login. Nothing exposes auth config pre-login today.
2. **Per-user session scoping** — Ask sessions are currently owned by a hardcoded
   `"anonymous"` and listed/deleted with no owner check. The client must give
   each authenticated user their own conversations: list/get/delete scoped to the
   caller, delete allowed for the owner (not admin-only). NoAuth behaviour must be
   preserved.
3. **Rename** — `PATCH /api/v1/projects/{id}/ask/sessions/{sid}` `{title}` so a
   user can rename a conversation.

Everything else the client needs already exists and is out of scope.

## 2. Verified facts (read from the code, not assumed)

- **Response envelope:** `writeJSON` wraps every success as `{"data": …}`; errors
  are `{"error"[,"code","details"]}` (`services/api/internal/handler/response.go`).
  → The discovery endpoint must bypass `writeJSON` and emit **raw** JSON.
- **Ask handler** (`search.go`): new community-RAG sessions are created with
  `UserID: "anonymous"` (`search.go:776`); an existing `session_id` is checked for
  project match only (`search.go:698`), no owner check.
- **Session list** (`ask_session_repo.go:137` `ListByProject`) filters by
  `project_id` only. `GetByID` has no owner filter. `Delete` is unconditional; the
  route is **admin-only** (`server.go:274`).
- **Auth:** `auth.UserPrincipal{Sub,Email,OrgID,Roles[]}`; `auth.FromContext`
  reads it from the request context; `NoAuthProvider` injects
  `Sub:"anonymous", Roles:["admin"]` (`libs/go-common/auth/noauth.go`). Default
  provider is NoAuth (`apiserver.go:165` → `auth.GetProvider()`).
- **Pre-auth mount pattern:** `server.go:324-328` builds a `root` mux; `/health*`
  is mounted **directly** on `root` (no auth middleware), and only `/` is wrapped
  by `authProvider.Middleware()`. The whole thing is wrapped by CORS + logging.
  → The discovery route mounts on `root` the same way.
- **Plugin-registry precedent:** `libs/go-common/systeminfo` is a go-common
  registry that out-of-tree builds (enterprise) fill via `init()` and a plain
  community handler (`handler/system.go`) reads at request time. `askoverride`,
  `discoverytrigger`, `runhooks`, the `auth`/`policy`/`sources` registries follow
  the same shape. → The discovery **auth config** uses this exact pattern so the
  enterprise auth plugin can inject OIDC details.
- **Enterprise `HandleAsk`** already sets `UserID: callerSub(r)` and checks project
  on an existing `session_id`, but **not owner** — that owner check is the paired
  enterprise change tracked by `decisionbox-io/decisionbox-enterprise#124` (out of
  scope here; noted for coordination).
- **Indexes:** `ask_sessions` already has `{project_id, updated_at:-1}` and
  `{user_id, updated_at:-1}` (`init.go:130-131`). The new per-user list wants a
  compound `{project_id, user_id, updated_at:-1}`.
- The enterprise agentic Ask **replaces** `POST /ask` via `askoverride`, so on
  enterprise deployments the community `Ask` owner logic is a no-op path; it still
  must be correct for community/NoAuth deployments and defense in depth.

## 3. Scope

**In scope (the three deltas, community/platform side only):**
- New unauthenticated discovery endpoint + a go-common registry seam for auth config.
- Per-user scoping of Ask sessions (create/read/list/delete + owner checks).
- Rename endpoint + repo `UpdateTitle`.
- Tests (unit + real testcontainer integration), docs, CHANGELOG.

**Explicitly NOT in scope** (per issue): no `/models`, no SSE, no upload, no
offset pagination, no live-response `tool_events` change, no source normalization,
no parallel thread/message API, no dashboard changes, no enterprise code changes
(enterprise owner check + mobile-client-id live in enterprise #124).

## 4. Design decisions

### 4.1 Discovery endpoint & the auth-config seam

The community platform is always NoAuth; OIDC config (issuer, mobile client id,
scopes, audience) lives in the enterprise auth plugin. So the discovery endpoint
reads its **auth block** from a new go-common registry that the enterprise plugin
fills — mirroring `systeminfo`. The community handler owns the static
`api_version` + `features`; the registry supplies `auth` (+ optional `branding`).

- **New package `libs/go-common/wellknown`** (importable by both the API service
  and enterprise, exactly like `libs/go-common/auth`):

  ```go
  package wellknown

  type OIDC struct {
      Issuer         string   `json:"issuer"`
      MobileClientID string   `json:"mobile_client_id"`
      Scopes         []string `json:"scopes"`
      Audience       string   `json:"audience"`
  }
  type Auth struct {
      Type string `json:"type"`          // "oidc" | "none" — always explicit
      OIDC *OIDC  `json:"oidc,omitempty"` // set iff Type=="oidc"
  }
  type Config struct {
      Auth     Auth            // required
      Branding json.RawMessage // optional passthrough; overlays may set, community leaves nil
  }
  func Register(c Config)          // called from init() by the enterprise auth plugin
  func Get() (Config, bool)        // read by the community handler; ok=false when unset
  func ResetForTest()              // test-only
  ```
  Thread-safe (`sync.RWMutex`). Doc comment states the **invariant**: any build
  that enables auth MUST `Register` a `Config{Auth:{Type:"oidc", OIDC:…}}` in the
  same `init()` that registers the OIDC provider, so the advertised auth mode can
  never diverge from the enforced one.

- **New handler `services/api/internal/handler/wellknown.go`** — a plain
  `http.HandlerFunc` (no deps, like `handler.SystemInfo`) that writes **raw** JSON
  (its own `json.NewEncoder`, *not* `writeJSON`):

  ```go
  const apiVersion = "v1"
  var features = []string{"projects", "grounded_chat", "ask_sessions", "me", "ask_session_rename"}

  type discoveryResponse struct {
      APIVersion string            `json:"api_version"`
      Features   []string          `json:"features"`
      Auth       wellknown.Auth    `json:"auth"`               // always present, explicit type
      OIDC       *wellknown.OIDC   `json:"oidc,omitempty"`     // top-level sibling, iff type=="oidc"
      Branding   json.RawMessage   `json:"branding,omitempty"`
  }
  ```
  Logic: read `wellknown.Get()`; if unset → `Auth{Type:"none"}`. Emit the top-level
  `oidc` block iff `Auth.Type=="oidc"` (copied from `Auth.OIDC`). Content-Type
  `application/json`, 200. The JSON matches the issue's contract exactly:
  `{ api_version, features[], auth:{type}, oidc?{…}, branding? }`.

  *Static features:* the issue enumerates the five flags flatly for this phase, so
  they are a static list (Rule 8 — no capability-derived gating). Alternative
  considered in §9.

- **Mount (unauthenticated), `server.go`:** add, alongside the health mounts on
  `root`, before the auth-wrapped `/`:
  ```go
  root.HandleFunc("GET /.well-known/decisionbox", handler.WellKnown)
  ```
  It sits outside `authProvider.Middleware()` (so it is pre-login) but inside CORS +
  logging (like health). Non-GET → 405 via net/http method routing.

### 4.2 Per-user session scoping

- **Caller helpers** — new `services/api/internal/handler/caller.go`:
  ```go
  func callerSub(r *http.Request) string      // principal.Sub, else "anonymous"
  func callerIsAdmin(r *http.Request) bool     // true iff principal has role "admin"
  func canAccessSession(r, s) bool             // s.UserID == callerSub(r) || callerIsAdmin(r)
  ```
  Mirror the enterprise `callerSub`/`callerRole`. Under NoAuth the middleware
  injects `Sub:"anonymous", Roles:["admin"]`, so `callerSub=="anonymous"` and
  `callerIsAdmin==true` → every check passes → **NoAuth behaviour preserved**.

- **`Ask` (`search.go`):**
  - `search.go:776` `UserID: "anonymous"` → `UserID: callerSub(r)`.
  - `search.go:695-705` existing-session branch: keep the project-match check
    (unchanged 400 for cross-project, preserving `TestAsk_SessionProjectMismatch`),
    and **add an owner check** — when the session is found and project matches but
    `!canAccessSession(r, session)`, return `404 "session not found"`
    (privacy-preserving; don't reveal another user's session). The not-found
    leniency is left as-is (Rule 8 — minimal change).

- **List** (`ListAskSessions` + repo): repo `ListByProject(projectID, limit)` →
  `ListByProjectAndUser(projectID, userID, limit)` filtering
  `{project_id, user_id}` sorted `updated_at:-1`; handler passes `callerSub(r)`.
  This is strict per-user scoping (an enterprise admin sees only their own list —
  intended: this is the user's chat inbox). Under NoAuth all sessions are owned by
  `"anonymous"` → all returned, unchanged.

- **Get** (`GetAskSession`): after the existing project check, add owner-or-admin:
  `!canAccessSession(r, session)` → `404 "session not found"`.

- **Delete** (`DeleteAskSession` + route): route role `admin`→`viewer`
  (`server.go:274`); handler adds owner-or-admin: `!canAccessSession(r, session)`
  → `404 "session not found"`. Success response unchanged
  (`200 {"data":{"status":"deleted"}}`).

  *Get/Delete are owner-**or-admin*** (not owner-only): an admin retains access to
  a specific known session, symmetric with delete, while **list** stays strictly
  per-user. This matches the issue's "a user can delete their own conversation
  (owner-or-admin)" and keeps admin from being locked out of a session it can
  delete. Rationale recorded in §9.

### 4.3 Rename

- **Route** (`server.go`, viewer at the route level, owner-or-admin in the handler):
  ```go
  mux.HandleFunc("PATCH /api/v1/projects/{id}/ask/sessions/{sessionId}", withRole(viewer, search.RenameAskSession))
  ```
- **Handler `RenameAskSession` (`search.go`):** bound the body with
  `http.MaxBytesReader` (small, e.g. `1<<20`); decode `{title}`; `strings.TrimSpace`;
  reject empty → `400 "title is required"`; cap length to a named constant
  (`maxSessionTitleRunes = 200`, truncating by rune to keep the list UI + DB tidy —
  a bound, not a tunable). Load the session, verify project match (404) and
  owner-or-admin (404), then `UpdateTitle`. Respond
  `200 {"data":{"id":sid,"title":title}}` via `writeJSON(map[string]string{...})`.
- **Repo `UpdateTitle(ctx, sessionID, title)`** — `UpdateOne` `$set` title +
  `updated_at`; return an error if `MatchedCount==0` so the handler can surface a
  clean 404 for an unknown id.

### 4.4 Repo / interface / index changes

- `database/interfaces.go` — `AskSessionRepo`: replace `ListByProject` with
  `ListByProjectAndUser(ctx, projectID, userID string, limit int)`; add
  `UpdateTitle(ctx, sessionID, title string) error`.
- `database/ask_session_repo.go` — implement both.
- `database/init.go` — add `ask_sessions` index `{project_id:1, user_id:1, updated_at:-1}`
  for the per-user list. (Existing single-field indexes retained.)

## 5. Files to change (exact)

**New**
- `libs/go-common/wellknown/wellknown.go` — registry (`Config/Auth/OIDC`, `Register/Get/ResetForTest`).
- `libs/go-common/wellknown/wellknown_test.go` — registry unit tests.
- `services/api/internal/handler/wellknown.go` — `WellKnown` raw-JSON handler + `apiVersion`/`features`.
- `services/api/internal/handler/wellknown_test.go` — handler unit tests.
- `services/api/internal/handler/caller.go` — `callerSub`/`callerIsAdmin`/`canAccessSession`.
- `services/api/internal/handler/caller_test.go` — helper unit tests.
- `services/api/database/ask_session_repo_integration_test.go` — testcontainer tests for `ListByProjectAndUser` + `UpdateTitle`.
- `services/api/internal/server/wellknown_http_integration_test.go` — E2E: discovery reachable **without** auth through the full pipeline; non-GET → 405.

**Changed**
- `services/api/internal/server/server.go` — mount discovery on `root`; DELETE role admin→viewer; add PATCH rename route.
- `services/api/internal/handler/search.go` — `Ask` (UserID + owner check); `ListAskSessions` (per-user); `GetAskSession` (owner-or-admin); `DeleteAskSession` (owner-or-admin); new `RenameAskSession`; `maxSessionTitleRunes` const.
- `services/api/database/ask_session_repo.go` — `ListByProjectAndUser`, `UpdateTitle`.
- `services/api/database/interfaces.go` — interface update.
- `services/api/database/init.go` — compound index.
- `services/api/internal/handler/search_test.go` — update `mockAskSessionRepo` (rename `ListByProject`→`ListByProjectAndUser`, add `UpdateTitle`); inject principals into Get/Delete tests; add owner-scoping + rename tests.
- `docs/reference/api.md` — new **Service Discovery** section (`GET /.well-known/decisionbox`) + **Ask Sessions** section (list/get/rename/delete with per-user + owner-or-admin semantics).
- `CHANGELOG.md` — `[Unreleased] → Added` entries for the three deltas.

## 6. Phased implementation steps

1. **Registry seam** — add `libs/go-common/wellknown` + tests; `go test ./libs/go-common/wellknown/`.
2. **Discovery handler + mount** — `handler/wellknown.go`, wire into `server.go`; handler unit tests + server E2E (unauth reachability, raw JSON, 405).
3. **Caller helpers** — `handler/caller.go` + tests.
4. **Repo + interface + index** — `ListByProjectAndUser`, `UpdateTitle`, interface, `init.go` index; integration tests.
5. **Session scoping in handlers** — `Ask` UserID+owner, `ListAskSessions`, `GetAskSession`, `DeleteAskSession`; update mock + existing tests; add owner tests.
6. **Rename** — route + `RenameAskSession` handler + tests.
7. **Docs + CHANGELOG** (Rule 4).
8. **Delete `PLAN-303.md`** in the final build commit (Rule 10); `gh pr ready`.
9. **Local gates** — `make build`, `make test-go`, `make lint-go` (after `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-integration` for the new repo/server integration tests. No UI touched → no `test-ui`/`lint-ui`.

## 7. Data / schema / API / UI impact

- **Data/schema:** no new collection; `ask_sessions.user_id` already exists and is
  now populated with the real caller subject on community-RAG sessions (was
  `"anonymous"`). One additive compound index. **No migration required** — legacy
  community sessions all carry `user_id:"anonymous"` and remain visible under
  NoAuth; a legacy `null` user_id simply won't match a per-user list (acceptable;
  pre-auth artifact).
- **API:**
  - **New:** `GET /.well-known/decisionbox` (unauth, raw JSON);
    `PATCH /api/v1/projects/{id}/ask/sessions/{sid}` (rename).
  - **Behaviour change:** `GET/DELETE .../ask/sessions[/{sid}]` now owner-scoped
    (`DELETE` route relaxed admin→viewer). `GET .../ask/sessions` list is per-user.
    `POST /ask` sets `user_id` from the caller and 404s a non-owned `session_id`.
    All no-ops under NoAuth.
- **UI:** none (no dashboard changes; the chat client is a separate repo). No
  enterprise UI overlay files touched.

## 8. Test strategy (cover failure + edge cases, Rule 9)

**Registry (`wellknown_test.go`)** — unset → `Get` ok=false; `Register` then `Get`
round-trips; `ResetForTest` clears; concurrent Register/Get race-clean (`-race`).

**Discovery handler (`wellknown_test.go`)**
- Unset registry → `auth.type=="none"`, **no** top-level `oidc`, features list exact,
  `api_version=="v1"`.
- Registered `Type:"oidc"` + OIDC → `auth.type=="oidc"` **and** top-level `oidc`
  block with issuer/mobile_client_id/scopes/audience.
- Response is **raw** (no `data` wrapper): assert top-level keys are
  `api_version/features/auth`, not `data`.
- `branding` passthrough emitted only when registered; omitted otherwise.
- Content-Type `application/json`.

**Server E2E (`wellknown_http_integration_test.go`, `//go:build integration`)** —
boot `server.NewWithRouteGroups` with NoAuth (like the route-groups integ test);
`GET /.well-known/decisionbox` with **no** Authorization header → 200 + valid body
(proves it's pre-auth); `POST` same path → 405; a normal `/api/v1/...` route still
requires the auth chain (sanity).

**Caller helpers (`caller_test.go`)** — no principal → `"anonymous"`/not-admin;
NoAuth principal (`anonymous`/`["admin"]`) → admin true; OIDC-style principal →
sub + admin-role detection; `canAccessSession` owner-match, admin-override,
stranger-deny.

**Session scoping (`search_test.go`)**
- `Ask` new session persists `UserID==callerSub` (owner principal in ctx).
- `Ask` with a `session_id` owned by another user + non-admin caller → 404
  (`TestAsk_SessionOwnerMismatch`); owner caller → 200 and history used.
- `TestAsk_SessionProjectMismatch` retained (400, unchanged).
- `GetAskSession`: owner → 200; stranger non-admin → 404; admin → 200
  (update existing success/wrong-project tests to inject a principal).
- `DeleteAskSession`: owner (viewer) → 200; stranger non-admin → 404; admin → 200.
- `ListAskSessions`: handler passes `callerSub` to `ListByProjectAndUser`
  (assert via mock capture).

**Rename (`search_test.go`)** — happy path → 200 `{"data":{"id","title"}}` +
`UpdateTitle` called with trimmed title; whitespace-only/empty → 400; overlong
title truncated to `maxSessionTitleRunes`; non-owner non-admin → 404; unknown
session id → 404 (repo `MatchedCount==0`).

**Repo integration (`ask_session_repo_integration_test.go`, testcontainer Mongo)** —
seed sessions for two users in one project + a third project; `ListByProjectAndUser`
returns only the caller's, correct `updated_at` order, honours limit; `UpdateTitle`
updates title + `updated_at` and errors on unknown id; legacy `user_id:"anonymous"`
sessions returned for the anonymous caller.

## 9. Risks & mitigations; alternatives considered

- **Advertised-vs-enforced auth drift** (discovery says `none` while auth is on):
  mitigated by the registry invariant (enterprise #124 registers the auth block in
  the same `init()` that enables OIDC) and a doc comment. Community alone is always
  genuinely NoAuth → `none` is correct. *Alternative:* derive `type` from the live
  `auth.Provider` in `server.New` and thread it into the handler — rejected as it
  still can't supply OIDC details (those are enterprise-only) and splits the source
  of truth; the registry is the single seam, consistent with `systeminfo`.
- **Behaviour change on session endpoints** could surprise the existing dashboard.
  Mitigated: every check is a no-op under NoAuth (admin), which is what the
  community dashboard runs; only OIDC deployments (enterprise) see enforcement,
  which is the intent.
- **`ListByProject`→`ListByProjectAndUser` signature change** touches the interface
  + mock. Contained: the only caller is `ListAskSessions`; mock lives in
  `search_test.go`. *Alternative:* keep `ListByProject` and add a second method —
  rejected as dead surface (Rule 6/8); the list must always be per-user now.
- **Static `features[]`** may advertise `grounded_chat` on a deployment without
  vector search (where community `/ask` 503s). *Alternative:* gate features on
  `vectorStore != nil` — rejected for this phase (Rule 8; issue enumerates the flat
  set; the enterprise agentic override doesn't require Qdrant). Recorded as a
  possible follow-up.
- **`branding`** included as an opaque `omitempty` passthrough sourced from the
  registry (part of the issue's JSON contract) but never populated by community —
  no community config knob is added, so it isn't gold-plating; reviewers may veto.
- **Dashboard proxy:** the client hits the deployment **API** directly for
  `/.well-known/decisionbox` (like `/health`), so no dashboard change is needed and
  the issue forbids one. If a deployment only exposes the Next.js dashboard URL to
  the client, proxying `/.well-known/decisionbox` would be a separate chat-app /
  deployment follow-up — noted, not implemented here.

## 10. Enterprise coordination (out of scope here — `decisionbox-enterprise#124`)

- Enterprise `HandleAsk` (`ask/handler/ask.go`) gets the **same owner check** on an
  existing `session_id` so the per-user guarantee holds on enterprise deployments.
- The enterprise auth `init()` (`auth/register.go`) calls `wellknown.Register` with
  `Type:"oidc"` + issuer/`AUTH_MOBILE_CLIENT_ID`/scopes/audience, so the discovery
  endpoint advertises OIDC on enterprise builds.
Both are tracked in enterprise #124 and are **not** part of this platform PR.

## 11. Acceptance-criteria mapping

- Raw unauthenticated discovery with explicit `auth.type`, wired to the deployment
  auth config + mobile client id → §4.1 (community seam + handler; enterprise fills
  values via #124).
- Per-user sessions; owner can delete their own; NoAuth preserved → §4.2.
- Rename (`PATCH`, owner-or-admin) → §4.3.
- Client rides on reused Ask endpoints + these three deltas; no parallel API, no
  other platform change → §3 scope.
- All on `chat-app`, request/response only → base branch (top of doc).

---

This document is a **PLAN for review**; implementation follows after approval.

Closes #303

— Co-coded with Jale 🤖
