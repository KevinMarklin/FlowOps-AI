package security

import (
	"strings"
	"testing"
)

func TestMaskPIIAndRestoreAllSupportedTypes(t *testing.T) {
	input := "Сотрудник Иванов Иван Иванович, email ivanov@example.ru, телефон +7 (999) 123-45-67, карта 4111 1111 1111 1111, СНИЛС 123-456-789 01, паспорт 4509 123456."
	masked := MaskPII(input)
	for _, placeholder := range []string{"[PERSON_1]", "[EMAIL_1]", "[PHONE_1]", "[CARD_1]", "[SNILS_1]", "[PASSPORT_1]"} {
		if !strings.Contains(masked.MaskedText, placeholder) {
			t.Errorf("masked text does not contain %s: %q", placeholder, masked.MaskedText)
		}
	}
	if masked.MaskedCount != 6 {
		t.Fatalf("expected 6 masked values, got %d: %q", masked.MaskedCount, masked.MaskedText)
	}
	if len(masked.MaskedTypes) != 6 {
		t.Fatalf("expected 6 masked types, got %#v", masked.MaskedTypes)
	}
	for _, value := range []string{"Иванов Иван Иванович", "ivanov@example.ru", "+7 (999) 123-45-67", "4111 1111 1111 1111", "123-456-789 01", "4509 123456"} {
		if strings.Contains(masked.MaskedText, value) {
			t.Errorf("PII value was not masked: %q in %q", value, masked.MaskedText)
		}
	}
	if got := UnmaskPII(masked.MaskedText, masked.Replacements); got != input {
		t.Fatalf("unmask did not restore the original text\nwant: %q\n got: %q", input, got)
	}
}

func TestMaskPIICommonRussianPhoneFormatsAndNumbering(t *testing.T) {
	input := "Звоните 8 (999) 123-45-67 или 89991234568, резерв +7-999-123-45-69."
	result := MaskPII(input)
	if result.MaskedCount != 3 {
		t.Fatalf("expected 3 phone numbers, got %d: %s", result.MaskedCount, result.MaskedText)
	}
	for _, placeholder := range []string{"[PHONE_1]", "[PHONE_2]", "[PHONE_3]"} {
		if !strings.Contains(result.MaskedText, placeholder) {
			t.Errorf("missing %s from %q", placeholder, result.MaskedText)
		}
	}
	if got := UnmaskPII(result.MaskedText, result.Replacements); got != input {
		t.Fatalf("unmask mismatch: got %q, want %q", got, input)
	}
}

func TestMaskPIISNILSWithoutSeparatorsWhenLabeled(t *testing.T) {
	input := "СНИЛС: 11223344595"
	result := MaskPII(input)
	if result.MaskedCount != 1 || !strings.Contains(result.MaskedText, "[SNILS_1]") {
		t.Fatalf("unlabeled numeric SNILS was not masked: %#v", result)
	}
	if got := UnmaskPII(result.MaskedText, result.Replacements); got != input {
		t.Fatalf("unmask mismatch: %q", got)
	}
}

func TestMaskPIIPassportWithoutSeparatorsWhenLabeled(t *testing.T) {
	input := "Данные паспорта: 4509123456"
	result := MaskPII(input)
	if result.MaskedCount != 1 || !strings.Contains(result.MaskedText, "[PASSPORT_1]") {
		t.Fatalf("passport without separator was not masked: %#v", result)
	}
	if got := UnmaskPII(result.MaskedText, result.Replacements); got != input {
		t.Fatalf("unmask mismatch: %q", got)
	}
}

func TestMaskPIINoPersonalData(t *testing.T) {
	input := "После обновления не подключается корпоративный VPN, ошибка 809."
	result := MaskPII(input)
	if result.MaskedText != input || result.MaskedCount != 0 || len(result.Replacements) != 0 || len(result.MaskedTypes) != 0 {
		t.Fatalf("non-PII text should remain unchanged: %#v", result)
	}
}

func TestMaskPIIMultipleEmailsHaveIndependentIndexes(t *testing.T) {
	result := MaskPII("Пишите на first@example.ru и second@example.ru")
	if result.MaskedCount != 2 || !strings.Contains(result.MaskedText, "[EMAIL_1]") || !strings.Contains(result.MaskedText, "[EMAIL_2]") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := UnmaskPII(result.MaskedText, result.Replacements); got != "Пишите на first@example.ru и second@example.ru" {
		t.Fatalf("unmask mismatch: %q", got)
	}
}
