package metis

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Computed by the server's own signer (internal/pkg/webhooksig.SignV2) from
// the inputs below, so this pins that both ends agree, not only that the SDK
// agrees with itself.
const (
	vectorBody      = `{"event":"task.completed","event_id":"e1"}`
	vectorSecret    = "whsec_test"
	vectorTimestamp = "1792577214"
	vectorID        = "0192f3a1-7c4e-7b20-9d1a-3e5f6a7b8c9d"
	vectorSignature = "v2=c4251a0905c590c2996b76173563b92af5d3c5bf1e4c2e5451c51b23bcc41018"
)

func TestSignWebhookMatchesTheServer(t *testing.T) {
	if got := SignWebhook([]byte(vectorBody), vectorSecret, vectorTimestamp, vectorID); got != vectorSignature {
		t.Errorf("SignWebhook = %s, want %s", got, vectorSignature)
	}
}

func TestVerifyWebhook(t *testing.T) {
	signedAt := time.Unix(1792577214, 0)
	body := []byte(vectorBody)

	if err := VerifyWebhook(body, vectorSecret, vectorTimestamp, vectorID, vectorSignature, signedAt.Add(4*time.Minute)); err != nil {
		t.Fatalf("a genuine delivery was refused: %v", err)
	}
	if err := VerifyWebhook(body, vectorSecret, vectorTimestamp, vectorID, "  "+strings.ToUpper(vectorSignature[3:]), signedAt); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a signature without its scheme = %v", err)
	}

	forged := map[string]struct {
		body                      string
		secret, ts, id, signature string
	}{
		"another body":      {`{"event":"task.completed","event_id":"e2"}`, vectorSecret, vectorTimestamp, vectorID, vectorSignature},
		"another secret":    {vectorBody, "guess", vectorTimestamp, vectorID, vectorSignature},
		"another timestamp": {vectorBody, vectorSecret, "1792577215", vectorID, vectorSignature},
		"another ID":        {vectorBody, vectorSecret, vectorTimestamp, "other", vectorSignature},
		"no signature":      {vectorBody, vectorSecret, vectorTimestamp, vectorID, ""},
		"not hex":           {vectorBody, vectorSecret, vectorTimestamp, vectorID, "v2=zz"},
		"the old scheme":    {vectorBody, vectorSecret, vectorTimestamp, vectorID, "sha256=" + vectorSignature[3:]},
	}
	for name, d := range forged {
		if err := VerifyWebhook([]byte(d.body), d.secret, d.ts, d.id, d.signature, signedAt); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}

	for name, now := range map[string]time.Time{
		"too late":  signedAt.Add(6 * time.Minute),
		"too early": signedAt.Add(-6 * time.Minute),
	} {
		if err := VerifyWebhook(body, vectorSecret, vectorTimestamp, vectorID, vectorSignature, now); !errors.Is(err, ErrStaleSignature) {
			t.Errorf("%s: err = %v, want ErrStaleSignature", name, err)
		}
	}

	if err := VerifyWebhook(body, " ", vectorTimestamp, vectorID, vectorSignature, signedAt); err == nil {
		t.Error("a delivery was verified with no secret")
	}
}

// Whatever follows the signature is only checked once it matches, so each of
// these is signed properly and refused for what it carries.
func TestVerifyWebhookRefusesWhatASignatureCannotSave(t *testing.T) {
	body := []byte(`{}`)
	now := time.Unix(1792577214, 0)
	for name, d := range map[string]struct{ ts, id string }{
		"a timestamp in milliseconds": {"1792577214000", "d-1"},
		"a timestamp that is no time": {"yesterday", "d-1"},
		"no delivery ID":              {vectorTimestamp, ""},
		"a dot in the ID":             {vectorTimestamp, "d.1"},
		"an ID too long":              {vectorTimestamp, strings.Repeat("d", 192)},
	} {
		if err := VerifyWebhook(body, vectorSecret, d.ts, d.id, SignWebhook(body, vectorSecret, d.ts, d.id), now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestReadWebhook(t *testing.T) {
	body := `{"event_id":"e-1","event":"incident.raised","occurred_at":"2026-10-21T10:41:00Z","project_id":"p-1",
		"instance":{"id":"i-1","business_key":"PO-4182","process_key":"purchase","process_version":3,"status":"active"},
		"node":{"id":"charge","name":"Charge the card"},"incident":{"id":"inc-1"}}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	req := httptest.NewRequest(http.MethodPost, "/hooks/metis", strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderDeliveryID, "d-1")
	req.Header.Set(HeaderSignature, SignWebhook([]byte(body), vectorSecret, ts, "d-1"))

	delivery, err := ReadWebhook(req, vectorSecret)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Event != EventIncidentRaised || delivery.DeliveryID != "d-1" || delivery.Incident.ID != "inc-1" ||
		delivery.Instance.BusinessKey != "PO-4182" || delivery.Instance.ProcessVersion != 3 || delivery.Node.Name != "Charge the card" {
		t.Errorf("delivery = %+v", delivery)
	}

	forged := httptest.NewRequest(http.MethodPost, "/hooks/metis", strings.NewReader(body))
	forged.Header = req.Header.Clone()
	if _, err := ReadWebhook(forged, "another secret"); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a delivery under another secret = %v", err)
	}

	notJSON := "not json"
	bad := httptest.NewRequest(http.MethodPost, "/hooks/metis", strings.NewReader(notJSON))
	bad.Header.Set(HeaderTimestamp, ts)
	bad.Header.Set(HeaderDeliveryID, "d-2")
	bad.Header.Set(HeaderSignature, SignWebhook([]byte(notJSON), vectorSecret, ts, "d-2"))
	if _, err := ReadWebhook(bad, vectorSecret); err == nil {
		t.Error("a signed body that is not a delivery was parsed")
	}

	huge := httptest.NewRequest(http.MethodPost, "/hooks/metis", strings.NewReader(strings.Repeat(" ", maxDeliveryBytes+1)))
	if _, err := ReadWebhook(huge, vectorSecret); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("an oversized delivery = %v", err)
	}
}

func TestManagingOutgoingWebhooks(t *testing.T) {
	f, client := newFakeServer(t)
	hook := `{"id":"w-1","project_id":"p-1","name":"Ledger","url":"https://ledger.example.com/hooks",
		"events":["process.completed"],"enabled":true,"created_at":"2026-10-22T09:00:00Z"`
	f.respond("POST /api/v1/projects/p-1/outgoing-webhooks", 200, `{"webhook":`+hook+`,"secret":"whsec_1"}}`)
	f.respond("GET /api/v1/projects/p-1/outgoing-webhooks", 200, `{"webhooks":[`+hook+`}]}`)
	f.respond("POST /api/v1/outgoing-webhooks/w-1/enabled", 200, `{}`)
	f.respond("DELETE /api/v1/outgoing-webhooks/w-1", 200, `{}`)

	created, err := client.CreateOutgoingWebhook(t.Context(), "p-1", "Ledger", "https://ledger.example.com/hooks", EventProcessCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret != "whsec_1" || created.Events[0] != EventProcessCompleted {
		t.Errorf("created = %+v", created)
	}
	body := f.last().Body
	if body["name"] != "Ledger" || body["url"] != "https://ledger.example.com/hooks" {
		t.Errorf("body = %#v", body)
	}
	if events, _ := body["events"].([]any); len(events) != 1 || events[0] != "process.completed" {
		t.Errorf("events = %#v", body["events"])
	}
	if _, err := client.CreateOutgoingWebhook(t.Context(), "p-1", "Ledger", "https://x"); err == nil {
		t.Error("a webhook for no events was registered")
	}

	hooks, err := client.ListOutgoingWebhooks(t.Context(), "p-1")
	if err != nil || len(hooks) != 1 || hooks[0].Secret != "" {
		t.Errorf("ListOutgoingWebhooks = %+v, %v", hooks, err)
	}
	if err := client.SetOutgoingWebhookEnabled(t.Context(), "w-1", false); err != nil {
		t.Fatal(err)
	}
	if enabled, sent := f.last().Body["enabled"]; !sent || enabled != false {
		t.Errorf("enabled = %#v, sent %v", enabled, sent)
	}
	if err := client.DeleteOutgoingWebhook(t.Context(), "w-1"); err != nil {
		t.Fatal(err)
	}
}

func TestTryingAnOutgoingWebhook(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/outgoing-webhooks/w-1/test", 200, `{"test":{"delivery_id":"d-1","event":"task.completed",
		"accepted":false,"duration_ms":120,
		"request":{"method":"POST","url":"https://x","headers":{"X-Metis-Event":"task.completed"},"body":{"test":true}},
		"response":{"status_code":401,"body":"bad signature"}}}`)

	test, err := client.SendOutgoingWebhookTest(t.Context(), "w-1", EventTaskCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if test.Accepted || test.Response.StatusCode != 401 || test.Request.Headers[HeaderEvent] != "task.completed" {
		t.Errorf("test = %+v", test)
	}
	if got := f.last().Body["event"]; got != "task.completed" {
		t.Errorf("event = %#v", got)
	}

	if _, err := client.SendOutgoingWebhookTest(t.Context(), "w-1", ""); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body == nil || len(body) != 0 {
		t.Errorf("a test of the first event sent %#v, want {}", body)
	}
}

func TestReadingADeliveryLog(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/outgoing-webhooks/w-1/deliveries", 200, `{"deliveries":[
		{"id":"d-1","webhook_id":"w-1","event":"process.completed","status":"pending","attempts":3,
		 "next_attempt_at":"2026-10-22T10:00:00Z","last_status_code":503,"body":{"event":"process.completed"},
		 "created_at":"2026-10-22T09:00:00Z"}],"next_before":"cursor-2"}`)
	f.respond("POST /api/v1/outgoing-webhook-deliveries/d-1/retry", 200, `{}`)
	f.respond("POST /api/v1/outgoing-webhook-deliveries/d-1/stop", 200, `{}`)

	page, err := client.ListOutgoingWebhookDeliveries(t.Context(), "w-1", ListDeliveriesOptions{Status: DeliveryPending, Before: "cursor-1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextBefore != "cursor-2" || page.Deliveries[0].NextAttemptAt == nil || page.Deliveries[0].LastStatusCode != 503 {
		t.Errorf("page = %+v", page)
	}
	query := f.last().Query
	if query.Get("status") != "pending" || query.Get("before") != "cursor-1" || query.Get("limit") != "20" {
		t.Errorf("query = %v", query)
	}

	if _, err := client.ListOutgoingWebhookDeliveries(t.Context(), "w-1", ListDeliveriesOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.last().Query) != 0 {
		t.Errorf("an unfiltered log sent %v", f.last().Query)
	}

	if err := client.RetryOutgoingWebhookDelivery(t.Context(), "d-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.StopOutgoingWebhookDelivery(t.Context(), "d-1"); err != nil {
		t.Fatal(err)
	}
	if last := f.last(); last.Path != "/api/v1/outgoing-webhook-deliveries/d-1/stop" {
		t.Errorf("stopped at %s", last.Path)
	}
}

func TestTryingAnIncomingWebhook(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/webhooks/in-1/simulate", 200, `{"simulation":{"would_act":true,"enabled":true,
		"signature":{"checked":true,"valid":true},"message_name":"PaymentReceived",
		"correlation_key":"PO-4182","waiting":[{"instance_id":"i-1","node_id":"wait","since":"2026-10-22T09:00:00Z"}],
		"waiting_total":1}}`)

	body := []byte(`{"order":"PO-4182"}`)
	result, err := client.SimulateWebhookDelivery(t.Context(), "in-1", WebhookSimulation{
		Body: body, Timestamp: vectorTimestamp, DeliveryID: "d-1", Signature: SignWebhook(body, "s", vectorTimestamp, "d-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WouldAct || !result.Signature.Valid || result.CorrelationKey != "PO-4182" || result.Waiting[0].Instance.ID != "i-1" {
		t.Errorf("result = %+v", result)
	}
	// The body goes as text: a signature is over those bytes, and decoding
	// and encoding it again would change them.
	sent := f.last().Body
	if sent["body"] != `{"order":"PO-4182"}` || sent["delivery_id"] != "d-1" || sent["timestamp"] != vectorTimestamp {
		t.Errorf("sent %#v", sent)
	}

	if _, err := client.SimulateWebhookDelivery(t.Context(), "in-1", WebhookSimulation{Body: body}); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.last().Body["signature"]; sent {
		t.Error("a signature was sent that nobody gave")
	}
}
