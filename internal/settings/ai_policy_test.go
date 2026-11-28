package settings

import "testing"

func TestAIPolicyValidation(t *testing.T) {
	for _, mode := range []string{"", "manual", "assistant", "partial", "automatic"} {
		if err := (AIPolicy{Mode: mode, DailyLimit: 100, ClarificationLimit: 5}).Validate(); err != nil {
			t.Fatal(mode, err)
		}
	}
	for _, policy := range []AIPolicy{{Mode: "unknown"}, {DailyLimit: -1}, {DailyLimit: 100001}, {ClarificationLimit: 21}, {Sources: []string{""}}} {
		if policy.Validate() == nil {
			t.Fatal("invalid AI policy accepted", policy)
		}
	}
}
