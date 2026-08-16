package chatmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// cloneToolInfos 深拷贝 []*schema.ToolInfo：使用 schema.ToolInfo 自带
// MarshalJSON/UnmarshalJSON 完成 JSON roundtrip，保留 ParamsOneOf 的
// params/jsonschema 两种表示，不写脆弱的手工浅拷贝。nil 列表保持 nil 透传
// （保留 Eino 语义）；nil 元素与序列化失败 fail-closed。
//
// 表示来源：toolutils.InferTool 走 jsonschema Reflector 后
// NewParamsOneOfByJSONSchema，产生 **jsonschema 表示**（ToJSONSchema 返回
// 内部指针，共享且可被第三方 Adapter 原地修改）；Eino Skill middleware 的
// buildParamsOneOf 产生 **params 表示**（ToJSONSchema 每次新建）。
//
// 保真门禁：jsonschema.Schema 的 Extras 字段标记为 json:"-"，Marshal 时由
// 该包手动合并进 JSON、Unmarshal 时被丢弃——roundtrip 对带 Extras 的 Schema
// 非保真。因此 clone 完成后再次 Marshal 副本并比较 JSON 语义；等价数值编码
// （如 1e3 与 1000）允许规范化，字段丢失或数值精度变化则 fail-closed（明确
// 错误，绝不把被静默改变的 Schema 交给底层 Adapter）。
//
// 边界说明：eino 的 ToolInfo roundtrip 对“ParamsOneOf 存在但 params 与
// jsonschema 均为 nil”的病态构造会恢复为空 params map，语义等价于无参数
// 工具，项目内不存在该构造（InferTool 与 Skill middleware 均产生合法表示）。
func cloneToolInfos(tools []*schema.ToolInfo) ([]*schema.ToolInfo, error) {
	if tools == nil {
		return nil, nil
	}
	cloned := make([]*schema.ToolInfo, len(tools))
	for i, info := range tools {
		if info == nil {
			return nil, errors.New("cannot isolate nil ToolInfo")
		}
		encoded, err := json.Marshal(info)
		if err != nil {
			return nil, fmt.Errorf("isolate ToolInfo %q: %w", info.Name, err)
		}
		var copy schema.ToolInfo
		if err := json.Unmarshal(encoded, &copy); err != nil {
			return nil, fmt.Errorf("isolate ToolInfo %q: %w", info.Name, err)
		}
		// 模型可见 JSON roundtrip 保真门禁：任何字节差异都 fail-closed，
		// 不得把删减后的 Schema 交给底层 Adapter。
		reencoded, err := json.Marshal(&copy)
		if err != nil {
			return nil, fmt.Errorf("isolate ToolInfo %q: %w", info.Name, err)
		}
		faithful, err := modelVisibleJSONEqual(encoded, reencoded)
		if err != nil {
			return nil, fmt.Errorf("isolate ToolInfo %q: compare model-visible JSON: %w", info.Name, err)
		}
		if !faithful {
			return nil, fmt.Errorf(
				"isolate ToolInfo %q: model-visible JSON roundtrip is not semantically faithful (%d bytes -> %d bytes); refusing to hand a changed Schema to the Adapter",
				info.Name, len(encoded), len(reencoded),
			)
		}
		cloned[i] = &copy
	}
	return cloned, nil
}

// modelVisibleJSONEqual compares JSON values rather than their textual
// encoding. The upstream ToolInfo roundtrip can normalize equivalent numbers
// such as 1e3 to 1000; JSON Schema treats those as the same mathematical value.
// UseNumber plus big.Rat still detects actual precision loss (for example a
// large integer rounded through float64), as well as missing keys such as
// jsonschema.Schema.Extras.
func modelVisibleJSONEqual(left, right []byte) (bool, error) {
	decode := func(data []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	leftValue, err := decode(left)
	if err != nil {
		return false, err
	}
	rightValue, err := decode(right)
	if err != nil {
		return false, err
	}
	return modelVisibleJSONValuesEqual(leftValue, rightValue), nil
}

func modelVisibleJSONValuesEqual(left, right any) bool {
	switch typedLeft := left.(type) {
	case nil:
		return right == nil
	case bool:
		typedRight, ok := right.(bool)
		return ok && typedLeft == typedRight
	case string:
		typedRight, ok := right.(string)
		return ok && typedLeft == typedRight
	case json.Number:
		typedRight, ok := right.(json.Number)
		if !ok {
			return false
		}
		var leftNumber, rightNumber big.Rat
		if _, ok := leftNumber.SetString(typedLeft.String()); !ok {
			return false
		}
		if _, ok := rightNumber.SetString(typedRight.String()); !ok {
			return false
		}
		return leftNumber.Cmp(&rightNumber) == 0
	case []any:
		typedRight, ok := right.([]any)
		if !ok || len(typedLeft) != len(typedRight) {
			return false
		}
		for i := range typedLeft {
			if !modelVisibleJSONValuesEqual(typedLeft[i], typedRight[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		typedRight, ok := right.(map[string]any)
		if !ok || len(typedLeft) != len(typedRight) {
			return false
		}
		for key, leftValue := range typedLeft {
			rightValue, exists := typedRight[key]
			if !exists || !modelVisibleJSONValuesEqual(leftValue, rightValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// cloneToolOptions 深拷贝 call-time options 中的全部 ToolInfo（Tools 与
// DeferredTools、ToolSearchTool），重建 option 列表：原 options 原样保留
// （impl-specific options 必须透传），追加克隆后的工具 options 覆盖原值
// （GetCommonOptions 按顺序 apply，追加项最后生效）。没有任何工具 option
// 时原样返回，零开销。
func cloneToolOptions(opts []model.Option) ([]model.Option, error) {
	applied := model.GetCommonOptions(nil, opts...)
	if len(applied.Tools) == 0 && len(applied.DeferredTools) == 0 && applied.ToolSearchTool == nil {
		return opts, nil
	}
	tools, err := cloneToolInfos(applied.Tools)
	if err != nil {
		return nil, err
	}
	deferred, err := cloneToolInfos(applied.DeferredTools)
	if err != nil {
		return nil, err
	}
	var searchTool *schema.ToolInfo
	if applied.ToolSearchTool != nil {
		cloned, err := cloneToolInfos([]*schema.ToolInfo{applied.ToolSearchTool})
		if err != nil {
			return nil, err
		}
		searchTool = cloned[0]
	}
	rebuilt := make([]model.Option, 0, len(opts)+3)
	rebuilt = append(rebuilt, opts...)
	if len(tools) > 0 {
		rebuilt = append(rebuilt, model.WithTools(tools))
	}
	if len(deferred) > 0 {
		rebuilt = append(rebuilt, model.WithDeferredTools(deferred))
	}
	if searchTool != nil {
		rebuilt = append(rebuilt, model.WithToolSearchTool(searchTool))
	}
	return rebuilt, nil
}

// toolSchemaIsolatingModel 是 Factory 返回模型的受保护装饰器：在把
// []*schema.ToolInfo 交给第三方 Adapter（Eino OpenAI ACL）之前的每个入口
// （WithTools 绑定式、Generate/Stream 的 call-time model.WithTools options）
// 都先做深拷贝，防止 openai ACL 的 toTools 原地 sort.Strings 修改调用方
// 持有的共享 ToolInfo/JSONSchema（InferTool.Info() 重复返回同一指针）。
// WithTools 返回的 bound model 继续由同一装饰器保护，第二次绑定与
// call-time options 都无法绕过隔离。底层错误原样返回；底层返回 nil model
// 或克隆失败 fail-closed，绝不产生半成品 model。装饰器同时透传 Eino 的
// Checker/Typer 合同，避免改变 Callback 所有权或组件身份。
type toolSchemaIsolatingModel struct {
	inner model.ToolCallingChatModel
}

func (m *toolSchemaIsolatingModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.inner)
}

func (m *toolSchemaIsolatingModel) GetType() string {
	componentType, _ := components.GetType(m.inner)
	return componentType
}

func (m *toolSchemaIsolatingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cloned, err := cloneToolInfos(tools)
	if err != nil {
		return nil, err
	}
	bound, err := m.inner.WithTools(cloned)
	if err != nil {
		return nil, err
	}
	if bound == nil {
		return nil, errors.New("isolated model: underlying WithTools returned nil model")
	}
	return &toolSchemaIsolatingModel{inner: bound}, nil
}

func (m *toolSchemaIsolatingModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	isolated, err := cloneToolOptions(opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, input, isolated...)
}

func (m *toolSchemaIsolatingModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	isolated, err := cloneToolOptions(opts)
	if err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, input, isolated...)
}
