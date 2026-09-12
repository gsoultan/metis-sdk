# Metis Go SDK

The Go client for a [Metis](https://github.com/gsoultan/metis) server — the
BPMN 2.0 workflow engine.

```bash
go get github.com/gsoultan/metis-sdk
```

**No dependencies outside the Go standard library.** Importing a client to call
an HTTP API should not make you inherit the engine's dependency graph — GORM,
goja, RabbitMQ, OpenTelemetry. CI fails if `go.mod` ever grows a `require` block.

## The model, in one pass

Five nouns carry everything:

| | what it is |
| :-- | :-- |
| **Project** | An organization's container. Everything belongs to one, and most calls take its ID. `ListProjects` is where you find it. |
| **Definition** | A deployed process *template* — the BPMN diagram, parsed. A blueprint; it does not run. |
| **Instance** | One *running execution* of a definition. Carries `Variables`, has a status. One definition, thousands of instances. |
| **Node** | One step of a definition — a BPMN element, under the ID the diagram gave it. |
| **Task** | One step of one instance that is *waiting for the outside world*. Two kinds — see below. |

> You deploy a **definition** into a **project**, start an **instance** of it,
> and that instance walks its **nodes** — pausing at each **task** until the
> outside world answers.

### Definition vs Instance

The distinction people trip on. A definition is the recipe; an instance is one
dinner being cooked. Deploying a new version of the recipe does not change the
dinners already in the oven.

`Definition` has both a `Key` and an `ID`, and they are not interchangeable:

- **`Key`** is the process ID from your diagram. It is stable across
  deployments, and it is what you *start a process by*. Redeploying and then
  calling `StartProcess` with the same key picks up the new version — no code
  change.
- **`ID`** names one specific *version*. Redeploying the same key mints a new
  one. Running instances keep the version they started with.

### The two kinds of task

Both are work an instance is blocked on. They differ in **who does the work**,
and that changes how you get it and how long you have:

| | `UserTask` | `ExternalTask` |
| :-- | :-- | :-- |
| who does it | a person | a program (your service) |
| BPMN element | `userTask` | `serviceTask` with a topic |
| how you get it | `ListTasks` — an inbox | `FetchAndLock` — a long poll |
| exclusivity | `ClaimTask`, held until released | a **lock** with an expiry, taken on fetch |
| time limit | none; it waits for a human | `LockDuration`, then back to the pool |
| finish it with | `CompleteTask` | `CompleteExternalTask` / `FailExternalTask` |

A **`UserTask`** sits in somebody's inbox. Nothing expires — a task can wait a
week for an approval. You claim it so two people do not work it in parallel.

An **`ExternalTask`** is how your own service performs a step of somebody else's
process: charge a card, send a letter, call an API the engine cannot reach. You
take a *lock* rather than a claim, because a program can crash — if the lock
expires the task becomes fetchable again and another worker picks it up. Use
`Worker` and that loop is written for you.

## Quick start

```go
package main

import (
	"context"
	"log"

	metis "github.com/gsoultan/metis-sdk"
)

func main() {
	ctx := context.Background()
	client := metis.NewClient("http://localhost:8080")

	if err := client.Login(ctx, "admin", "secret"); err != nil {
		log.Fatal(err)
	}

	// Most calls are scoped to a project; this is where you find its ID.
	projects, err := client.ListProjects(ctx)
	if err != nil {
		log.Fatal(err)
	}

	id, err := client.StartProcess(ctx, projects[0].ID, "invoice-approval",
		metis.Variables{"amount": 900})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("started instance", id)
}
```

`NewClient` takes the server's base URL; the `/api/v1` prefix is added for you.
A `Client` is safe for concurrent use, and every request is bounded by a 30s
timeout so a wedged server cannot hold a caller's goroutine forever.

If the token comes from a secret store rather than a password, skip `Login`:

```go
client := metis.NewClient(baseURL, metis.WithToken(os.Getenv("METIS_TOKEN")))
```

`WithHTTPClient` replaces the underlying `*http.Client` for custom TLS, proxies,
or instrumentation. Accounts belonging to more than one organization pick one
with `client.SetOrganization(orgID)`.

## Variables

`Variables` is the data an instance carries. Values arrive as `any`, and **JSON
has one number type — so every number the server sends is a `float64`**, even
one you originally sent as an `int`. That makes the obvious thing wrong:

```go
amount := task.Variables["amount"].(int)   // panics: it is a float64
name   := task.Variables["name"].(string)  // panics if the key is absent
```

The accessors never panic. Each reports whether the key was there and held the
type you asked for:

```go
amount, ok := vars.Int("amount")            // 900, true — 900.0 is whole
price,  ok := vars.Float64("price")         // 42.50, true
name       := vars.StringOr("name", "anon") // fallback when absent
urgent     := vars.BoolOr("urgent", false)
due,    ok := vars.Time("due")              // RFC 3339 or a time.Time
```

`Int` refuses a fractional number rather than truncating — a quantity that
arrived as `2.5` is a bug worth seeing, not a `2`.

Nested objects come back as `Variables` too, so the accessors work at any depth,
and there is an escape hatch when you would rather declare your shape once:

```go
customer, ok := vars.Map("customer")
tier,     ok := customer.Int("tier")

var order struct {
	Amount   float64 `json:"amount"`
	Customer string  `json:"customer"`
}
err := task.Variables.Decode(&order)
```

`Has` tells absent from present-but-null, and `Merge` layers two sets without
mutating either. A `nil` `Variables` is safe to read from.

### Why `map[string]any` and not a generic type

Because the wire is heterogeneous. One instance carries a number, a string, a
bool and a nested object *at the same time*, so there is no single `T` that
`Variables[T]` could be — the map's value type is genuinely `any`.

A generic **accessor** is possible, but it cannot be honest. Values
arrive as `any` and every JSON number is a `float64`, so `v[key].(T)` fails for
the most common case there is:

```go
vars.Get[int]("amount")   // 0, false — the value is a float64
```

Making it work means hard-coding the supported types inside a signature that
advertises every type — `Get[int]` succeeds, `Get[uint]` silently returns
`false`, and nothing in the signature tells you which is which. `vars.Int(...)`
and its siblings say exactly what they handle.

Where a type parameter *is* honest is when you supply the shape, because then
the conversion is a decoder's job rather than a guess:

```go
type Refund struct {
	ChargeID string  `json:"chargeID"`
	Amount   float64 `json:"amount"`
	Attempt  int     `json:"attempt"`   // an int, because you declared it one
}

refund, err := task.Variables.As[Refund]()      // generic form of Decode
vars, err := metis.VariablesOf(result)          // and back again
```

### Typed workers

Once the shape is declared, a whole worker can work in it — `NewTypedWorker`
decodes the input and encodes the result, so the handler never touches a map:

```go
worker := metis.NewTypedWorker(client, "reverse-charge", workerID,
	metis.WorkerOptions{},
	func(ctx context.Context, task *metis.TypedTask[Refund]) (Result, error) {
		if err := payments.Refund(ctx, task.Input.ChargeID); err != nil {
			return Result{}, err
		}
		return Result{Reversed: true}, nil
	})
```

Both type parameters are inferred from the handler, so you never write them out.
It returns a plain `*Worker` — same polling, same lock budget, same `OnError` —
so typed and untyped workers mix freely, and `task.Variables` is still there for
anything the struct omits.

A task whose variables do not fit the declared input **fails that task**, not
the worker: same path as a handler error, so it retries and then lands for an
operator. That is deliberate — a shape mismatch means the diagram and the worker
have drifted apart, which retrying will not fix. If you would rather tolerate a
loose shape, use `metis.Variables` as the input type.

## Deploying and starting

```go
projects, err  := client.ListProjects(ctx)
defID, err     := client.ImportDefinition(ctx, projectID, bpmnXML)
xml, err       := client.ExportDefinition(ctx, defID)          // by version ID
instanceID, err := client.StartProcess(ctx, projectID, "refund", vars) // by Key
instance, err  := client.GetInstance(ctx, instanceID)
entries, err   := client.GetTimeline(ctx, instanceID)
```

### Finding deployed processes

```go
definitions, page, err := client.ListDefinitions(ctx, metis.ListDefinitionsOptions{
	ProjectID: projectID,   // optional — empty lists the whole organization
	PageSize:  50,
})
```

This is where you find a definition ID you did not deploy yourself — the ID
`ExportDefinition` and `ListUserTaskNodes` take. `ImportDefinition` returns one
too, but only for a process you just deployed in the same program.

A project keeps **every version of every process it has ever had**, so the list
is longer than it looks: one process redeployed twenty times is twenty entries
sharing a `Key` and differing by `Version`. To *start* a process you want the
`Key` — the engine picks the latest version itself.

`Definition.Nodes` is always empty here; the server selects scalar columns for a
listing and never loads the graph. Use `ListDefinitionNodes` for one
definition's steps.

### Finding past runs

```go
instances, page, err := client.ListInstances(ctx, metis.ListInstancesOptions{
	ProjectID: projectID,
	PageSize:  50,
})                                                     // newest first
instance, err := client.LatestInstance(ctx, projectID) // the last run
```

`ListInstances` returns instances **most recent first** — the server orders by
creation time descending. `LatestInstance` is the first of those, and reports
`ErrNoInstances` when the project has never run anything, rather than handing
back a nil you have to remember to check:

```go
instance, err := client.LatestInstance(ctx, projectID)
switch {
case errors.Is(err, metis.ErrNoInstances):
	// nothing has run yet
case err != nil:
	return err
}
```

`ProjectID` is required: an empty one is refused locally rather than spending a
round trip on the 400 the server would answer. `LatestInstance` asks for a
single row rather than a page it would discard.

"Latest" is by creation time across the whole project, so it is the last process
*started* — not necessarily the last to finish, nor one you started yourself.
There is no server-side filter by definition, so it cannot be narrowed to a
single process type. When that matters, keep the ID `StartProcess` returned
rather than looking it up afterwards.

Statuses are typed — compare against the constants, not string literals:

```go
if instance.Status == metis.ProcessActive { ... }
if instance.IsFinished() { ... }  // completed or failed — suspended is neither
```

`ProcessActive`, `ProcessCompleted`, `ProcessSuspended`, `ProcessFailed`.

`GetTimeline` returns the plain-language audit trail; `entry.Text()` gives the
line to show a person (the narrative, falling back to the message).

## When a run goes wrong

An instance at `ProcessFailed` has incidents explaining why — a service task
whose retries ran out, a script that threw, an expression that would not
evaluate:

```go
incidents, err := client.ListIncidents(ctx, instanceID)
for _, incident := range incidents {
	if incident.IsOpen() {
		log.Printf("stuck at %s: %s", incident.NodeID(), incident.Error)
	}
}

err = client.ResolveIncident(ctx, incident.ID)
```

Resolved incidents come back too, so an instance that recovered keeps its
history; `IsOpen()` filters to what still blocks. `ResolveIncident` is an
operator action rather than a retry — it says the underlying problem is handled,
and does not by itself re-run the step that failed.

To see the route a run took, loops included:

```go
path, err := client.GetExecutionPath(ctx, instanceID)

path.Reached("escalate")   // did it ever get there?
path.Visits("review")      // 4 — entered four times
path.Looped()              // any step entered more than once
```

The path is built from the audit trail, so its nodes carry BPMN element IDs
without labels — `ListDefinitionNodes` has those.

## Messages and signals

```go
// A message goes to the one instance waiting on it. correlationKey selects
// which, when several wait on the same name.
err := client.SendMessage(ctx, projectID, "payment-received", orderID, vars)

// A signal goes to every instance in the project waiting on it.
err := client.BroadcastSignal(ctx, projectID, "market-closed", vars)
```

Pass an empty `correlationKey` only when the message name alone is unambiguous —
an empty key matches every waiting subscription for that name.

## Human tasks

```go
tasks, page, err := client.ListTasks(ctx, metis.ListTasksOptions{
	ProjectID: projectID,
	PageSize:  50,
})

err = client.ClaimTask(ctx, taskID)                                   // for you
err = client.CompleteTask(ctx, taskID, metis.Variables{"ok": true})
err = client.UnclaimTask(ctx, taskID)                                 // give it back
err = client.AssignTask(ctx, taskID, "alice")                         // hand it over
err = client.DelegateTask(ctx, taskID, "bob")                         // have them act on it
```

For one person's inbox there is a listing of its own:

```go
tasks, page, err := client.ListTasksByAssignee(ctx, "alice", metis.PageOptions{PageSize: 50})
```

It takes no project or instance filter — a different endpoint from `ListTasks`,
which is why its options are paging only.

**Who acts is the token, always.** `ClaimTask` and `CompleteTask` take no user
argument: the server reads the acting user from the `Authorization` header and
ignores any override. An application acting for many people needs a client per
person, not one client passing user IDs around. To hand a task to somebody
specific, that is `AssignTask` — the one call where naming a person is real.

`AssignTask` and `DelegateTask` are allowed only for an **administrator or the
person currently holding the task**, and answer 403 otherwise. An ordinary user
can pass their own task on; reassigning one they never held is an admin
operation. The two differ in intent rather than mechanics — assigning says the
task is now theirs, delegating says they are acting on it for you.

`ListTasks` returns every status. Filter for an inbox:

```go
for _, task := range tasks {
	if task.Status.IsOpen() {   // not completed, not canceled
		render(task)
	}
}
```

`TaskUnclaimed`, `TaskClaimed`, `TaskCompleted`, `TaskCanceled`,
`TaskDelegated`, `TaskEscalated`. Convenience accessors handle the nil relations
that sparse responses leave behind: `task.AssigneeUsername()`,
`task.InstanceID()`, `task.NodeID()`, `task.IsOpen()`.

### Which BPMN node a task came from

Each task names its diagram element:

```go
task.NodeID()   // "approve" — the BPMN element ID
task.Type       // metis.NodeUserTask — the element kind
task.Name       // "Approve the refund" — the label to display
task.CreatedAt  // when the instance reached this step
```

Read the kind from **`task.Type`**, not `task.Node.Type`. Both carry it against
a current server, but only `task.Type` does against an older one.

`task.Node.Name` is always empty. A task can be renamed after it is created,
which detaches its name from the diagram's label, so the server declines to
guess which of the two you meant — `task.Name` is the label.

`NodeType` constants cover the vocabulary: `NodeUserTask`, `NodeServiceTask`,
`NodeScriptTask`, `NodeManualTask`, `NodeExclusiveGateway`, and the rest.
`AuditEntry` carries `NodeID` and `NodeName` as its own fields, both populated.

### The latest user task

```go
task, err := client.LatestTask(ctx, projectID)   // newest, whatever its status
```

`ListTasks` is already newest-first, so `LatestTask` is its first element, with
`ErrNoTasks` for an empty project. For the newest task somebody still has to
*act* on, filter instead — the listing includes completed ones:

```go
for _, task := range tasks {   // already newest-first
	if task.Status.IsOpen() {
		return &task, nil
	}
}
```

Filter by instance to ask what one run is waiting on:

```go
task, err := client.LatestTask(ctx, metis.ListTasksOptions{InstanceID: instanceID})
tasks, page, err := client.ListTasks(ctx, metis.ListTasksOptions{InstanceID: instanceID})
```

`LatestTask` asks the server for a single row rather than a page it would throw
away, so `Page` and `PageSize` in the options are ignored.

### Listing a definition's user task nodes

```go
nodes, err := client.ListUserTaskNodes(ctx, definitionID)
for _, node := range nodes {
	fmt.Println(node.ID, "—", node.Name)   // "approve — Approve the refund"
}
```

This is the **design-time** question: which human steps the process has, whether
or not anything has reached them. `ListTasks` answers the runtime version — the
steps instances have actually arrived at.

For everything in the diagram, not just the human steps:

```go
nodes, err := client.ListDefinitionNodes(ctx, definitionID)
human := metis.NodesOfType(nodes, metis.NodeUserTask, metis.NodeManualTask)
```

`ListUserTaskNodes` returns only `userTask`. A `manualTask` is also work for a
person — a step done outside the system and merely acknowledged — but it is a
different BPMN element and the engine types it separately, so combining them is
your call, not the SDK's. `NodeType.IsHumanStep()` covers both.

**How it works, and what it costs.** The engine has no endpoint that returns a
definition's nodes: `GET /api/v1/definitions` selects only scalar columns and
never populates `nodes`, and there is no single-definition route. So these two
methods export the BPMN XML and parse it client-side with `encoding/xml` — no
new dependency, but one round trip and the size of the diagram. Worth caching if
you are asking per request rather than per deployment.

If you already have the XML — checking a diagram before you deploy it, say —
skip the round trip:

```go
nodes, err := metis.ParseNodes(bpmnXML)
```

The parser matches elements by **local name**, so `<bpmn:userTask>`,
`<semantic:userTask>` and a bare `<userTask>` all work; whichever prefix the
exporting tool chose is irrelevant. Nodes inside sub-processes are included,
flattened into the same list, and every `<process>` in the file contributes — so
a collaboration returns all of its pools' steps. End events are typed by their
children, so `NodeErrorEndEvent` and `NodeTerminateEndEvent` come back
distinguished from a plain `NodeEndEvent`.

On hostile input: entity attacks are refused because `encoding/xml` is strict —
it rejects an entity it was not given and never fetches an external one, so
billion-laughs and `file:///etc/passwd` payloads both fail to parse. Sub-process
nesting is bounded at 100 levels, because the walk recurses and a Go stack
overflow cannot be recovered. Both are pinned by tests.

## External-task workers

A `Worker` is how your own service performs a step of someone else's process:

```go
worker := metis.NewWorker(client, "reverse-charge", workerID,
	metis.WorkerOptions{},
	func(ctx context.Context, task *metis.ExternalTask) (metis.Variables, error) {
		chargeID, ok := task.Variables.String("chargeID")
		if !ok {
			return nil, fmt.Errorf("task %s carries no chargeID", task.ID)
		}
		if err := payments.Refund(ctx, chargeID); err != nil {
			return nil, err // retried after RetryDelay until retries run out
		}
		return metis.Variables{"reversed": true}, nil
	})

worker.OnError = func(err error) { log.Println("worker:", err) }

if err := worker.Run(ctx); err != nil { // returns ctx.Err() on shutdown
	log.Println(err)
}
```

`Run` polls until the context is cancelled and does **not** give up on transient
errors — it reports them through `OnError` and keeps going, because a worker
that exits on the first network blip takes the whole integration down with it. A
panicking handler is contained too: one poison task must not stop every other
task on the topic.

`WorkerOptions` is usable as its zero value — `MaxTasks` 5, `LockDuration` 1
minute, `PollInterval` 2 seconds, `RetryDelay` 10 seconds.

**Handlers must be idempotent.** `LockDuration` is both how long the tasks stay
yours and the budget the handler gets: its context is cancelled when the lock
would expire, because finishing work on a lock another worker may now hold means
two workers charging the same card. And if the work succeeds but reporting it
back fails, the SDK deliberately does not re-run your handler — the side effect
already happened. The lock expires, the engine re-dispatches, and your handler
sees the task a second time.

The lower-level calls are exported when you want to run the loop yourself:
`FetchAndLock`, `CompleteExternalTask`, `FailExternalTask`.

## Errors

Non-2xx answers come back as `*metis.APIError`, carrying the status and the
server's message. Branch on the predicates rather than status codes:

```go
switch {
case metis.IsNotFound(err):     // gone — or, under tenant scoping, never yours
case metis.IsUnauthorized(err): // 401 or 403: token missing, expired, or not allowed
case metis.IsInvalid(err):      // your request was wrong; retrying will not help
case metis.IsServerError(err):  // the engine faltered; worth retrying
}
```

Another organization's resource answers **404, not 403** — "not yours" and "does
not exist" are deliberately indistinguishable, because a 403 would confirm the
thing exists. All predicates unwrap, so they still work after `fmt.Errorf("%w")`.

## The whole journey, end to end

[`examples/quickstart`](examples/quickstart) runs everything above against a
live server — deploy a definition, start an instance, serve its external task
with a worker, complete its human task, read the timeline back:

```bash
METIS_URL=http://localhost:8080 \
METIS_USERNAME=admin METIS_PASSWORD=secret \
METIS_PROJECT="Default Project" \
go run ./examples/quickstart
```

## Compatibility

Requires **Go 1.27 or later**, matching the [Metis server](https://github.com/gsoultan/metis)
so both build on one toolchain.

The code itself needs only Go 1.24 — the `omitzero` struct tags, and
`testing.T.Context` in the tests. If you need to import this from a project on
an older Go, lowering the `go` directive to `1.24` is all it takes; the source
compiles unchanged.

`Variables.As[T]()` is a generic method, which needs Go 1.27 specifically. It is
the one declaration that would have to change if that floor ever came down.

This module was extracted from the server repository, where it lived at
`github.com/gsoultan/metis/sdk`. See [CHANGELOG.md](CHANGELOG.md) for what
changed in the move, including the renamed types and the corrected
claim/complete signatures.

## License

MIT. See [LICENSE](LICENSE).
