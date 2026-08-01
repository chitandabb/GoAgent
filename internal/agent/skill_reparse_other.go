//go:build !windows

package agent

func isSkillReparsePoint(string) (bool, error) {
	return false, nil
}
