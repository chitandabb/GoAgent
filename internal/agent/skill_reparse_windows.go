//go:build windows

package agent

import "golang.org/x/sys/windows"

func isSkillReparsePoint(filePath string) (bool, error) {
	utf16Path, err := windows.UTF16PtrFromString(filePath)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(utf16Path)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
