# Expense approvals: a web sample

A small web app that runs an expense approval on metis. The backend is Go and
talks to metis **only through metis-sdk**; the UI is React. It shows the
**approval line** of each request, with the buttons of each step on the step
itself:

```
Submitted  →  Supervisor approval  →  Unit head approval  →  Finance approval  →  Completed
[Submit]      [Approve|Revise|Reject] [Approve|Revise|Reject] [Approve|Revise|Reject]
```

*Revise* sends the request back to the requester, whose start step offers
*Revise and resubmit*; it then starts over at the supervisor. *Reject* ends
it. Rejecting and sending back need a comment.

## The whole process is data

There is no BPMN here. The process is a metis **approval line**, the list of
approvers in `line.go`:

```go
var expenseLine = metis.ApprovalLine{
	Key: "expense", Name: "Expense approval", AllowRevise: true,
	Steps: []metis.ApprovalStep{
		{ID: "supervisor", Name: "Supervisor approval", Groups: []string{"supervisors"}},
		{ID: "unit-head", Name: "Unit head approval", Groups: []string{"unit-heads"}},
		{ID: "finance", Name: "Finance approval", Groups: []string{"finance"}},
	},
	Fields: []metis.ApprovalField{{ID: "amount", Label: "Amount", Type: "number"} /* … */},
}
```

and running it is five calls:

```go
approvals := client.Approvals(projectID)
approvals.Define(ctx, expenseLine)                         // seed: deploy it
req, _ := approvals.Submit(ctx, "expense", vars)           // as the requester
approvals.Approve(ctx, req.Waiting().Waiting.TaskID, "")   // as the approver (or Reject, Revise)
approvals.Resubmit(ctx, revision.TaskID, corrected)        // as the requester, after a Revise
req, _ = approvals.Get(ctx, req.ID)                        // where it is: each step, who decided
```

metis generates the process, keeps every decision on the request's trail, and
says where each request is and who it waits on. Changing `expenseLine` and
running `seed` again deploys a new version. Requests already running keep the
version they were submitted on.

## The people

`seed` creates five metis accounts with the password `sample-password`
(`APPROVALS_PEOPLE_PASSWORD` changes it), with `EnsureAccount`. The **Acting
as** picker in the UI switches between them. Each has their own SDK client,
signed in as them, so metis itself decides who may act: Dewi cannot approve
the supervisor's step, and the server says so.

| Person | Group | Does |
| :-- | :-- | :-- |
| Rina | requesters | submits, and revises what is sent back |
| Budi | supervisors | supervisor approval |
| Sari | unit-heads | unit head approval |
| Dewi | finance | finance approval |
| Tono | | supervisor once Budi leaves (see below) |

## Running it

One command, from this repository:

```bash
./examples/approvals/run.sh
```

Then open **http://localhost:8090**. Ctrl-C stops it.

When no metis is running, the script starts one for you. It uses the metis
checkout next to this repository (`../metis`) or clones it into
`~/.cache/metis-approvals`. Its `scripts/dev.sh` runs PostgreSQL in an Apple
container (macOS on Apple silicon; `brew install container`), and the script
sets metis up with the administrator `admin` / `metis-admin`. The script then
builds the UI, creates the sample's people and approval line (safe to run
again), and serves the app.

You need Go, bun or npm, and git. The first run takes a few minutes, because
metis and its UI are built once. PostgreSQL keeps running afterwards:
`container stop metis-dev-pg` stops it.

The metis needs approval lines, which are in
[gsoultan/metis#232](https://github.com/gsoultan/metis/pull/232). Until that
is merged, start its branch:
`METIS_REF=claude/project-thread-xb7u13 ./examples/approvals/run.sh`.

To use a metis you already run, pass its administrator:
`METIS_PASSWORD=... ./examples/approvals/run.sh`.

`run.sh seed`, `run.sh serve` and `run.sh dev` do one part each, against a
metis that is running; `dev` runs the UI with hot reload on
http://localhost:5180. The settings are at the top of `run.sh`.

## A walk through

1. As **Rina**, open *New request*, fill it in and press **Submit**. The
   request page opens with the supervisor's step current.
2. Switch to **Budi**. The supervisor step shows a comment box and
   **Approve**, **Revise**, **Reject**. Approve.
3. Switch to **Sari** and press **Revise** with a comment. The line marks her
   step *Sent back* and Rina's start step current.
4. Switch to **Rina**, press **Revise and resubmit**, change the amount and
   resubmit. The request starts over at the supervisor, round 2.
5. Approve as Budi, Sari and Dewi. The line ends at *Completed*.

## When the line changes: Budi leaves

Requests already running keep the line they were submitted on. That is
what you want most of the time, but not when the person a request waits for
is gone. With the app running, in another terminal:

1. As Rina, submit two requests. Approve the second one as Budi, so it moves
   on to Sari.
2. Run `./examples/approvals/run.sh change`. It deploys a new version of the
   line where the supervisor step is Tono's, then lists the requests still on
   older versions with `Approvals.Behind`, and decides one by one:

   ```
   ✓ line changed: version 2 gives the supervisor step to Tono
     version 1: "Taxi to client" waits for the supervisor → move it to Tono
     version 1: "Hotel Bandung" is at Unit head approval → keep it on its line
   ✓ moved 1 request(s) to version 2: Tono now has them
   ```

3. Reload the first request. Its supervisor step now waits for **Tono**.
   Budi is refused if he tries, and Tono can approve. The second request stays
   on version 1 and goes on to Sari and Dewi as before.

The move is `Approvals.Move`, which uses metis's instance migration, so the
same safety rules apply. A request is left where it is, with the reason, if
its step is gone from the new line or moving it would skip an approval. New
requests always follow the newest line.

`change.go` is the whole scenario. In metis, a person in a project's People
can also be deactivated (`client.Project(id).People().Deactivate`), handing
their open tasks to someone else. That is the other way to settle a leaver's
work when the line itself stays the same.

## How it is built

| File | What is in it |
| :-- | :-- |
| `line.go` | the approval line, and how a request is drawn from what metis says about it |
| `seed.go` | setting metis up: `EnsureAccount` for each person, `Approvals.Define` for the line |
| `app.go` | the backend's API for the UI |
| `main.go` | settings and the `seed` / `serve` commands |
| `web/` | the React app (Vite + TypeScript) |

The Go backend serves the built UI from `web/dist` on disk rather than
embedding it, so this example stays inside the SDK's module without a
`go.mod` of its own, adds nothing to the SDK's (still stdlib-only)
dependencies, and `go vet ./...` passes on a clean clone without building the
UI first.

The backend's own API, which the UI calls:

| Route | Does |
| :-- | :-- |
| `GET /api/people` | the four people |
| `GET /api/requests?as=` | requests, the requester's own when `as` is given (`Approvals.List`) |
| `POST /api/requests` | submit (`Approvals.Submit` as the requester) |
| `GET /api/requests/{id}` | a request with its approval line and history (`Approvals.Get`) |
| `GET /api/inbox?as=` | what waits for that person (`Inbox`) |
| `POST /api/tasks/{id}/decision` | approve, revise or reject (`Approvals.Decide` as the approver) |
| `POST /api/tasks/{id}/resubmit` | resubmit after revising (`Approvals.Resubmit` as the requester) |

This is a sample: the backend holds each person's session and trusts the
*Acting as* picker, which a real app would replace with its own sign-in.
