package security

import (
	"errors"
	"strings"
	"testing"
)

func TestUppercaseEmailAndAccountAreMasked(t *testing.T) {
	input := "Contact IVANOV@EXAMPLE.COM, счёт 40702810900000000001"
	result := MaskPII(input)
	if strings.Contains(result.MaskedText, "IVANOV@EXAMPLE.COM") || strings.Contains(result.MaskedText, "40702810900000000001") || result.MaskedCount != 2 {
		t.Fatalf("sensitive fields unmasked: %q", result.MaskedText)
	}
	if UnmaskPII(result.MaskedText, result.Replacements) != input {
		t.Fatal("masking changed original values")
	}
}

func TestSafeLogErrorRedactsPersonalDataAndCredentials(t *testing.T) {
	got := SafeLogError(errors.New("request for IVANOV@EXAMPLE.COM +7 (999) 123-45-67?key=AIza012345678901234567890 failed"))
	for _, secret := range []string{"IVANOV@EXAMPLE.COM", "+7 (999) 123-45-67", "AIza012345678901234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sensitive value leaked in safe error: %q", got)
		}
	}
	if !strings.Contains(got, "request") || !strings.Contains(got, "failed") {
		t.Fatalf("technical error context was lost: %q", got)
	}
}
