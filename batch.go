package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// BatchKind is what a batch operation does to each instance it matches.
type BatchKind string

const (
	// BatchRetry resolves every open incident of each instance, so its failed
	// step runs again, as resolving one incident at a time does. An
	// operator's.
	BatchRetry BatchKind = "retry"
	// BatchCancel cancels each instance where it stands, as an
	// administrator's cancel of one instance does. An administrator's.
	BatchCancel BatchKind = "cancel"
)

// BatchStatus is where a batch operation stands.
type BatchStatus string

const (
	BatchRunning   BatchStatus = "running"
	BatchCompleted BatchStatus = "completed"
	// BatchStopped was stopped before it was through: by somebody, with a
	// reason, or by itself when whoever asked for it lost the role.
	BatchStopped BatchStatus = "stopped"
)

// BatchItemStatus is what came of one instance of a batch operation.
type BatchItemStatus string

const (
	BatchItemPending   BatchItemStatus = "pending"
	BatchItemSucceeded BatchItemStatus = "succeeded"
	BatchItemFailed    BatchItemStatus = "failed"
	// BatchItemSkipped had nothing to act on — no incident to retry, an
	// instance already ended — or waits for a second administrator, or was not
	// reached before the operation was stopped. The detail says which.
	BatchItemSkipped BatchItemStatus = "skipped"
)

// BatchFilter chooses the instances of a project an operation acts on: the
// filter of [Client.ListInstances], every field optional.
type BatchFilter struct {
	Status         ProcessStatus `json:"status,omitempty"`
	DefinitionID   string        `json:"definition_id,omitempty"`
	BusinessKey    string        `json:"business_key,omitempty"`
	NeedsAttention bool          `json:"needs_attention,omitempty"`
}

// BatchRequest asks for an operation on a project's instances.
type BatchRequest struct {
	Kind BatchKind
	// Reason is kept beside whoever asked, and required.
	Reason string
	Filter BatchFilter
	// Apply starts the operation. Without it the request is a dry run: the
	// answer says how many instances match and nothing changes.
	Apply bool
}

// BatchPreview is the answer to a [BatchRequest]: how many instances the filter
// matches and, when it was applied, the operation started.
type BatchPreview struct {
	Kind    BatchKind `json:"kind"`
	Matched int64     `json:"matched"`
	// TooMany is true when more match than one operation takes, Limit.
	// Applied, such a request is refused with a 400 ([IsInvalid]): narrow the
	// filter.
	TooMany bool `json:"too_many"`
	Limit   int  `json:"limit"`
	// Operation is the operation started, and nil for a dry run.
	Operation *BatchOperation `json:"operation,omitempty"`
}

// BatchOperation is one retry or cancel of many instances, worked through in
// the background.
type BatchOperation struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"project_id"`
	Kind        BatchKind   `json:"kind"`
	Reason      string      `json:"reason"`
	Filter      BatchFilter `json:"filter"`
	RequestedBy string      `json:"requested_by"`
	RequestedAt time.Time   `json:"requested_at"`
	Status      BatchStatus `json:"status"`
	// Total is how many instances it took on; the four counts after it add up
	// to that.
	Total      int64      `json:"total"`
	Succeeded  int64      `json:"succeeded"`
	Failed     int64      `json:"failed"`
	Skipped    int64      `json:"skipped"`
	Pending    int64      `json:"pending"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	StoppedBy  string     `json:"stopped_by,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
}

// IsFinished reports whether the operation will act on nothing more.
func (o BatchOperation) IsFinished() bool { return o.Status != BatchRunning }

// BatchItem is one instance of an operation and what came of it.
type BatchItem struct {
	InstanceID string          `json:"instance_id"`
	Status     BatchItemStatus `json:"status"`
	// Detail says why it failed or was skipped.
	Detail    string     `json:"detail,omitempty"`
	SettledAt *time.Time `json:"settled_at,omitempty"`
}

// StartBatchOperation retries or cancels every instance of a project that a
// filter matches, in one request. Ask without [BatchRequest.Apply] first to
// see how many that is:
//
//	req := metis.BatchRequest{
//		Kind:   metis.BatchRetry,
//		Reason: "the payment provider is back",
//		Filter: metis.BatchFilter{NeedsAttention: true},
//	}
//	preview, err := client.StartBatchOperation(ctx, projectID, req)
//	// … check preview.Matched, then:
//	req.Apply = true
//	started, err := client.StartBatchOperation(ctx, projectID, req)
//	op := started.Operation
//
// One operation takes up to 10,000 instances, fixed when it starts, and a
// project runs at most five at once. Each is acted on exactly as the
// single-instance retry or cancel acts, as whoever asked, who is checked again
// every 50 instances; the operation stops itself if they lose the role. Follow
// it with [Client.GetBatchOperation] and [Client.ListBatchOperationItems].
//
// Retrying is an operator's, cancelling an administrator's. It needs a server
// newer than **Metis v0.5.0**; an older one answers 404 ([IsNotFound]).
func (c *Client) StartBatchOperation(ctx context.Context, projectID string, req BatchRequest) (*BatchPreview, error) {
	if req.Kind == "" {
		return nil, errors.New("metis: StartBatchOperation needs a kind — BatchRetry or BatchCancel")
	}
	if req.Reason == "" {
		return nil, errors.New("metis: StartBatchOperation needs a reason, which is kept beside who asked")
	}
	var out BatchPreview
	err := c.do(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/batch-operations", map[string]any{
		"kind":   req.Kind,
		"reason": req.Reason,
		"filter": req.Filter,
		// Always said, never left to the server's default, so a request reads
		// as what it does.
		"dry_run": !req.Apply,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListBatchOperations lists a project's batch operations, newest first.
func (c *Client) ListBatchOperations(ctx context.Context, projectID string) ([]BatchOperation, error) {
	var out struct {
		Operations []BatchOperation `json:"operations"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(projectID)+"/batch-operations", nil, &out)
	return out.Operations, err
}

// GetBatchOperation returns one batch operation, with how far it has got.
func (c *Client) GetBatchOperation(ctx context.Context, operationID string) (*BatchOperation, error) {
	var out struct {
		Operation *BatchOperation `json:"operation"`
	}
	if err := c.do(ctx, http.MethodGet, batchOperationPath(operationID), nil, &out); err != nil {
		return nil, err
	}
	if out.Operation == nil {
		return nil, fmt.Errorf("metis: the server answered for batch operation %s without it", operationID)
	}
	return out.Operation, nil
}

// ListBatchItemsOptions filters and pages what came of an operation's
// instances.
type ListBatchItemsOptions struct {
	// Status keeps one outcome, such as [BatchItemFailed]. Empty is every one.
	Status BatchItemStatus
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many per page. Zero means the server's default.
	PageSize int
}

// BatchItemPage is one page of an operation's instances.
type BatchItemPage struct {
	Items    []BatchItem `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

// ListBatchOperationItems reads a page of what came of an operation's
// instances — the failed ones, typically, and why:
//
//	page, err := client.ListBatchOperationItems(ctx, op.ID,
//		metis.ListBatchItemsOptions{Status: metis.BatchItemFailed})
func (c *Client) ListBatchOperationItems(ctx context.Context, operationID string, opts ListBatchItemsOptions) (*BatchItemPage, error) {
	query := url.Values{}
	if opts.Status != "" {
		query.Set("status", string(opts.Status))
	}
	if opts.Page > 0 {
		query.Set("page", fmt.Sprint(opts.Page))
	}
	if opts.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(opts.PageSize))
	}
	path := batchOperationPath(operationID) + "/items"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out BatchItemPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StopBatchOperation stops an operation before it is through, with a reason
// kept beside who stopped it, and returns it as it then stands. An instance
// already acted on stays as it is — stopping does not undo a cancel — and the
// ones not reached are skipped. Anybody who runs the organization may stop
// one.
func (c *Client) StopBatchOperation(ctx context.Context, operationID, reason string) (*BatchOperation, error) {
	if reason == "" {
		return nil, errors.New("metis: StopBatchOperation needs a reason, which is kept beside who stopped it")
	}
	var out struct {
		Operation *BatchOperation `json:"operation"`
	}
	if err := c.do(ctx, http.MethodPost, batchOperationPath(operationID)+"/stop", map[string]any{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return out.Operation, nil
}

func batchOperationPath(operationID string) string {
	return "/api/v1/batch-operations/" + url.PathEscape(operationID)
}
