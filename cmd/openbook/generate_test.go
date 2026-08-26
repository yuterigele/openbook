package main

import (
	"go/format"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGenerateCreatesTemplatesAndNeverOverwrites(t *testing.T) {
	t.Chdir(t.TempDir())

	if code := RunGenerate(io.Discard, []string{"profile", "spa_booking"}); code != 0 {
		t.Fatalf("profile generation exit code = %d", code)
	}
	profilePath := filepath.Join("profiles", "spa_booking", "profile.go")
	profileBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(profileBytes), `ID:          "spa_booking"`) {
		t.Fatalf("generated profile does not contain stable id: %s", profileBytes)
	}

	if code := RunGenerate(io.Discard, []string{"profile", "spa_booking"}); code != 1 {
		t.Fatalf("duplicate profile generation exit code = %d, want 1", code)
	}
	profileAfter, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(profileAfter) != string(profileBytes) {
		t.Fatal("duplicate generation changed an existing file")
	}
}

func TestRunGenerateSupportsToolAndChannelTemplates(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, kind := range []string{"tool", "channel"} {
		if code := RunGenerate(io.Discard, []string{kind, "demo_extension"}); code != 0 {
			t.Fatalf("%s generation exit code = %d", kind, code)
		}
	}
	if _, err := os.Stat(filepath.Join("tools", "demo_extension.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("sdk", "channel", "demo_extension.go")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join("tools", "demo_extension.go"),
		filepath.Join("tools", "demo_extension_test.go"),
		filepath.Join("sdk", "channel", "demo_extension.go"),
		filepath.Join("sdk", "channel", "demo_extension_test.go"),
	} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := format.Source(source); err != nil {
			t.Fatalf("generated file %s is not valid Go: %v", path, err)
		}
	}
}

func TestRunGenerateRejectsUnsafeOrUnknownInput(t *testing.T) {
	for _, args := range [][]string{
		{"profile", "../escape"},
		{"unknown", "valid_name"},
		{"profile"},
	} {
		if code := RunGenerate(io.Discard, args); code != 2 {
			t.Fatalf("RunGenerate(%v) = %d, want 2", args, code)
		}
	}
}
