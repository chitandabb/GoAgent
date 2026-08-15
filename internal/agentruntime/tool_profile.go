package agentruntime

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type ToolProfileID string

const (
	ToolProfileConversation ToolProfileID = "conversation-default"
	ToolProfileDiagnosis    ToolProfileID = "diagnosis-default"
	// ToolProfileEvaluationWide 是评测 wide 臂专用的固定宽 Profile：同一部署
	// 配置下 conversation-default ∪ diagnosis-default 的稳定并集。它不是生产
	// 授权接口：生产两个 Runner 只绑定 conversation-default 与
	// diagnosis-default；评测用它与 diagnosis-default 的窄 Schema 配对，
	// 保证 baseline 是 experiment 的严格 Schema 超集（Prompt Token 对照
	// 变量有效）。
	ToolProfileEvaluationWide ToolProfileID = "evaluation-wide-v2"
)

func (id ToolProfileID) Valid() bool {
	return id == ToolProfileConversation || id == ToolProfileDiagnosis || id == ToolProfileEvaluationWide
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ToolProfile is the immutable, deployment-level set of Tool names whose
// schemas may be exposed to a RuntimeKind. Per-run access is enforced later by
// RunAccess and must not mutate this set.
type ToolProfile struct {
	id        ToolProfileID
	toolNames []string
}

func NewToolProfile(id ToolProfileID, toolNames []string) (ToolProfile, error) {
	if !id.Valid() {
		return ToolProfile{}, fmt.Errorf("invalid Tool Profile id %q", id)
	}
	names := append([]string(nil), toolNames...)
	seen := make(map[string]struct{}, len(names))
	for index, name := range names {
		name = strings.TrimSpace(name)
		if !toolNamePattern.MatchString(name) {
			return ToolProfile{}, fmt.Errorf("invalid Tool name %q", name)
		}
		if _, exists := seen[name]; exists {
			return ToolProfile{}, fmt.Errorf("duplicate Tool name %q", name)
		}
		seen[name] = struct{}{}
		names[index] = name
	}
	sort.Strings(names)
	return ToolProfile{id: id, toolNames: names}, nil
}

func (profile ToolProfile) ID() ToolProfileID { return profile.id }

func (profile ToolProfile) ToolNames() []string {
	return append([]string(nil), profile.toolNames...)
}

func (profile ToolProfile) Has(name string) bool {
	name = strings.TrimSpace(name)
	index := sort.SearchStrings(profile.toolNames, name)
	return index < len(profile.toolNames) && profile.toolNames[index] == name
}

type ToolProfiles struct {
	profiles map[ToolProfileID]ToolProfile
}

func NewToolProfiles(profiles ...ToolProfile) (ToolProfiles, error) {
	if len(profiles) == 0 {
		return ToolProfiles{}, errors.New("at least one Tool Profile is required")
	}
	byID := make(map[ToolProfileID]ToolProfile, len(profiles))
	for _, profile := range profiles {
		if !profile.id.Valid() {
			return ToolProfiles{}, fmt.Errorf("invalid Tool Profile id %q", profile.id)
		}
		if _, exists := byID[profile.id]; exists {
			return ToolProfiles{}, fmt.Errorf("duplicate Tool Profile %q", profile.id)
		}
		copyProfile, err := NewToolProfile(profile.id, profile.toolNames)
		if err != nil {
			return ToolProfiles{}, err
		}
		byID[profile.id] = copyProfile
	}
	return ToolProfiles{profiles: byID}, nil
}

func (profiles ToolProfiles) Profile(id ToolProfileID) (ToolProfile, bool) {
	profile, ok := profiles.profiles[id]
	if !ok {
		return ToolProfile{}, false
	}
	copyProfile, err := NewToolProfile(profile.id, profile.toolNames)
	if err != nil {
		return ToolProfile{}, false
	}
	return copyProfile, true
}
