package metis

import (
	"context"
	"errors"
	"slices"
)

// maxInboxPages bounds how much of an inbox is read: 20 pages of 100 tasks.
// An inbox longer than that is a queue to page through with
// [Client.ListClaimableTasks], not an inbox.
const maxInboxPages = 20

// Inbox is every open task the signed-in user can act on now: the tasks
// assigned to them and the tasks offered to them, oldest first, each once.
//
// It is the two calls an inbox needs, [Client.ListTasksByAssignee] for the
// user and [Client.ListClaimableTasks], put together.
func (c *Client) Inbox(ctx context.Context) ([]UserTask, error) {
	if c.signedInAsPerson() {
		return c.personInbox(ctx)
	}
	me, err := c.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if me == nil || me.Username == "" {
		return nil, errors.New("metis: Inbox needs a signed-in user")
	}
	seen := map[string]bool{}
	var out []UserTask
	add := func(tasks []UserTask) {
		for _, t := range tasks {
			if !seen[t.ID] && t.IsOpen() {
				seen[t.ID] = true
				out = append(out, t)
			}
		}
	}
	for page := 1; page <= maxInboxPages; page++ {
		tasks, info, err := c.ListTasksByAssignee(ctx, me.Username, PageOptions{Page: page, PageSize: 100})
		if err != nil {
			return nil, err
		}
		add(tasks)
		if info == nil || !info.HasMore {
			break
		}
	}
	for page := 1; page <= maxInboxPages; page++ {
		tasks, info, err := c.ListClaimableTasks(ctx, PageOptions{Page: page, PageSize: 100})
		if err != nil {
			return nil, err
		}
		add(tasks)
		if info == nil || !info.HasMore {
			break
		}
	}
	slices.SortStableFunc(out, func(a, b UserTask) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

// personInbox is a person's work, which their own listing already gives as
// one list.
func (c *Client) personInbox(ctx context.Context) ([]UserTask, error) {
	var out []UserTask
	for page := 1; page <= maxInboxPages; page++ {
		tasks, info, err := c.ListPersonTasks(ctx, PageOptions{Page: page, PageSize: 100})
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			if t.IsOpen() {
				out = append(out, t)
			}
		}
		if info == nil || !info.HasMore {
			break
		}
	}
	return out, nil
}
