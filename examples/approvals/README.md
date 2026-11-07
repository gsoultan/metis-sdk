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

You need Go, bun or npm, and a running metis that includes
[gsoultan/metis#231](https://github.com/gsoultan/metis/pull/231): BPMN import
keeping a service task's address, a business rule task's decision and a user
task's form. Without it the budget check calls nothing, the decision step
fails and approvals cannot record their decision.

1. Start metis so it may call this app on localhost. metis refuses calls to
   private addresses by default (SSRF protection), and the budget check is
   one. In the metis repository:

   ```bash
   METIS_HTTP_ALLOW_PRIVATE_NETWORKS=true ./scripts/dev.sh backend
   ```

   That serves metis on `http://localhost:8273`. Finish its setup wizard once
   and note the administrator password.

2. In this repository:

   ```bash
   METIS_PASSWORD='the-admin-password' ./examples/approvals/run.sh
   ```

   It builds the UI, sets metis up (`seed`: the groups, the people, the
   decision table and the process; safe to run again), and serves the app on
   **http://localhost:8090**.

`run.sh seed`, `run.sh serve` and `run.sh dev` do one part each; `dev` runs
the UI with hot reload on http://localhost:5180. The settings are at the top
of `run.sh`.

Without the script:

```bash
(cd examples/approvals/web && npm install && npm run build)
METIS_PASSWORD=... go run ./examples/approvals seed
METIS_PASSWORD=... APPROVALS_WEB_DIR=examples/approvals/web/dist go run ./examples/approvals
```

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
