package knowledge

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCompressSearchContextKeepsWholeChunksAndGroupCoverage(t *testing.T) {
	firstDocument, secondDocument := uuid.New(), uuid.New()
	firstVersion, secondVersion := uuid.New(), uuid.New()
	firstHitID, secondHitID := uuid.New(), uuid.New()
	firstHit := compressionHit(firstDocument, firstVersion, firstHitID, 2, "ERP-504 无法报工")
	secondHit := compressionHit(secondDocument, secondVersion, secondHitID, 8, "报工接口返回超时")
	firstRelevant := compressionChunk(3, "检查 ERP-504 对应的接口网关和重试状态。")
	firstNoise := compressionChunk(1, "员工食堂开放时间与访客登记说明。")
	secondRelevant := compressionChunk(9, "无法报工时核对事务日志中的超时错误。")
	secondNoise := compressionChunk(7, "季度培训计划和会议室使用规则。")
	groups := []SearchContextGroup{
		compressionGroup(firstDocument, firstVersion, firstHitID, firstNoise, firstRelevant),
		compressionGroup(secondDocument, secondVersion, secondHitID, secondNoise, secondRelevant),
	}
	plan, err := OriginalQueryPlan("ERP-504 无法报工")
	if err != nil {
		t.Fatal(err)
	}

	compressed, stats, err := CompressSearchContext(
		plan, []SearchResult{firstHit, secondHit}, groups,
		ContextCompressionConfig{Enabled: true, MaxChunks: 2, MaxRunes: 128, MinScore: 0.05},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) != 2 || len(compressed[0].Chunks) != 1 || len(compressed[1].Chunks) != 1 {
		t.Fatalf("compressed groups = %+v", compressed)
	}
	if compressed[0].Chunks[0].ChunkID != firstRelevant.ChunkID ||
		compressed[1].Chunks[0].ChunkID != secondRelevant.ChunkID {
		t.Fatalf("selected chunks = %+v", compressed)
	}
	for _, group := range compressed {
		if !group.Truncated || group.Chunks[0].ContentSHA256 != SHA256Hex(group.Chunks[0].ContentText) {
			t.Fatalf("compressed group lost traceability = %+v", group)
		}
	}
	if stats.InputChunks != 4 || stats.OutputChunks != 2 || stats.OmittedChunks != 2 ||
		stats.OutputRunes != len([]rune(firstRelevant.ContentText))+len([]rune(secondRelevant.ContentText)) {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestCompressSearchContextEnforcesGlobalRuneBudget(t *testing.T) {
	documentID, versionID := uuid.New(), uuid.New()
	hitID := uuid.New()
	hit := compressionHit(documentID, versionID, hitID, 1, "连接池超时")
	long := compressionChunk(2, "连接池超时"+strings.Repeat("检查最大连接数和慢事务。", 16))
	other := compressionChunk(0, "检查服务健康状态。")
	plan, err := OriginalQueryPlan("连接池超时")
	if err != nil {
		t.Fatal(err)
	}
	groups := []SearchContextGroup{compressionGroup(documentID, versionID, hitID, other, long)}
	compressed, stats, err := CompressSearchContext(
		plan, []SearchResult{hit}, groups,
		ContextCompressionConfig{Enabled: true, MaxChunks: 2, MaxRunes: 40, MinScore: 0},
	)
	if err == nil {
		t.Fatalf("CompressSearchContext accepted a maxRunes value below the configured safety floor: groups=%+v stats=%+v", compressed, stats)
	}

	compressed, stats, err = CompressSearchContext(
		plan, []SearchResult{hit}, groups,
		ContextCompressionConfig{Enabled: true, MaxChunks: 2, MaxRunes: 128, MinScore: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OutputRunes > 128 || len(compressed) != 1 || len(compressed[0].Chunks) != 1 ||
		compressed[0].Chunks[0].ChunkID != other.ChunkID {
		t.Fatalf("groups=%+v stats=%+v", compressed, stats)
	}
}

func TestCompressSearchContextDeduplicatesEquivalentContent(t *testing.T) {
	documentID, versionID := uuid.New(), uuid.New()
	hitID := uuid.New()
	hit := compressionHit(documentID, versionID, hitID, 1, "错误 1205")
	first := compressionChunk(0, "错误 1205 表示事务被选为死锁牺牲者。")
	duplicate := first
	duplicate.ChunkID = uuid.New()
	duplicate.Ordinal = 2
	plan, err := OriginalQueryPlan("错误 1205")
	if err != nil {
		t.Fatal(err)
	}
	compressed, stats, err := CompressSearchContext(
		plan, []SearchResult{hit}, []SearchContextGroup{
			compressionGroup(documentID, versionID, hitID, first, duplicate),
		}, ContextCompressionConfig{Enabled: true, MaxChunks: 4, MaxRunes: 256, MinScore: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) != 1 || len(compressed[0].Chunks) != 1 || stats.OmittedChunks != 1 {
		t.Fatalf("groups=%+v stats=%+v", compressed, stats)
	}
}

func compressionHit(documentID, versionID, chunkID uuid.UUID, ordinal int, content string) SearchResult {
	return SearchResult{
		DocumentID: documentID, DocumentVersionID: versionID, ChunkID: chunkID,
		Title: "排查手册", Scope: ScopeGlobal, Ordinal: ordinal, ElementType: ElementText,
		ContentText: content, ContentSHA256: SHA256Hex(content),
	}
}

func compressionChunk(ordinal int, content string) SearchContextChunk {
	return SearchContextChunk{
		ChunkID: uuid.New(), Ordinal: ordinal, ElementType: ElementText,
		ContentText: content, ContentSHA256: SHA256Hex(content),
	}
}

func compressionGroup(
	documentID, versionID, hitID uuid.UUID,
	chunks ...SearchContextChunk,
) SearchContextGroup {
	return SearchContextGroup{
		DocumentID: documentID, DocumentVersionID: versionID,
		HitChunkIDs: []uuid.UUID{hitID}, Chunks: chunks,
	}
}
