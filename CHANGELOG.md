# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this
project uses [semantic versioning](https://semver.org/spec/v2.0.0.html), and
while the major version is 0 the API may still move.

## [0.1.0] — 2026-09-13

The first release published from this module path.

The client existed before, inside the server repository at
`github.com/gsoultan/metis/sdk`, but was never tagged there — it versioned with
the engine. Everything under *Fixed* and *Changed* below is relative to that
in-server version; **for a new user this is simply what the package does**, and
the last section lists it in full.

### Requires

Any Metis server for the bulk of the surface. Three things need one newer than
**v0.2.0**, because the endpoints behind them were added or corrected after that
release: filtering tasks by instance, paging on the instance and by-assignee
listings, and `Node.Type` on a task. See the server compatibility table in the
README; the SDK refuses rather than guesses where the difference would otherwise
be silent.

### Fixed

- **`ClaimTask` and `CompleteTask` took a user the server ignores.** Both had a
  `userID` parameter that was sent as `user_id` in the body and then discarded:
  the server reads the acting user from the token (`principal.Username`) and has
  no way to accept an override. Passing somebody else's ID looked like acting on
  their behalf and silently acted as yourself.

  The parameter is gone, because a parameter that does nothing is worse than no
  parameter at all:

  ```go
  client.ClaimTask(ctx, taskID, userID)              →  client.ClaimTask(ctx, taskID)
  client.CompleteTask(ctx, taskID, userID, vars)     →  client.CompleteTask(ctx, taskID, vars)
  ```

  If you were passing the signed-in user's own ID, delete the argument. If you
  were passing somebody else's, you were not doing what you thought — use the
  new `AssignTask`, which genuinely names a person.

### Added

- **Go 1.27 is required**, matching the server so both repositories build on one
  toolchain. The code itself needs only 1.24 — the `omitzero` struct tags, and
  `testing.T.Context` in the tests — so the directive can be lowered to that
  without touching a line of source if importing from an older project ever
  matters more than staying in step.

  `Variables.As[T]()` is a generic method, legal only from 1.27, and is the one
  declaration that pins that floor. Everything else generic here —
  `VariablesOf`, `NewTypedWorker` — is a package-level function that would
  compile on any Go with generics.

- **`Variables.As[T]()`, `VariablesOf`, and `NewTypedWorker`** — types for the
  half of the problem where types are knowable.

  `Variables` stays `map[string]any` because the wire is heterogeneous: one
  instance carries a number, a string, a bool and a nested object at once, so no
  single type parameter describes it. What *is* knowable is the shape your own
  code expects, and there a type parameter is honest — the conversion becomes a
  decoder's job rather than a guess:

  ```go
  refund, err := task.Variables.As[Refund]()   // generic form of Decode
  vars, err := metis.VariablesOf(result)       // and back
  ```

  `NewTypedWorker` applies that to a whole worker: the handler receives a
  `*TypedTask[In]` with the variables already decoded and returns a value that
  becomes the variables written back, so an `int` declared as an `int` arrives
  as one. Both type parameters are inferred from the handler. It returns a plain
  `*Worker`, so typed and untyped workers mix. A task whose variables do not fit
  fails *that task* — the same path as a handler error — because a shape
  mismatch means the diagram and the worker have drifted apart, and retrying
  will not fix it.

  Deliberately absent: a generic `Get[T](key)` accessor. Values arrive as `any`
  and every JSON number is a `float64`, so `v[key].(T)` fails for the commonest
  case, and making it work means a hard-coded list of types behind a signature
  that claims to accept all of them — `Get[int]` succeeding while `Get[uint]`
  silently returns false. The named accessors say what they handle.

- **Typed accessors on `Variables`**, because JSON has one number type and every
  number the engine sends is therefore a `float64`. `vars["amount"].(int)`
  panics on values the server sends routinely; `vars.Int("amount")` reads 900
  out of a `900.0` and reports failure instead of panicking on an absent key or
  a surprising type. `String`, `Bool`, `Int`, `Int64`, `Float64`, `Time`, `Map`,
  `Slice`, each with an `…Or` fallback form, plus `Has`, `Decode` into a struct,
  and a non-mutating `Merge`. `Int` refuses a fractional number rather than
  truncating it. A nil `Variables` is safe to read from.

  `Variables` changed from a type alias to a defined type to carry them. Go's
  assignability rules mean a plain `map[string]any` still passes wherever
  `Variables` is expected, so this is a source-compatible change for
  essentially all code.

- **Typed statuses.** `ProcessStatus` and `TaskStatus` replace bare strings, with
  the full set of constants the server actually produces — the previous doc
  comment listed four of the six task statuses and trailed off in an ellipsis.
  `TaskStatus.IsOpen` is the predicate an inbox filters on; `ProcessStatus.IsFinished`
  is deliberately false for `suspended`, which is stopped but waiting for an
  operator rather than done.

- **`ListDefinitions`**, for finding a definition ID you did not deploy
  yourself. The SDK could only produce one from `ImportDefinition`, which made
  `ExportDefinition` and `ListUserTaskNodes` reachable only for a process
  deployed in the same program. A project keeps every version of every process,
  so the listing is paged, and `Definition` gained `CreatedAt` (what it is
  ordered by) and `Nodes` — the latter always empty in a listing, because the
  server selects scalar columns there and never loads the graph.

- **`ListIncidents` and `ResolveIncident`.** The SDK could report an instance at
  `ProcessFailed` and not say why. An incident names the step that failed and
  what it said; resolved ones are returned too, so an instance that recovered
  keeps its history, and `Incident.IsOpen` filters to what still blocks.
  Resolving is an operator action, not a retry.

- **`GetExecutionPath`**, the route a run took: the steps it reached in
  first-visit order, with a count per step. `Visits`, `Reached` and `Looped`
  read the counts, which are what make a loop visible — a node entered four
  times is one entry with a four. Built from the audit trail, so the nodes carry
  element IDs without labels.

- **`ListTasksByAssignee`**, the listing behind one person's inbox, with a
  paging-only `PageOptions` because that endpoint takes no other filter.

- **`DelegateTask`**, completing the hand-over pair. Assigning says the task is
  now theirs; delegating says they are acting on it for you. Both are guarded
  server-side for an administrator or the current holder.

- **`ListTasksOptions.InstanceID`**, to ask what one run is waiting on. The
  task endpoint previously read only a project, so this had to be answered by
  listing a project and matching client-side — which worked only while the task
  was still on a reachable page. It is now a server-side filter, so the total
  and the paging describe the instance's tasks rather than the project's.
  `LatestTask` takes the same options and answers for either scope.

- **Paging on `ListInstances`**, via the new `ListInstancesOptions`. The
  endpoint declared `page` and `page_size` and the query honoured them, but the
  HTTP decoder never read them off the query string, so every caller got the
  first page and no way past it. Both `LatestInstance` and `LatestTask` now ask
  the server for a single row rather than a page they would discard.

- **`ListInstances` and `LatestInstance`**, for finding a run you did not start
  yourself. The SDK could previously only fetch an instance by an ID you already
  held, so a process started elsewhere was unreachable. Instances come back
  newest first — the server orders by creation time descending — and
  `LatestInstance` returns `ErrNoInstances` for an empty project rather than a
  nil that callers have to remember to check.

- **`ListUserTaskNodes` and `ListDefinitionNodes`**, answering what a process
  *has* rather than what a run of it has reached — the design-time question the
  SDK previously could not answer at all. `ListUserTaskNodes` returns a
  definition's `userTask` elements in diagram order; `ListDefinitionNodes`
  returns every node, typed.

  The engine has no endpoint for this: the definition listing selects scalar
  columns only and never populates `nodes`, and there is no single-definition
  route. So both export the BPMN and parse it client-side with `encoding/xml` —
  no new dependency, but one round trip and the size of the diagram, which is
  worth caching. `ParseNodes` is exported for callers who already hold the XML,
  and `NodesOfType` filters a result.

  The parser matches elements by local name, so any namespace prefix works; it
  descends into sub-processes and covers every `<process>` in a collaboration;
  and it types end events by their children, distinguishing
  `NodeErrorEndEvent` and `NodeTerminateEndEvent`. Entity attacks fail because
  `encoding/xml` is strict, and sub-process nesting is bounded at 100 levels
  because the walk recurses and a Go stack overflow cannot be recovered. Both
  properties are pinned by tests rather than assumed.

- **`NodeType.IsHumanStep`**, true for `NodeUserTask` and `NodeManualTask` —
  the two elements that wait for a person.

- **`UserTask.Type` and `UserTask.CreatedAt`**, two fields the server has always
  sent and the SDK silently dropped. `Type` is the BPMN element kind, and it is
  the field to read: current servers also fill in `Node.Type`, but older ones do
  not, while `UserTask.Type` works against both. `CreatedAt` is what listings
  are ordered by, so without it "latest" was not answerable from a response.
  `Node.Name` stays empty by design — a task can be renamed, which detaches its
  name from the diagram's label.

- **`NodeType` with the engine's full element vocabulary** — `NodeUserTask`,
  `NodeServiceTask`, `NodeScriptTask`, `NodeManualTask`, the gateways, the
  events. `Node.Type` is typed with it too, though that one is empty in
  practice; the doc now says so rather than implying a value that never comes.

- **`UserTask.NodeID` and `ExternalTask.NodeID`**, for the BPMN element a task
  came from, without a nil check at every call site.

- **`LatestTask`**, the most recently created human task in a project, with
  `ErrNoTasks` for an empty one. It mirrors `LatestInstance`, and returns the
  newest task whatever its status — `TaskStatus.IsOpen` is the filter for the
  newest task still needing somebody.

- **`UnclaimTask`** to give a task back to the inbox, and **`AssignTask`** to
  hand one to a named person. The inbox cycle was previously claim-and-complete
  with no way to release, and no way to act for somebody else at all.

- **`IsInvalid`** (400 — your request was wrong, retrying will not help) and
  **`IsServerError`** (5xx — worth retrying) alongside the existing `IsNotFound`
  and `IsUnauthorized`. Notably absent: a conflict predicate, because the server
  never answers 409.

- **Convenience accessors for the nil relations** sparse responses leave behind:
  `UserTask.AssigneeUsername`, `UserTask.InstanceID`, `ExternalTask.InstanceID`,
  `Instance.IsFinished`, `UserTask.IsOpen`, and `AuditEntry.Text` for the line
  to show a person.

- **`doc.go`**, explaining the domain model — project, definition, instance,
  node, task — and the difference between the two kinds of task, which is the
  thing the package's own naming did least to make obvious.

### Changed

- **`Task` is now `UserTask`.** The package has two kinds of task and the bare
  name did not say which; `UserTask` and `ExternalTask` read as the siblings
  they are. Several other types lost their `Ref` suffix, which described how the
  server serializes them rather than what they are:

  ```go
  Task            →  UserTask
  ProcessInstance →  Instance
  DefinitionRef   →  Definition
  NodeRef         →  Node
  UserRef         →  User
  ProjectRef      →  Project
  ```

  **Every old name still works** — each is a type alias, marked deprecated. No
  code breaks on this rename; the aliases go in a future release.

- **The SDK is its own repository.** It was already its own Go module inside the
  server repository; it is now published from `github.com/gsoultan/metis-sdk`.
  The package name and every exported symbol are unchanged, so the move is one
  line in `go.mod` and one in each import:

  ```go
  github.com/gsoultan/metis/sdk  →  github.com/gsoultan/metis-sdk
  ```

  Splitting it out makes the zero-dependency promise enforceable on its own
  terms — CI here fails the build if `go.mod` grows a `require` block — and lets
  the client version independently of the engine it talks to.

### The client in full

Listed for anyone arriving without the history above. This is the surface as it
shipped inside the server, and it is still here.

- **A client with nothing in it but the client.** `Client` talks to one server
  as one authenticated principal, is safe for concurrent use, and depends on
  nothing outside the standard library. Every request is bounded by a 30s
  default timeout; error bodies are read under an 8 MiB limit so a proxy's HTML
  error page cannot become an unbounded allocation.
- **Authentication.** `Login` with a username and password, or `WithToken` when
  the token comes from a secret store. `SetOrganization` picks which of a
  caller's organizations later requests act in.
- **Definitions and instances.** `ListProjects`, `ImportDefinition`,
  `ExportDefinition`, `StartProcess`, `GetInstance`, and `GetTimeline` for the
  plain-language audit trail.
- **Messages and signals.** `SendMessage` correlates into the one instance
  waiting; `BroadcastSignal` reaches every instance in the project.
- **Human tasks.** `ListTasks` with paging, `GetTask`, `ClaimTask`,
  `CompleteTask`.
- **External-task workers.** `Worker` long-polls a topic and runs a `Handler`
  per task. It survives transient failures rather than exiting on them,
  surfacing them through `OnError`; it contains a panicking handler so one
  poison task cannot stop the topic; and it cancels the handler's context when
  the lock would expire, because working past a lock another worker may hold is
  how two workers charge the same card twice. `FetchAndLock`,
  `CompleteExternalTask` and `FailExternalTask` are exported for callers who
  want to run the loop themselves.
- **Errors worth branching on.** `*APIError` carries the status and the server's
  message; `IsNotFound` and `IsUnauthorized` cover what callers actually check.
- **Lean response types** that ignore unknown fields, so a newer server keeps
  working with an older SDK.
- **`examples/quickstart`**, which runs the whole journey against a live server
  rather than a mock.
