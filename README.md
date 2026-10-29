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

Give an instance your own name for the case — an order number, a claim
reference — and find it by that later, without keeping the ID:

```go
id, err := client.StartProcess(ctx, projectID, "refund", vars,
	metis.WithBusinessKey("ORDER-4411"))

instances, _, err := client.ListInstances(ctx, metis.ListInstancesOptions{
	ProjectID: projectID, BusinessKey: "ORDER-4411",
})
```

A key is optional, not unique, at most 255 characters, and not encrypted, so
keep personal data out of it. A sub-process started by a call activity carries
its caller's key. `WithVersion(n)` starts a deployed version other than the
latest. `ListInstancesOptions` also filters by `Status`, `DefinitionID` and
`NeedsAttention` (an open incident).

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
line to show a person (the narrative, falling back to the message). A server
from Metis v0.5.0 on sends at most 1,000 entries a read, so `GetTimeline` reads
page after page until it has them all. For only the newest part of a long trail,
read one page:

```go
page, err := client.GetTimelinePage(ctx, instanceID, metis.TimelinePageOptions{Limit: 20})
// page.Entries: the twenty newest, oldest first. page.Total: how many in all.
```

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
err = client.ResolveTask(ctx, taskID)                                 // the delegate hands it back

delegated, page, err := client.ListDelegatedTasks(ctx, metis.PageOptions{}) // still with somebody
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

**Anybody but the holder says why.** From Metis v0.5.0, assigning, delegating,
releasing or resolving a task you do not hold — with an administrator's token,
typically — is refused with a 400 until it carries a reason, which the server
keeps in the task's history:

```go
err = client.AssignTask(ctx, taskID, "bob", metis.WithReason("Alice is on leave"))
```

The same release refuses a username that is not an account in the
organization, and answers 400 to assigning a task to the person already holding
it.

**Completing sets only what the form declares.** From Metis v0.5.0,
`CompleteTask` may write only the variables the step's form has fields for; a
completion carrying any other is refused with a 400 naming them, and nothing is
written.

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

### User tasks in the order a case reaches them

`ListUserTasksInOrder` reads a deployed diagram into the chain a case
travels, with each step linked to the ones after and before it:

```go
flows, err := client.ListUserTasksInOrder(ctx, definitionID)
for _, step := range flows[0].Steps {
	fmt.Println(step.Order, step.Name, step.Lane) // 1 Submit Requester
	for _, next := range step.Next() {
		if step.SendsBackTo(next) {
			fmt.Println("   can send back to", next.Name)
		} else {
			fmt.Println("   then", next.Name)
		}
	}
}
```

Gateways, events and system steps between two user tasks are passed over.
Steps inside a sub-process come where it stands, with `SubProcess` and
`Depth()` saying where. `Assignment` says who a step is for and
`MultiInstance` how it repeats ("two of three managers approve").
`task.Step().In(flows[0])` finds where a running task sits in the chain.
`ParseFlow` does the same for a diagram you hold, for any kinds of step.

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

## People

People are the members of a project's own directory: the employees, customers
or suppliers a process hands work to, as opposed to the Users who run Metis. A
person signs in to one project and sees only the tasks the process gives them.
Their token works the People calls and nothing else, so use a client of its own:

```go
person := metis.NewClient(baseURL)
session, err := person.LoginPerson(ctx, projectID, "ana", password)

tasks, page, err := person.ListPersonTasks(ctx, metis.PageOptions{PageSize: 20})
err = person.ClaimPersonTask(ctx, taskID)
err = person.CompletePersonTask(ctx, taskID, metis.Variables{"signed": true})
err = person.ReleasePersonTask(ctx, taskID)                 // give it back
```

**A person's own system** works their tasks with one of their API keys. They make
it once, signed in, and the token is shown that one time:

```go
key, token, err := person.CreatePersonAPIKey(ctx, "payroll export")
// store token where the payroll system can read it; then, over there:
payroll := metis.NewClient(baseURL, metis.WithToken(token))
tasks, _, err := payroll.ListPersonTasks(ctx, metis.PageOptions{})
```

A key works tasks and nothing more: making, listing and revoking keys
(`ListPersonAPIKeys`, `RevokePersonAPIKey`) take the person's sign-in.

**Task links** are mailed to a person so they can do one task without signing
in. A page of your own that the link opens reads and completes it with the
link's token, which the SDK sends in the body rather than the address:

```go
link, err := client.ReadTaskLink(ctx, token)        // changes nothing
err = client.CompleteByTaskLink(ctx, token, metis.Variables{"approved": true})
```

People need a server newer than Metis v0.5.0 with `METIS_PEOPLE_TOKEN_KEY` set.

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

The budget is per batch. The tasks of one fetch are locked together and run one
after another, so the last of `MaxTasks` gets whatever the earlier ones left, and
a task whose lock runs out before its turn is skipped and fetched again later.
Size `LockDuration` for `MaxTasks` handlers in a row, or lower `MaxTasks`.

**Work that can outlast its lock** can keep it instead. With `ExtendLock` set, the
worker asks for another `LockDuration` each time half of what is left has passed,
for as long as the handler runs, and cancels the handler's context only if the
lock could not be kept — at once if the server says it is no longer this
worker's, so a task is never finished by two workers.

```go
worker := metis.NewWorker(client, "render-report", workerID,
	metis.WorkerOptions{LockDuration: time.Minute, MaxTasks: 1, ExtendLock: true}, handler)
```

A handler that hangs then holds its task for as long as the process lives, so put
your own timeout on work that can. Only the task running is extended, which is
why `MaxTasks: 1` goes with it. It needs Metis v0.5.0; against an older server
the worker says so once through `OnError` and keeps to the lock it fetched.

On shutdown, a handler that finished still has its result reported, for up to
ten seconds after the context is done. One that returned because it was
cancelled is not reported as a failure, so a deploy never spends a retry.

Every fetch says what the worker is — `metis-sdk/` and the SDK's version unless
`WorkerOptions.Version` names your build — and the server lists who serves each
topic. A topic with work waiting and no live worker is `Unserved`, which is
otherwise indistinguishable from a quiet one:

```go
topics, err := client.ListExternalTaskWorkers(ctx)
for _, topic := range topics.Topics {
	if topic.Unserved {
		log.Printf("%d tasks wait on %q and nothing fetches it", topic.Waiting, topic.Topic)
	}
}
```

The lower-level calls are exported when you want to run the loop yourself:
`FetchAndLock` (with `WithWorkerVersion`), `ExtendExternalTaskLock`,
`CompleteExternalTask`, `FailExternalTask`.

## Simulating a process

A simulation runs a process on the real engine against a virtual clock, inside
a transaction that is always rolled back: nothing is kept, nothing is sent, no
service is called. The steps the outside world would answer are answered by the
request, and the whole trace comes back.

```go
run, err := client.Simulate(ctx, projectID, metis.SimulationRequest{
	DefinitionKey: "expense",                  // or BPMNXML / Definition: a draft
	Variables:     metis.Variables{"amount": 4200},
	Answers: []metis.SimulationAnswer{
		{Node: "approve", Kind: metis.AnswerPerson, Actor: "marc", After: "PT2H",
			Returns: metis.Variables{"approved": true}},
	},
})
switch run.Outcome {
case metis.SimulationOutcomeNeedsAnswer: // run.AwaitingNode is the step to answer next
case metis.SimulationOutcomeIncident:    // run.Incidents says what the engine refused
}
```

`ReplayInstanceID` runs a real instance's case — what it started with, who did
each step and when, what its services answered — on another version, and
`run.Replay` says where the two went apart. Nothing about the instance changes.

**Saved cases** are kept with the process, with what each must end with.
`RunSimulationCases` runs them all against a version, or against the `.bpmn`
about to be deployed, which is the check a pipeline wants:

```go
results, err := client.RunSimulationCases(ctx, projectID, "expense",
	metis.SimulationRequest{BPMNXML: bpmn})
for _, r := range results {
	if !r.Passed() {
		log.Printf("%s: %s", r.Case.Name, strings.Join(r.Misses, " "))
	}
}
```

`ListSimulationCases`, `CreateSimulationCase`, `UpdateSimulationCase` and
`DeleteSimulationCase` manage them; a process keeps at most 20. Simulating is a
designer's.

## Webhooks

An **outgoing webhook** posts a project's events to your address, each signed
with the webhook's own secret, until the receiver accepts it. Register one (an
administrator's), and keep the secret, which is shown once:

```go
hook, err := client.CreateOutgoingWebhook(ctx, projectID, "Finance ledger",
	"https://ledger.example.com/hooks/metis",
	metis.EventProcessCompleted, metis.EventIncidentRaised)
// hook.Secret
```

The receiving end verifies and parses a delivery in one call. Deliveries arrive
at least once and in no particular order, so remember the IDs you have handled:

```go
func handle(w http.ResponseWriter, r *http.Request) {
	delivery, err := metis.ReadWebhook(r, secret)
	if err != nil {
		http.Error(w, "not from Metis", http.StatusUnauthorized)
		return
	}
	if alreadyHandled(delivery.DeliveryID) {
		return
	}
	// delivery.Event, delivery.Instance.BusinessKey, delivery.Incident.ID …
}
```

`WebhookHandler` does that whole receiver: it verifies, answers a repeat of a
delivery already handled without handling it again, and routes by event with
`WebhookHandlers`. An event with no function is accepted and left alone.

```go
http.Handle("/hooks/metis", metis.WebhookHandler(secret,
	metis.WebhookHandlers{
		metis.EventTaskCreated:    notifyAssignee,
		metis.EventIncidentRaised: pageOnCall,
	}.Handle,
	metis.WebhookHandlerOptions{}))
```

It answers 204 when the function returned nil or the delivery was handled
before, and 500 when the function failed, so Metis sends it again. A forged or
stale delivery gets 401, and a repeat that arrives while the first is still
being handled gets 409. Handled IDs are kept in memory (10,000 by default).
Behind a load balancer, give it a `DeliveryStore` the receivers share. Either
way, make the function safe to run twice for one delivery.

A delivery carries no process variables, and an `incident.raised` one names the
incident without its text; ask the API for either. `SendOutgoingWebhookTest`
posts a signed sample now and returns the request and the answer, and
`ListOutgoingWebhookDeliveries`, `RetryOutgoingWebhookDelivery` and
`StopOutgoingWebhookDelivery` read and act on the delivery log.

For the other direction — a partner posting to one of Metis's **incoming**
webhooks — `SignWebhook` computes the `X-Metis-Signature` the partner sends, and
`SimulateWebhookDelivery` tries a body against the webhook and says what a real
delivery would do, acting on nothing.

## Operating many instances at once

Retry or cancel every instance of a project that a filter matches. Ask without
`Apply` first, which only counts:

```go
req := metis.BatchRequest{
	Kind:   metis.BatchRetry, // an operator's; BatchCancel is an administrator's
	Reason: "the payment provider is back",
	Filter: metis.BatchFilter{NeedsAttention: true},
}
preview, err := client.StartBatchOperation(ctx, projectID, req) // preview.Matched
req.Apply = true
started, err := client.StartBatchOperation(ctx, projectID, req)

op, err := client.GetBatchOperation(ctx, started.Operation.ID)
failed, err := client.ListBatchOperationItems(ctx, op.ID,
	metis.ListBatchItemsOptions{Status: metis.BatchItemFailed})
```

One operation takes up to 10,000 instances and is worked through in the
background, each exactly as the single-instance retry or cancel; it can be
stopped with `StopBatchOperation`. Fix the cause before a retry.

After an outage, `RetryFailed` does the preview, the guard and the start in one
call. It retries the failed step of every instance with an open incident that
the filter matches, refuses with `ErrTooMany` when more match than
`MaxInstances` (100 by default), and stops the operation at once if more
failed between the preview and the start. `WaitForBatch` reads the operation
until it has finished.

```go
started, err := client.RetryFailed(ctx, projectID, metis.RetryFailedOptions{
	Reason:       "the payment provider is back",
	Filter:       metis.BatchFilter{DefinitionID: payments.ID},
	MaxInstances: 500,
})
if err != nil {
	return err // errors.Is(err, metis.ErrTooMany): narrow the filter
}
done, err := client.WaitForBatch(ctx, started.Operation.ID, 0)
```

`WatchIncidents` yields each open incident of a project once, as it appears,
for paging someone. The server has no project-wide incident listing, so each
look lists the instances needing attention and reads each one's incidents; poll
accordingly (30 seconds by default). A look that fails in a way that may pass
is yielded as an error and the watch goes on.

```go
for incident, err := range client.WatchIncidents(ctx, projectID, metis.WatchOptions{}) {
	if err != nil {
		log.Print(err)
		continue
	}
	alert(incident)
}
```

`StartMany` starts one instance per request, a few at a time (4 by default, 32
at most), and returns a result per request in order. A request with a business
key goes through `StartProcessOnce`, so an import run twice starts each case
once.

For an administrator, `ListOperations` reads the organization's **audit log** —
who cancelled, waived, migrated or handed over what, newest first — and the
retention calls keep an instance from the retention sweep
(`PlaceRetentionHold`, `ReleaseRetentionHold`, `ListRetentionHolds`) or give a
process its own period (`SetRetentionPeriod`, `ListRetentionPeriods`,
`RetentionPeriodHistory`).

## Decisions, versions and where work stands

A DMN table can answer a service directly, without a process around it:

```go
result, err := client.EvaluateDecision(ctx, projectID, "credit-limit", metis.Variables{"score": 712})
limit, _ := result.Values.Int("limit") // result.MatchedRuleIDs says which lines decided
```

`ImportDefinition` makes the version it deploys the one new instances start
on. `ListVersions` shows which is live and how much each still runs;
`PromoteVersion` makes another one live now (to roll back, say),
`ScheduleVersion` at a set time, and `CancelScheduledVersion` drops a cutover
not yet reached.

`ReleaseVersion` is a release with a guard in front. It checks that the
diagram holds the process, runs every saved simulation case on it as a draft,
and deploys only if they all pass; otherwise `ErrReleaseBlocked` comes back
with the failures and nothing is deployed. The report names the version
released, the one that was live before, and the older versions still running
instances. With `PlanMigrations` it carries, for each of those, a dry-run plan
of moving their instances across.

```go
report, err := client.ReleaseVersion(ctx, projectID, "expense", bpmn,
	metis.ReleaseOptions{RequireCases: true, PlanMigrations: true})
if errors.Is(err, metis.ErrReleaseBlocked) {
	for _, r := range report.Failed() {
		log.Printf("%s: %s", r.Case.Name, strings.Join(r.Misses, "; "))
	}
}
```

`PlanMigration` asks for one such plan on its own: where the work sits, where
it would land, and what refuses or warns. It never moves anything. Applying a
migration rewrites running cases and can need acknowledgements and a second
administrator, so do that in the designer.

For a dashboard or a health check, `ListInstancesPage` returns a page with the
project-wide counts by status and of instances needing attention, in one
request. `ListWaiting` says on which steps running work sits, `ListDeadlines`
lists open tasks by due date, overdue first, and `ListSubProcesses` follows a
call activity to the child instance it started.

`UpdateTask` moves a task's due date, priority or name, with a reason, and
`ActivateAdHocTask` starts an optional step of an ad-hoc sub-process.

## References between objects

Where one object names another, it holds a reference rather than an ID
string or a half-filled copy: an operation's `Instance` is an `InstanceRef`,
a deadline's `Process` a `ProcessRef`, an audit entry's `Step` a `StepRef`.
A reference carries the ID and loads, or acts on, what it names — only when
asked:

```go
for _, op := range page.Operations {
	instance, err := op.Instance.Get(ctx, client) // one request, now
	...
}
started, err := metis.ProjectRef{ID: projectID}.Process("order").Start(ctx, client, vars)
incidents, err := started.Incidents(ctx, client)
err = incidents[0].Ref().Resolve(ctx, client)
step := task.Step().In(flow) // where a running task sits in the diagram
```

Each kind is its own type, so an instance ID cannot be passed where a task is
meant. On the wire a reference is the bare ID, as the server sends it. Calls
that take an object to act on still take its ID as a string; `ref.ID` is
always there.

## Waiting, and doing a thing once

The calls in this section are built from the ones above, for the places where
an integration otherwise writes the same loop every time.

**Wait for an outcome.** `WaitForInstance` reads an instance until
`WaitOptions.Until` holds (by default, until it finishes) and returns it then.
`StartAndWait` starts a process and waits, for a short unattended process whose
result is needed now. Bound both with the context. A failed instance ends the
wait with `ErrInstanceFailed`; a context that ends first gives a `*WaitError`
with the instance as last read.

```go
ctx, cancel := context.WithTimeout(ctx, time.Minute)
defer cancel()
done, err := client.StartAndWait(ctx, project, "credit-check", vars, metis.WaitOptions{})
```

**Send a message when somebody waits for it.** The server drops a message that
nothing is waiting for, and `SendMessage` still answers success. A reply that
arrives while the instance is still on the step before is lost that way.
`SendMessageTo` reads the instance until one of its paths stands at the step
that catches the message, then sends. If the instance finishes or the context
ends first, nothing is sent and the error wraps `ErrNoReceiver`.

```go
err := client.SendMessageTo(ctx, project, "payment-received", orderID, vars,
	metis.Receiver{Instance: id, WaitingAt: "WaitForPayment"})
```

`Instance.Tokens` and `Instance.WaitingAt(step)` say where an instance from
`GetInstance` stands.

**Start a case once.** `StartProcessOnce(project, key, businessKey, vars)`
returns the instance of that process already started for the business key, a
finished one included, or starts one. After a start whose answer was lost it
looks again, so calling it twice for the same order makes one instance. It is
look-then-start with nothing held in between, so give one case to one caller at
a time.

**Complete a task once.** `CompleteTaskSafely` reads the task before and after
completing it. A task the signed-in user already completed answers nil without
a second completion. One that somebody else holds or completed gives a
`*TaskConflictError` matching `ErrTaskTakenByOther`, and a withdrawn one
matches `ErrTaskClosed`. A failure that leaves the task open comes back as it
was, and the call can be made again. `ClaimAndComplete` claims first when the
task is not the user's yet, for working an item off a shared queue:

```go
switch err := client.ClaimAndComplete(ctx, task.ID, vars); {
case errors.Is(err, metis.ErrTaskTakenByOther):
	// somebody was quicker: take the next one
case err != nil:
	return err
}
```

Both need to know who is signed in. `CurrentUser` says, from `Login` or from
`GET /api/v1/users/me` once.

## Reading a whole listing

Every paged listing has an `All…` form that walks every page as a
range-over-func loop, asking for the next page only when the loop gets there:

```go
for task, err := range client.AllTasks(ctx, metis.ListTasksOptions{ProjectID: p}) {
	if err != nil {
		return err // the first error ends the walk
	}
	fmt.Println(task.Name)
}
```

`AllInstances`, `AllTasks`, `AllClaimableTasks`, `AllDefinitions` and
`AllBatchOperationItems` read numbered pages; `AllOperations` and
`AllOutgoingWebhookDeliveries` follow the cursor each page hands back. Breaking
out of the loop stops the reading.

A listing changes while it is read. The numbered ones are newest first, so a
row added during the walk pushes older rows onto the next page; rows are told
apart by ID and each is yielded once. A row that leaves the listing pulls the
others up instead, and the one that moves back onto a page already read is
missed, so narrow the listing to rows that will stay in it when that matters.
A server that answers the same rows whatever page is asked for ends the walk
with `ErrPagingStalled` rather than looping.

## Retrying reads

`WithRetry` sends a read again when it got no answer, or an answer that may
pass: 429, 500, 502, 503 or 504. Waits double from `Base`, less a random part
so many clients do not come back together, and a `Retry-After` from the server
is honoured up to `Max`.

```go
client := metis.NewClient(url, metis.WithRetry(metis.RetryPolicy{
	Attempts: 4,                      // in all, the first included
	Base:     200 * time.Millisecond, // the default
	Max:      30 * time.Second,       // the default
}))
```

Only GET requests are retried. A start, a completion or any other write is sent
once whatever the policy says: the server has no idempotency key for them, so a
write whose answer was lost may have happened, and sending it again could do it
twice. `APIError.RetryAfter` carries the server's `Retry-After` for code that
retries writes itself after looking first.

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

## Server compatibility

The SDK talks to any Metis server; a few calls need **v0.3.0 or later**, and a
few more **v0.5.0 or later**, because the endpoints behind them were added or
corrected in those releases.

| Needs Metis v0.3.0+ | Against v0.2.0 and earlier |
| :-- | :-- |
| `ListTasksOptions.InstanceID` | Refused with `ErrFilterUnsupported` — the server ignores the filter and answers with every task the caller can see, so the SDK checks and will not hand that back as if it were filtered. |
| `ListInstances` / `ListTasksByAssignee` paging | `Page` and `PageSize` are ignored; you get the first page at the server's default. `PageInfo` still reports the true total. |
| `UserTask.Node.Type` | Empty. `UserTask.Type` carries the same kind and works against every server — read that instead. |

| Needs Metis v0.4.0+ | Against v0.3.0 and earlier |
| :-- | :-- |
| `ListWaiting`, `ListDeadlines` | No such route: `IsNotFound`. |

| Needs Metis v0.5.0+ | Against v0.4.0 and earlier |
| :-- | :-- |
| `ResolveTask`, `ListDelegatedTasks` | No such route: an error, `IsNotFound` for `ResolveTask`. |
| `CurrentUser` with a token from `WithToken`, and so `CompleteTaskSafely` and `ClaimAndComplete` | No such route: `IsNotFound`. After `Login` the user is known without it, against any server. |
| `ExtendExternalTaskLock`, `WorkerOptions.ExtendLock` | `ExtendExternalTaskLock` answers `IsNotFound`. A worker with `ExtendLock` says so once through `OnError` and keeps to the lock it fetched. |
| `GetTimelinePage` paging | `Limit` and `Offset` are ignored and the whole trail comes back, with a `Total` of zero. `GetTimeline` works against both. |

| Needs a release newer than Metis v0.5.0 | Against v0.5.0 and earlier |
| :-- | :-- |
| `WithBusinessKey` | Ignored: the instance starts without a key. |
| `ReleaseVersion` | The saved cases cannot be read or run on a draft: an error, `IsNotFound` or `IsInvalid`, and nothing is deployed. |
| `StartProcessOnce`, and `StartMany` for requests with a business key | The server keeps no business key, so the case cannot be found again. Once the project has any instance the call is refused with `ErrFilterUnsupported`, from the listing it reads first; in an empty project it starts one without the key. |
| `ListInstancesOptions.BusinessKey` | Refused with `ErrFilterUnsupported`, as for the instance filter above: the server lists every instance, and the SDK will not hand that back as the case's. |
| `WorkerOptions.Version`, `WithWorkerVersion` | Ignored. |
| Simulating a draft (`Definition`, `BPMNXML`) or a replay | Refused with a 400 (`IsInvalid`): a replay or a `.bpmn` draft with a message saying it is not supported yet, a `Definition` draft as an unreadable request. `Simulate` and `SimulateBatch` of a deployed version work on both. |
| Saved simulation cases, outgoing webhooks, `SimulateWebhookDelivery`, batch operations, `ListExternalTaskWorkers`, `ListOperations`, retention holds and periods | No such route: `IsNotFound`. |
| People: `LoginPerson`, the person's tasks, API keys and task links | No such route. A request with a User's token answers `IsNotFound`; one with no token the server knows, such as every `LoginPerson`, answers `IsUnauthorized`. |

Going the other way, a v0.5.0 server asks two things of code written for an
older one: a `WithReason` on a hand-over by anybody but the task's holder, and a
form field for every variable `CompleteTask` sets. A reason sent to an older
server is ignored.

Everything else — deploying, starting, messages and signals, the task lifecycle,
external-task workers, definitions, incidents, execution paths — works against
v0.2.0 and earlier unchanged. So do `EvaluateDecision`, version promotion and
scheduling, `ListSubProcesses`, `UpdateTask`, `ActivateAdHocTask` and the
counts on `ListInstancesPage`, all of which v0.3.0 already serves.

`LatestTask` and `LatestInstance` ask for a single row; an older server ignores
that and fetches a default page, but still returns the right one.

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
