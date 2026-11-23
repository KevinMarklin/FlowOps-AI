package channels

import (
	"strings"
	"testing"
)

func TestFormatTelegramMarkdownEscapesTextAndConvertsMarkdown(t *testing.T) {
	got := FormatTelegramMarkdown("# Заголовок\n**важно** и `код` <тег> & текст\n- пункт\n* второй")
	want := "<b>Заголовок</b>\n<b>важно</b> и <code>код</code> &lt;тег&gt; &amp; текст\n• пункт\n• второй"
	if got != want {
		t.Fatalf("FormatTelegramMarkdown() = %q, want %q", got, want)
	}
}

func TestFormatMAXMarkdownConvertsHeadingsListsAndRemovesSource(t *testing.T) {
	got := FormatMAXMarkdown("## Настройка\n- первый шаг\n[Источник: «VPN» (knowledge_base/it_vpn.md)]")
	want := "**Настройка**\n• первый шаг"
	if got != want || strings.Contains(got, "knowledge_base") {
		t.Fatalf("FormatMAXMarkdown() = %q, want %q", got, want)
	}
}

func TestFormatEmailHTMLBuildsSafeStructuredMessage(t *testing.T) {
	got := FormatEmailHTML(42, `Не работает <script>alert("x")</script> & почта`, "# Настройка\nПроверьте **доступ** и `VPN <test>`.\n- первый шаг\n- второй шаг\n[Источник: «VPN» (knowledge_base/it_vpn.md)]", true)
	for _, part := range []string{"FlowOps AI • Служба поддержки", "Заявка #42", "ВАШЕ ОБРАЩЕНИЕ", "Не работает &lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; почта", "💡 АВТОМАТИЧЕСКОЕ РЕШЕНИЕ", "<h3", "Настройка</h3>", "<strong>доступ</strong>", "<code", "VPN &lt;test&gt;", "<ul", "<li style", "первый шаг", "второй шаг", "Если решение не помогло"} {
		if !strings.Contains(got, part) {
			t.Errorf("HTML email missing %q: %s", part, got)
		}
	}
	if strings.Contains(got, "# Настройка") {
		t.Fatal("raw Markdown heading marker leaked into email")
	}
	if strings.Contains(got, "knowledge_base") || strings.Contains(got, "it_vpn.md") {
		t.Fatal("internal knowledge-base path leaked into email")
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, `alert("x")`) {
		t.Fatal("unsafe source text was not escaped")
	}
	operatorHTML := FormatEmailHTML(42, "Запрос", "Принято **в работу**", false)
	if !strings.Contains(operatorHTML, "👨‍💻 ОТВЕТ СПЕЦИАЛИСТА") || strings.Contains(operatorHTML, "💡 АВТОМАТИЧЕСКОЕ РЕШЕНИЕ") || !strings.Contains(operatorHTML, "<strong>в работу</strong>") {
		t.Fatal("operator response heading was not selected")
	}
}

func TestUserReplyTemplates(t *testing.T) {
	answer := PublicReplyText(42, "Инструкция.\n[Источник: «VPN» (knowledge_base/it_vpn.md)]")
	if !strings.Contains(answer, "**💡 Решение по заявке #42**") || !strings.Contains(answer, "Если решение не помогло") || strings.Contains(answer, "knowledge_base") {
		t.Fatalf("unexpected public RAG reply: %q", answer)
	}
	if got := EscalationReplyText(42, "IT"); got != "✅ Обращение принято в работу (Заявка #42).\nНаправлено в профильный отдел: IT.\nОтвет специалиста придёт прямо сюда автоматически." {
		t.Fatalf("unexpected escalation reply: %q", got)
	}
	if got := EscalationReplyText(42, "General"); strings.Contains(got, "профильный отдел") {
		t.Fatalf("general department should not be named: %q", got)
	}
	if got := OperatorReplyText(42, "Готово"); got != "**👨‍💻 Ответ специалиста по заявке #42:**\n\nГотово" {
		t.Fatalf("unexpected operator reply: %q", got)
	}
}
