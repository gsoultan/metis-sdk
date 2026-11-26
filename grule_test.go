package metis

import "testing"

func TestImportGruleRulesSendsTheFilesAndReadsTheReport(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/decisions/import/grule", 200, `{"import": {
		"decisions": [{"key": "pricing-discount", "name": "Discount", "version": 1}],
		"rules": [
			{"name": "Gold", "position": "pricing.grl:2", "decision": "pricing-discount"},
			{"name": "Audit", "position": "pricing.grl:9", "reason": "it calls Trim(), which FEEL has no equal for"}],
		"results": {"Order.Discount": "Order_Discount"},
		"stored": true, "process_id": "def-9"}}`)

	report, err := client.ImportGruleRules(t.Context(), "p-1", "pricing",
		[]RuleFile{{Name: "pricing.grl", Content: "rule Gold {}"}}, GruleDeploy(), GruleProcessName("Pricing"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Stored || report.ProcessID != "def-9" || report.Decisions[0].Key != "pricing-discount" || report.Results["Order.Discount"] != "Order_Discount" {
		t.Errorf("report = %+v", report)
	}
	if skipped := report.Skipped(); len(skipped) != 1 || skipped[0].Name != "Audit" || skipped[0].Reason == "" {
		t.Errorf("skipped = %+v", skipped)
	}

	body := f.last().Body
	if body["project_id"] != "p-1" || body["key"] != "pricing" || body["name"] != "Pricing" || body["deploy"] != true {
		t.Errorf("body = %v", body)
	}
	if _, sent := body["preview"]; sent {
		t.Error("preview was sent without being asked for")
	}
	files, _ := body["files"].([]any)
	if file, _ := files[0].(map[string]any); len(files) != 1 || file["name"] != "pricing.grl" || file["content"] != "rule Gold {}" {
		t.Errorf("files = %v", body["files"])
	}
}

func TestImportGruleRulesPreviewStoresNothing(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/decisions/import/grule", 200, `{"import": {"rules": []}}`)

	report, err := client.ImportGruleRules(t.Context(), "p-1", "pricing", []RuleFile{{Name: "a.grl"}}, GrulePreview())
	if err != nil {
		t.Fatal(err)
	}
	if report.Stored {
		t.Error("a preview says it stored")
	}
	if body := f.last().Body; body["preview"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestImportGruleRulesRefusalIsInvalid(t *testing.T) {
	f, client := newFakeServer(t)
	f.respond("POST /api/v1/decisions/import/grule", 400, `{"err": "broken.grl: rule A: line 3: expected a value, found `+"`then`"+`"}`)

	_, err := client.ImportGruleRules(t.Context(), "p-1", "pricing", []RuleFile{{Name: "broken.grl"}})
	if !IsInvalid(err) {
		t.Fatalf("err = %v, want IsInvalid", err)
	}
}

func TestImportGruleRulesRefusesNothingToSendWithoutAsking(t *testing.T) {
	f, client := newFakeServer(t)
	for _, call := range []func() error{
		func() error { _, err := client.ImportGruleRules(t.Context(), "", "k", []RuleFile{{}}); return err },
		func() error { _, err := client.ImportGruleRules(t.Context(), "p-1", "", []RuleFile{{}}); return err },
		func() error { _, err := client.ImportGruleRules(t.Context(), "p-1", "k", nil); return err },
	} {
		if call() == nil {
			t.Error("no error")
		}
	}
	if f.count() != 0 {
		t.Error("a request was sent")
	}
}
