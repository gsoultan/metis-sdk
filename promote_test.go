package metis

import (
	"context"
	"testing"
)

const stagedPromotion = `{"release":{"id":"r-1","project_id":"p1","name":"2026.11.1","hash":"abc","origin":"imported","state":"staged",
	"items":[{"kind":"decision","key":"limit","version":4,"source_version":2},{"kind":"process","key":"claims","version":0,"source_version":7}],
	"connections":["payments-api"],"cut_at":"2026-10-31T09:00:00Z","cut_by":"dana","acted_by":"lee","imported_by":"lee","created_at":"2026-10-31T10:00:00Z"}}`

func TestAPromotionIsCutImportedApprovedAndMadeLive(t *testing.T) {
	f, c := newFakeServer(t)
	ctx := context.Background()
	f.respond("POST /api/v1/projects/p1/releases", 200, stagedPromotion)
	f.respond("POST /api/v1/projects/p1/releases/import", 200, stagedPromotion)
	f.respond("POST /api/v1/projects/p1/releases/2026.11.1/approve", 200, stagedPromotion)
	f.respond("POST /api/v1/projects/p1/releases/2026.11.1/go-live", 200, stagedPromotion)
	f.respond("POST /api/v1/projects/p1/releases/rollback", 200, stagedPromotion)

	if _, err := c.CutPromotion(ctx, "p1", CutPromotion{Name: "2026.11.1", Processes: []string{"claims"}}); err != nil {
		t.Fatalf("cut: %v", err)
	}
	cut := f.last()
	if cut.Body["name"] != "2026.11.1" || len(cut.Body["processes"].([]any)) != 1 || cut.Body["decisions"] == nil {
		t.Fatalf("cut sent %v", cut.Body)
	}

	got, err := c.ImportPromotion(ctx, "p1", "env-dev", "2026.11.1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if f.last().Body["from_environment"] != "env-dev" || f.last().Body["name"] != "2026.11.1" {
		t.Fatalf("import sent %v", f.last().Body)
	}
	if got.State != "staged" || len(got.Items) != 2 || got.Items[1].Version != 0 || got.Connections[0] != "payments-api" || got.ImportedBy != "lee" {
		t.Fatalf("import read %+v", got)
	}

	for name, call := range map[string]func() (*Promotion, error){
		"approve":  func() (*Promotion, error) { return c.ApprovePromotion(ctx, "p1", "2026.11.1") },
		"go live":  func() (*Promotion, error) { return c.GoLive(ctx, "p1", "2026.11.1") },
		"rollback": func() (*Promotion, error) { return c.RollBackPromotion(ctx, "p1") },
	} {
		if _, err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestAPromotionFileIsExportedAndImportedWhole(t *testing.T) {
	f, c := newFakeServer(t)
	ctx := context.Background()
	f.respond("GET /api/v1/projects/p1/releases/r1/export", 200, `{"file":{"format":"metis.release-file/v1","hash":"abc","key":"k","signature":"s","release":{"name":"r1"}}}`)
	f.respond("POST /api/v1/projects/p2/releases/import-file", 200, stagedPromotion)

	file, err := c.ExportPromotion(ctx, "p1", "r1")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := c.ImportPromotionFile(ctx, "p2", file); err != nil {
		t.Fatalf("import the file: %v", err)
	}
	sent, _ := f.last().Body["file"].(map[string]any)
	if sent["signature"] != "s" || sent["hash"] != "abc" {
		t.Fatalf("import sent the file as %v", f.last().Body)
	}
	if _, err := c.ImportPromotionFile(ctx, "p2", []byte("not json")); err == nil {
		t.Fatal("a file that is not JSON was sent")
	}
}

func TestACutPromotionNeedsAName(t *testing.T) {
	_, c := newFakeServer(t)
	if _, err := c.CutPromotion(context.Background(), "p1", CutPromotion{}); err == nil {
		t.Fatal("a promotion with no name was cut")
	}
}
