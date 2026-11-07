package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	metis "github.com/gsoultan/metis-sdk"
)

// app is the sample's backend. Everything it asks of metis goes through the
// SDK: an administrator's client to read requests back and serve the payment
// worker, and one client for each person, signed in as them, so that a
// request is started by the requester and each approval is claimed and
// completed by the person it was offered to.
type app struct {
	cfg       config
	projectID string
	admin     *metis.Client
	as        map[string]*metis.Client
	steps     []*metis.FlowStep
}

func serve(ctx context.Context, cfg config) error {
	a, err := newApp(ctx, cfg)
	if err != nil {
		return err
	}

	// The payment system, as far as metis can tell: a worker that takes the
	// "pay out" step's work, pays, and says how.
	worker := metis.NewWorker(a.admin, "expense-pay-out", "approvals-sample",
		metis.WorkerOptions{PollInterval: 500 * time.Millisecond}, payOut)
	go func() {
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Print("approvals: the payment worker stopped: ", err)
		}
	}()

	srv := &http.Server{Addr: cfg.Addr, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("approvals: listening on %s (metis at %s)", cfg.Addr, cfg.MetisURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newApp(ctx context.Context, cfg config) (*app, error) {
	flows, err := metis.ParseFlow(processXML(cfg.PublicURL), lineTypes...)
	if err != nil || len(flows) == 0 {
		return nil, fmt.Errorf("process.bpmn: %v", err)
	}
	a := &app{cfg: cfg, admin: metis.NewClient(cfg.MetisURL), as: map[string]*metis.Client{}, steps: flows[0].Steps}

	if err := a.admin.Login(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return nil, fmt.Errorf("sign in as %s: %w", cfg.AdminUser, err)
	}
	if a.projectID, err = findProject(ctx, a.admin, cfg.Project); err != nil {
		return nil, err
	}
	for _, p := range people {
		c := metis.NewClient(cfg.MetisURL)
		if err := c.Login(ctx, p.Username, cfg.PeoplePassword); err != nil {
			return nil, fmt.Errorf("sign in as %s (has seed been run?): %w", p.Username, err)
		}
		a.as[p.Username] = c
	}
	return a, nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/people", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, people) })
	mux.HandleFunc("GET /api/requests", a.listRequests)
	mux.HandleFunc("POST /api/requests", a.submit)
	mux.HandleFunc("GET /api/requests/{id}", a.request)
	mux.HandleFunc("GET /api/inbox", a.inbox)
	mux.HandleFunc("POST /api/tasks/{id}/decision", a.decide)
	mux.HandleFunc("POST /api/tasks/{id}/resubmit", a.resubmit)
	mux.HandleFunc("POST /api/budget/check", checkBudget)
	mux.Handle("/", spa(a.cfg.WebDir))
	return mux
}

// --- requests -------------------------------------------------------------

type submission struct {
	As         string  `json:"as"`
	Title      string  `json:"title"`
	Amount     float64 `json:"amount"`
	Category   string  `json:"category"`
	CostCentre string  `json:"costCentre"`
	Reason     string  `json:"reason"`
}

func (a *app) submit(w http.ResponseWriter, r *http.Request) {
	var in submission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	client, ok := a.as[in.As]
	switch {
	case !ok:
		writeError(w, http.StatusBadRequest, "pick who is submitting")
		return
	case in.Title == "" || in.Amount <= 0 || in.CostCentre == "":
		writeError(w, http.StatusBadRequest, "a request needs what it is for, an amount over 0 and a cost centre")
		return
	}
	id, err := client.StartProcess(r.Context(), a.projectID, processKey, metis.Variables{
		"title":       in.Title,
		"amount":      in.Amount,
		"category":    in.Category,
		"costCentre":  in.CostCentre,
		"reason":      in.Reason,
		"submittedBy": in.As,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not start the request: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

type requestSummary struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Amount      float64 `json:"amount"`
	SubmittedBy string  `json:"submittedBy"`
	Status      string  `json:"status"`
	// Outcome is how the request stands in words: in progress, sent back,
	// approved, rejected or no budget.
	Outcome   string    `json:"outcome"`
	CreatedAt time.Time `json:"createdAt"`
}

func outcomeOf(i metis.Instance) string {
	v := i.Variables
	for _, prefix := range []string{"supervisor", "unitHead", "finance"} {
		switch v.StringOr(prefix+"Decision", "") {
		case "reject":
			return "rejected"
		case "revise":
			if !i.IsFinished() {
				return "sent back"
			}
		}
	}
	if ok, known := v.Bool("budgetOk"); known && !ok {
		return "no budget"
	}
	if i.Status == metis.ProcessCompleted {
		return "approved"
	}
	if i.IsFinished() {
		return string(i.Status)
	}
	return "in progress"
}

func summarise(i metis.Instance) requestSummary {
	return requestSummary{
		ID:          i.ID,
		Title:       i.Variables.StringOr("title", "(untitled)"),
		Amount:      i.Variables.Float64Or("amount", 0),
		SubmittedBy: i.Variables.StringOr("submittedBy", ""),
		Status:      string(i.Status),
		Outcome:     outcomeOf(i),
		CreatedAt:   i.CreatedAt,
	}
}

func (a *app) listRequests(w http.ResponseWriter, r *http.Request) {
	instances, _, err := a.admin.ListInstances(r.Context(), metis.ListInstancesOptions{ProjectID: a.projectID, PageSize: 100})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not list the requests: "+err.Error())
		return
	}
	as := r.URL.Query().Get("as")
	out := []requestSummary{}
	for _, i := range instances {
		if i.Definition != nil && i.Definition.Key != "" && i.Definition.Key != processKey {
			continue
		}
		if !i.Variables.Has("title") {
			continue
		}
		if as != "" && i.Variables.StringOr("submittedBy", "") != as {
			continue
		}
		out = append(out, summarise(i))
	}
	slices.SortFunc(out, func(x, y requestSummary) int { return y.CreatedAt.Compare(x.CreatedAt) })
	writeJSON(w, http.StatusOK, out)
}

type requestDetail struct {
	requestSummary
	Variables metis.Variables `json:"variables"`
	Line      []lineStep      `json:"line"`
	History   []historyEntry  `json:"history"`
}

type historyEntry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	Step string    `json:"step,omitempty"`
}

func (a *app) request(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instance, err := a.admin.GetInstance(ctx, r.PathValue("id"))
	if metis.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "no such request")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not answer: "+err.Error())
		return
	}
	tasks, _, err := a.admin.ListTasks(ctx, metis.ListTasksOptions{ProjectID: a.projectID, InstanceID: instance.ID, PageSize: 100})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not list the request's tasks: "+err.Error())
		return
	}
	open := map[string]string{}
	for _, t := range tasks {
		if t.IsOpen() && t.Node != nil {
			open[t.Node.ID] = t.ID
		}
	}
	entries, err := a.admin.GetTimeline(ctx, instance.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not give the request's history: "+err.Error())
		return
	}
	history := make([]historyEntry, 0, len(entries))
	for _, e := range entries {
		history = append(history, historyEntry{At: e.CreatedAt, Text: e.Text(), Step: e.Step.ID})
	}
	// Newest first. The timeline comes oldest first, and steps that ran in one
	// go share a timestamp, so it is reversed rather than sorted.
	slices.Reverse(history)

	writeJSON(w, http.StatusOK, requestDetail{
		requestSummary: summarise(*instance),
		Variables:      instance.Variables,
		Line:           approvalLine(a.steps, instance, open),
		History:        history,
	})
}

// --- approvals ------------------------------------------------------------

type inboxItem struct {
	TaskID    string          `json:"taskId"`
	Step      string          `json:"step"`
	StepName  string          `json:"stepName"`
	Request   requestSummary  `json:"request"`
	Variables metis.Variables `json:"variables"`
	Since     time.Time       `json:"since"`
}

// inbox is what is waiting for one person: the tasks offered to their group
// and the ones already given to them, as metis decides it for that person.
func (a *app) inbox(w http.ResponseWriter, r *http.Request) {
	as := r.URL.Query().Get("as")
	client, ok := a.as[as]
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is looking")
		return
	}
	ctx := r.Context()
	offered, _, err := client.ListClaimableTasks(ctx, metis.PageOptions{PageSize: 100})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not list the tasks: "+err.Error())
		return
	}
	mine, _, err := client.ListTasksByAssignee(ctx, as, metis.PageOptions{PageSize: 100})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not list the tasks: "+err.Error())
		return
	}
	seen := map[string]bool{}
	out := []inboxItem{}
	for _, t := range append(mine, offered...) {
		if seen[t.ID] || !t.IsOpen() || t.Node == nil || (approvals[t.Node.ID] == "" && t.Node.ID != reviseStep) {
			continue
		}
		seen[t.ID] = true
		item := inboxItem{TaskID: t.ID, Step: t.Node.ID, StepName: t.Name, Variables: t.Variables, Since: t.CreatedAt}
		if t.Instance != nil {
			item.Request = summarise(*t.Instance)
			if item.Request.Title == "(untitled)" {
				item.Request = summarise(metis.Instance{ID: t.Instance.ID, Status: t.Instance.Status, Variables: t.Variables, CreatedAt: t.CreatedAt})
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

type decisionIn struct {
	As       string `json:"as"`
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

// decide records one approver's answer. The person claims the task and
// completes it with their decision, both as themselves; the gateway after the
// step reads the decision and sends the request on, or to "rejected".
func (a *app) decide(w http.ResponseWriter, r *http.Request) {
	var in decisionIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the decision could not be read")
		return
	}
	client, ok := a.as[in.As]
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is deciding")
		return
	}
	if in.Decision != "approve" && in.Decision != "reject" && in.Decision != "revise" {
		writeError(w, http.StatusBadRequest, "a decision is approve, reject or revise")
		return
	}
	ctx := r.Context()
	task, err := client.GetTask(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not find the task: "+err.Error())
		return
	}
	prefix := ""
	if task.Node != nil {
		prefix = approvals[task.Node.ID]
	}
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "that task is not one of this process's approvals")
		return
	}
	err = client.ClaimAndComplete(ctx, task.ID, metis.Variables{
		prefix + "Decision": in.Decision,
		prefix + "Comment":  strings.TrimSpace(in.Comment),
		prefix + "By":       in.As,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not take the decision: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resubmit sends a request that was sent back on its way again, as the
// requester changed it. It completes the revise task with the new values and
// clears the three decisions, so each approver decides on what was resubmitted.
func (a *app) resubmit(w http.ResponseWriter, r *http.Request) {
	var in submission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	client, ok := a.as[in.As]
	switch {
	case !ok:
		writeError(w, http.StatusBadRequest, "pick who is resubmitting")
		return
	case in.Title == "" || in.Amount <= 0 || in.CostCentre == "":
		writeError(w, http.StatusBadRequest, "a request needs what it is for, an amount over 0 and a cost centre")
		return
	}
	ctx := r.Context()
	task, err := client.GetTask(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not find the task: "+err.Error())
		return
	}
	if task.Node == nil || task.Node.ID != reviseStep {
		writeError(w, http.StatusBadRequest, "that task is not a request waiting to be revised")
		return
	}
	if task.Variables.StringOr("submittedBy", "") != in.As {
		writeError(w, http.StatusForbidden, "only the person who submitted a request can resubmit it")
		return
	}
	err = client.ClaimAndComplete(ctx, task.ID, metis.Variables{
		"title":              in.Title,
		"amount":             in.Amount,
		"category":           in.Category,
		"costCentre":         in.CostCentre,
		"reason":             in.Reason,
		"supervisorDecision": "",
		"unitHeadDecision":   "",
		"financeDecision":    "",
		"resubmittedBy":      in.As,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "metis did not take the resubmission: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- the outside world ----------------------------------------------------

// budgets is what each cost centre has left. A real one would be a finance
// system; this one is a map, so the sample needs nothing else running.
var budgets = map[string]float64{
	"CC-210 Platform":   42_000_000,
	"CC-120 Sales":      15_000_000,
	"CC-330 Operations": 5_000_000,
}

// checkBudget is the API the "check the budget" service task calls. metis
// posts the request's variables and merges the reply into them, so budgetOk
// and budgetLeft are what the gateway after the step and the approvers read.
func checkBudget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount     float64 `json:"amount"`
		CostCentre string  `json:"costCentre"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the budget check could not be read")
		return
	}
	left := budgets[in.CostCentre]
	writeJSON(w, http.StatusOK, map[string]any{"budgetOk": in.Amount <= left, "budgetLeft": left})
}

// payOut serves the "pay out" step. It stands in for a payment system: it
// answers with how the money went, and metis completes the request.
func payOut(_ context.Context, task *metis.ExternalTask) (metis.Variables, error) {
	method := task.Variables.StringOr("paymentMethod", "bank-transfer")
	ref := fmt.Sprintf("PAY-%s", strings.ToUpper(task.ID[:min(8, len(task.ID))]))
	return metis.Variables{"paymentReference": ref, "paidVia": method, "paidAt": time.Now().UTC().Format(time.RFC3339)}, nil
}

// --- plumbing -------------------------------------------------------------

// spa serves the built React app, and its index for any path that is not a
// file, so a reload on /requests/123 still lands in the app.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean("/"+r.URL.Path))); err != nil {
			index := filepath.Join(dir, "index.html")
			if _, err := os.Stat(index); err != nil {
				http.Error(w, "the UI is not built: run `cd examples/approvals/web && npm install && npm run build`", http.StatusNotFound)
				return
			}
			http.ServeFile(w, r, index)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
