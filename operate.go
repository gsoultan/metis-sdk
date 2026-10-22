package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Everything in this file needs a server newer than **Metis v0.5.0**. An older
// one has none of these routes and answers 404 ([IsNotFound]).

// --- Workers ----------------------------------------------------------------

// ExternalTaskTopics is the organization's external-task topics and the
// workers serving each, as [Client.ListExternalTaskWorkers] reads them.
type ExternalTaskTopics struct {
	// LiveWindow is how recently a worker must have fetched to count as live.
	LiveWindow time.Duration
	Topics     []ExternalTaskTopic
}

// ExternalTaskTopic is one topic: what waits on it, what is locked, and who
// fetches it.
type ExternalTaskTopic struct {
	Topic string `json:"topic"`
	// Waiting is how many tasks are on offer: unlocked, with tries left.
	// Tasks waiting out a retry, or held by an incident, are not counted.
	Waiting int64 `json:"waiting"`
	// Locked is how many a worker holds now.
	Locked  int64                `json:"locked"`
	Workers []ExternalTaskWorker `json:"workers"`
	// Unserved is true when tasks wait and no live worker fetches the topic:
	// they will wait until one starts, and nothing else says so.
	Unserved bool `json:"unserved"`
}

// ExternalTaskWorker is one worker of a topic.
type ExternalTaskWorker struct {
	WorkerID string `json:"worker_id"`
	// Version is what the worker said it is ([WorkerOptions.Version]); empty
	// when it said nothing.
	Version string `json:"version,omitempty"`
	// LastSeenAt is its last fetch. Nil for a worker holding a lock it took
	// before workers were recorded, or one since forgotten.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// Live is whether it fetched within the topics' LiveWindow.
	Live bool `json:"live"`
	// Locked is how many of the topic's tasks it holds now.
	Locked int64 `json:"locked"`
}

// ListExternalTaskWorkers lists the organization's external-task topics, each
// with the workers fetching it — to tell a topic nobody serves from one that is
// merely quiet:
//
//	topics, err := client.ListExternalTaskWorkers(ctx)
//	for _, topic := range topics.Topics {
//		if topic.Unserved {
//			log.Printf("%d tasks on %q and no worker", topic.Waiting, topic.Topic)
//		}
//	}
//
// A worker is recorded at most once every ten seconds per topic, and forgotten
// after seven days without a fetch.
func (c *Client) ListExternalTaskWorkers(ctx context.Context) (*ExternalTaskTopics, error) {
	var out struct {
		LiveWindowSeconds int64               `json:"live_window_seconds"`
		Topics            []ExternalTaskTopic `json:"topics"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/external-task-workers", nil, &out); err != nil {
		return nil, err
	}
	return &ExternalTaskTopics{
		LiveWindow: time.Duration(out.LiveWindowSeconds) * time.Second,
		Topics:     out.Topics,
	}, nil
}

// --- The audit log ----------------------------------------------------------

// Operation is one thing a person did, as the organization's audit log keeps
// it: a task claimed or handed over, a step waived, an instance cancelled or
// migrated, a request for a second administrator decided, a retention hold
// placed. The engine's own entries are on each instance's timeline only.
type Operation struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
	// Type is the kind of act, such as "instance_cancelled".
	Type       string `json:"type"`
	ProjectID  string `json:"project_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	NodeName   string `json:"node_name,omitempty"`
	// Narrative is the entry in words, for a person.
	Narrative string `json:"narrative"`
}

// ListOperationsOptions filters and pages the audit log. Every field is
// optional.
type ListOperationsOptions struct {
	// Actor keeps one account's acts, by username.
	Actor string
	// Type keeps one kind of act, such as "instance_cancelled".
	Type string
	// ProjectID keeps one project's.
	ProjectID string
	// From and To bound when: From is the earliest moment kept, and every
	// entry kept is before To.
	From, To time.Time
	// Before continues a listing: the Next of the page before.
	Before string
	// Limit is how many to a page, at most 200. Zero is the server's 50.
	Limit int
}

// OperationPage is one page of the audit log, newest first.
type OperationPage struct {
	Operations []Operation `json:"operations"`
	// Next goes back as [ListOperationsOptions.Before] to read the page after
	// this one. Empty on the last page.
	Next string `json:"next,omitempty"`
}

// ListOperations reads a page of the organization's audit log: who did what,
// newest first. It is an administrator's, of the organization the client is
// signed in to.
//
//	opts := metis.ListOperationsOptions{Actor: "boss", Type: "instance_cancelled"}
//	for {
//		page, err := client.ListOperations(ctx, opts)
//		if err != nil {
//			return err
//		}
//		for _, op := range page.Operations {
//			fmt.Println(op.At, op.Narrative)
//		}
//		if page.Next == "" {
//			break
//		}
//		opts.Before = page.Next
//	}
//
// An entry outlives the instance it names: the retention sweep removes
// finished instances and never the trail. Acts recorded before the server's
// migration 46 are only on their instance's timeline.
func (c *Client) ListOperations(ctx context.Context, opts ListOperationsOptions) (*OperationPage, error) {
	query := url.Values{}
	set := func(name, value string) {
		if value != "" {
			query.Set(name, value)
		}
	}
	set("actor", opts.Actor)
	set("type", opts.Type)
	set("project", opts.ProjectID)
	set("before", opts.Before)
	if !opts.From.IsZero() {
		query.Set("from", opts.From.Format(time.RFC3339Nano))
	}
	if !opts.To.IsZero() {
		query.Set("to", opts.To.Format(time.RFC3339Nano))
	}
	if opts.Limit > 0 {
		query.Set("limit", fmt.Sprint(opts.Limit))
	}
	path := "/api/v1/audit/operations"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out OperationPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Retention --------------------------------------------------------------

// RetentionHold keeps an instance, and the whole case it belongs to, out of
// the server's retention sweep until it is released.
type RetentionHold struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	InstanceID string    `json:"instance_id"`
	Reason     string    `json:"reason"`
	PlacedBy   string    `json:"placed_by"`
	PlacedAt   time.Time `json:"placed_at"`
}

// GetRetentionHold returns the hold on an instance, or nil when it has none.
// Anybody who may read the instance may ask.
func (c *Client) GetRetentionHold(ctx context.Context, instanceID string) (*RetentionHold, error) {
	var out struct {
		Held bool           `json:"held"`
		Hold *RetentionHold `json:"hold"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+url.PathEscape(instanceID)+"/retention-hold", nil, &out)
	if err != nil || !out.Held {
		return nil, err
	}
	return out.Hold, nil
}

// PlaceRetentionHold keeps an instance from being removed by the retention
// sweep: the instance that started its chain of calls and every instance
// started from it are kept, whatever their age, until the hold is released.
// The reason goes on the instance's audit trail.
//
// It is an administrator's. A second hold on a held instance is refused with
// a 400 ([IsInvalid]).
func (c *Client) PlaceRetentionHold(ctx context.Context, instanceID, reason string) (*RetentionHold, error) {
	return c.changeRetentionHold(ctx, "/api/v1/instances/"+url.PathEscape(instanceID)+"/retention-hold", reason)
}

// ReleaseRetentionHold lifts the hold on an instance, with a reason the
// instance's audit trail keeps, and returns the hold released. It is an
// administrator's.
func (c *Client) ReleaseRetentionHold(ctx context.Context, instanceID, reason string) (*RetentionHold, error) {
	return c.changeRetentionHold(ctx, "/api/v1/instances/"+url.PathEscape(instanceID)+"/retention-hold/release", reason)
}

func (c *Client) changeRetentionHold(ctx context.Context, path, reason string) (*RetentionHold, error) {
	if reason == "" {
		return nil, errors.New("metis: a retention hold is placed and released with a reason, which the instance's audit trail keeps")
	}
	var out struct {
		Hold *RetentionHold `json:"hold"`
	}
	if err := c.do(ctx, http.MethodPost, path, map[string]any{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return out.Hold, nil
}

// ListRetentionHolds lists a project's holds, newest first, at most 1,000. It
// is an administrator's.
func (c *Client) ListRetentionHolds(ctx context.Context, projectID string) ([]RetentionHold, error) {
	var out struct {
		Holds []RetentionHold `json:"holds"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID)+"/retention-holds", nil, &out)
	return out.Holds, err
}

// RetentionPeriod is how long one process's finished instances are kept, or,
// in a period's history, one change to it.
type RetentionPeriod struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	ProcessKey string `json:"process_key"`
	// Days is the period, or nil when the process follows the installation's
	// setting.
	Days   *int      `json:"days"`
	Reason string    `json:"reason"`
	SetBy  string    `json:"set_by"`
	SetAt  time.Time `json:"set_at"`
}

// SetRetentionPeriod gives a process, by key and for every version of it, its
// own number of days after which the retention sweep removes its completed
// and cancelled instances. A nil days returns it to the installation's
// setting. Every change is kept, with who made it and the reason.
//
//	days := 365
//	_, err := client.SetRetentionPeriod(ctx, projectID, "purchase", &days, "kept a year for audit")
//
// It is an administrator's. Holds, open incidents and running parents or
// children still keep an instance whatever its process's period.
func (c *Client) SetRetentionPeriod(ctx context.Context, projectID, processKey string, days *int, reason string) (*RetentionPeriod, error) {
	if reason == "" {
		return nil, errors.New("metis: SetRetentionPeriod needs a reason, which is kept with the change")
	}
	var out struct {
		Period *RetentionPeriod `json:"period"`
	}
	// days goes as null rather than being left out: the server refuses a body
	// that does not say, so that leaving it out never clears a period.
	err := c.do(ctx, http.MethodPut, retentionPeriodsPath(projectID)+"/"+url.PathEscape(processKey),
		map[string]any{"days": days, "reason": reason}, &out)
	if err != nil {
		return nil, err
	}
	return out.Period, nil
}

// ListRetentionPeriods lists, for each of a project's processes that was ever
// given a period, its latest change: Days nil means it was returned to the
// installation's setting. Anybody who may read the project may ask.
func (c *Client) ListRetentionPeriods(ctx context.Context, projectID string) ([]RetentionPeriod, error) {
	var out struct {
		Periods []RetentionPeriod `json:"periods"`
	}
	err := c.do(ctx, http.MethodGet, retentionPeriodsPath(projectID), nil, &out)
	return out.Periods, err
}

// RetentionPeriodHistory lists every change to one process's period, newest
// first.
func (c *Client) RetentionPeriodHistory(ctx context.Context, projectID, processKey string) ([]RetentionPeriod, error) {
	var out struct {
		Periods []RetentionPeriod `json:"periods"`
	}
	err := c.do(ctx, http.MethodGet, retentionPeriodsPath(projectID)+"/"+url.PathEscape(processKey)+"/history", nil, &out)
	return out.Periods, err
}

func retentionPeriodsPath(projectID string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/retention-periods"
}
