package metis

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type reply struct {
	status int
	body   string
}

// replies answers pattern with each reply in turn, repeating the last.
func replies(f *fakeServer, pattern string, rs ...reply) {
	var mu sync.Mutex
	next := 0
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		r := rs[min(next, len(rs)-1)]
		next++
		mu.Unlock()
		w.WriteHeader(r.status)
		_, _ = w.Write([]byte(r.body))
	})
}

func ok(body string) reply { return reply{200, body} }

const quickPoll = time.Millisecond

func TestWaitForInstanceUntilFinished(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/instances/i1",
		ok(`{"instance":{"id":"i1","status":"active"}}`),
		reply{503, `{"error":"busy"}`},
		ok(`{"instance":{"id":"i1","status":"completed","variables":{"approved":true}}}`),
	)
	inst, err := client.WaitForInstance(t.Context(), "i1", WaitOptions{Poll: quickPoll})
	if err != nil || inst.Status != ProcessCompleted || inst.Variables["approved"] != true {
		t.Fatalf("inst = %+v, %v; want completed with its variables", inst, err)
	}
	if n := f.count(); n != 3 {
		t.Errorf("requests = %d, want 3: the 503 is polled past", n)
	}
}

func TestWaitForInstanceReportsAFailure(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/i1", 200, `{"instance":{"id":"i1","status":"failed"}}`)
	inst, err := client.WaitForInstance(t.Context(), "i1", WaitOptions{
		Poll:  quickPoll,
		Until: func(i *Instance) bool { return i.Status == ProcessCompleted },
	})
	if !errors.Is(err, ErrInstanceFailed) || inst == nil || inst.ID != "i1" {
		t.Errorf("inst = %v, err = %v; want the instance and ErrInstanceFailed", inst, err)
	}
}

func TestWaitForInstanceStopsOnAHardError(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/i1", 404, `{"error":"no such instance"}`)
	if _, err := client.WaitForInstance(t.Context(), "i1", WaitOptions{Poll: quickPoll}); !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
	if _, err := client.WaitForInstance(t.Context(), "", WaitOptions{}); err == nil {
		t.Error("an empty ID was accepted")
	}
}

func TestWaitForInstanceGivesTheLastSeenOnTimeout(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/i1", 200, `{"instance":{"id":"i1","status":"suspended"}}`)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	inst, err := client.WaitForInstance(ctx, "i1", WaitOptions{Poll: quickPoll})
	waitErr, isWait := errors.AsType[*WaitError](err)
	if !isWait || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a WaitError over the deadline", err)
	}
	if waitErr.Instance == nil || waitErr.Instance.Status != ProcessSuspended || inst != waitErr.Instance {
		t.Errorf("last seen = %+v, want the suspended instance", waitErr.Instance)
	}
	if msg := err.Error(); msg == "" {
		t.Error("no message")
	}
	if msg := (&WaitError{Err: context.Canceled}).Error(); msg == "" {
		t.Error("no message without an instance")
	}
}

func TestStartAndWait(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i1"}`)
	replies(f, "GET /api/v1/instances/i1",
		ok(`{"instance":{"id":"i1","status":"active"}}`),
		ok(`{"instance":{"id":"i1","status":"completed"}}`),
	)
	inst, err := client.StartAndWait(t.Context(), "p1", "credit-check", Variables{"amount": 10}, WaitOptions{Poll: quickPoll}, WithBusinessKey("C-1"))
	if err != nil || inst.Status != ProcessCompleted {
		t.Fatalf("inst = %+v, %v; want completed", inst, err)
	}
}

func TestStartAndWaitKeepsTheIDWhenTheWaitFails(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i1"}`)
	f.respond("GET /api/v1/instances/i1", 403, `{"error":"not yours"}`)
	inst, err := client.StartAndWait(t.Context(), "p1", "x", nil, WaitOptions{Poll: quickPoll})
	if !IsUnauthorized(err) || inst == nil || inst.ID != "i1" {
		t.Errorf("inst = %v, err = %v; want i1 and the 403", inst, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f2, client2 := newFakeServer(t)
	f2.respond("POST /api/v1/process/start", 200, `{"instance_id":"i2"}`)
	// The start itself is refused by the cancelled context.
	if _, err := client2.StartAndWait(ctx, "p1", "x", nil, WaitOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestStartAndWaitFillsTheWaitErrorsInstance(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i1"}`)
	f.mux.HandleFunc("GET /api/v1/instances/i1", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answers before the deadline
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	inst, err := client.StartAndWait(ctx, "p1", "x", nil, WaitOptions{Poll: quickPoll})
	waitErr, isWait := errors.AsType[*WaitError](err)
	if !isWait || waitErr.Instance == nil || waitErr.Instance.ID != "i1" || inst.ID != "i1" {
		t.Errorf("inst = %v, err = %v; want i1 on the WaitError", inst, err)
	}
}

func TestStartAndWaitStartFailure(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/process/start", 400, `{"error":"no such process"}`)
	if inst, err := client.StartAndWait(t.Context(), "p1", "x", nil, WaitOptions{}); !IsInvalid(err) || inst != nil {
		t.Errorf("inst = %v, err = %v; want nil and the 400", inst, err)
	}
}

func TestInstanceWaitingAt(t *testing.T) {
	var inst Instance
	mustDecode(t, `{"id":"i1","tokens":[
		{"id":"k1","node":{"id":"Review"},"status":"completed"},
		{"id":"k2","node":{"id":"WaitForPayment"},"status":"active","created_at":"2026-10-28T10:00:00Z"}]}`, &inst)
	if !inst.WaitingAt("WaitForPayment") || inst.WaitingAt("Review") || inst.WaitingAt("Ship") {
		t.Errorf("WaitingAt wrong for %+v", inst.Tokens)
	}
	if inst.Tokens[1].Step.ID != "WaitForPayment" || inst.Tokens[1].CreatedAt.IsZero() {
		t.Errorf("token = %+v", inst.Tokens[1])
	}
}

func TestSendMessageToWaitsForTheReceiver(t *testing.T) {
	f, client := newFakeServer(t)
	replies(f, "GET /api/v1/instances/i1",
		ok(`{"instance":{"id":"i1","status":"active","tokens":[{"node":{"id":"Charge"},"status":"active"}]}}`),
		ok(`{"instance":{"id":"i1","status":"active","tokens":[{"node":{"id":"WaitForPayment"},"status":"active"}]}}`),
	)
	f.respond("POST /api/v1/processes/message", 200, `{}`)

	err := client.SendMessageTo(t.Context(), "p1", "payment-received", "ORDER-1", Variables{"paid": true},
		Receiver{Instance: "i1", WaitingAt: "WaitForPayment", Poll: quickPoll})
	if err != nil {
		t.Fatal(err)
	}
	sent := f.last()
	if sent.Path != "/api/v1/processes/message" || sent.Body["message_name"] != "payment-received" || sent.Body["correlation_key"] != "ORDER-1" {
		t.Errorf("sent %s %v", sent.Path, sent.Body)
	}
	if n := f.count(); n != 3 {
		t.Errorf("requests = %d, want two reads then the send", n)
	}
}

func TestSendMessageToRefusesWhenNobodyWaits(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/done", 200, `{"instance":{"id":"done","status":"completed"}}`)
	f.respond("GET /api/v1/instances/stuck", 200, `{"instance":{"id":"stuck","status":"active","tokens":[{"node":{"id":"Charge"},"status":"active"}]}}`)
	f.respond("GET /api/v1/instances/gone", 404, `{"error":"no such instance"}`)

	err := client.SendMessageTo(t.Context(), "p1", "m", "k", nil, Receiver{Instance: "done", WaitingAt: "W", Poll: quickPoll})
	if !errors.Is(err, ErrNoReceiver) {
		t.Errorf("finished: err = %v, want ErrNoReceiver", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = client.SendMessageTo(ctx, "p1", "m", "k", nil, Receiver{Instance: "stuck", WaitingAt: "W", Poll: quickPoll})
	if !errors.Is(err, ErrNoReceiver) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: err = %v, want ErrNoReceiver and the deadline", err)
	}

	if err := client.SendMessageTo(t.Context(), "p1", "m", "k", nil, Receiver{Instance: "gone", WaitingAt: "W"}); !IsNotFound(err) {
		t.Errorf("gone: err = %v, want 404", err)
	}
	if err := client.SendMessageTo(t.Context(), "p1", "m", "k", nil, Receiver{Instance: "i1"}); err == nil {
		t.Error("a receiver with no step was accepted")
	}
	for _, r := range f.snapshot() {
		if r.Path == "/api/v1/processes/message" {
			t.Fatal("a message was sent with nobody waiting")
		}
	}
}
