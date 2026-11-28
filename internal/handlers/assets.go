package handlers

import (
	"encoding/json"
	"errors"
	"flowops-ai/internal/security"
	"github.com/skip2/go-qrcode"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type OrganizationAsset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	UnitID      string `json:"unit_id"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Revision    int64  `json:"revision"`
	URL         string `json:"url,omitempty"`
}

func assetURL(id string) (string, error) {
	base := strings.TrimRight(os.Getenv("FLOWOPS_PUBLIC_URL"), "/")
	if base == "" {
		base = "http://localhost:8080"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return "", errors.New("Настройте FLOWOPS_PUBLIC_URL")
	}
	u.Path = "/"
	u.RawQuery = url.Values{"asset": {id}}.Encode()
	return u.String(), nil
}

const assetVisibleSQL = `NOT EXISTS(SELECT 1 FROM organization_member_roles m JOIN organization_access_roles r ON r.tenant_id=m.tenant_id AND r.id=m.role_id WHERE m.tenant_id=$1 AND m.member_id=$2 AND r.scope<>'organization') OR EXISTS(WITH RECURSIVE roots AS(SELECT u.id FROM organization_units u JOIN organization_member_roles m ON m.tenant_id=u.tenant_id JOIN organization_access_roles r ON r.tenant_id=m.tenant_id AND r.id=m.role_id WHERE u.tenant_id=$1 AND m.member_id=$2 AND u.active AND r.scope='units' AND r.unit_ids @> to_jsonb(ARRAY[u.id]) UNION SELECT c.id FROM organization_units c JOIN roots p ON c.parent_id=p.id WHERE c.tenant_id=$1 AND c.active) SELECT 1 FROM roots WHERE id=$3) OR EXISTS(SELECT 1 FROM organization_unit_members m JOIN organization_units u ON u.tenant_id=m.tenant_id AND u.id=m.unit_id WHERE m.tenant_id=$1 AND m.member_id=$2 AND m.unit_id=$3 AND u.active)`

func (h *TicketsHandler) Assets(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	tenant := p.TenantID
	id := r.PathValue("id")
	if strings.HasSuffix(r.URL.Path, "/context") {
		raw, err := h.resolveAsset(r, id)
		if err != nil {
			writeError(w, 404, "Объект недоступен")
			return
		}
		writeJSON(w, 200, json.RawMessage(raw))
		return
	}

	if p.Role != "admin" {
		writeError(w, 403, "Объекты настраивает администратор")
		return
	}
	if r.Method == "GET" {
		if strings.HasSuffix(r.URL.Path, "/qr") {
			var active bool
			if h.db.QueryRow(r.Context(), `SELECT active FROM organization_assets WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&active) != nil || !active {
				writeError(w, 404, "Активный объект не найден")
				return
			}
			link, err := assetURL(id)
			if err != nil {
				writeError(w, 503, err.Error())
				return
			}
			png, err := qrcode.Encode(link, qrcode.Medium, 512)
			if err != nil {
				writeError(w, 503, "Не удалось создать QR-код")
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="asset-`+id+`.png"`)
			w.Header().Set("Cache-Control", "no-store")
			w.Write(png)
			return
		}
		rows, err := h.db.Query(r.Context(), `SELECT id,name,kind,unit_id,description,active,revision FROM organization_assets WHERE tenant_id=$1 ORDER BY name LIMIT 1000`, tenant)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить объекты")
			return
		}
		defer rows.Close()
		items := []OrganizationAsset{}
		for rows.Next() {
			var a OrganizationAsset
			if err = rows.Scan(&a.ID, &a.Name, &a.Kind, &a.UnitID, &a.Description, &a.Active, &a.Revision); err != nil {
				break
			}
			a.URL, _ = assetURL(a.ID)
			items = append(items, a)
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать объекты")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	a := OrganizationAsset{Active: true}
	if !decodeDialogue(w, r, &a) {
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len([]rune(a.Name)) > 160 || a.Kind == "" || len([]rune(a.Kind)) > 60 || len([]rune(a.Description)) > 2000 || a.Revision < 0 {
		writeError(w, 400, "Некорректный объект")
		return
	}
	if a.UnitID != "" {
		var found bool
		if h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_units WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, a.UnitID).Scan(&found) != nil || !found {
			writeError(w, 400, "Подразделение недоступно")
			return
		}
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось сохранить объект")
		return
	}
	defer tx.Rollback(r.Context())
	if r.Method == "POST" {
		a.ID = operationID()
		a.Revision = 1
		_, err = tx.Exec(r.Context(), `INSERT INTO organization_assets(tenant_id,id,name,kind,unit_id,description,active) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenant, a.ID, a.Name, a.Kind, a.UnitID, a.Description, a.Active)
	} else {
		if id != a.ID {
			writeError(w, 400, "ID не совпадает")
			return
		}
		tag, changeErr := tx.Exec(r.Context(), `UPDATE organization_assets SET name=$3,kind=$4,unit_id=$5,description=$6,active=$7,revision=revision+1 WHERE tenant_id=$1 AND id=$2 AND revision=$8`, tenant, id, a.Name, a.Kind, a.UnitID, a.Description, a.Active, a.Revision)
		err = changeErr
		if err == nil && tag.RowsAffected() != 1 {
			writeError(w, 409, "Объект уже изменён")
			return
		}
		a.Revision++
	}
	if err != nil {
		writeError(w, 503, "Не удалось сохранить объект")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ASSET_SAVED',$2,$3)`, tenant, p.ID, a.ID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить объект")
		return
	}
	a.URL, _ = assetURL(a.ID)
	writeJSON(w, 200, a)
}

func (h *TicketsHandler) resolveAsset(r *http.Request, id string, queries ...rowQuerier) ([]byte, error) {
	var query rowQuerier = h.db
	if len(queries) > 0 {
		query = queries[0]
	}
	if id == "" {
		return []byte("{}"), nil
	}
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		return nil, errors.New("Необходим вход")
	}
	var a OrganizationAsset
	if err := query.QueryRow(r.Context(), `SELECT id,name,kind,unit_id,description,active,revision FROM organization_assets WHERE tenant_id=$1 AND id=$2 AND active`, p.TenantID, id).Scan(&a.ID, &a.Name, &a.Kind, &a.UnitID, &a.Description, &a.Active, &a.Revision); err != nil {
		return nil, errors.New("Объект недоступен")
	}
	var visible bool
	if err := query.QueryRow(r.Context(), `SELECT `+assetVisibleSQL, p.TenantID, p.ID, a.UnitID).Scan(&visible); err != nil || !visible {
		return nil, errors.New("Объект недоступен")
	}
	return json.Marshal(a)
}
