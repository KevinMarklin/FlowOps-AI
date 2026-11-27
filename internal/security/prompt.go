package security

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const MaxTicketBytes = 16 << 10
const MaxDocumentBytes = 32 << 10

// These signatures are a conservative tripwire, not proof that text is safe.
// Security must also hold when an injection evades every signature below.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(ignore|disregard|forget|override|bypass).{0,100}(instruction|prompt|rules|policy|previous|above|system|developer|safety)`),
	regexp.MustCompile(`(?i)(игнорир|забудь|забудьте|отмени|обойди|обойти|не выполняй|не следуй).{0,100}(инструкц|правил|промпт|указани|системн|ограничени|предыдущ)`),
	regexp.MustCompile(`(?i)(reveal|print|show|repeat|leak|выведи|покажи|раскрой|распечатай).{0,80}(system.?prompt|developer.?message|api.?key|secret|системн.{0,20}(промпт|инструкц)|ключ.{0,20}api)`),
	regexp.MustCompile(`(?i)(you are now|act as|pretend to be|теперь ты|твоя новая роль|действуй как).{0,80}(admin|system|developer|unrestricted|админ|систем|разработчик|без ограничени)`),
	regexp.MustCompile(`(?i)(<\|[^>]{0,80}\|>|\[/?INST\]|<<\s*/?SYS\s*>>|</?(system|developer|assistant|tool)(\s|>)|(^|\n)\s*(system|developer|assistant)\s*:)`),
	regexp.MustCompile(`(?i)(set|return|output|установи|присвой|верни|выстави).{0,50}(department|priority|rag_answer|proposed_solution|matched_article|auto_resolved|приоритет.{0,20}(high|высок)|отдел.{0,20}(billing|hr|it))`),
	regexp.MustCompile(`(?i)(do not|don't|не).{0,30}(tell|mention|disclose|сообщай|говори|упоминай).{0,50}(user|operator|пользовател|оператор)`),
	regexp.MustCompile(`(?i)(jailbreak|developer mode|режим разработчика|DAN mode)`),
}

var encodedToken = regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`)
var credentialPattern = regexp.MustCompile(`(?i)(\bsk-[a-z0-9_-]{16,}|\bAIza[a-z0-9_-]{20,}|\bBearer\s+[a-z0-9._~+/-]{12,}|(password|api[_ -]?key|secret|пароль|токен)\s*[:=]\s*\S{4,})`)
var externalLink = regexp.MustCompile(`(?i)(https?://|ftp://|mailto:|file:|javascript:|data:|www\.|\[[^\]\n]*\]\s*\(|!\[|<\s*/?\s*[a-z][^>]*>|\\\\[a-z0-9])`)
var logEmail = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@([a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+)`)
var logPhone = regexp.MustCompile(`(?:\+?\d[\d ()-]{8,}\d)`)
var logSecretQuery = regexp.MustCompile(`(?i)([?&](?:key|api_key|token|access_token)=)[^&\s"']+`)

var logTelegramCredential = regexp.MustCompile(`(?i)(https?://api[.]telegram[.]org/bot)[^/\s"']+`)

// SafeLogError preserves technical diagnostics while removing common personal
// identifiers and credentials that may be echoed by a remote service.
func SafeLogError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := logTelegramCredential.ReplaceAllString(err.Error(), "${1}[REDACTED]")
	message = credentialPattern.ReplaceAllString(message, "[REDACTED]")
	message = logSecretQuery.ReplaceAllString(message, "$1[REDACTED]")
	message = logEmail.ReplaceAllString(message, "***@$1")
	message = logPhone.ReplaceAllString(message, "[REDACTED]")
	return message
}

// CheckPromptInput never rewrites business text: a suspect input goes to review.
// Reason codes are deliberately content-free and suitable for security logs.
func CheckPromptInput(text string, limit int) string {
	if len(text) > limit {
		return "input_too_large"
	}
	if !utf8.ValidString(text) {
		return "invalid_utf8"
	}
	for _, r := range text {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return "hidden_control_character"
		}
	}
	normalized := norm.NFKC.String(text)
	variants := []string{normalized, html.UnescapeString(normalized)}
	if decoded, err := url.QueryUnescape(variants[1]); err == nil {
		variants = append(variants, decoded)
	}
	for _, token := range encodedToken.FindAllString(normalized, 16) {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if decoded, err := encoding.DecodeString(token); err == nil && utf8.Valid(decoded) {
				variants = append(variants, string(decoded))
				break
			}
		}
	}
	for _, variant := range variants {
		if credentialPattern.MatchString(variant) {
			return "possible_credential"
		}
		variant = strings.Join(strings.Fields(strings.ToLower(variant)), " ")
		for _, pattern := range injectionPatterns {
			if pattern.MatchString(variant) {
				return "instruction_override"
			}
		}
	}
	// Check role labels before whitespace folding, which discards line boundaries.
	for _, pattern := range injectionPatterns {
		if pattern.MatchString(normalized) {
			return "instruction_override"
		}
	}
	return ""
}

// SafeAnswerText intentionally forbids all external links and active markup in
// automatic answers. Links can still be supplied by a human after review.
func SafeAnswerText(text string) bool {
	return strings.TrimSpace(text) != "" && CheckPromptInput(text, MaxDocumentBytes) == "" && !externalLink.MatchString(html.UnescapeString(norm.NFKC.String(text)))
}

func ValidateKnowledge(title string, keywords []string, content string) error {
	if len(title) == 0 || len(title) > 240 || len(keywords) > 32 || len(content) == 0 || len(content) > MaxDocumentBytes {
		return fmt.Errorf("knowledge document exceeds security limits")
	}
	if strings.ContainsAny(title, "\r\n") {
		return fmt.Errorf("knowledge title must be one line")
	}
	for _, keyword := range keywords {
		if len(keyword) > 100 || strings.ContainsAny(keyword, "\r\n") {
			return fmt.Errorf("invalid knowledge keyword")
		}
	}
	combined := title + "\n" + strings.Join(keywords, " ") + "\n" + content
	if reason := CheckPromptInput(combined, MaxDocumentBytes+4096); reason != "" {
		return fmt.Errorf("knowledge document requires security review: %s", reason)
	}
	if !SafeAnswerText(content) || !SafeAnswerText(title) || externalLink.MatchString(strings.Join(keywords, " ")) {
		return fmt.Errorf("external links or active markup require manual review")
	}
	return nil
}
