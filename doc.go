// Package metis is the Go client for a Metis server — a BPMN 2.0 workflow
// engine.
//
// It depends on nothing outside the standard library.
//
// # The model, in one pass
//
// Five nouns carry everything. They nest, roughly, in this order:
//
//	Project      an organization's container. Everything below belongs to one,
//	             and most calls take its ID. ListProjects is where you find it.
//
//	Definition   a deployed process *template*: the BPMN diagram, parsed and
//	             stored. It is a blueprint — it does not run. Deploy one with
//	             ImportDefinition.
//
//	Instance     one *running execution* of a definition. Starting a process
//	             creates an instance; the definition is unchanged and can have
//	             thousands of instances at once. An instance carries Variables
//	             and has a ProcessStatus.
//
//	Node         one step in the definition — a BPMN element, identified by the
//	             ID the diagram gave it. Definitions have nodes; instances move
//	             through them.
//
//	Task         one step of one instance that is *waiting for somebody or
//	             something outside the engine*. This is the noun with two kinds,
//	             and the distinction is the one thing worth reading twice.
//
// The relationship in a sentence: you deploy a *definition* into a *project*,
// start an *instance* of it, and that instance walks its *nodes* — pausing at
// each *task* until the outside world answers.
//
// # The two kinds of task
//
// Both are work an instance is blocked on. They differ in who does the work,
// and that changes how you get it and how long you have.
//
//	                  UserTask                    ExternalTask
//	who does it       a person                    a program (your service)
//	BPMN element      userTask                    serviceTask with a topic
//	how you get it    ListTasks — an inbox        FetchAndLock — a long poll
//	exclusivity       ClaimTask, held until       a lock with an expiry, taken
//	                  released or completed       automatically on fetch
//	time limit        none; it waits for a human  LockDuration, then it returns
//	                                              to the pool for another worker
//	finish it with    CompleteTask                CompleteExternalTask
//	                                              (or FailExternalTask)
//
// A [UserTask] sits in somebody's inbox. Nothing expires; a task can wait a
// week for an approval. You claim it so two people do not work it in parallel,
// then complete it with whatever the person decided.
//
// An [ExternalTask] is how your own service performs a step of somebody else's
// process — charge a card, send a letter, call an API the engine cannot reach.
// You take a *lock* rather than a claim, because a program can crash: if the
// lock expires the task simply becomes fetchable again, and another worker
// picks it up. Use [Worker] and the loop is written for you.
//
// # Variables
//
// [Variables] is the data an instance carries. It is a map, so values arrive as
// any, and JSON decoding means a number is a float64 no matter what you put in.
// The typed accessors exist so that fact cannot surprise you:
//
//	amount, ok := task.Variables.Float64("amount")  // 900, true
//	count, ok := task.Variables.Int("amount")       // 900, true — 900.0 is a whole number
//	name := task.Variables.StringOr("name", "anonymous")
//
// A plain type assertion works too, but v.(int) panics on every number the
// server ever sends. See [Variables] for the full set.
//
// # Types, when you know the shape
//
// The map is the wire's shape, not one you have to work in. Where your side
// knows what a process carries, declare it and let encoding/json do the
// conversion once:
//
//	type Refund struct {
//		ChargeID string  `json:"chargeID"`
//		Amount   float64 `json:"amount"`
//		Attempt  int     `json:"attempt"`   // an int, because you said so
//	}
//
//	refund, err := task.Variables.As[Refund]()
//
// [NewTypedWorker] does this for a whole worker, so the handler receives a
// struct and returns one — see [TypedTask].
//
// There is no generic Get[T](key) accessor, and that is deliberate rather than
// an omission. Values arrive as any and JSON numbers are all float64, so
// converting one to an arbitrary T means a hard-coded list of types inside a
// signature that claims to take every type: Get[int] would work and Get[uint]
// would silently return false. [Variables.Int] and its siblings say what they
// handle, and [Variables.As] is where a type parameter is honest — the shape is
// yours, so the conversion is a decoder's job rather than a guess.
//
// # A first program
//
//	client := metis.NewClient("http://localhost:8080")
//	if err := client.Login(ctx, "admin", "secret"); err != nil {
//		return err
//	}
//
//	instanceID, err := client.StartProcess(ctx, projectID, "invoice-approval",
//		metis.Variables{"amount": 900})
//
// From there: [Client.ListTasks] for the human half, [Worker] for the machine
// half, and [Client.GetTimeline] to read back what happened in plain language.
//
// To find a run you did not start yourself, [Client.ListInstances] lists a
// project's instances newest first and [Client.LatestInstance] returns the most
// recent one.
//
// For the shape of a process rather than a run of it,
// [Client.ListUserTaskNodes] returns a definition's human steps and
// [Client.ListDefinitionNodes] returns every node in the diagram.
// [Client.ListDefinitions] is where you find the definition ID those take, for
// a process somebody else deployed.
//
// When a run goes wrong, [Client.ListIncidents] is the answer to why: an
// instance at [ProcessFailed] has one or more incidents naming the step that
// failed and what it said. [Client.GetExecutionPath] shows the route it took to
// get there, loops included.
//
// # Errors
//
// Non-2xx answers come back as [*APIError]. Branch on [IsNotFound] and
// [IsUnauthorized] rather than on status codes — under tenant scoping, another
// organization's resource answers 404, so "not yours" and "does not exist" are
// deliberately indistinguishable.
//
// # Identity
//
// Who you are is the token, always. [Client.Login] stores one, or supply your
// own with [WithToken]. Actions that record an actor — claiming a task,
// completing it — record the token's user, and the server does not accept an
// override. An application acting for many people therefore needs a client per
// person, not one client passing user IDs around.
package metis
