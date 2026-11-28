// Package onec implements bounded, read-only access to explicitly registered 1C services.
package onec

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Mode       string `json:"mode"`
	BaseURL    string `json:"base_url"`
	Auth       string `json:"auth"`
	Username   string `json:"username"`
	HealthPath string `json:"health_path"`
}

var resourcePattern = regexp.MustCompile(`^[\p{L}\p{N}_-]+(/[\p{L}\p{N}_-]+)*$`)
var ErrDenied = errors.New("Адрес 1С не разрешён: добавьте точный URL сервиса в FLOWOPS_1C_ALLOWED_URLS на сервере")

func (c Config) Validate(allowed string) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || len(c.BaseURL) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || strings.Contains(u.Path, "..") || u.RawPath != "" {
		return errors.New("Укажите полный адрес опубликованного сервиса без пароля и параметров")
	}
	found := false
	for _, entry := range strings.Split(allowed, ",") {
		if strings.TrimRight(strings.TrimSpace(entry), "/") == strings.TrimRight(c.BaseURL, "/") {
			found = true
		}
	}
	if !found {
		return ErrDenied
	}
	if c.Mode != "odata" && c.Mode != "http" {
		return errors.New("Выберите OData или HTTP-сервис")
	}
	if c.Auth != "basic" && c.Auth != "bearer" && c.Auth != "none" {
		return errors.New("Неизвестный тип авторизации")
	}
	if len(c.Username) > 160 || strings.ContainsAny(c.Username, "\r\n:") || len(c.HealthPath) > 160 || (c.HealthPath != "" && !resourcePattern.MatchString(c.HealthPath)) {
		return errors.New("Проверьте логин и относительный путь проверки")
	}
	if c.Auth == "basic" && c.Username == "" {
		return errors.New("Укажите логин 1С")
	}
	return nil
}

func Seal(key, tenant, secret string) (string, error) {
	a, err := box(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(secret), []byte(tenant))), nil
}
func Open(key, tenant, sealed string) (string, error) {
	a, err := box(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < a.NonceSize() {
		return "", errors.New("Не удалось прочитать секрет 1С")
	}
	plain, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(tenant))
	if err != nil {
		return "", errors.New("Не удалось расшифровать секрет 1С")
	}
	return string(plain), nil
}
func box(key string) (cipher.AEAD, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("Настройте FLOWOPS_1C_SECRET_KEY: 32 случайных байта в Base64")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c Config) Get(ctx context.Context, allowed, secret, resource string, skip, limit int) ([]byte, error) {
	if err := c.Validate(allowed); err != nil {
		return nil, err
	}
	if len(resource) > 160 || (resource != "$metadata" && !resourcePattern.MatchString(resource)) {
		return nil, errors.New("Укажите относительное имя ресурса без параметров")
	}
	if c.Auth != "none" && secret == "" {
		return nil, errors.New("Сохраните пароль или токен 1С")
	}
	u, _ := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/" + resource)
	if resource != "$metadata" && limit > 0 {
		q := u.Query()
		if c.Mode == "odata" {
			q.Set("$format", "json")
			q.Set("$top", fmt.Sprint(limit))
			q.Set("$skip", fmt.Sprint(skip))
		} else {
			q.Set("limit", fmt.Sprint(limit))
			q.Set("offset", fmt.Sprint(skip))
		}
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errors.New("Не удалось подготовить запрос 1С")
	}
	req.Header.Set("Accept", "application/json")
	if resource == "$metadata" {
		req.Header.Set("Accept", "application/xml")
	}
	if c.Auth == "basic" {
		req.SetBasicAuth(c.Username, secret)
	}
	if c.Auth == "bearer" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.MaxConnsPerHost = 2
	tr.ResponseHeaderTimeout = 8 * time.Second
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("1С недоступна: проверьте адрес, сеть и сертификат сервера")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errors.New("1С отклонила авторизацию: проверьте реквизиты и права пользователя")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("1С вернула HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("Ответ 1С превышает 2 МБ или не прочитан: уменьшите размер страницы")
	}
	if resource != "$metadata" && !json.Valid(data) {
		return nil, errors.New("Сервис 1С должен возвращать JSON")
	}
	return data, nil
}

func EntitySets(data []byte) ([]string, error) {
	d := xml.NewDecoder(strings.NewReader(string(data)))
	names := []string{}
	root := false
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("1С вернула некорректное описание OData")
		}
		if e, ok := t.(xml.StartElement); ok {
			if !root {
				if e.Name.Local != "Edmx" {
					return nil, errors.New("Ответ не является метаданными OData")
				}
				root = true
			}
			if e.Name.Local == "EntitySet" {
				for _, a := range e.Attr {
					if a.Name.Local == "Name" && resourcePattern.MatchString(a.Value) {
						names = append(names, a.Value)
					}
				}
			}
		}
	}
	if !root {
		return nil, errors.New("Описание OData пустое")
	}
	return names, nil
}
