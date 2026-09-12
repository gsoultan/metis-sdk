package metis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Client talks to one metis server as one authenticated principal.
//
// It is safe for concurrent use. The zero value is not usable; construct with
// NewClient.
type Client struct {
	baseURL string
	http    *http.Client

	mu    sync.RWMutex
	token string
	orgID string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client — for custom TLS,
// proxies, or instrumentation.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithToken supplies a token obtained elsewhere, instead of calling Login.
// Useful when the token comes from a secret store rather than a password.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// defaultTimeout bounds every request. Without it a wedged server holds the
// caller's goroutine forever, and integrations inherit the outage.
const defaultTimeout = 30 * time.Second

// NewClient builds a client for the server at baseURL, e.g.
// "https://bpm.example.com". The /api/v1 prefix is added by the client.
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError is a non-2xx answer from the server, carrying the HTTP status and
// the server's message.
//
// Reach for the predicates below rather than comparing StatusCode yourself —
// they say what the status means here, and "unauthorized" is two codes:
//
//	switch {
//	case metis.IsNotFound(err):     // gone, or never yours
//	case metis.IsUnauthorized(err): // token missing, expired, or not allowed
//	case metis.IsInvalid(err):      // your request was wrong; retrying will not help
//	case metis.IsServerError(err):  // the engine faltered; worth retrying
//	}
type APIError struct {
	// StatusCode is the HTTP status. The server uses 400, 401, 403, 404 and
	// 500; it does not answer 409.
	StatusCode int
	// Message is the server's own explanation, or the response text when the
	// answer was not the usual JSON — a proxy's error page, say.
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("metis: server returned %d: %s", e.StatusCode, e.Message)
}

// statusIs reports whether err is an APIError with one of the given statuses.
func statusIs(err error, statuses ...int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return slices.Contains(statuses, apiErr.StatusCode)
}

// IsNotFound reports whether err is the server saying a resource does not
// exist — which, under tenant scoping, is also what "not yours" looks like.
// Another organization's instance is a 404 and not a 403, deliberately: a 403
// would confirm that it exists.
func IsNotFound(err error) bool {
	return statusIs(err, http.StatusNotFound)
}

// IsUnauthorized reports whether err means the token is missing, expired, or
// not allowed to do this. It covers both 401 and 403, because from a caller's
// side the fix is the same one: authenticate again, or ask for the right.
func IsUnauthorized(err error) bool {
	return statusIs(err, http.StatusUnauthorized, http.StatusForbidden)
}

// IsInvalid reports whether the server rejected the request as the caller's own
// mistake — a malformed ID, a missing field, a process key that is not
// deployed. Retrying the same request will fail the same way; fix the call.
func IsInvalid(err error) bool {
	return statusIs(err, http.StatusBadRequest)
}

// IsServerError reports whether the engine itself faltered (any 5xx). Unlike
// the others this one is worth retrying, with a delay — it is the failure that
// may simply not be there a second time.
//
// It is deliberately false for a transport failure, which is not an answer from
// the server at all. To retry both, check this or a network error:
//
//	if metis.IsServerError(err) || !errors.As(err, new(*metis.APIError)) {
//		// no answer, or a bad one: try again later
//	}
func IsServerError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 500
}

// Login authenticates with a username and password and stores the token on
// the client for every later call.
func (c *Client) Login(ctx context.Context, username, password string) error {
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/login", map[string]string{
		"username": username,
		"password": password,
	}, &out)
	if err != nil {
		return err
	}
	if out.Token == "" {
		return errors.New("metis: login answered without a token")
	}

	c.mu.Lock()
	c.token = out.Token
	c.mu.Unlock()
	return nil
}

// SetOrganization selects which of the caller's organizations later requests
// act in. Only needed for accounts that belong to more than one — the server
// validates the choice against the caller's actual memberships.
func (c *Client) SetOrganization(organizationID string) {
	c.mu.Lock()
	c.orgID = organizationID
	c.mu.Unlock()
}

// do runs one JSON request. in may be nil for no body; out may be nil to
// discard the response body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("metis: encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("metis: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.mu.RLock()
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.orgID != "" {
		req.Header.Set("X-Organization-ID", c.orgID)
	}
	c.mu.RUnlock()

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("metis: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	// Bounded read: an error page should not become an unbounded allocation.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("metis: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return &APIError{StatusCode: resp.StatusCode, Message: serverMessage(payload)}
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("metis: decode response: %w", err)
	}
	return nil
}

// serverMessage extracts the server's {"error": "..."} body, falling back to
// the raw text so a proxy's HTML error page is still visible, just truncated.
func serverMessage(payload []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(payload, &body); err == nil && body.Error != "" {
		return body.Error
	}
	text := strings.TrimSpace(string(payload))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	if text == "" {
		return "(no body)"
	}
	return text
}
