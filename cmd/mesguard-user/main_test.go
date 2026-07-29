package main

import "testing"

func TestParseOptions(t *testing.T) {
	options, err := parseOptions([]string{
		"-username", "admin01",
		"-display-name", "系统管理员",
		"-role", "admin",
	})
	if err != nil {
		t.Fatalf("parseOptions(): %v", err)
	}
	if options.username != "admin01" || options.displayName != "系统管理员" || options.role != "admin" {
		t.Fatalf("options = %+v", options)
	}
	if options.passwordEnv != defaultPasswordEnv {
		t.Fatalf("passwordEnv = %q, want %q", options.passwordEnv, defaultPasswordEnv)
	}
	if !options.mustChangePassword {
		t.Fatal("mustChangePassword = false, want true by default")
	}
}

func TestParseOptionsRejectsMissingRequiredFlags(t *testing.T) {
	if _, err := parseOptions([]string{"-username", "admin01"}); err == nil {
		t.Fatal("parseOptions() error = nil, want usage error")
	}
}
