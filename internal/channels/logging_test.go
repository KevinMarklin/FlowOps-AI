package channels

import "testing"

func TestSafeSenderMasksPersonalIdentifiers(t *testing.T) {
	tests := []struct {
		channel string
		sender  string
		want    string
	}{
		{"bot", "tg:123456789", "tg:123456789"},
		{"bot", "max_chat:987654321", "max_chat:987654321"},
		{"bot", "tg:123456789?name=Ivan", "unknown"},
		{"email", "Ivanov.Name@mail.ru", "***@mail.ru"},
		{"email", "not-an-email", "***"},
	}
	for _, test := range tests {
		if got := SafeSender(test.channel, test.sender); got != test.want {
			t.Errorf("SafeSender(%q, %q) = %q, want %q", test.channel, test.sender, got, test.want)
		}
	}
}
