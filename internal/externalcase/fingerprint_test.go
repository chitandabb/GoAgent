package externalcase

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFingerprintIsCanonicalAndExcludesStorageLocation(t *testing.T) {
	item := fingerprintFixture()
	first, err := Fingerprint(item)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	item.ID = uuid.New()
	item.DataSourceID = uuid.New()
	item.Description = "  第一行\r\n第二行  "
	item.Attachments[0], item.Attachments[1] = item.Attachments[1], item.Attachments[0]
	item.Attachments[0].ObjectKey = "erp/changed-storage-location"
	second, err := Fingerprint(item)
	if err != nil {
		t.Fatalf("fingerprint changed item: %v", err)
	}
	if first != second {
		t.Fatalf("canonical fingerprint changed: first=%s second=%s", first, second)
	}
}

func TestFingerprintChangesWhenDiagnosisInputChanges(t *testing.T) {
	item := fingerprintFixture()
	first, _ := Fingerprint(item)
	item.Production.BatchNo = "BATCH-NEW"
	second, _ := Fingerprint(item)
	if first == second {
		t.Fatal("fingerprint did not change after batch number changed")
	}

	item = fingerprintFixture()
	first, _ = Fingerprint(item)
	item.Attachments[0].ContentHash = "sha256:changed"
	second, _ = Fingerprint(item)
	if first == second {
		t.Fatal("fingerprint did not change after attachment content changed")
	}
}

func fingerprintFixture() ExternalCase {
	reported := time.Date(2026, 7, 25, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	return ExternalCase{
		ExternalCaseKey: "TKT-1001", CaseType: "production_fault", Title: "库存未更新",
		Description: "第一行\n第二行", Status: StatusOpen, Priority: PriorityHigh,
		SourceStatus: "New", SourcePriority: "Urgent", ReportedAt: reported,
		SourceUpdatedAt: reported.Add(time.Hour),
		Production:      ProductionContext{WorkOrderNo: "WO-1", BatchNo: "BATCH-1"},
		Attributes:      map[string]any{"tenant": "A"},
		Attachments: []ExternalAttachment{
			{ExternalAttachmentKey: "ATT-2", FileName: "b.png", MediaType: "image/png", SizeBytes: 2, ObjectKey: "erp/b", ContentHash: "sha256:b", SourceUpdatedAt: reported},
			{ExternalAttachmentKey: "ATT-1", FileName: "a.png", MediaType: "image/png", SizeBytes: 1, ObjectKey: "erp/a", ContentHash: "sha256:a", SourceUpdatedAt: reported},
		},
	}
}
