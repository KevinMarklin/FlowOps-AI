package security

import (
	"regexp"
	"strings"
)

type MaskResult struct {
	MaskedText   string            `json:"masked_text"`
	Replacements map[string]string `json:"-"`
	MaskedCount  int               `json:"pii_masked_count"`
	MaskedTypes  []string          `json:"pii_masked_types"`
}

type piiPattern struct {
	tokenType string
	display   string
	pattern   *regexp.Regexp
	valueSlot int
}

var piiPatterns = []piiPattern{
	{tokenType: "EMAIL", display: "Email", pattern: regexp.MustCompile(`(^|[^\w.+%\-])([a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~\-]+@[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?)+)`), valueSlot: 2},
	{tokenType: "SNILS", display: "СНИЛС", pattern: regexp.MustCompile(`(^|[^\d])(\d{3}-\d{3}-\d{3}\s?\d{2})([^\d]|$)`), valueSlot: 2},
	{tokenType: "SNILS", display: "СНИЛС", pattern: regexp.MustCompile(`(?i)(снилс[^\d]{0,12})(\d{11})([^\d]|$)`), valueSlot: 2},
	{tokenType: "CARD", display: "Банковская карта", pattern: regexp.MustCompile(`(^|[^\d])((?:\d[ -]?){15}\d)([^\d]|$)`), valueSlot: 2},
	{tokenType: "PASSPORT", display: "Паспорт", pattern: regexp.MustCompile(`(паспорт[^\d]{0,12})(\d{4}[ -]?\d{6})([^\d]|$)`), valueSlot: 2},
	{tokenType: "PASSPORT", display: "Паспорт", pattern: regexp.MustCompile(`(^|[^\d])((?:\d{4}[ -]\d{6}))([^\d]|$)`), valueSlot: 2},
	{tokenType: "PHONE", display: "Телефон", pattern: regexp.MustCompile(`(^|[^\d])((?:\+7|7|8)[\s()\-]*\d(?:[\s()\-]*\d){9})([^\d]|$)`), valueSlot: 2},
	{tokenType: "PERSON", display: "ФИО", pattern: regexp.MustCompile(`(^|[^\p{L}])([А-ЯЁ][а-яё]+(?:ов|ова|ев|ева|ин|ина|ын|ына|ский|ская)\s+[А-ЯЁ][а-яё]+\s+[А-ЯЁ][а-яё]*(?:ович|евич|ич|овна|евна|ична))([^а-яё]|$)`), valueSlot: 2},
	{tokenType: "PERSON", display: "ФИО", pattern: regexp.MustCompile(`(^|[^\p{L}])([А-ЯЁ][а-яё]+\s+[А-ЯЁ][а-яё]+\s+[А-ЯЁ][а-яё]*(?:ович|евич|ич|овна|евна|ична))([^а-яё]|$)`), valueSlot: 2},
}

func MaskPII(text string) MaskResult {
	result := MaskResult{MaskedText: text, Replacements: make(map[string]string), MaskedTypes: make([]string, 0)}
	counts := make(map[string]int)
	seenTypes := make(map[string]bool)
	for _, item := range piiPatterns {
		result.MaskedText = item.pattern.ReplaceAllStringFunc(result.MaskedText, func(match string) string {
			indices := item.pattern.FindStringSubmatchIndex(match)
			if len(indices) < (item.valueSlot+1)*2 {
				return match
			}
			start, end := indices[item.valueSlot*2], indices[item.valueSlot*2+1]
			if start < 0 || end < start {
				return match
			}
			value := match[start:end]
			counts[item.tokenType]++
			placeholder := "[" + item.tokenType + "_" + itoa(counts[item.tokenType]) + "]"
			result.Replacements[placeholder] = value
			result.MaskedCount++
			if !seenTypes[item.tokenType] {
				result.MaskedTypes = append(result.MaskedTypes, item.display)
				seenTypes[item.tokenType] = true
			}
			return match[:start] + placeholder + match[end:]
		})
	}
	return result
}

func UnmaskPII(text string, replacements map[string]string) string {
	for placeholder, value := range replacements {
		text = strings.ReplaceAll(text, placeholder, value)
	}
	return text
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	i := len(buffer)
	for value > 0 {
		i--
		buffer[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[i:])
}
