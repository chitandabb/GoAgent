package chatmodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// toolInfoFingerprint 对 ToolInfo 做与评测一致的全量 JSON 指纹：json.Marshal
// 走 schema.ToolInfo 自带 MarshalJSON（保留 params/jsonschema 两种表示）。
func toolInfoFingerprint(t *testing.T, info *schema.ToolInfo) string {
	t.Helper()
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal ToolInfo: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// sourceJSONSchemaToolInfoForTest 构造 required 顺序明确为 ["zeta","alpha"]
// 的 JSONSchema 表示 ToolInfo：与根因同形（ParamsOneOf.ToJSONSchema 在
// JSONSchema 表示下直接返回内部指针，openai ACL 的 toTools 会原地
// sort.Strings(sc.Required)，从而修改调用方持有的源 Schema）。
func sourceJSONSchemaToolInfoForTest() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: "read_external_case",
		Desc: "read an external case",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type:     "object",
			Required: []string{"zeta", "alpha"},
		}),
	}
}

// assertSourceToolInfoUnchanged 断言源 ToolInfo 的 Required 顺序、JSON 字节与
// 指纹全部未变。
func assertSourceToolInfoUnchanged(
	t *testing.T,
	source *schema.ToolInfo,
	wantJSON string,
	wantFingerprint string,
) {
	t.Helper()
	if source.ParamsOneOf == nil {
		t.Fatal("source ParamsOneOf became nil")
	}
	js, err := source.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	if js == nil {
		t.Fatal("source JSONSchema became nil")
	}
	if len(js.Required) != 2 || js.Required[0] != "zeta" || js.Required[1] != "alpha" {
		t.Fatalf("source Required mutated: %v", js.Required)
	}
	if got := toolInfoFingerprint(t, source); got != wantFingerprint {
		t.Fatalf("source fingerprint mutated: %s != %s", got, wantFingerprint)
	}
	if got, err := json.Marshal(source); err != nil || string(got) != wantJSON {
		t.Fatalf("source JSON mutated: %s (err=%v)", got, err)
	}
}

// TestRealOpenAIAdapterWithToolsDoesNotMutateSourceSchema 使用真实 Eino
// OpenAI Adapter，但只调用 WithTools、绝不 Generate/Stream，因此不发生任何
// HTTP/Provider 请求。构造 required 顺序明确为 ["zeta","alpha"] 的
// JSONSchema，证明旧 Factory 返回的裸模型会修改源 Schema，修复后源
// Schema、JSON、canonical fingerprint 全部不变。
func TestRealOpenAIAdapterWithToolsDoesNotMutateSourceSchema(t *testing.T) {
	t.Setenv("MESGUARD_TEST_CHAT_KEY", "test-key")
	profile := profileForTest("stepfun", "step-3.7-flash", "low", "")
	// 不可达地址：本测试只 WithTools，绝不 Generate，任何 HTTP 都是测试失败。
	profile.BaseURL = "http://127.0.0.1:9/v1"
	instance, err := New(context.Background(), "fixture", profile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !components.IsCallbacksEnabled(instance.Model) {
		t.Fatal("Factory decorator must preserve OpenAI callback ownership")
	}
	if componentType, ok := components.GetType(instance.Model); !ok || componentType != "OpenAI" {
		t.Fatalf("Factory decorator must preserve OpenAI component type, got %q (ok=%v)", componentType, ok)
	}

	source := sourceJSONSchemaToolInfoForTest()
	wantJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint := toolInfoFingerprint(t, source)

	bound, err := instance.Model.WithTools([]*schema.ToolInfo{source})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if bound == nil {
		t.Fatal("WithTools returned nil model")
	}
	assertSourceToolInfoUnchanged(t, source, string(wantJSON), wantFingerprint)

	// bound model 再次绑定同一源仍必须稳定（第二次绑定与 call-time options
	// 都不能绕过隔离）。
	if _, err := bound.WithTools([]*schema.ToolInfo{source}); err != nil {
		t.Fatalf("re-bind bound model: %v", err)
	}
	assertSourceToolInfoUnchanged(t, source, string(wantJSON), wantFingerprint)
	if _, err := instance.Model.WithTools([]*schema.ToolInfo{source}); err != nil {
		t.Fatalf("re-bind base model: %v", err)
	}
	assertSourceToolInfoUnchanged(t, source, string(wantJSON), wantFingerprint)
}

// TestConcurrentWithToolsDoesNotMutateSharedSource 使用同一源 ToolInfo 并发
// WithTools：-race 下不得发生数据竞争（旧实现并发 sort 同一 Required 切片），
// 且源 Schema 不得漂移。嵌套 array->object 覆盖 openai ACL 的递归排序路径。
func TestConcurrentWithToolsDoesNotMutateSharedSource(t *testing.T) {
	t.Setenv("MESGUARD_TEST_CHAT_KEY", "test-key")
	profile := profileForTest("stepfun", "step-3.7-flash", "low", "")
	profile.BaseURL = "http://127.0.0.1:9/v1"
	instance, err := New(context.Background(), "fixture", profile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	source := &schema.ToolInfo{
		Name: "execute_readonly_query",
		Desc: "run a read-only SQL query",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type: "array",
			Items: &jsonschema.Schema{
				Type:     "object",
				Required: []string{"zeta", "alpha"},
			},
		}),
	}
	wantJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint := toolInfoFingerprint(t, source)

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := instance.Model.WithTools([]*schema.ToolInfo{source})
			errs[index] = err
		}(i)
	}
	wg.Wait()
	for index, bindErr := range errs {
		if bindErr != nil {
			t.Fatalf("goroutine %d WithTools: %v", index, bindErr)
		}
	}

	if source.ParamsOneOf == nil {
		t.Fatal("source ParamsOneOf became nil")
	}
	js, err := source.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	if js == nil || js.Items == nil {
		t.Fatalf("source Items became nil: %#v", js)
	}
	if len(js.Items.Required) != 2 || js.Items.Required[0] != "zeta" || js.Items.Required[1] != "alpha" {
		t.Fatalf("source nested Required mutated: %v", js.Items.Required)
	}
	if got := toolInfoFingerprint(t, source); got != wantFingerprint {
		t.Fatalf("source fingerprint mutated: %s != %s", got, wantFingerprint)
	}
	if got, err := json.Marshal(source); err != nil || string(got) != string(wantJSON) {
		t.Fatalf("source JSON mutated: %s (err=%v)", got, err)
	}
}

// mutatingFakeModel 模拟第三方 Adapter 的破坏性行为：无论 WithTools 还是
// Generate/Stream 的 call-time options，都原地修改收到的 ToolInfo（改名 +
// 排序 Required），并记录收到的引用供测试断言隔离。
type mutatingFakeModel struct {
	withToolsError error
	nilBound       bool
	callbacks      bool
	componentType  string
	lastBound      []*schema.ToolInfo
	lastGenerate   []*schema.ToolInfo
	lastStream     []*schema.ToolInfo
	boundCalls     int
	generateCalls  int
	streamCalls    int
}

func mutateToolInfosForTest(tools []*schema.ToolInfo) {
	for _, info := range tools {
		if info == nil {
			continue
		}
		info.Name = "mutated_" + info.Name
		if info.ParamsOneOf == nil {
			continue
		}
		js, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil || js == nil {
			continue
		}
		if js.Required != nil {
			sort.Strings(js.Required)
		}
	}
}

func (m *mutatingFakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.boundCalls++
	m.lastBound = tools
	mutateToolInfosForTest(tools)
	if m.withToolsError != nil {
		return nil, m.withToolsError
	}
	if m.nilBound {
		return nil, nil
	}
	return &mutatingFakeModel{callbacks: m.callbacks, componentType: m.componentType}, nil
}

func (m *mutatingFakeModel) IsCallbacksEnabled() bool { return m.callbacks }

func (m *mutatingFakeModel) GetType() string { return m.componentType }

func (m *mutatingFakeModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.generateCalls++
	applied := model.GetCommonOptions(nil, opts...)
	m.lastGenerate = applied.Tools
	mutateToolInfosForTest(applied.Tools)
	return &schema.Message{Role: schema.Assistant, Content: "ok"}, nil
}

func (m *mutatingFakeModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.streamCalls++
	applied := model.GetCommonOptions(nil, opts...)
	m.lastStream = applied.Tools
	mutateToolInfosForTest(applied.Tools)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "ok"}}), nil
}

// TestIsolatingDecoratorShieldsEveryBindingEntryPoint 用本地 fake Adapter
// 验证：WithTools 输入被隔离；Generate/Stream 的 call-time
// model.WithTools(...) 输入被隔离；bound model 再次 WithTools/Generate 仍被
// 隔离（第二次绑定与 call-time options 都不能绕过）。
func TestIsolatingDecoratorShieldsEveryBindingEntryPoint(t *testing.T) {
	ctx := context.Background()

	assertSource := func(t *testing.T, source *schema.ToolInfo) {
		t.Helper()
		wantJSON, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		assertSourceToolInfoUnchanged(t, source, string(wantJSON), toolInfoFingerprint(t, source))
	}

	// B1: WithTools 输入隔离。
	inner := &mutatingFakeModel{}
	isolated := &toolSchemaIsolatingModel{inner: inner}
	source := sourceJSONSchemaToolInfoForTest()
	assertSource(t, source)
	bound, err := isolated.WithTools([]*schema.ToolInfo{source})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	assertSource(t, source)
	if len(inner.lastBound) != 1 || inner.lastBound[0] == source {
		t.Fatalf("adapter must receive an isolated copy, got %d refs", len(inner.lastBound))
	}
	if inner.lastBound[0].Name != "mutated_read_external_case" {
		t.Fatalf("adapter must have received a mutable copy, got %q", inner.lastBound[0].Name)
	}

	// B4: bound model 再次 WithTools 仍隔离。
	if _, err := bound.WithTools([]*schema.ToolInfo{source}); err != nil {
		t.Fatalf("bound re-bind: %v", err)
	}
	assertSource(t, source)

	// B2: Generate 的 call-time model.WithTools(...) 输入隔离（base 与 bound）。
	if _, err := isolated.Generate(ctx, nil, model.WithTools([]*schema.ToolInfo{source})); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	assertSource(t, source)
	if len(inner.lastGenerate) != 1 || inner.lastGenerate[0] == source {
		t.Fatalf("Generate adapter must receive an isolated copy, got %d refs", len(inner.lastGenerate))
	}
	if _, err := bound.Generate(ctx, nil, model.WithTools([]*schema.ToolInfo{source})); err != nil {
		t.Fatalf("bound Generate: %v", err)
	}
	assertSource(t, source)

	// B3: Stream 的 call-time model.WithTools(...) 输入隔离。
	innerStream := &mutatingFakeModel{}
	isolatedStream := &toolSchemaIsolatingModel{inner: innerStream}
	reader, err := isolatedStream.Stream(ctx, nil, model.WithTools([]*schema.ToolInfo{source}))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	reader.Close()
	assertSource(t, source)
	if len(innerStream.lastStream) != 1 || innerStream.lastStream[0] == source {
		t.Fatalf("Stream adapter must receive an isolated copy, got %d refs", len(innerStream.lastStream))
	}

	// 无工具 option 时原样透传且不报错。
	if _, err := isolated.Generate(ctx, nil, model.WithTemperature(0)); err != nil {
		t.Fatalf("Generate without tools: %v", err)
	}
	if inner.lastGenerate != nil {
		t.Fatalf("Generate without tools must not set tools")
	}
}

// TestIsolatingDecoratorPreservesComponentContracts verifies that introducing
// the schema-isolation decorator does not change Eino's callback ownership or
// component identity. Both the base and bound models must retain the inner
// model's Checker and Typer results.
func TestIsolatingDecoratorPreservesComponentContracts(t *testing.T) {
	inner := &mutatingFakeModel{callbacks: true, componentType: "OpenAI"}
	isolated := &toolSchemaIsolatingModel{inner: inner}

	assertContracts := func(t *testing.T, current model.ToolCallingChatModel) {
		t.Helper()
		if !components.IsCallbacksEnabled(current) {
			t.Fatal("schema isolation must preserve callback ownership")
		}
		componentType, ok := components.GetType(current)
		if !ok || componentType != "OpenAI" {
			t.Fatalf("schema isolation must preserve component type, got %q (ok=%v)", componentType, ok)
		}
	}

	assertContracts(t, isolated)
	bound, err := isolated.WithTools([]*schema.ToolInfo{sourceJSONSchemaToolInfoForTest()})
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	assertContracts(t, bound)
}

// TestIsolatingDecoratorFailsClosedAndPropagatesErrors 验证 fail-closed：
// 序列化失败、nil ToolInfo、底层错误、底层 nil model 全部原样失败且不产生
// 半成品 model；nil/空工具列表保留 Eino 语义透传。
func TestIsolatingDecoratorFailsClosedAndPropagatesErrors(t *testing.T) {
	ctx := context.Background()
	isolated := &toolSchemaIsolatingModel{inner: &mutatingFakeModel{}}

	// 序列化失败（Extra 含不可 JSON 序列化值）→ 三个入口全部 fail-closed。
	bad := sourceJSONSchemaToolInfoForTest()
	bad.Extra = map[string]any{"channel": make(chan int)}
	if _, err := isolated.WithTools([]*schema.ToolInfo{bad}); err == nil {
		t.Fatal("WithTools must fail closed on serialization failure")
	}
	if _, err := isolated.Generate(ctx, nil, model.WithTools([]*schema.ToolInfo{bad})); err == nil {
		t.Fatal("Generate must fail closed on serialization failure")
	}
	if _, err := isolated.Stream(ctx, nil, model.WithTools([]*schema.ToolInfo{bad})); err == nil {
		t.Fatal("Stream must fail closed on serialization failure")
	}

	// nil ToolInfo 元素 → fail-closed。
	if _, err := isolated.WithTools([]*schema.ToolInfo{nil}); err == nil {
		t.Fatal("WithTools must fail closed on nil ToolInfo")
	}

	// 底层错误原样返回。
	innerErr := errors.New("adapter bind exploded")
	isolatedErr := &toolSchemaIsolatingModel{inner: &mutatingFakeModel{withToolsError: innerErr}}
	if _, err := isolatedErr.WithTools([]*schema.ToolInfo{sourceJSONSchemaToolInfoForTest()}); !errors.Is(err, innerErr) {
		t.Fatalf("underlying error must propagate, got %v", err)
	}

	// 底层返回 nil bound → fail-closed。
	isolatedNil := &toolSchemaIsolatingModel{inner: &mutatingFakeModel{nilBound: true}}
	if _, err := isolatedNil.WithTools([]*schema.ToolInfo{sourceJSONSchemaToolInfoForTest()}); err == nil {
		t.Fatal("must fail closed when underlying WithTools returns nil model")
	}

	// nil/空工具列表保留 Eino 语义：透传给底层（由底层决定语义）。
	if _, err := isolated.WithTools(nil); err != nil {
		t.Fatalf("nil tools must pass through: %v", err)
	}
	if _, err := isolated.WithTools([]*schema.ToolInfo{}); err != nil {
		t.Fatalf("empty tools must pass through: %v", err)
	}
}

// extrasToolInfoForTest 构造带 JSONSchema.Extras 的 ToolInfo：jsonschema 包
// 的 Extras 字段标记为 json:"-"，Marshal 时手动合并进 JSON、Unmarshal 时被
// 丢弃，是 roundtrip 非保真字段。ToJSONSchema 对 jsonschema 表示返回内部
// 指针，因此可直接修改。
func extrasToolInfoForTest() *schema.ToolInfo {
	source := sourceJSONSchemaToolInfoForTest()
	js, err := source.ParamsOneOf.ToJSONSchema()
	if err != nil || js == nil {
		panic("extrasToolInfoForTest: source JSONSchema unavailable")
	}
	js.Extras = map[string]any{"x-mesguard-constraint": "strict"}
	return source
}

// TestCloneToolInfosRejectsNonFaithfulExtrasRoundtrip 证明 Extras 关键字在
// clone roundtrip 后消失（当前实现静默删减模型可见 Schema）：clone 要么
// 保真保留该关键字，要么 fail-closed 返回错误；绝不返回被删减的 Schema。
func TestCloneToolInfosRejectsNonFaithfulExtrasRoundtrip(t *testing.T) {
	source := extrasToolInfoForTest()
	cloned, err := cloneToolInfos([]*schema.ToolInfo{source})
	if err != nil {
		if !strings.Contains(err.Error(), "not semantically faithful") {
			t.Fatalf("Extras loss must fail through the semantic fidelity gate: %v", err)
		}
		return
	}
	if len(cloned) != 1 {
		t.Fatalf("clone count = %d, want 1", len(cloned))
	}
	reencoded, err := json.Marshal(cloned[0])
	if err != nil {
		t.Fatalf("marshal clone: %v", err)
	}
	if !strings.Contains(string(reencoded), "x-mesguard-constraint") {
		t.Fatalf("roundtrip silently dropped Extras keyword (clone JSON %s); must fail closed before handing a truncated Schema to the Adapter", reencoded)
	}
}

// TestIsolatingDecoratorFailsClosedBeforeAdapterOnNonFaithfulSchema 验证在
// 调用底层 Adapter 之前 fail-closed：WithTools/Generate/Stream 三入口对
// roundtrip 非保真的 Schema 全部返回错误，底层调用次数均为 0，且原
// ToolInfo 始终不变。
func TestIsolatingDecoratorFailsClosedBeforeAdapterOnNonFaithfulSchema(t *testing.T) {
	ctx := context.Background()
	inner := &mutatingFakeModel{}
	isolated := &toolSchemaIsolatingModel{inner: inner}
	source := extrasToolInfoForTest()
	wantJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint := toolInfoFingerprint(t, source)

	if _, err := isolated.WithTools([]*schema.ToolInfo{source}); err == nil {
		t.Fatal("WithTools must fail closed on non-faithful roundtrip")
	}
	if _, err := isolated.Generate(ctx, nil, model.WithTools([]*schema.ToolInfo{source})); err == nil {
		t.Fatal("Generate must fail closed on non-faithful roundtrip")
	}
	if _, err := isolated.Stream(ctx, nil, model.WithTools([]*schema.ToolInfo{source})); err == nil {
		t.Fatal("Stream must fail closed on non-faithful roundtrip")
	}
	if inner.boundCalls != 0 || inner.generateCalls != 0 || inner.streamCalls != 0 {
		t.Fatalf("adapter must not be called before fail-closed: bound=%d generate=%d stream=%d",
			inner.boundCalls, inner.generateCalls, inner.streamCalls)
	}
	assertSourceToolInfoUnchanged(t, source, string(wantJSON), wantFingerprint)
}

// TestCloneToolInfosPreservesFaithfulRepresentations 普通 jsonschema（无
// Extras）、params、TypeEnhanced、boolean Schema 的 roundtrip 保真：克隆
// 成功且克隆后的模型可见 JSON 与原 JSON 字节一致。
func TestCloneToolInfosPreservesFaithfulRepresentations(t *testing.T) {
	params := &schema.ToolInfo{
		Name: "params_tool",
		Desc: "params representation",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "query text", Required: true},
		}),
	}
	typeEnhanced := &schema.ToolInfo{
		Name: "type_enhanced_tool",
		Desc: "type enhanced representation",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			TypeEnhanced: []string{"string", "null"},
		}),
	}
	booleanSchema := &schema.ToolInfo{
		Name:        "boolean_tool",
		Desc:        "boolean schema representation",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(jsonschema.TrueSchema),
	}
	for _, source := range []*schema.ToolInfo{
		sourceJSONSchemaToolInfoForTest(), params, typeEnhanced, booleanSchema,
	} {
		original, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("marshal source: %v", err)
		}
		cloned, err := cloneToolInfos([]*schema.ToolInfo{source})
		if err != nil {
			t.Fatalf("faithful representation must clone successfully: %v", err)
		}
		if len(cloned) != 1 {
			t.Fatalf("clone count = %d, want 1", len(cloned))
		}
		reencoded, err := json.Marshal(cloned[0])
		if err != nil {
			t.Fatalf("marshal clone: %v", err)
		}
		if !bytes.Equal(original, reencoded) {
			t.Fatalf("faithful roundtrip must be byte-identical:\n original %s\n clone   %s", original, reencoded)
		}
	}
}

// TestCloneToolInfosAllowsEquivalentNumericEncoding covers JSON Schema numbers
// whose textual encoding changes during the upstream ToolInfo roundtrip without
// changing their mathematical value. The isolation guard must reject semantic
// loss, not harmless normalization such as 1e3 -> 1000.
func TestCloneToolInfosAllowsEquivalentNumericEncoding(t *testing.T) {
	source := &schema.ToolInfo{
		Name: "numeric_schema_tool",
		Desc: "schema with equivalent numeric encodings",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type:     "number",
			Default:  json.Number("1e3"),
			Examples: []any{json.Number("1.0")},
		}),
	}
	original, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}

	cloned, err := cloneToolInfos([]*schema.ToolInfo{source})
	if err != nil {
		t.Fatalf("semantically equivalent numeric normalization must remain cloneable: %v", err)
	}
	if len(cloned) != 1 {
		t.Fatalf("clone count = %d, want 1", len(cloned))
	}
	if current, err := json.Marshal(source); err != nil || !bytes.Equal(original, current) {
		t.Fatalf("source ToolInfo changed during clone: %s (err=%v)", current, err)
	}
}

// TestCloneToolInfosRejectsNumericPrecisionLoss distinguishes harmless number
// normalization from a changed mathematical value. A large integer rounded by
// the upstream any -> float64 roundtrip must still fail closed.
func TestCloneToolInfosRejectsNumericPrecisionLoss(t *testing.T) {
	source := &schema.ToolInfo{
		Name: "large_integer_schema_tool",
		Desc: "schema with a precision-sensitive integer",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type:    "integer",
			Default: json.Number("9007199254740993"),
		}),
	}
	original, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}

	if _, err := cloneToolInfos([]*schema.ToolInfo{source}); err == nil {
		t.Fatal("numeric precision loss must fail closed")
	} else if !strings.Contains(err.Error(), "not semantically faithful") {
		t.Fatalf("numeric precision loss must fail through the semantic fidelity gate: %v", err)
	}
	if current, err := json.Marshal(source); err != nil || !bytes.Equal(original, current) {
		t.Fatalf("source ToolInfo changed during rejected clone: %s (err=%v)", current, err)
	}
}
