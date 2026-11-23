package security

import (
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
