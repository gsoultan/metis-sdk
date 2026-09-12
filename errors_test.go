package metis

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// Each predicate has to answer for every status the server actually uses, not
// just the one it is named after — a caller branches through them in order, and
// a predicate that quietly says yes to the wrong code sends a request down the
// wrong path.
func TestErrorPredicatesAgreeOnEveryStatus(t *testing.T) {
	cases := []struct {
		status                                            int
		notFound, unauthorized, invalid, serverErr, retry bool
	}{
		{status: http.StatusBadRequest, invalid: true},
		{status: http.StatusUnauthorized, unauthorized: true},
		{status: http.StatusForbidden, unauthorized: true},
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusInternalServerError, serverErr: true, retry: true},
		{status: http.StatusBadGateway, serverErr: true, retry: true},
	}

	for _, c := range cases {
		t.Run(http.StatusText(c.status), func(t *testing.T) {
			err := error(&APIError{StatusCode: c.status, Message: "boom"})

			if got := IsNotFound(err); got != c.notFound {
				t.Errorf("IsNotFound = %v, want %v", got, c.notFound)
			}
			if got := IsUnauthorized(err); got != c.unauthorized {
				t.Errorf("IsUnauthorized = %v, want %v", got, c.unauthorized)
			}
			if got := IsInvalid(err); got != c.invalid {
				t.Errorf("IsInvalid = %v, want %v", got, c.invalid)
			}
			if got := IsServerError(err); got != c.serverErr {
				t.Errorf("IsServerError = %v, want %v", got, c.serverErr)
			}
		})
	}
}

// Callers wrap SDK errors with their own context; the predicates have to see
// through that, or they stop working the moment somebody adds a %w.
func TestPredicatesSeeThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("approving invoice 9: %w",
		&APIError{StatusCode: http.StatusNotFound, Message: "no such task"})

	if !IsNotFound(wrapped) {
		t.Error("IsNotFound did not unwrap")
	}

	var apiErr *APIError
	if !errors.As(wrapped, &apiErr) || apiErr.StatusCode != 404 {
		t.Error("errors.As did not recover the APIError")
	}
}

// A transport failure is not an answer, so it is none of these — including
// IsServerError, whose whole job is to name a bad answer.
func TestNonAPIErrorsAreNoneOfThem(t *testing.T) {
	err := errors.New("dial tcp: connection refused")

	for name, predicate := range map[string]func(error) bool{
		"IsNotFound":     IsNotFound,
		"IsUnauthorized": IsUnauthorized,
		"IsInvalid":      IsInvalid,
		"IsServerError":  IsServerError,
	} {
		if predicate(err) {
			t.Errorf("%s claimed a transport error", name)
		}
	}
	if predicateNil := IsNotFound(nil); predicateNil {
		t.Error("IsNotFound(nil) was true")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	err := &APIError{StatusCode: 404, Message: "no such task"}
	want := "metis: server returned 404: no such task"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// The server's own {"error": "..."} is what a caller should see. When the
// answer is not that shape — a proxy's HTML page, say — the raw text is better
// than nothing, and an empty body still has to say something.
func TestErrorBodyFallbacks(t *testing.T) {
	t.Run("server JSON", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/projects", 403, `{"error":"not a member"}`)

		_, err := client.ListProjects(t.Context())
		if !IsUnauthorized(err) {
			t.Fatalf("err = %v", err)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr); apiErr.Message != "not a member" {
			t.Errorf("message = %q", apiErr.Message)
		}
	})

	t.Run("a proxy's HTML", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/projects", 502, `<html>bad gateway</html>`)

		_, err := client.ListProjects(t.Context())
		if !IsServerError(err) {
			t.Fatalf("err = %v", err)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr); apiErr.Message != "<html>bad gateway</html>" {
			t.Errorf("message = %q", apiErr.Message)
		}
	})

	t.Run("an empty body still says something", func(t *testing.T) {
		f, client := newFakeServer(t)
		f.respond("GET /api/v1/projects", 500, ``)

		_, err := client.ListProjects(t.Context())
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %v", err)
		}
		if apiErr.Message == "" {
			t.Error("an empty body produced an empty message")
		}
	})
}
