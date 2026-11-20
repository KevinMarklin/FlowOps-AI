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
