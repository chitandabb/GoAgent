package knowledge

import (
	"strings"
	"testing"
)

func TestChunkMarkdownPreservesSectionsAndTables(t *testing.T) {
	content := `# 报工故障

设备 E-100 在工单 WO-2026-001 报工时返回事务超时。

## 处理步骤

| 步骤 | 动作 |
| --- | --- |
| 1 | 检查接口日志 |

重试前确认 ERP 状态。`
	chunks, err := ChunkMarkdown(content, TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatalf("ChunkMarkdown(): %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3: %#v", len(chunks), chunks)
	}
	if chunks[0].ElementType != ElementText || strings.Join(chunks[0].SectionPath, "/") != "报工故障" {
		t.Fatalf("first chunk = %#v", chunks[0])
	}
	if chunks[1].ElementType != ElementTable || strings.Join(chunks[1].SectionPath, "/") != "报工故障/处理步骤" {
		t.Fatalf("table chunk = %#v", chunks[1])
	}
	if chunks[2].ContentSHA256 != SHA256Hex(chunks[2].ContentText) {
		t.Fatalf("content hash = %q", chunks[2].ContentSHA256)
	}
	if !strings.Contains(" "+chunks[1].SearchText+" ", " 处理 ") {
		t.Fatalf("table search text = %q, want section heading tokens", chunks[1].SearchText)
	}
}

func TestChunkMarkdownSplitsLongContentWithBoundedOverlap(t *testing.T) {
	chunks, err := ChunkMarkdown(strings.Repeat("故障定位需要核对日志。", 40), TextChunkOptions{
		MaxRunes: 128, OverlapRunes: 16,
	})
	if err != nil {
		t.Fatalf("ChunkMarkdown(): %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("len(chunks) = %d, want multiple chunks", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk.ContentText)) > 128 {
			t.Fatalf("chunk length = %d", len([]rune(chunk.ContentText)))
		}
	}
}

func TestChunkElementsSplitsMarkdownTableByRowsAndRepeatsHeader(t *testing.T) {
	page := 2
	table := "| alarm | meaning |\n| --- | --- |\n" +
		"| E01 | motor overload detected during startup |\n" +
		"| E02 | pressure sensor signal is unavailable |\n" +
		"| E03 | controller heartbeat timed out |\n" +
		"| E04 | emergency stop circuit is open |"
	chunks, err := ChunkElements([]DocumentElement{{
		Index: 0, PageNumber: &page, ElementType: ElementTable, ContentText: table,
	}}, TextChunkOptions{MaxRunes: 128, OverlapRunes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.ElementType != ElementTable || chunk.PageNumber == nil || *chunk.PageNumber != page ||
			!strings.HasPrefix(chunk.ContentText, "| alarm | meaning |\n| --- | --- |\n") ||
			len([]rune(chunk.ContentText)) > 128 {
			t.Fatalf("chunk = %+v", chunk)
		}
	}
}

func TestNormalizeSearchTextSupportsChineseAndTechnicalIdentifiers(t *testing.T) {
	got := NormalizeSearchText("设备报工失败 WO-2026 SQLServer")
	wantTerms := []string{"设备", "备报", "报工", "工失", "失败", "wo", "2026", "sqlserver"}
	for _, term := range wantTerms {
		if !strings.Contains(" "+got+" ", " "+term+" ") {
			t.Fatalf("NormalizeSearchText() = %q, missing %q", got, term)
		}
	}
	query, err := BuildTSQuery("报工失败 报工")
	if err != nil {
		t.Fatalf("BuildTSQuery(): %v", err)
	}
	if strings.Count(query, "报工") != 1 {
		t.Fatalf("BuildTSQuery() = %q, want deduplicated terms", query)
	}
}
