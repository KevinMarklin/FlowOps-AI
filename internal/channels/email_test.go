package channels

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeMIMEBodyUTF8QuotedPrintable(t *testing.T) {
	body, err := decodeMIMEBody("text/plain; charset=utf-8", "quoted-printable", strings.NewReader("=D0=97=D0=B4=D1=80=D0=B0=D0=B2=D1=81=D1=82=D0=B2=D1=83=D0=B9=D1=82=D0=B5"))
	if err != nil {
		t.Fatal(err)
	}
	if body != "Здравствуйте" {
		t.Fatalf("decoded body = %q", body)
	}
}

func TestDecodeMIMEBodyWindows1251Base64(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2, 0x2c, 0x20, 0xec, 0xe8, 0xf0, 0x21})
	body, err := decodeMIMEBody("text/plain; charset=windows-1251", "base64", strings.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if body != "Привет, мир!" {
		t.Fatalf("decoded body = %q", body)
	}
}

func TestCleanEmailTextRemovesInvisibleControls(t *testing.T) {
	got := cleanEmailText("line\x00\r\nwith\uFEFFzero\u200B\u200C\u200D\tindent")
	if got != "line\nwithzero\tindent" {
		t.Fatalf("cleanEmailText() = %q", got)
	}
}

func TestTrimEmailQuoteRemovesReplyHistory(t *testing.T) {
	for _, input := range []string{
		"Новый вопрос\n---\nFlowOps AI • Служба поддержки\nЗаявка #42",
		"Новый вопрос\n> старый ответ\n> ещё строка",
		"Новый вопрос\nOn Tue, user@example.test wrote:\nстарый текст",
	} {
		if got := trimEmailQuote(input); got != "Новый вопрос" {
			t.Errorf("trimEmailQuote(%q) = %q", input, got)
		}
	}
}

func TestIsSystemEmailSender(t *testing.T) {
	for _, address := range []string{
		"alerts@e.mail.ru",
		"user@corp.mail.ru",
		"noreply@example.test",
		"NO-REPLY@example.test",
		"mailer-daemon@example.test",
		"postmaster@example.test",
	} {
		if !isSystemEmailSender(address) {
			t.Errorf("system sender %q was not ignored", address)
		}
	}
	if isSystemEmailSender("person@mail.ru") {
		t.Fatal("regular sender was ignored")
	}
}
