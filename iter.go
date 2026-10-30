package metis

import (
	"context"
	"errors"
	"fmt"
	"iter"
)

// The All* calls walk a whole listing, page by page, as a range-over-func
// sequence:
//
//	for task, err := range client.AllTasks(ctx, metis.ListTasksOptions{ProjectID: p}) {
//		if err != nil {
//			return err
//		}
//		// ...
//	}
//
// A page is asked for only when the loop reaches it, and breaking out of the
// loop stops there. The first error ends the walk: it is yielded once, with a
// zero row, and nothing after it.
//
// A listing is read while it changes. The numbered listings (instances, tasks,
// definitions, batch items) are newest first, so a row added during the walk
// pushes older rows a place down and the next page would start with a row
// already seen. Rows are therefore told apart by ID and each is yielded once.
// A row that leaves the listing during the walk pulls the others up instead,
// and the row that moves back onto a page already read is missed. When that
// matters, narrow the listing to rows that will not leave it while it is
// read, or read it again.

// ErrPagingStalled means a walk read several pages in a row that held nothing
// new: the server is answering the same rows whatever page is asked for. The
// walk stops rather than asking forever.
var ErrPagingStalled = errors.New("metis: paging stalled: the server answered no new rows for several pages")

// stalledPages is how many pages with nothing new a walk reads before it
// gives up. One such page is normal under heavy inserts; several are not.
const stalledPages = 3

// walkPages walks numbered pages from first. fetch reads one page and says
// whether more follow; id tells rows apart.
func walkPages[T any](ctx context.Context, first int, fetch func(page int) ([]T, bool, error), id func(T) string) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		seen := make(map[string]struct{})
		stale := 0
		for page := max(first, 1); ; page++ {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			rows, more, err := fetch(page)
			if err != nil {
				yield(zero, err)
				return
			}
			fresh := 0
			for _, row := range rows {
				key := id(row)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				fresh++
				if !yield(row, nil) {
					return
				}
			}
			if !more || len(rows) == 0 {
				return
			}
			if fresh > 0 {
				stale = 0
				continue
			}
			if stale++; stale >= stalledPages {
				yield(zero, fmt.Errorf("%w (last page %d)", ErrPagingStalled, page))
				return
			}
		}
	}
}

// walkCursor walks a listing that hands back a cursor to the next page. fetch
// reads the page at cursor, empty for the first, and returns the next cursor,
// empty after the last.
func walkCursor[T any](ctx context.Context, first string, fetch func(cursor string) ([]T, string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		cursor := first
		for {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			rows, next, err := fetch(cursor)
			if err != nil {
				yield(zero, err)
				return
			}
			for _, row := range rows {
				if !yield(row, nil) {
					return
				}
			}
			if next == "" || len(rows) == 0 {
				return
			}
			if next == cursor {
				yield(zero, fmt.Errorf("%w (cursor %q)", ErrPagingStalled, cursor))
				return
			}
			cursor = next
		}
	}
}

// hasMore reads a page's PageInfo. A listing the server did not page is all
// there is.
func hasMore(page *PageInfo) bool {
	return page != nil && page.HasMore
}

// AllInstances walks every instance [Client.ListInstances] would list for
// opts, starting at opts.Page. Each instance is yielded once, even when
// instances started during the walk push it onto the next page; one that
// leaves the listing during the walk can make another be missed.
func (c *Client) AllInstances(ctx context.Context, opts ListInstancesOptions) iter.Seq2[Instance, error] {
	return walkPages(ctx, opts.Page, func(page int) ([]Instance, bool, error) {
		opts.Page = page
		rows, info, err := c.ListInstances(ctx, opts)
		return rows, hasMore(info), err
	}, func(i Instance) string { return i.ID })
}

// AllTasks walks every task [Client.ListTasks] would list for opts, starting
// at opts.Page. It includes completed tasks, as the listing does.
func (c *Client) AllTasks(ctx context.Context, opts ListTasksOptions) iter.Seq2[UserTask, error] {
	return walkPages(ctx, opts.Page, func(page int) ([]UserTask, bool, error) {
		opts.Page = page
		rows, info, err := c.ListTasks(ctx, opts)
		return rows, hasMore(info), err
	}, func(t UserTask) string { return t.ID })
}

// AllClaimableTasks walks every task [Client.ListClaimableTasks] would list:
// the unclaimed tasks the signed-in user may claim.
func (c *Client) AllClaimableTasks(ctx context.Context, opts PageOptions) iter.Seq2[UserTask, error] {
	return walkPages(ctx, opts.Page, func(page int) ([]UserTask, bool, error) {
		opts.Page = page
		rows, info, err := c.ListClaimableTasks(ctx, opts)
		return rows, hasMore(info), err
	}, func(t UserTask) string { return t.ID })
}

// AllDefinitions walks every deployed definition [Client.ListDefinitions]
// would list for opts — every version of every process, not one per key.
func (c *Client) AllDefinitions(ctx context.Context, opts ListDefinitionsOptions) iter.Seq2[Definition, error] {
	return walkPages(ctx, opts.Page, func(page int) ([]Definition, bool, error) {
		opts.Page = page
		rows, info, err := c.ListDefinitions(ctx, opts)
		return rows, hasMore(info), err
	}, func(d Definition) string { return d.ID })
}

// AllBatchOperationItems walks what came of every instance a batch operation
// touched, narrowed by opts.Status.
func (c *Client) AllBatchOperationItems(ctx context.Context, operationID string, opts ListBatchItemsOptions) iter.Seq2[BatchItem, error] {
	return walkPages(ctx, opts.Page, func(page int) ([]BatchItem, bool, error) {
		opts.Page = page
		out, err := c.ListBatchOperationItems(ctx, operationID, opts)
		if err != nil {
			return nil, false, err
		}
		size := out.PageSize
		if size <= 0 {
			size = len(out.Items)
		}
		return out.Items, int64(page)*int64(size) < out.Total, nil
	}, func(b BatchItem) string { return b.Instance.ID })
}

// AllOperations walks the organization's audit log for opts, newest first,
// following each page's cursor. An administrator's.
func (c *Client) AllOperations(ctx context.Context, opts ListOperationsOptions) iter.Seq2[Operation, error] {
	return walkCursor(ctx, opts.Before, func(cursor string) ([]Operation, string, error) {
		opts.Before = cursor
		out, err := c.ListOperations(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		return out.Operations, out.Next, nil
	})
}

// AllOutgoingWebhookDeliveries walks a webhook's delivery log for opts,
// newest first, following each page's cursor. An administrator's.
func (c *Client) AllOutgoingWebhookDeliveries(ctx context.Context, webhookID string, opts ListDeliveriesOptions) iter.Seq2[WebhookDelivery, error] {
	return walkCursor(ctx, opts.Before, func(cursor string) ([]WebhookDelivery, string, error) {
		opts.Before = cursor
		out, err := c.ListOutgoingWebhookDeliveries(ctx, webhookID, opts)
		if err != nil {
			return nil, "", err
		}
		return out.Deliveries, out.NextBefore, nil
	})
}
