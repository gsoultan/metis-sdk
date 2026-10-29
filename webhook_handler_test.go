package metis

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// signedDelivery is a delivery request as Metis would post it.
func signedDelivery(t *testing.T, id, event string) *http.Request {
	t.Helper()
	body := `{"event_id":"e-` + id + `","event":"` + event + `","occurred_at":"2026-10-21T10:41:00Z","project_id":"p-1","task":{"id":"t-1"}}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/hooks/metis", strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderDeliveryID, id)
	req.Header.Set(HeaderSignature, SignWebhook([]byte(body), vectorSecret, ts, id))
	return req
}

func serve(h http.Handler, r *http.Request) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestWebhookHandlerHandlesEachDeliveryOnce(t *testing.T) {
	var calls atomic.Int32
	h := WebhookHandler(vectorSecret, func(_ context.Context, d *Delivery) error {
		calls.Add(1)
		if d.Task.ID != "t-1" || d.DeliveryID != "d-1" {
			t.Errorf("delivery = %+v", d)
		}
		return nil
	}, WebhookHandlerOptions{})

	for range 2 {
		if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusNoContent {
			t.Errorf("status = %d, want 204", code)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("handled %d times, want once", n)
	}
}

func TestWebhookHandlerRefusals(t *testing.T) {
	var reported []error
	h := WebhookHandler(vectorSecret, func(context.Context, *Delivery) error {
		t.Error("handle was called for a refused delivery")
		return nil
	}, WebhookHandlerOptions{OnError: func(_ *http.Request, err error) { reported = append(reported, err) }})

	forged := signedDelivery(t, "d-1", "task.created")
	forged.Header.Set(HeaderSignature, "v2=00")
	if code := serve(h, forged); code != http.StatusUnauthorized {
		t.Errorf("forged: %d, want 401", code)
	}

	stale := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	ts := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	stale.Header.Set(HeaderTimestamp, ts)
	stale.Header.Set(HeaderDeliveryID, "d-2")
	stale.Header.Set(HeaderSignature, SignWebhook([]byte(`{}`), vectorSecret, ts, "d-2"))
	if code := serve(h, stale); code != http.StatusUnauthorized {
		t.Errorf("stale: %d, want 401", code)
	}

	notJSON := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`nope`))
	ts = strconv.FormatInt(time.Now().Unix(), 10)
	notJSON.Header.Set(HeaderTimestamp, ts)
	notJSON.Header.Set(HeaderDeliveryID, "d-3")
	notJSON.Header.Set(HeaderSignature, SignWebhook([]byte(`nope`), vectorSecret, ts, "d-3"))
	if code := serve(h, notJSON); code != http.StatusBadRequest {
		t.Errorf("not JSON: %d, want 400", code)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Errorf("GET: %d %v, want 405 allowing POST", w.Code, w.Header())
	}
	if len(reported) != 4 {
		t.Errorf("reported %d errors, want 4", len(reported))
	}
}

func TestWebhookHandlerAsksAgainAfterAFailure(t *testing.T) {
	fail := true
	var calls int
	h := WebhookHandler(vectorSecret, func(context.Context, *Delivery) error {
		calls++
		if fail {
			return errors.New("the database is down")
		}
		return nil
	}, WebhookHandlerOptions{})

	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusInternalServerError {
		t.Errorf("failed: %d, want 500 so Metis sends it again", code)
	}
	fail = false
	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusNoContent {
		t.Errorf("again: %d, want 204", code)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want the failed delivery handled again", calls)
	}
}

func TestWebhookHandlerRefusesAConcurrentRepeat(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	h := WebhookHandler(vectorSecret, func(context.Context, *Delivery) error {
		close(entered)
		<-release
		return nil
	}, WebhookHandlerOptions{})

	var first int
	var wg sync.WaitGroup
	wg.Go(func() { first = serve(h, signedDelivery(t, "d-1", "task.created")) })
	<-entered
	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusConflict {
		t.Errorf("repeat while in hand: %d, want 409", code)
	}
	close(release)
	wg.Wait()
	if first != http.StatusNoContent {
		t.Errorf("first: %d, want 204", first)
	}
}

type brokenStore struct{ lookup, mark error }

func (s brokenStore) Handled(context.Context, string) (bool, error) { return false, s.lookup }
func (s brokenStore) MarkHandled(context.Context, string) error     { return s.mark }

func TestWebhookHandlerStoreFailures(t *testing.T) {
	handle := func(context.Context, *Delivery) error { return nil }
	h := WebhookHandler(vectorSecret, handle, WebhookHandlerOptions{Store: brokenStore{lookup: errors.New("down")}})
	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusInternalServerError {
		t.Errorf("lookup failed: %d, want 500", code)
	}

	var reported error
	h = WebhookHandler(vectorSecret, handle, WebhookHandlerOptions{
		Store:   brokenStore{mark: errors.New("down")},
		OnError: func(_ *http.Request, err error) { reported = err },
	})
	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusNoContent {
		t.Errorf("mark failed: %d, want 204: it was handled", code)
	}
	if reported == nil || !strings.Contains(reported.Error(), "remember") {
		t.Errorf("reported = %v, want the failed mark", reported)
	}
}

func TestMemoryDeliveryStoreForgetsTheOldest(t *testing.T) {
	ctx := t.Context()
	s := NewMemoryDeliveryStore(2)
	for _, id := range []string{"a", "b", "a", "c"} {
		if err := s.MarkHandled(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]bool{"a": true, "b": false, "c": true} {
		if got, _ := s.Handled(ctx, id); got != want {
			t.Errorf("Handled(%s) = %v, want %v", id, got, want)
		}
	}
	if NewMemoryDeliveryStore(0).capacity != 10_000 {
		t.Error("the default capacity is not 10,000")
	}
}

func TestWebhookHandlersRouteByEvent(t *testing.T) {
	var got []string
	hs := WebhookHandlers{
		EventTaskCreated:    func(_ context.Context, d *Delivery) error { got = append(got, "task"); return nil },
		EventIncidentRaised: func(context.Context, *Delivery) error { return errors.New("pager down") },
	}
	h := WebhookHandler(vectorSecret, hs.Handle, WebhookHandlerOptions{})
	if code := serve(h, signedDelivery(t, "d-1", "task.created")); code != http.StatusNoContent {
		t.Errorf("task: %d", code)
	}
	if code := serve(h, signedDelivery(t, "d-2", "process.started")); code != http.StatusNoContent {
		t.Errorf("unrouted event: %d, want 204 and left alone", code)
	}
	if code := serve(h, signedDelivery(t, "d-3", "incident.raised")); code != http.StatusInternalServerError {
		t.Errorf("failing route: %d, want 500", code)
	}
	if strings.Join(got, ",") != "task" {
		t.Errorf("routed = %v", got)
	}
}
