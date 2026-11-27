package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluationRejectsUnlabelledOrRealData(t *testing.T) {
	for _, value := range []string{`{"kind":"real","cases":[{"id":"a","text":"x"}]}`, `{"kind":"synthetic","cases":[]}`} {
		file := filepath.Join(t.TempDir(), "cases.json")
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := run(file, t.TempDir(), "", true); err == nil {
			t.Fatal("unsafe or empty evaluation accepted")
		}
	}
}
