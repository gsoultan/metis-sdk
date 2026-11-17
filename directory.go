package metis

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

// The organization's accounts and groups: who can sign in, and the groups an
// approval step or a task is offered to. Setting them up needs an
// administrator.

// Organization is an organization the signed-in account belongs to.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Account is a user who can sign in to metis.
type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	FullName    string `json:"full_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
}

// Group is a named set of accounts that work can be offered to.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// NewAccount is an account [Client.EnsureAccount] makes.
type NewAccount struct {
	Username string
	Name     string
	Email    string
	Password string
	// Groups are made if missing and the account added to each.
	Groups []string
}

// ListOrganizations lists the organizations the signed-in account belongs to.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var out struct {
		Organizations []Organization `json:"organizations"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/organizations", nil, &out)
	return out.Organizations, err
}

// ListAccounts lists an organization's accounts.
func (c *Client) ListAccounts(ctx context.Context, organizationID string) ([]Account, error) {
	if organizationID == "" {
		return nil, errors.New("metis: ListAccounts needs an organization ID")
	}
	var out struct {
		Users []Account `json:"users"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/organizations/"+url.PathEscape(organizationID)+"/users", nil, &out)
	return out.Users, err
}

// ListGroups lists an organization's groups.
func (c *Client) ListGroups(ctx context.Context, organizationID string) ([]Group, error) {
	if organizationID == "" {
		return nil, errors.New("metis: ListGroups needs an organization ID")
	}
	var out struct {
		Groups []Group `json:"groups"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/organizations/"+url.PathEscape(organizationID)+"/groups", nil, &out)
	return out.Groups, err
}

// CreateGroup makes a group in an organization.
func (c *Client) CreateGroup(ctx context.Context, organizationID, name, description string) error {
	if organizationID == "" || name == "" {
		return errors.New("metis: CreateGroup needs an organization ID and a name")
	}
	body := map[string]any{"group": map[string]any{"name": name, "description": description}}
	return c.do(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organizationID)+"/groups", body, nil)
}

// CreateAccount makes an account in an organization.
func (c *Client) CreateAccount(ctx context.Context, organizationID string, a NewAccount) error {
	if organizationID == "" || a.Username == "" || a.Password == "" {
		return errors.New("metis: CreateAccount needs an organization ID, a username and a password")
	}
	body := map[string]any{
		"user": map[string]any{
			// An account in no organization is refused on every request.
			"organizations": []map[string]string{{"id": organizationID}},
			"username":      a.Username,
			"full_name":     a.Name,
			"display_name":  a.Name,
			"email":         a.Email,
		},
		"password": a.Password,
	}
	return c.do(ctx, http.MethodPost, "/api/v1/users", body, nil)
}

// AddToGroup adds an account to a group.
func (c *Client) AddToGroup(ctx context.Context, groupID, accountID string) error {
	if groupID == "" || accountID == "" {
		return errors.New("metis: AddToGroup needs a group ID and an account ID")
	}
	return c.do(ctx, http.MethodPost, "/api/v1/groups/"+url.PathEscape(groupID)+"/members/"+url.PathEscape(accountID), nil, nil)
}

// EnsureGroup makes the group unless the organization has one by that name.
func (c *Client) EnsureGroup(ctx context.Context, organizationID, name string) (*Group, error) {
	if g, err := c.groupNamed(ctx, organizationID, name); err != nil || g != nil {
		return g, err
	}
	if err := c.CreateGroup(ctx, organizationID, name, ""); err != nil {
		return nil, err
	}
	g, err := c.groupNamed(ctx, organizationID, name)
	if err == nil && g == nil {
		err = fmt.Errorf("metis: group %s was made and cannot be found", name)
	}
	return g, err
}

// EnsureAccount makes the account unless the organization has one by that
// username, makes its groups, and puts it in each (adding a member twice is
// no change). Running it again changes
// nothing, so set-up code can call it every time it starts. An existing
// account's password is left as it is.
func (c *Client) EnsureAccount(ctx context.Context, organizationID string, a NewAccount) (*Account, error) {
	account, err := c.accountNamed(ctx, organizationID, a.Username)
	if err != nil {
		return nil, err
	}
	if account == nil {
		if err := c.CreateAccount(ctx, organizationID, a); err != nil {
			return nil, err
		}
		if account, err = c.accountNamed(ctx, organizationID, a.Username); err != nil {
			return nil, err
		}
		if account == nil {
			return nil, fmt.Errorf("metis: account %s was made and cannot be found", a.Username)
		}
	}
	for _, name := range a.Groups {
		g, err := c.EnsureGroup(ctx, organizationID, name)
		if err != nil {
			return nil, err
		}
		if err := c.AddToGroup(ctx, g.ID, account.ID); err != nil {
			return nil, fmt.Errorf("metis: add %s to %s: %w", a.Username, name, err)
		}
	}
	return account, nil
}

func (c *Client) groupNamed(ctx context.Context, organizationID, name string) (*Group, error) {
	groups, err := c.ListGroups(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(groups, func(g Group) bool { return g.Name == name }); i >= 0 {
		return &groups[i], nil
	}
	return nil, nil
}

func (c *Client) accountNamed(ctx context.Context, organizationID, username string) (*Account, error) {
	accounts, err := c.ListAccounts(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(accounts, func(a Account) bool { return a.Username == username }); i >= 0 {
		return &accounts[i], nil
	}
	return nil, nil
}
