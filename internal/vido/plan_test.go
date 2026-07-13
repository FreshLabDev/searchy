package vido

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeliveryPlanFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "delivery_plan_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan DeliveryPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlan(plan, 123, t.TempDir()); err != nil {
		t.Fatalf("fixture rejected: %v", err)
	}
}

func TestDeliveryPlanRejectsCacheEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(outside, []byte("not-media"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := DeliveryPlan{
		Version: 1,
		JobID:   7,
		Operations: []Operation{{
			OperationID: "media-1",
			Type:        "video",
			Source:      &Source{Kind: "local_file_uri", Value: "file://" + outside},
		}},
	}
	if err := ValidatePlan(plan, 7, root); err == nil {
		t.Fatal("cache-root escape was accepted")
	}
}

func TestDeliveryPlanRejectsSymlinkInsideCache(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(outside, []byte("not-media"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "media.mp4")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plan := DeliveryPlan{
		Version: 1,
		JobID:   8,
		Operations: []Operation{{
			OperationID: "media-1",
			Type:        "video",
			Source:      &Source{Kind: "local_file_uri", Value: "file://" + link},
		}},
	}
	if err := ValidatePlan(plan, 8, root); err == nil {
		t.Fatal("symlink inside cache was accepted")
	}
}
