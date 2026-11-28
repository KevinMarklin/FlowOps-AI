package handlers

import "testing"

func TestCustomFieldValidation(t *testing.T) {
	f := TicketFormDefinition{Fields: []CustomField{{ID: "kind", Label: "Kind", Type: "select", Required: true, Options: []string{"equipment", "access"}}, {ID: "items", Label: "Items", Type: "multi", Options: []string{"mouse", "printer"}, ShowWhen: "kind", Equals: "equipment"}, {ID: "urgent", Label: "Urgent", Type: "boolean"}}}
	if err := validForm(f); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		values map[string]any
		ok     bool
	}{
		{map[string]any{"kind": "equipment", "items": []any{"mouse"}, "urgent": false}, true},
		{map[string]any{"kind": "equipment", "items": []any{"mouse", "mouse"}}, false},
		{map[string]any{"kind": "access", "items": []any{"mouse"}}, false},
		{map[string]any{"kind": "equipment", "urgent": "true"}, false},
		{map[string]any{"kind": "invalid"}, false},
		{map[string]any{}, false},
	} {
		if err := validateFormValues(f, tc.values); (err == nil) != tc.ok {
			t.Fatalf("values %#v: %v", tc.values, err)
		}
	}
	f.Fields[0].ShowWhen = "urgent"
	if validForm(f) == nil {
		t.Fatal("forward condition allowed")
	}
}
