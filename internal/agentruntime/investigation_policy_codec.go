package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// investigationPolicyJSON 是 InvestigationPolicy 的严格持久化格式。
// 所有集合字段都由 NewPermissionSet/NewResourceGrants 规范化（排序、去重、
// 拒绝空 UUID），因此 Marshal 输出在同一 Policy 值上是确定性的；Unmarshal
// 通过构造器重建值，未知字段、非法 Permission、重复值和空 UUID 都被拒绝。
type investigationPolicyJSON struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Permissions   []string               `json:"permissions"`
	Grants        resourceGrantsJSONView `json:"grants"`
}

type resourceGrantsJSONView struct {
	DataSourceIDs   []string `json:"dataSourceIds,omitempty"`
	ExternalCaseIDs []string `json:"externalCaseIds,omitempty"`
	AttachmentIDs   []string `json:"attachmentIds,omitempty"`
	TaskIDs         []string `json:"taskIds,omitempty"`
	Repositories    []string `json:"repositories,omitempty"`
}

// MarshalInvestigationPolicy 先把 Policy 通过构造器重新规范化再编码，
// 保证相同语义的 Policy 序列化结果逐字节一致。
func MarshalInvestigationPolicy(policy InvestigationPolicy) ([]byte, error) {
	normalized, err := NewInvestigationPolicy(policy.SchemaVersion(), policy.Permissions(), policy.Grants())
	if err != nil {
		return nil, fmt.Errorf("marshal investigation policy: %w", err)
	}
	view := investigationPolicyJSON{
		SchemaVersion: normalized.SchemaVersion(),
		Permissions:   permissionStrings(normalized.Permissions()),
		Grants: resourceGrantsJSONView{
			DataSourceIDs:   uuidStrings(normalized.Grants().DataSourceIDs()),
			ExternalCaseIDs: uuidStrings(normalized.Grants().ExternalCaseIDs()),
			AttachmentIDs:   uuidStrings(normalized.Grants().AttachmentIDs()),
			TaskIDs:         uuidStrings(normalized.Grants().TaskIDs()),
			Repositories:    normalized.Grants().Repositories(),
		},
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("marshal investigation policy: %w", err)
	}
	return encoded, nil
}

// UnmarshalInvestigationPolicy 严格解码持久化 Policy：拒绝未知字段、
// 尾随 JSON 值、非 object 载荷、非法/重复 Permission、空 UUID 与非法版本。
// 解码成功后返回经 NewInvestigationPolicy 再次校验的不可变值。
func UnmarshalInvestigationPolicy(raw []byte) (InvestigationPolicy, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return InvestigationPolicy{}, errors.New("investigation policy JSON is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var view investigationPolicyJSON
	if err := decoder.Decode(&view); err != nil {
		return InvestigationPolicy{}, fmt.Errorf("decode investigation policy: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return InvestigationPolicy{}, errors.New("investigation policy contains multiple JSON values")
		}
		return InvestigationPolicy{}, fmt.Errorf("decode investigation policy: %w", err)
	}
	permissions := make([]Permission, 0, len(view.Permissions))
	for _, rawPermission := range view.Permissions {
		permissions = append(permissions, Permission(rawPermission))
	}
	permissionSet, err := NewPermissionSet(permissions...)
	if err != nil {
		return InvestigationPolicy{}, fmt.Errorf("decode investigation policy: %w", err)
	}
	grants, err := NewResourceGrants(ResourceGrantsConfig{
		DataSourceIDs:   parseUUIDs(view.Grants.DataSourceIDs),
		ExternalCaseIDs: parseUUIDs(view.Grants.ExternalCaseIDs),
		AttachmentIDs:   parseUUIDs(view.Grants.AttachmentIDs),
		TaskIDs:         parseUUIDs(view.Grants.TaskIDs),
		Repositories:    view.Grants.Repositories,
	})
	if err != nil {
		return InvestigationPolicy{}, fmt.Errorf("decode investigation policy: %w", err)
	}
	policy, err := NewInvestigationPolicy(view.SchemaVersion, permissionSet, grants)
	if err != nil {
		return InvestigationPolicy{}, fmt.Errorf("decode investigation policy: %w", err)
	}
	return policy, nil
}

func permissionStrings(set PermissionSet) []string {
	values := set.Values()
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func uuidStrings(values []uuid.UUID) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.String()
	}
	return result
}

func parseUUIDs(values []string) []uuid.UUID {
	result := make([]uuid.UUID, len(values))
	for index, raw := range values {
		// 非法 UUID 归一为 uuid.Nil，交给 NewResourceGrants 的 normalizeUUIDs
		// 拒绝，避免在本层丢失稳定的校验错误。
		parsed, err := uuid.Parse(raw)
		if err != nil {
			parsed = uuid.Nil
		}
		result[index] = parsed
	}
	return result
}
