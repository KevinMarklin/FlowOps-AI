package channels

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var (
	knowledgeSourceLine = regexp.MustCompile(`(?m)^\s*\[Источник:.*\]\s*$`)
	htmlTag             = regexp.MustCompile(`<[^>]*>`)
	markdownHeading     = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)
	markdownListItem    = regexp.MustCompile(`^\s*[-*+]\s+(.+)$`)
	markdownStrong      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	markdownCode        = regexp.MustCompile("`([^`]+)`")
)

// PublicReplyText removes operator/audit citations and builds the common reply
// text returned by the ingest API. The original RAGAnswer remains on the ticket.
func PublicReplyText(ticketID int, answer string) string {
	answer = stripKnowledgeSource(answer)
	return "**💡 Решение по заявке #" + strconv.Itoa(ticketID) + "**\n\n" + strings.TrimSpace(answer) + "\n\nЕсли решение не помогло, просто напишите сюда ещё раз — мы подключим специалиста."
}

// EscalationReplyText formats the public acknowledgement for an operator handoff.
func EscalationReplyText(ticketID int, department string) string {
	text := "✅ Обращение принято в работу (Заявка #" + strconv.Itoa(ticketID) + ")."
	if department != "" && !strings.EqualFold(department, "Unassigned") && !strings.EqualFold(department, "General") {
		text += "\nНаправлено в профильный отдел: " + department + "."
	}
	return text + "\nОтвет специалиста придёт прямо сюда автоматически."
}

// OperatorReplyText adds a consistent header before an operator's response.
func OperatorReplyText(ticketID int, reply string) string {
	return "**👨‍💻 Ответ специалиста по заявке #" + strconv.Itoa(ticketID) + ":**\n\n" + stripKnowledgeSource(reply)
}

func stripKnowledgeSource(text string) string {
	text = knowledgeSourceLine.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// FormatTelegramMarkdown converts the limited Markdown used in knowledge-base
// replies into Telegram's HTML parse mode. Text is escaped before tags are added.
func FormatTelegramMarkdown(text string) string {
	lines := strings.Split(stripKnowledgeSource(text), "\n")
	var out strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if heading := markdownHeading.FindStringSubmatch(trimmed); heading != nil {
			out.WriteString("<b>")
			out.WriteString(telegramInline(heading[1]))
			out.WriteString("</b>\n")
			continue
		}
		if item := markdownListItem.FindStringSubmatch(line); item != nil {
			out.WriteString("• ")
			out.WriteString(telegramInline(item[1]))
		} else {
			out.WriteString(telegramInline(line))
		}
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// FormatTelegramHTML is the channel formatter alias used by the sandbox UI/API.
func FormatTelegramHTML(text string) string { return FormatTelegramMarkdown(text) }

// FormatMAX formats a reply using MAX-compatible Markdown.
func FormatMAX(text string) string { return FormatMAXMarkdown(text) }

func telegramInline(text string) string {
	var out strings.Builder
	for len(text) > 0 {
		strongAt, codeAt := strings.Index(text, "**"), strings.IndexByte(text, '`')
		if strongAt < 0 && codeAt < 0 {
			out.WriteString(html.EscapeString(text))
			break
		}
		if codeAt >= 0 && (strongAt < 0 || codeAt < strongAt) {
			out.WriteString(html.EscapeString(text[:codeAt]))
			end := strings.IndexByte(text[codeAt+1:], '`')
			if end < 0 {
				out.WriteString(html.EscapeString(text[codeAt:]))
				break
			}
			out.WriteString("<code>")
			out.WriteString(html.EscapeString(text[codeAt+1 : codeAt+1+end]))
			out.WriteString("</code>")
			text = text[codeAt+2+end:]
			continue
		}
		out.WriteString(html.EscapeString(text[:strongAt]))
		end := strings.Index(text[strongAt+2:], "**")
		if end < 0 {
			out.WriteString(html.EscapeString(text[strongAt:]))
			break
		}
		out.WriteString("<b>")
		out.WriteString(telegramInline(text[strongAt+2 : strongAt+2+end]))
		out.WriteString("</b>")
		text = text[strongAt+4+end:]
	}
	return out.String()
}

// FormatMAXMarkdown keeps MAX's supported bold Markdown while removing raw
// heading markers, source citations and code-fence decoration.
func FormatMAXMarkdown(text string) string {
	text = html.UnescapeString(htmlTag.ReplaceAllString(stripKnowledgeSource(text), ""))
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			lines[i] = ""
			continue
		}
		if heading := markdownHeading.FindStringSubmatch(trimmed); heading != nil {
			lines[i] = "**" + strings.TrimSpace(heading[1]) + "**"
			continue
		}
		if item := markdownListItem.FindStringSubmatch(line); item != nil {
			lines[i] = "• " + strings.TrimSpace(item[1])
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// FormatEmailHTML builds a self-contained support email. originalMessage may be
// HTML-escaped by the caller; it is normalized and escaped again here so the
// renderer remains safe for direct callers too.
func FormatEmailHTML(ticketID int, originalMessage, replyBody string, isAutoReply bool) string {
	originalMessage = html.EscapeString(html.UnescapeString(originalMessage))
	originalMessage = strings.ReplaceAll(originalMessage, "\r\n", "\n")
	originalMessage = strings.ReplaceAll(originalMessage, "\r", "\n")
	originalHTML := strings.ReplaceAll(originalMessage, "\n", "<br>")

	lines := strings.Split(stripKnowledgeSource(replyBody), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && (strings.Contains(lines[0], "Решение по заявке #") || strings.Contains(lines[0], "Ответ специалиста по заявке #")) {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "Если решение не помогло, просто напишите сюда ещё раз") {
		lines = lines[:len(lines)-1]
	}
	var content strings.Builder
	listOpen := false
	closeList := func() {
		if listOpen {
			content.WriteString("</ul>")
			listOpen = false
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		if trimmed == "" {
			closeList()
			continue
		}
		if heading := markdownHeading.FindStringSubmatch(trimmed); heading != nil {
			closeList()
			content.WriteString(`<h3 style="margin:20px 0 10px;font-size:18px;line-height:1.35;color:#0f172a">`)
			content.WriteString(emailInline(heading[1]))
			content.WriteString("</h3>")
			continue
		}
		if item := markdownListItem.FindStringSubmatch(line); item != nil {
			if !listOpen {
				content.WriteString(`<ul style="margin:8px 0 16px;padding-left:24px">`)
				listOpen = true
			}
			content.WriteString(`<li style="margin:0 0 6px">`)
			content.WriteString(emailInline(item[1]))
			content.WriteString("</li>")
			continue
		}
		closeList()
		content.WriteString(`<p style="margin:0 0 12px;line-height:1.6;color:#0f172a">`)
		content.WriteString(emailInline(trimmed))
		content.WriteString("</p>")
	}
	closeList()

	answerHeading := "👨‍💻 ОТВЕТ СПЕЦИАЛИСТА"
	if isAutoReply {
		answerHeading = "💡 АВТОМАТИЧЕСКОЕ РЕШЕНИЕ"
	}
	return `<!doctype html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body style="margin:0;padding:24px 12px;background:#f4f6f9;font-family:Arial,Helvetica,sans-serif;color:#0f172a;line-height:1.6">
	<div style="width:100%;max-width:600px;margin:0 auto;background:#ffffff;border:1px solid #e2e8f0;border-radius:12px;overflow:hidden">
	<div style="padding:22px 24px;border-bottom:1px solid #e2e8f0"><table role="presentation" style="width:100%;border-collapse:collapse"><tr><td style="font-size:17px;font-weight:700;color:#0f172a">FlowOps AI • Служба поддержки</td><td style="text-align:right;white-space:nowrap;font-size:13px;color:#64748b">Заявка #` + strconv.Itoa(ticketID) + `</td></tr></table></div>
	<div style="padding:24px">
	<div style="margin:0 0 10px;font-size:11px;letter-spacing:0.05em;color:#64748b;font-weight:600">ВАШЕ ОБРАЩЕНИЕ</div>
	<div style="background:#f8fafc;border-left:3px solid #94a3b8;padding:12px 16px;border-radius:6px;color:#475569;font-style:italic;line-height:1.6">` + originalHTML + `</div>
	<hr style="height:0;border:0;border-top:1px solid #e2e8f0;margin:24px 0">
	<div style="margin:0 0 12px;font-size:12px;letter-spacing:0.04em;color:#2563eb;font-weight:700">` + answerHeading + `</div>
	<div style="line-height:1.6;color:#0f172a">` + content.String() + `</div>
	</div>
	<div style="padding:16px 24px;background:#f8fafc;border-top:1px solid #e2e8f0;color:#64748b;font-size:13px;line-height:1.5">Если решение не помогло или остались вопросы — просто ответьте на это письмо, и мы продолжим работу по заявке #` + strconv.Itoa(ticketID) + `.</div>
	</div></body></html>`
}

// FormatEmailPreview returns the regular HTML email layout for a synthetic ticket.
func FormatEmailPreview(originalMessage, replyBody string, autoReplied bool) string {
	return FormatEmailHTML(0, originalMessage, replyBody, autoReplied)
}

func emailInline(text string) string {
	text = html.EscapeString(text)
	text = markdownStrong.ReplaceAllString(text, "<strong>$1</strong>")
	text = markdownCode.ReplaceAllString(text, `<code style="padding:2px 5px;background:#f1f5f9;border:1px solid #e2e8f0;border-radius:4px;font-family:monospace">$1</code>`)
	return text
}
