package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	metis "github.com/gsoultan/metis-sdk"
)

// app is the sample's backend. Everything it asks of metis goes through the
// SDK's approval lines: an administrator's client to read requests back, and
// one client for each person, signed in as them, so that a request is
// submitted by the requester and each step decided by the person it was
// offered to. metis decides who may decide what.
type app struct {
	cfg       config
	projectID string
	admin     *metis.Client
	as        map[string]*metis.Client
}

func serve(ctx context.Context, cfg config) error {
	a, err := newApp(ctx, cfg)
	if err != nil {
		return err
	}
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
	a := &app{cfg: cfg, admin: metis.NewClient(cfg.MetisURL), as: map[string]*metis.Client{}}
	if err := a.admin.Login(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return nil, err
	}
	projectID, err := findProject(ctx, a.admin, cfg.Project)
	if err != nil {
		return nil, err
	}
	a.projectID = projectID
	for _, p := range people {
		c := metis.NewClient(cfg.MetisURL)
		if err := c.Login(ctx, p.Username, cfg.PeoplePassword); err != nil {
			return nil, err
		}
		a.as[p.Username] = c
	}
	return a, nil
}

// approvals is the project's approval lines, as username.
func (a *app) approvals(username string) (metis.Approvals, bool) {
	c, ok := a.as[username]
	if !ok {
		return metis.Approvals{}, false
	}
	return c.Approvals(a.projectID), true
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

func (s submission) variables() metis.Variables {
	return metis.Variables{"title": s.Title, "amount": s.Amount, "category": s.Category, "costCentre": s.CostCentre, "reason": s.Reason}
}

func readSubmission(w http.ResponseWriter, r *http.Request) (submission, bool) {
	var in submission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the request could not be read")
		return in, false
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || in.Amount <= 0 || in.CostCentre == "" {
		writeError(w, http.StatusBadRequest, "a request needs what it is for, an amount over 0 and a cost centre")
		return in, false
	}
	return in, true
}

func (a *app) submit(w http.ResponseWriter, r *http.Request) {
	in, ok := readSubmission(w, r)
	if !ok {
		return
	}
	approvals, ok := a.approvals(in.As)
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is submitting")
		return
	}
	req, err := approvals.Submit(r.Context(), lineKey, in.variables())
	if err != nil {
		writeMetisError(w, "metis did not take the request", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": req.ID})
}

type requestSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Amount      float64   `json:"amount"`
	SubmittedBy string    `json:"submittedBy"`
	Status      string    `json:"status"`
	Outcome     string    `json:"outcome"`
	CreatedAt   time.Time `json:"createdAt"`
}

var outcomes = map[string]string{
	metis.RequestInProgress: "in progress",
	metis.RequestSentBack:   "sent back",
	metis.RequestApproved:   "approved",
	metis.RequestRejected:   "rejected",
	metis.RequestWithdrawn:  "withdrawn",
	metis.RequestStopped:    "stopped",
}

func summarise(req *metis.ApprovalRequest) requestSummary {
	return requestSummary{
		ID:          req.ID,
		Title:       req.Variables.StringOr("title", "(untitled)"),
		Amount:      req.Variables.Float64Or("amount", 0),
		SubmittedBy: req.SubmittedBy,
		Status:      req.Status,
		Outcome:     outcomes[req.Status],
		CreatedAt:   req.SubmittedAt,
	}
}

func (a *app) listRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := a.admin.Approvals(a.projectID).List(r.Context(), lineKey, r.URL.Query().Get("as"))
	if err != nil {
		writeMetisError(w, "metis did not list the requests", err)
		return
	}
	out := make([]requestSummary, 0, len(requests))
	for i := range requests {
		out = append(out, summarise(&requests[i]))
	}
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
	req, err := a.admin.Approvals(a.projectID).Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMetisError(w, "metis did not find the request", err)
		return
	}
	writeJSON(w, http.StatusOK, requestDetail{
		requestSummary: summarise(req),
		Variables:      req.Variables,
		Line:           approvalLine(req),
		History:        history(req),
	})
}

// --- deciding -------------------------------------------------------------

type inboxItem struct {
	TaskID   string         `json:"taskId"`
	Step     string         `json:"step"`
	StepName string         `json:"stepName"`
	Request  requestSummary `json:"request"`
	Since    time.Time      `json:"since"`
}

// inbox is what is waiting for one person: their tasks and the ones offered
// to their group, which the SDK's Inbox puts together.
func (a *app) inbox(w http.ResponseWriter, r *http.Request) {
	client, ok := a.as[r.URL.Query().Get("as")]
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is looking")
		return
	}
	tasks, err := client.Inbox(r.Context())
	if err != nil {
		writeMetisError(w, "metis did not list the tasks", err)
		return
	}
	out := []inboxItem{}
	for _, t := range tasks {
		step := t.ApprovalStep()
		if step == "" || t.Instance == nil {
			continue
		}
		req := &metis.ApprovalRequest{ID: t.Instance.ID, Variables: t.Variables, SubmittedBy: t.Variables.StringOr("submittedBy", ""),
			Status: metis.RequestInProgress, SubmittedAt: t.CreatedAt}
		if step == "revise" {
			req.Status = metis.RequestSentBack
		}
		out = append(out, inboxItem{TaskID: t.ID, Step: step, StepName: t.Name, Request: summarise(req), Since: t.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

type decisionIn struct {
	As       string `json:"as"`
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

// decide is one approver's answer. Who may give it, whether it needs a
// comment and where the request goes next are metis's to say.
func (a *app) decide(w http.ResponseWriter, r *http.Request) {
	var in decisionIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the decision could not be read")
		return
	}
	approvals, ok := a.approvals(in.As)
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is deciding")
		return
	}
	if _, err := approvals.Decide(r.Context(), r.PathValue("id"), in.Decision, in.Comment, nil); err != nil {
		writeMetisError(w, "metis did not take the decision", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resubmit sends a request that was sent back through the line again, as the
// requester corrected it.
func (a *app) resubmit(w http.ResponseWriter, r *http.Request) {
	in, ok := readSubmission(w, r)
	if !ok {
		return
	}
	approvals, ok := a.approvals(in.As)
	if !ok {
		writeError(w, http.StatusBadRequest, "pick who is resubmitting")
		return
	}
	if _, err := approvals.Resubmit(r.Context(), r.PathValue("id"), in.variables()); err != nil {
		writeMetisError(w, "metis did not take the resubmission", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- plumbing -------------------------------------------------------------

// writeMetisError passes on what metis said, with the status that fits: a
// refusal stays a refusal, so the UI can say "not yours to decide".
func writeMetisError(w http.ResponseWriter, what string, err error) {
	status := http.StatusBadGateway
	switch {
	case metis.IsUnauthorized(err):
		status = http.StatusForbidden
	case metis.IsInvalid(err):
		status = http.StatusBadRequest
	case metis.IsNotFound(err):
		status = http.StatusNotFound
	}
	writeError(w, status, what+": "+err.Error())
}

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
