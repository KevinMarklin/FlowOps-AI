package analyzer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestYandexExtraCAIsScopedAndPreservesVerification(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic inspection CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"ai.api.cloud.yandex.net"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	fixture.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	fixture.StartTLS()
	defer fixture.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}}
	a := &Analyzer{client: &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: rejectProviderRedirect}}
	req, _ := http.NewRequest("GET", yandexBaseURL, nil)
	t.Setenv("YANDEX_CA_FILE", "")
	if response, err := a.doProviderRequest(req); err == nil {
		response.Body.Close()
		t.Fatal("untrusted fixture accepted")
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YANDEX_CA_FILE", file)
	response, err := a.doProviderRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	other, _ := http.NewRequest("GET", "https://other.example.invalid", nil)
	if response, err = a.doProviderRequest(other); err == nil {
		response.Body.Close()
		t.Fatal("Yandex CA leaked to another host")
	}
	transport.TLSClientConfig = &tls.Config{ServerName: "wrong.example.invalid"}
	if response, err = a.doProviderRequest(req); err == nil {
		response.Body.Close()
		t.Fatal("hostname verification bypassed")
	}
	t.Setenv("YANDEX_CA_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	if response, err = a.doProviderRequest(req); err == nil {
		response.Body.Close()
		t.Fatal("invalid CA configuration did not fail closed")
	}
}
