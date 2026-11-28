package onec

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadOnlyODataAndBoundary(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "reader" || pass != "private" || r.Method != "GET" {
			t.Error("wrong auth or method")
		}
		switch r.URL.Path {
		case "/base/$metadata":
			w.Write([]byte(`<edmx:Edmx xmlns:edmx="urn:test"><DataServices><Schema><EntityContainer><EntitySet Name="Catalog_Сотрудники"/></EntityContainer></Schema></DataServices></edmx:Edmx>`))
		case "/base/Catalog_Сотрудники":
			if r.URL.Query().Get("$top") != "20" || r.URL.Query().Get("$skip") != "40" || r.URL.Query().Get("$format") != "json" {
				t.Error("pagination missing")
			}
			w.Write([]byte(`{"value":[{"Description":"Сотрудник"}]}`))
		case "/base/redirect":
			http.Redirect(w, r, "/base/target", 302)
		default:
			w.WriteHeader(401)
			w.Write([]byte("secret upstream error"))
		}
	}))
	defer srv.Close()
	c := Config{BaseURL: srv.URL + "/base", Mode: "odata", Auth: "basic", Username: "reader"}
	b, err := c.Get(context.Background(), c.BaseURL, "private", "$metadata", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sets, err := EntitySets(b)
	if err != nil || len(sets) != 1 || sets[0] != "Catalog_Сотрудники" {
		t.Fatal(sets, err)
	}
	if _, err = c.Get(context.Background(), c.BaseURL, "private", "Catalog_Сотрудники", 40, 20); err != nil {
		t.Fatal(err)
	}
	before := calls
	for _, resource := range []string{"../secret", "https://evil.test", "x?password=1", "//host", "%2e%2e/secret"} {
		if _, err = c.Get(context.Background(), c.BaseURL, "private", resource, 0, 20); err == nil {
			t.Fatal("unsafe resource accepted", resource)
		}
	}
	if calls != before {
		t.Fatal("unsafe requests reached upstream")
	}
	if _, err = c.Get(context.Background(), "", "private", "x", 0, 20); err == nil {
		t.Fatal("allowlist ignored")
	}
	if _, err = c.Get(context.Background(), c.BaseURL, "private", "redirect", 0, 20); err == nil {
		t.Fatal("redirect followed")
	}
	if _, err = c.Get(context.Background(), c.BaseURL, "private", "denied", 0, 20); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("raw upstream error leaked", err)
	}
}
func TestSecretEncryptionTenantBindingAndMetadata(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	encrypted, err := Seal(key, "tenant-a", "password")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, "tenant-a", encrypted)
	if err != nil || plain != "password" {
		t.Fatal(plain, err)
	}
	if _, err = Open(key, "tenant-b", encrypted); err == nil {
		t.Fatal("secret usable across tenants")
	}
	if _, err = Seal("bad", "tenant-a", "password"); err == nil {
		t.Fatal("weak key accepted")
	}
	for _, b := range []string{"", `<html>Login</html>`, `<Edmx>`} {
		if _, err = EntitySets([]byte(b)); err == nil {
			t.Fatal("invalid metadata accepted", b)
		}
	}
}
func TestHTTPJSONAndSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", (2<<20)+1)))
			return
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing bearer")
		}
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("offset") != "5" {
			t.Error("missing HTTP pagination")
		}
		w.Write([]byte(`[{"id":1}]`))
	}))
	defer srv.Close()
	c := Config{BaseURL: srv.URL, Mode: "http", Auth: "bearer"}
	if _, err := c.Get(context.Background(), c.BaseURL, "token", "items", 5, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), c.BaseURL, "token", "large", 0, 10); err == nil {
		t.Fatal("oversized response accepted")
	}
}
