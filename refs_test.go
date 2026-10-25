package metis

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestARefIsTheBareIDOnTheWire(t *testing.T) {
	encoded, err := json.Marshal(struct {
		Instance InstanceRef `json:"instance_id"`
		Missing  InstanceRef `json:"missing,omitzero"`
	}{Instance: InstanceRef{ID: "i-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"instance_id":"i-1"}` {
		t.Errorf("encoded = %s", encoded)
	}
	var decoded struct {
		A TaskRef     `json:"a"`
		B ProjectRef  `json:"b"`
		C WebhookRef  `json:"c"`
		D DeliveryRef `json:"d"`
		E IncidentRef `json:"e"`
	}
	if err := json.Unmarshal([]byte(`{"a":"t-1","b":null,"c":"w-1","d":"d-1","e":"x-1"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.A.ID != "t-1" || !decoded.B.IsZero() || decoded.C.String() != "w-1" || decoded.D.ID != "d-1" || decoded.E.ID != "x-1" {
		t.Errorf("decoded = %+v", decoded)
	}
	if err := json.Unmarshal([]byte(`{"a":7}`), &decoded); err == nil {
		t.Error("a number was taken for an ID")
	}
}

func TestResponsesCarryRefs(t *testing.T) {
	var op Operation
	mustDecode(t, `{"id":"a-1","project_id":"p-1","instance_id":"i-1","node_id":"approve","node_name":"Approve"}`, &op)
	if op.Project.ID != "p-1" || op.Instance.ID != "i-1" || op.Step != (StepRef{ID: "approve", Name: "Approve"}) {
		t.Errorf("operation = %+v", op)
	}

	var period RetentionPeriod
	mustDecode(t, `{"id":"r-1","project_id":"p-1","process_key":"order","reason":"audit"}`, &period)
	if period.Process != (ProjectRef{ID: "p-1"}).Process("order") || period.Reason != "audit" {
		t.Errorf("period = %+v", period)
	}

	var sc SimulationCase
	mustDecode(t, `{"id":"c-1","project_id":"p-1","process_key":"order","name":"happy path"}`, &sc)
	if sc.Process.Key != "order" || sc.Process.Project.ID != "p-1" || sc.Name != "happy path" {
		t.Errorf("case = %+v", sc)
	}

	var hold RetentionHold
	mustDecode(t, `{"id":"h-1","project_id":"p-1","instance_id":"i-1"}`, &hold)
	if hold.Project.ID != "p-1" || hold.Instance.ID != "i-1" {
		t.Errorf("hold = %+v", hold)
	}

	var item BatchItem
	mustDecode(t, `{"instance_id":"i-9","status":"failed"}`, &item)
	if item.Instance.ID != "i-9" {
		t.Errorf("item = %+v", item)
	}

	var delivery Delivery
	mustDecode(t, `{"event_id":"e-1","project_id":"p-1","instance":{"id":"i-1","process_key":"order"},
		"node":{"id":"approve","name":"Approve"},"task":{"id":"t-1"},"incident":{"id":"x-1"}}`, &delivery)
	if delivery.Instance.Process != (ProjectRef{ID: "p-1"}).Process("order") || delivery.Instance.Ref().ID != "i-1" {
		t.Errorf("delivery instance = %+v", delivery.Instance)
	}
	if delivery.Node.Ref().Name != "Approve" || delivery.Task.Ref().ID != "t-1" || delivery.Incident.Ref().ID != "x-1" {
		t.Errorf("delivery = %+v", delivery)
	}

	var log WebhookDelivery
	mustDecode(t, `{"id":"d-1","webhook_id":"w-1"}`, &log)
	if log.Webhook.ID != "w-1" {
		t.Errorf("log = %+v", log)
	}
}

func mustDecode(t *testing.T, body string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), into); err != nil {
		t.Fatal(err)
	}
}

func TestRefsLoadWhatTheyName(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("GET /api/v1/instances/i-1", 200, `{"instance":{"id":"i-1","status":"active"}}`)
	f.respond("GET /api/v1/tasks/t-1", 200, `{"task":{"id":"t-1","name":"Check","status":"claimed"}}`)
	f.respond("GET /api/v1/projects/p-1", 200, `{"project":{"id":"p-1","name":"Loans"}}`)
	f.respond("GET /api/v1/incidents/i-1", 200, `{"incidents":[{"id":"x-1","error":"boom","status":"open"}]}`)
	f.respond("POST /api/v1/incidents/x-1/resolve", 200, `{}`)
	f.respond("POST /api/v1/outgoing-webhook-deliveries/d-1/retry", 200, `{}`)
	f.respond("POST /api/v1/definitions/versions/schedule/cancel", 200, `{}`)
	f.respond("POST /api/v1/process/start", 200, `{"instance_id":"i-2"}`)
	ctx := t.Context()

	instance, err := InstanceRef{ID: "i-1"}.Get(ctx, client)
	if err != nil || instance.Ref() != (InstanceRef{ID: "i-1"}) {
		t.Fatalf("instance = %+v, %v", instance, err)
	}
	task, err := TaskRef{ID: "t-1"}.Get(ctx, client)
	if err != nil || task.Ref().ID != "t-1" {
		t.Fatalf("task = %+v, %v", task, err)
	}
	project, err := ProjectRef{ID: "p-1"}.Get(ctx, client)
	if err != nil || project.Name != "Loans" {
		t.Fatalf("project = %+v, %v", project, err)
	}
	incidents, err := instance.Ref().Incidents(ctx, client)
	if err != nil || len(incidents) != 1 {
		t.Fatalf("incidents = %+v, %v", incidents, err)
	}
	if err := incidents[0].Ref().Resolve(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := (DeliveryRef{ID: "d-1"}).Retry(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := (ReleaseRef{Project: ProjectRef{ID: "p-1"}, ID: "rel-1"}).Cancel(ctx, client); err != nil {
		t.Fatal(err)
	}
	if body := f.last().Body; body["project_id"] != "p-1" || body["release_id"] != "rel-1" {
		t.Errorf("cancel body = %v", body)
	}
	started, err := ProjectRef{ID: "p-1"}.Process("order").Start(ctx, client, Variables{"amount": 5})
	if err != nil || started.ID != "i-2" {
		t.Fatalf("started = %v, %v", started, err)
	}
	if body := f.last().Body; body["definition_key"] != "order" || body["project_id"] != "p-1" {
		t.Errorf("start body = %v", body)
	}
}

func TestAnEmptyRefAsksNothing(t *testing.T) {
	f, client := newFakeServer(t)
	ctx := t.Context()
	errs := []error{}
	_, err := InstanceRef{}.Get(ctx, client)
	errs = append(errs, err)
	_, err = TaskRef{}.Get(ctx, client)
	errs = append(errs, err)
	_, err = ProcessRef{Key: "order"}.Versions(ctx, client)
	errs = append(errs, err)
	errs = append(errs, IncidentRef{}.Resolve(ctx, client), DeliveryRef{}.Stop(ctx, client), ReleaseRef{}.Cancel(ctx, client))
	for i, err := range errs {
		if err == nil {
			t.Errorf("call %d went ahead with an empty reference", i)
		}
	}
	if !strings.Contains(errs[2].Error(), "project") {
		t.Errorf("a process with no project says %q", errs[2])
	}
	if f.count() != 0 {
		t.Errorf("%d requests were sent", f.count())
	}
}

func TestAStepRefFindsItsStep(t *testing.T) {
	flow := oneFlow(t, approvalXML, NodeUserTask)
	task := UserTask{ID: "t-1", Name: "Checker", Node: &Node{ID: "check"}}
	step := task.Step().In(flow)
	if step == nil || step.Order != 2 {
		t.Fatalf("step = %+v", step)
	}
	if (StepRef{ID: "ok"}).In(flow) != nil || (StepRef{ID: "check"}).In(nil) != nil {
		t.Error("a step was found that is not there")
	}
	if (UserTask{}).Step() != (StepRef{}) {
		t.Error("a task with no node has a step")
	}
}
