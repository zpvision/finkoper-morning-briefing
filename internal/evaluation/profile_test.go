package evaluation

import (
	"os"
	"path/filepath"
	"testing"

	"finkoper-morning-briefing/internal/briefing"
)

func Test14BProfileIsExplicitPinnedAndIsolated(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "")
	dir := t.TempDir()
	if os.WriteFile(filepath.Join(dir, "manifest.json"), profile14Manifest, 0600) != nil {
		t.Fatal("write")
	}
	counter := &briefing.PythonCounter{Model: "qwen3:8b", Resources: "original-8b"}
	if err := ConfigureProfile(counter, Profile8B, "", "think-false", "development"); err != nil || counter.Resources != "original-8b" || counter.DiagnosticProfile != "" {
		t.Fatal("8B configuration changed")
	}
	if err := ConfigureProfile(counter, Profile14B, dir, "think-false", "development"); err != nil || counter.Model != Model14B || counter.Resources != dir || counter.DiagnosticProfile != Profile14B {
		t.Fatal("14B configuration", err)
	}
	for _, pair := range [][2]string{{"think-true", "development"}, {"think-false", "holdout"}} {
		if ConfigureProfile(counter, Profile14B, dir, pair[0], pair[1]) == nil {
			t.Fatal("unvalidated mode accepted")
		}
	}
	if ConfigureProfile(counter, "anything", dir, "think-false", "development") == nil {
		t.Fatal("unknown profile")
	}
	if ConfigureFileProfile(counter, Profile14B, dir) != nil {
		t.Fatal("standalone pin rejected")
	}
	if ConfigureFileProfile(counter, Profile8B, dir) == nil {
		t.Fatal("standalone profile must be explicit 14B")
	}
	t.Setenv("OLLAMA_MODEL", "qwen3:8b")
	if ConfigureProfile(counter, Profile14B, dir, "think-false", "development") == nil {
		t.Fatal("environment conflict")
	}
	t.Setenv("OLLAMA_MODEL", "")
	os.WriteFile(filepath.Join(dir, "manifest.json"), append(append([]byte{}, profile14Manifest...), ' '), 0600)
	if ConfigureProfile(counter, Profile14B, dir, "think-false", "development") == nil {
		t.Fatal("modified manifest accepted")
	}
}
