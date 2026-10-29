package metis

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Outgoing webhooks are Metis telling your system what happened in a project:
// each event a webhook asks for is posted to its address, signed with its own
// secret, until the receiver accepts it. Everything here needs a server newer
// than **Metis v0.5.0**; an older one answers 404 ([IsNotFound]).

// WebhookEvent names a kind of event an outgoing webhook can be sent. The
// names are a public contract and never change.
type WebhookEvent string

const (
	// EventProcessStarted is an instance of a process starting.
	EventProcessStarted WebhookEvent = "process.started"
	// EventProcessCompleted is an instance finishing.
	EventProcessCompleted WebhookEvent = "process.completed"
	// EventTaskCreated is a task waiting for somebody.
	EventTaskCreated WebhookEvent = "task.created"
	// EventTaskCompleted is somebody completing a task.
	EventTaskCompleted WebhookEvent = "task.completed"
	// EventIncidentRaised is an incident opened at a step: a call or a worker
	// failed with no retries left, or somebody held the instance there. The
	// delivery names the incident and not what went wrong; read that with
	// [Client.ListIncidents] or GET /api/v1/incidents/{id}.
	EventIncidentRaised WebhookEvent = "incident.raised"
)

// OutgoingWebhook is an address a project's events are posted to.
type OutgoingWebhook struct {
	ID        string         `json:"id"`
	Project   ProjectRef     `json:"project_id"`
	Name      string         `json:"name"`
	URL       string         `json:"url"`
	Events    []WebhookEvent `json:"events"`
	Enabled   bool           `json:"enabled"`
	CreatedAt time.Time      `json:"created_at"`
	// Secret signs every delivery. It is set only in what
	// [Client.CreateOutgoingWebhook] returns, and no read gives it back: put
	// it in the receiver's settings then.
	Secret string `json:"secret,omitempty"`
}

// ListOutgoingWebhooks lists a project's outgoing webhooks, oldest first,
// without their secrets. Anybody in the project's organization may.
func (c *Client) ListOutgoingWebhooks(ctx context.Context, projectID string) ([]OutgoingWebhook, error) {
	var out struct {
		Webhooks []OutgoingWebhook `json:"webhooks"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID)+"/outgoing-webhooks", nil, &out)
	return out.Webhooks, err
}

// CreateOutgoingWebhook registers an address for a project's events and
// returns the webhook with its [OutgoingWebhook.Secret], **shown this once**.
//
//	hook, err := client.CreateOutgoingWebhook(ctx, projectID, "Finance ledger",
//		"https://ledger.example.com/hooks/metis",
//		metis.EventProcessCompleted, metis.EventTaskCompleted)
//	// store hook.Secret where the receiver reads it
//
// The address must be https, with no user name or password in it, and is
// checked against the server's egress policy when saved and at every attempt.
// It is an administrator's.
func (c *Client) CreateOutgoingWebhook(ctx context.Context, projectID, name, address string, events ...WebhookEvent) (*OutgoingWebhook, error) {
	if len(events) == 0 {
		return nil, errors.New("metis: CreateOutgoingWebhook needs at least one event to send")
	}
	var out struct {
		Webhook *OutgoingWebhook `json:"webhook"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/outgoing-webhooks", map[string]any{
		"name":   name,
		"url":    address,
		"events": events,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.Webhook, nil
}

// SetOutgoingWebhookEnabled switches a webhook on or off. Switched off, what
// it is owed is held until it is switched on again. An administrator's.
func (c *Client) SetOutgoingWebhookEnabled(ctx context.Context, webhookID string, enabled bool) error {
	return c.do(ctx, http.MethodPost, outgoingWebhookPath(webhookID)+"/enabled", map[string]any{"enabled": enabled}, nil)
}

// DeleteOutgoingWebhook removes a webhook, and with it whatever it was still
// owed. An administrator's.
func (c *Client) DeleteOutgoingWebhook(ctx context.Context, webhookID string) error {
	return c.do(ctx, http.MethodDelete, outgoingWebhookPath(webhookID), nil, nil)
}

func outgoingWebhookPath(webhookID string) string {
	return "/api/v1/outgoing-webhooks/" + url.PathEscape(webhookID)
}

// WebhookTest is a test delivery: the request exactly as it went out, and what
// the receiver said back.
type WebhookTest struct {
	Delivery DeliveryRef  `json:"delivery_id"`
	Event    WebhookEvent `json:"event"`
	// Accepted is whether the receiver answered 2xx in time.
	Accepted   bool  `json:"accepted"`
	DurationMS int64 `json:"duration_ms"`
	Request    struct {
		Method string `json:"method"`
		URL    string `json:"url"`
		// Headers carry the signature, which does not reveal the secret.
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"request"`
	Response struct {
		StatusCode int `json:"status_code,omitempty"`
		// Body is the start of the receiver's answer.
		Body string `json:"body,omitempty"`
		// Error says why there was no answer.
		Error string `json:"error,omitempty"`
	} `json:"response"`
}

// SendOutgoingWebhookTest posts a signed sample of an event to a webhook now,
// whether or not it is switched on, and returns the request and the answer.
// The sample is marked "test": true and names a made-up instance with the
// business key TEST-0001. An empty event sends the webhook's first. A test is
// logged like any delivery and never retried. An administrator's.
func (c *Client) SendOutgoingWebhookTest(ctx context.Context, webhookID string, event WebhookEvent) (*WebhookTest, error) {
	body := map[string]any{}
	if event != "" {
		body["event"] = event
	}
	var out struct {
		Test *WebhookTest `json:"test"`
	}
	if err := c.do(ctx, http.MethodPost, outgoingWebhookPath(webhookID)+"/test", body, &out); err != nil {
		return nil, err
	}
	return out.Test, nil
}

// DeliveryStatus is where a delivery stands.
type DeliveryStatus string

const (
	// DeliveryPending is waiting for its next attempt.
	DeliveryPending DeliveryStatus = "pending"
	// DeliveryDelivered was accepted by the receiver.
	DeliveryDelivered DeliveryStatus = "delivered"
	// DeliveryFailed ran out of attempts, was refused for good, or was
	// stopped. It stays in the log.
	DeliveryFailed DeliveryStatus = "failed"
)

// WebhookDelivery is one entry of a webhook's delivery log.
type WebhookDelivery struct {
	ID       string         `json:"id"`
	Webhook  WebhookRef     `json:"webhook_id"`
	Event    WebhookEvent   `json:"event"`
	Status   DeliveryStatus `json:"status"`
	Attempts int            `json:"attempts"`
	// NextAttemptAt is set only while the delivery is pending.
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastStatusCode int        `json:"last_status_code,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	// LastResponse is the start of the receiver's last answer.
	LastResponse string `json:"last_response,omitempty"`
	Test         bool   `json:"test,omitempty"`
	// Body is what was posted.
	Body      json.RawMessage `json:"body"`
	CreatedAt time.Time       `json:"created_at"`
}

// ListDeliveriesOptions filters and pages a webhook's delivery log.
type ListDeliveriesOptions struct {
	// Status keeps one kind. Empty is every one.
	Status DeliveryStatus
	// Before continues a listing: the NextBefore of the page before.
	Before string
	// Limit is how many to a page, at most 200. Zero is the server's 50.
	Limit int
}

// DeliveryPage is one page of a delivery log, newest first.
type DeliveryPage struct {
	Deliveries []WebhookDelivery `json:"deliveries"`
	// NextBefore goes back as [ListDeliveriesOptions.Before] to read on. Empty
	// when there is nothing more.
	NextBefore string `json:"next_before,omitempty"`
}

// ListOutgoingWebhookDeliveries reads a page of a webhook's delivery log,
// newest first. Deliveries are kept for 30 days. An administrator's.
func (c *Client) ListOutgoingWebhookDeliveries(ctx context.Context, webhookID string, opts ListDeliveriesOptions) (*DeliveryPage, error) {
	query := url.Values{}
	if opts.Status != "" {
		query.Set("status", string(opts.Status))
	}
	if opts.Before != "" {
		query.Set("before", opts.Before)
	}
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	path := outgoingWebhookPath(webhookID) + "/deliveries"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out DeliveryPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RetryOutgoingWebhookDelivery makes a pending or failed delivery due at
// once. A failed one gets one more attempt and keeps its count, so a refusal
// fails it again rather than starting a day of retries over. An
// administrator's.
func (c *Client) RetryOutgoingWebhookDelivery(ctx context.Context, deliveryID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/outgoing-webhook-deliveries/"+url.PathEscape(deliveryID)+"/retry", nil, nil)
}

// StopOutgoingWebhookDelivery fails a pending delivery, so nothing more is
// sent of it. An administrator's.
func (c *Client) StopOutgoingWebhookDelivery(ctx context.Context, deliveryID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/outgoing-webhook-deliveries/"+url.PathEscape(deliveryID)+"/stop", nil, nil)
}

// --- Receiving a delivery ---------------------------------------------------

// The headers a signed delivery carries, in either direction.
const (
	// HeaderSignature is "v2=" and the hex HMAC-SHA256 of
	// "<timestamp>.<delivery id>.<raw body>", keyed with the webhook's secret.
	HeaderSignature = "X-Metis-Signature"
	// HeaderTimestamp is when the delivery was signed, in Unix seconds.
	HeaderTimestamp = "X-Metis-Timestamp"
	// HeaderDeliveryID is the delivery's ID, the same on every attempt at it.
	HeaderDeliveryID = "X-Delivery-Id"
	// HeaderEvent names an outgoing delivery's event, to route on before
	// parsing.
	HeaderEvent = "X-Metis-Event"
)

// SignatureTolerance is how far a delivery's timestamp may be from the
// receiver's clock, either way, before it is refused as stale.
const SignatureTolerance = 5 * time.Minute

// maxDeliveryIDLength is the longest delivery ID Metis accepts.
const maxDeliveryIDLength = 191

// ErrBadSignature means a delivery was not signed with the secret it was
// checked against: forged, changed after signing, or the wrong secret.
var ErrBadSignature = errors.New("metis: the webhook signature does not match the delivery")

// ErrStaleSignature means a delivery was signed more than
// [SignatureTolerance] from the receiver's clock.
var ErrStaleSignature = errors.New("metis: the webhook delivery was not signed within five minutes of this clock")

// SignWebhook returns the X-Metis-Signature value for a delivery: what a
// partner posting to one of Metis's incoming webhooks sends. timestamp is the
// moment of this attempt, retries included, in Unix seconds.
//
//	ts := strconv.FormatInt(time.Now().Unix(), 10)
//	req.Header.Set(metis.HeaderTimestamp, ts)
//	req.Header.Set(metis.HeaderDeliveryID, eventID) // the same on every retry
//	req.Header.Set(metis.HeaderSignature, metis.SignWebhook(body, secret, ts, eventID))
func SignWebhook(body []byte, secret, timestamp, deliveryID string) string {
	return "v2=" + hex.EncodeToString(webhookDigest(body, secret, timestamp, deliveryID))
}

func webhookDigest(body []byte, secret, timestamp, deliveryID string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write([]byte(deliveryID))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return mac.Sum(nil)
}

// VerifyWebhook checks a delivery's signature over its raw body, then that it
// was signed within [SignatureTolerance] of now and carries a delivery ID. It
// returns nil for a genuine delivery, [ErrBadSignature] for anything not
// signed with secret, and an error wrapping [ErrStaleSignature] for one signed
// too long ago or ahead. Check it before parsing the body.
func VerifyWebhook(body []byte, secret, timestamp, deliveryID, signature string, now time.Time) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("metis: VerifyWebhook needs the webhook's secret; with none, nothing can be verified")
	}
	digest, ok := strings.CutPrefix(strings.TrimSpace(signature), "v2=")
	if !ok {
		return ErrBadSignature
	}
	candidate, err := hex.DecodeString(digest)
	if err != nil || !hmac.Equal(webhookDigest(body, secret, timestamp, deliveryID), candidate) {
		return ErrBadSignature
	}
	signedAt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %s is not Unix seconds", ErrStaleSignature, HeaderTimestamp)
	}
	// Compared on the bounds rather than the difference, which would overflow
	// for a timestamp near the edge of int64 and come out looking fresh.
	limit, current := int64(SignatureTolerance/time.Second), now.Unix()
	if signedAt < current-limit || signedAt > current+limit {
		return fmt.Errorf("%w: it was signed at %d, and it is %d", ErrStaleSignature, signedAt, current)
	}
	if deliveryID == "" || strings.Contains(deliveryID, ".") || len(deliveryID) > maxDeliveryIDLength {
		return fmt.Errorf("metis: %s must be set, at most %d characters and without a dot", HeaderDeliveryID, maxDeliveryIDLength)
	}
	return nil
}

// Delivery is the body of an outgoing webhook delivery. It names what
// happened and to which case, and carries no process variables: a receiver
// that needs them asks the API with credentials of its own.
type Delivery struct {
	// EventID is the event's, the same in every webhook it went to; the
	// delivery's own ID, the same on every retry, is DeliveryID.
	EventID    string       `json:"event_id"`
	Event      WebhookEvent `json:"event"`
	OccurredAt time.Time    `json:"occurred_at"`
	Project    ProjectRef   `json:"project_id"`

	Instance *DeliveryInstance `json:"instance,omitempty"`
	Node     *DeliveryNode     `json:"node,omitempty"`
	Task     *DeliveryTask     `json:"task,omitempty"`
	Incident *DeliveryIncident `json:"incident,omitempty"`

	// Test is true on a sample sent with [Client.SendOutgoingWebhookTest].
	Test bool `json:"test,omitempty"`

	// DeliveryID is X-Delivery-Id, filled in by [ReadWebhook]: the key to
	// remember a handled delivery by, since one may arrive more than once.
	DeliveryID string `json:"-"`
}

// UnmarshalJSON reads a delivery and gives its instance's process the
// delivery's project, which the body names once.
func (d *Delivery) UnmarshalJSON(data []byte) error {
	type plain Delivery
	var wire plain
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*d = Delivery(wire)
	if d.Instance != nil {
		d.Instance.Process.Project = d.Project
	}
	return nil
}

// DeliveryInstance is the case an event is about, as the delivery describes
// it; [DeliveryInstance.Ref] loads the instance itself.
type DeliveryInstance struct {
	ID             string        `json:"id"`
	BusinessKey    string        `json:"business_key,omitempty"`
	Process        ProcessRef    `json:"-"`
	ProcessName    string        `json:"process_name,omitempty"`
	ProcessVersion int           `json:"process_version,omitempty"`
	Status         ProcessStatus `json:"status,omitempty"`
}

// Ref names the instance.
func (i DeliveryInstance) Ref() InstanceRef { return InstanceRef{ID: i.ID} }

// UnmarshalJSON reads the process from process_key.
func (i *DeliveryInstance) UnmarshalJSON(data []byte) error {
	type plain DeliveryInstance
	var wire struct {
		plain
		ProcessKey string `json:"process_key"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*i = DeliveryInstance(wire.plain)
	i.Process.Key = wire.ProcessKey
	return nil
}

// DeliveryNode is the step an event happened at.
type DeliveryNode struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Ref names the step.
func (n DeliveryNode) Ref() StepRef { return StepRef{ID: n.ID, Name: n.Name} }

// DeliveryTask is the task a task event is about.
type DeliveryTask struct {
	ID       string `json:"id,omitempty"`
	Assignee string `json:"assignee,omitempty"`
}

// Ref names the task.
func (t DeliveryTask) Ref() TaskRef { return TaskRef{ID: t.ID} }

// DeliveryIncident is the incident an incident event is about.
type DeliveryIncident struct {
	ID string `json:"id"`
}

// Ref names the incident.
func (i DeliveryIncident) Ref() IncidentRef { return IncidentRef{ID: i.ID} }

// maxDeliveryBytes bounds what ReadWebhook reads of a request. A delivery
// carries no variables, so a real one is a few hundred bytes.
const maxDeliveryBytes = 1 << 20

// ReadWebhook reads an outgoing webhook delivery from the request your
// receiver was posted, verifies it with the webhook's secret, and parses it.
//
//	func handle(w http.ResponseWriter, r *http.Request) {
//		delivery, err := metis.ReadWebhook(r, secret)
//		if err != nil {
//			http.Error(w, "not a delivery from Metis", http.StatusUnauthorized)
//			return
//		}
//		if seen(delivery.DeliveryID) { // it may come more than once
//			return
//		}
//		...
//	}
//
// Answer 2xx within ten seconds or the delivery is tried again. Deliveries
// arrive at least once and in no particular order: order by OccurredAt.
func ReadWebhook(r *http.Request, secret string) (*Delivery, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDeliveryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("metis: read the webhook delivery: %w", err)
	}
	if len(body) > maxDeliveryBytes {
		return nil, fmt.Errorf("metis: the webhook delivery is larger than %d MiB, which no delivery from Metis is", maxDeliveryBytes>>20)
	}
	deliveryID := r.Header.Get(HeaderDeliveryID)
	err = VerifyWebhook(body, secret, r.Header.Get(HeaderTimestamp), deliveryID, r.Header.Get(HeaderSignature), time.Now())
	if err != nil {
		return nil, err
	}
	var delivery Delivery
	if err := json.Unmarshal(body, &delivery); err != nil {
		return nil, fmt.Errorf("metis: parse the webhook delivery: %w", err)
	}
	delivery.DeliveryID = deliveryID
	return &delivery, nil
}

// --- Trying an incoming webhook ---------------------------------------------

// WebhookSimulation is a delivery to an incoming webhook to try without acting
// on it: the body a partner would post, and optionally the three headers it
// would sign it with.
type WebhookSimulation struct {
	Body       []byte
	Timestamp  string
	DeliveryID string
	Signature  string
}

// WebhookSimulationResult is what a real delivery like the simulated one would
// have done. None of it happened.
type WebhookSimulationResult struct {
	// WouldAct is true when a real delivery would reach at least one process.
	// With no signature given, one that checks out is assumed.
	WouldAct bool `json:"would_act"`
	// Refusal is why a real delivery would be refused, in the words its
	// sender would be answered with.
	Refusal   string `json:"refusal,omitempty"`
	Enabled   bool   `json:"enabled"`
	Signature struct {
		Checked bool   `json:"checked"`
		Valid   bool   `json:"valid"`
		Problem string `json:"problem,omitempty"`
	} `json:"signature"`
	// BodyProblem is why the body could not be read, or why the correlation
	// rule found nothing in it; nothing after it was looked at.
	BodyProblem           string `json:"body_problem,omitempty"`
	MessageName           string `json:"message_name"`
	CorrelationExpression string `json:"correlation_expression,omitempty"`
	CorrelationKey        string `json:"correlation_key,omitempty"`
	// AlreadyReceived is true when a delivery with this ID was acted on
	// recently: a real one would be a duplicate and change nothing.
	AlreadyReceived bool `json:"already_received,omitempty"`
	// Waiting is the first few instances a real delivery would move on, of
	// WaitingTotal.
	Waiting      []WebhookWaiter `json:"waiting"`
	WaitingTotal int             `json:"waiting_total"`
	// Starts is the processes a real delivery would start, which only an
	// uncorrelated webhook does.
	Starts []WebhookStart `json:"starts,omitempty"`
}

// WebhookWaiter is an instance a delivery would move on, and the step it
// waits at. Its process names no project: the answer does not say which.
type WebhookWaiter struct {
	Instance    InstanceRef
	Process     ProcessRef
	ProcessName string
	BusinessKey string
	Step        StepRef
	Since       time.Time
}

// UnmarshalJSON reads a waiter as the server writes it.
func (w *WebhookWaiter) UnmarshalJSON(data []byte) error {
	var wire struct {
		InstanceID  string    `json:"instance_id"`
		ProcessKey  string    `json:"process_key"`
		ProcessName string    `json:"process_name"`
		BusinessKey string    `json:"business_key"`
		NodeID      string    `json:"node_id"`
		NodeName    string    `json:"node_name"`
		Since       time.Time `json:"since"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*w = WebhookWaiter{
		Instance:    InstanceRef{ID: wire.InstanceID},
		Process:     ProcessRef{Key: wire.ProcessKey},
		ProcessName: wire.ProcessName,
		BusinessKey: wire.BusinessKey,
		Step:        StepRef{ID: wire.NodeID, Name: wire.NodeName},
		Since:       wire.Since,
	}
	return nil
}

// WebhookStart is a process a delivery would start. Its process names no
// project: the answer does not say which.
type WebhookStart struct {
	Process     ProcessRef
	ProcessName string
	// StartEvent is the label of the start event it would start at.
	StartEvent string
}

// UnmarshalJSON reads a start as the server writes it.
func (s *WebhookStart) UnmarshalJSON(data []byte) error {
	var wire struct {
		ProcessKey  string `json:"process_key"`
		ProcessName string `json:"process_name"`
		NodeName    string `json:"node_name"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*s = WebhookStart{Process: ProcessRef{Key: wire.ProcessKey}, ProcessName: wire.ProcessName, StartEvent: wire.NodeName}
	return nil
}

// SimulateWebhookDelivery tries a delivery against one of the project's
// incoming webhooks — the ones partners post to — and says what a real one
// would do: whether its signature matches, which value the correlation rule
// finds, which waiting instances it would move on or which processes it would
// start, and why it would be refused. Nothing moves and the delivery ID is not
// used up. The answer never contains the signature the body should have had.
// A designer's.
func (c *Client) SimulateWebhookDelivery(ctx context.Context, webhookID string, sim WebhookSimulation) (*WebhookSimulationResult, error) {
	body := map[string]any{"body": string(sim.Body)}
	if sim.Timestamp != "" {
		body["timestamp"] = sim.Timestamp
	}
	if sim.DeliveryID != "" {
		body["delivery_id"] = sim.DeliveryID
	}
	if sim.Signature != "" {
		body["signature"] = sim.Signature
	}
	var out struct {
		Simulation WebhookSimulationResult `json:"simulation"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/webhooks/"+url.PathEscape(webhookID)+"/simulate", body, &out); err != nil {
		return nil, err
	}
	return &out.Simulation, nil
}
