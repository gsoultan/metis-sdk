package metis

import (
	"errors"
	"testing"
)

func TestLoginAskedForACodeKeepsNoToken(t *testing.T) {
	f, client := newFakeServer(t)
	client.token = ""
	f.respond("POST /api/v1/login", 200,
		`{"second_factor":"code","challenge":"ch-1","challenge_expires_at":"2026-11-17T10:05:00Z"}`)

	err := client.Login(t.Context(), "admin", "secret")
	if !errors.Is(err, ErrSecondFactorRequired) {
		t.Fatalf("err = %v, want ErrSecondFactorRequired", err)
	}
	var sf *SecondFactorError
	if !errors.As(err, &sf) {
		t.Fatalf("err = %T, want *SecondFactorError", err)
	}
	if sf.Step != SecondFactorCode || sf.Challenge != "ch-1" || sf.ExpiresAt.IsZero() {
		t.Errorf("got %+v, want step code, challenge ch-1 and an expiry", sf)
	}
	if client.token != "" {
		t.Errorf("token = %q, want none kept from a challenge", client.token)
	}
}

func TestLoginWithCodeStoresTheSession(t *testing.T) {
	f, client := newFakeServer(t)
	client.token = ""
	f.respond("POST /api/v1/login/code", 200, `{"token":"session-1","user":{"id":"u1","username":"admin"}}`)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	if err := client.LoginWithCode(t.Context(), "ch-1", "123456"); err != nil {
		t.Fatalf("login with code: %v", err)
	}
	sent := f.last().Body
	if sent["challenge"] != "ch-1" || sent["code"] != "123456" {
		t.Errorf("body = %v, want the challenge and the code", sent)
	}
	if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.last().Auth; got != "Bearer session-1" {
		t.Errorf("Authorization = %q, want the session LoginWithCode stored", got)
	}
}

func TestLoginForAnAccountWithoutAnApp(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/login", 200, `{"second_factor":"enrol","challenge":"ch-2"}`)

	err := client.Login(t.Context(), "admin", "secret")
	var sf *SecondFactorError
	if !errors.As(err, &sf) || sf.Step != SecondFactorEnrol {
		t.Fatalf("err = %v, want a SecondFactorError for step enrol", err)
	}
}

func TestLoginWithAWrongCodeIsUnauthorized(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/login/code", 401, `{"error":"that code is not right"}`)

	err := client.LoginWithCode(t.Context(), "ch-1", "000000")
	if !IsUnauthorized(err) {
		t.Fatalf("err = %v, want IsUnauthorized", err)
	}
}

func TestAnIntegrationKeyIsSentAsTheBearer(t *testing.T) {
	f, client := newFakeServer(t)
	WithAPIKey("mik_abc")(client)
	f.respond("GET /api/v1/tasks", 200, `{"tasks":[]}`)

	if _, _, err := client.ListTasks(t.Context(), ListTasksOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.last().Auth; got != "Bearer mik_abc" {
		t.Errorf("Authorization = %q, want the integration key", got)
	}
}
