// Evaluate reproducible, labelled synthetic requests without changing service data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"flowops-ai/internal/security"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
)

type sample struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Department string `json:"department"`
	Priority   string `json:"priority"`
	Source     string `json:"source"`
}
type dataset struct {
	Kind  string   `json:"kind"`
	Cases []sample `json:"cases"`
}

func main() {
	path := flag.String("dataset", "testdata/pilot/cases.json", "labelled synthetic dataset")
	kb := flag.String("kb", "testdata/pilot/knowledge_base", "isolated knowledge fixture")
	out := flag.String("out", "", "optional JSON report file")
	external := flag.Bool("allow-external", false, "explicitly allow the configured provider for synthetic inputs")
	flag.Parse()
	if err := run(*path, *kb, *out, *external); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, kb, out string, external bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var data dataset
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&data); err != nil {
		return err
	}
	if data.Kind != "synthetic" || len(data.Cases) == 0 || len(data.Cases) > 500 {
		return fmt.Errorf("requires 1..500 explicitly synthetic samples")
	}
	if !external {
		_ = os.Setenv("FLOWOPS_AI_PROVIDERS", "")
	} else if os.Getenv("FLOWOPS_AI_PROVIDERS") == "" {
		return fmt.Errorf("configure approved providers before external evaluation")
	}
	base, err := knowledge.Load(kb)
	if err != nil {
		return err
	}
	a := analyzer.New(base)
	rows := []map[string]any{}
	dept, priority, source := 0, 0, 0
	for _, c := range data.Cases {
		if c.ID == "" || c.Text == "" || c.Department == "" || c.Priority == "" {
			return fmt.Errorf("sample labels must be nonempty")
		}
		started := time.Now()
		r := a.Analyze(context.Background(), c.Text)
		dc, pc := r.Department == c.Department, r.Priority == c.Priority
		sc := (c.Source == "" && r.RAGAnswer == nil) || (c.Source != "" && r.RAGAnswer != nil && strings.Contains(*r.RAGAnswer, c.Source))
		if dc {
			dept++
		}
		if pc {
			priority++
		}
		if sc {
			source++
		}
		mode, provider, model := a.ActiveProvider()
		if security.CheckPromptInput(c.Text, security.MaxTicketBytes) != "" {
			mode, provider, model = "security_review", "input_policy", ""
		}
		rows = append(rows, map[string]any{"id": c.ID, "actual_department": r.Department, "actual_priority": r.Priority, "department_correct": dc, "priority_correct": pc, "source_correct": sc, "mode": mode, "provider": provider, "model": model, "elapsed_ms": time.Since(started).Milliseconds()})
	}
	n := float64(len(rows))
	report := map[string]any{"dataset_kind": "synthetic", "production_accuracy_proven": false, "external_allowed": external, "samples": len(rows), "department_accuracy_percent": float64(dept) * 100 / n, "priority_accuracy_percent": float64(priority) * 100 / n, "source_accuracy_percent": float64(source) * 100 / n, "results": rows}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if out != "" {
		if err = os.WriteFile(out, append(encoded, '\n'), 0600); err != nil {
			return err
		}
	}
	fmt.Println(string(encoded))
	return nil
}
