package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// ListParticipantsOptions selects one page of a project's People.
//
// The zero value of everything but ProjectID asks for the first page at the
// server's default size.
type ListParticipantsOptions struct {
	// ProjectID names the project. Required.
	ProjectID string
	// Search keeps the people whose username, display name or email contains
	// it, ignoring case. At most 200 characters.
	Search string
	// Page is 1-based. Zero means the first page.
	Page int
	// PageSize is how many people per page. Zero means the server's default;
	// the server serves at most 200.
	PageSize int
}

// ListParticipantsPage returns one page of a project's People, alphabetically
// by username, with a [*PageInfo] saying how many there are in all. The search
// runs on the server, so it finds people on pages not read.
//
// It needs a Metis server that pages GET /api/v1/participants. An older one
// answers the whole directory and no page: the [*PageInfo] is then nil and
// Search is not applied.
func (c *Client) ListParticipantsPage(ctx context.Context, opts ListParticipantsOptions) ([]Participant, *PageInfo, error) {
	if opts.ProjectID == "" {
		return nil, nil, errors.New("metis: ListParticipantsPage needs a project ID")
	}
	query := url.Values{"project_id": {opts.ProjectID}}
	// The page is always named, so the server answers a page even when the
	// caller asked for nothing but the first one.
	query.Set("page", fmt.Sprint(max(opts.Page, 1)))
	if opts.PageSize > 0 {
		query.Set("page_size", fmt.Sprint(opts.PageSize))
	}
	if opts.Search != "" {
		query.Set("q", opts.Search)
	}
	var out struct {
		Participants []Participant `json:"participants"`
		Page         *PageInfo     `json:"page"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/participants?"+query.Encode(), nil, &out)
	return out.Participants, out.Page, err
}
