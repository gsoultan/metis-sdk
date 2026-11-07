# Expense approvals: a web sample

A small web app that runs an expense approval on metis. The backend is Go and
talks to metis **only through metis-sdk**; the UI is React. It shows the
**approval line** of each request, the start and the three approvals, with
the buttons of each step on the step itself:

```
Submitted  →  Supervisor approval  →  Unit head approval  →  Finance approval  →  Completed
[Submit]      [Approve|Revise|Reject] [Approve|Revise|Reject] [Approve|Revise|Reject]
```

*Revise* sends the request back to the requester, whose start step offers
*Revise and resubmit*; the request then goes through the checks again and
starts over at the supervisor. *Reject* ends it.

## Every kind of task metis runs

The approval line shows the steps people do. Between them, metis does the
rest, and `process.bpmn` has one of each kind of task:

| Step | Kind | What it does |
| :-- | :-- | :-- |
| Submitted | start event | Rina's form starts the process (`StartProcess`) |
| Check the form | **script task** | JavaScript works out `needsReceipt` and `checkedAt` |
| Check the budget | **service task that calls an API** | metis POSTs the request to this app's `/api/budget/check`; the reply's `budgetOk` and `budgetLeft` become variables. A budget that cannot cover it ends the request |
| Classify the request | **business rule task** | the `expense-classify` decision table (`decision.json`) sets `riskLevel` and `paymentMethod` |
| Supervisor, unit head, finance approval | **user tasks** | offered to a group each; the person claims and completes it as themselves (`ClaimAndComplete`) |
| Revise the request | **user task** | offered back to the requester when an approver chooses *Revise* |
| Pay out | **service task served by a worker** | this app's `metis.Worker` on topic `expense-pay-out` stands in for the payment system |
| (after each approval) | exclusive gateway | approve, revise or reject |

## The people

`seed` creates four metis users with the password `sample-password`
(`APPROVALS_PEOPLE_PASSWORD` changes it). The **Acting as** picker in the UI
switches between them. Each has their own SDK client, signed in as them, so
metis itself decides who may act: Dewi cannot approve the supervisor's step,
and the server says so.

| Person | Group | Does |
| :-- | :-- | :-- |
| Rina | requesters | submits, and revises what is sent back |
| Budi | supervisors | supervisor approval |
| Sari | unit-heads | unit head approval |
| Dewi | finance | finance approval |

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
sets metis up with the administrator `admin` / `metis-admin`. That metis also
allows the budget check's call to localhost. The script then builds the UI,
creates the sample's people, groups, decision table and process (safe to run
again), and serves the app.

You need Go, bun or npm, and git. The first run takes a few minutes, because
metis and its UI are built once. PostgreSQL keeps running afterwards:
`container stop metis-dev-pg` stops it.

The metis has to include
[gsoultan/metis#231](https://github.com/gsoultan/metis/pull/231) (merged):
BPMN import keeping a service task's address, a business rule task's decision
and a user task's form. `run.sh` starts metis's `main`, which has it.

To use a metis you already run, start it with
`METIS_HTTP_ALLOW_PRIVATE_NETWORKS=true` and pass its administrator:
`METIS_PASSWORD=... ./examples/approvals/run.sh`.

`run.sh seed`, `run.sh serve` and `run.sh dev` do one part each, against a
metis that is running; `dev` runs the UI with hot reload on
http://localhost:5180. The settings are at the top of `run.sh`.

## A walk through

1. As **Rina**, open *New request*, fill it in and press **Submit** on the
   line. The request page opens; the checks run in a second and the
   supervisor's step turns current.
2. Switch to **Budi**. The supervisor step shows a comment box and
   **Approve**, **Revise**, **Reject**. Approve.
3. Switch to **Sari** and press **Revise** with a comment. The line marks her
   step *Sent back* and Rina's start step current.
4. Switch to **Rina**, press **Revise and resubmit**, change the amount and
   resubmit. Every approval is pending again.
5. Approve as Budi, Sari and Dewi. The payment worker pays it, and the line
   ends at *Completed* with the payment reference.

Try a request over Rp 5.000.000 on *CC-330 Operations* to see the budget
check stop one, and *Reject* to see a rejection.

## How it is built

| File | What is in it |
| :-- | :-- |
| `process.bpmn` | the process, deployed with `ImportDefinition` |
| `decision.json` | the decision table |
| `main.go` | settings and the `seed` / `serve` commands |
| `seed.go` | setting metis up. The process goes through the SDK; users, groups and the decision table are administration the SDK does not cover, so those few calls use the REST API directly |
| `app.go` | the backend's API for the UI, the budget API and the payment worker |
| `line.go` | the approval line, worked out from the instance and its open tasks |
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
| `GET /api/requests?as=` | requests, the requester's own when `as` is given |
| `POST /api/requests` | submit (`StartProcess` as the requester) |
| `GET /api/requests/{id}` | a request with its approval line and history (`GetInstance`, `ListTasks`, `GetTimeline`) |
| `GET /api/inbox?as=` | what waits for that person (`ListClaimableTasks`, `ListTasksByAssignee`) |
| `POST /api/tasks/{id}/decision` | approve, revise or reject (`ClaimAndComplete` as the approver) |
| `POST /api/tasks/{id}/resubmit` | resubmit after revising (`ClaimAndComplete` as the requester) |
| `POST /api/budget/check` | the budget API metis calls |

This is a sample: the backend holds each person's session and trusts the
*Acting as* picker, which a real app would replace with its own sign-in.
