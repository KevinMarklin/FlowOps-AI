package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Choice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type Calendar struct {
	Days     []int    `json:"days"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Holidays []string `json:"holidays"`
}
type Economics struct {
	Currency              string             `json:"currency"`
	HourlyRate            float64            `json:"hourly_rate"`
	DepartmentRates       map[string]float64 `json:"department_rates"`
	BaselineMinutes       float64            `json:"baseline_minutes"`
	MonthlyInfrastructure float64            `json:"monthly_infrastructure"`
	Implementation        float64            `json:"implementation"`
	MonthlyMaintenance    float64            `json:"monthly_maintenance"`
}
type SLAPolicy struct {
	FirstResponseMinutes int    `json:"first_response_minutes"`
	ResolutionMinutes    int    `json:"resolution_minutes"`
	Clock                string `json:"clock"`
}
type PlatformConfig struct {
	Process        ProcessSettings `json:"process"`
	AI             AIPolicy        `json:"ai"`
	Revision       int64           `json:"revision"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Industry       string          `json:"industry"`
	Timezone       string          `json:"timezone"`
	ContactEmail   string          `json:"contact_email"`
	Calendar       Calendar        `json:"calendar"`
	Economics      Economics       `json:"economics"`
	SLA            SLAPolicy       `json:"sla"`
	ClosureReasons []Choice        `json:"closure_reasons"`
}

func DefaultPlatformConfig() PlatformConfig {
	return PlatformConfig{Timezone: "Europe/Moscow", Calendar: Calendar{Days: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "18:00", Holidays: []string{}},
		Economics: Economics{Currency: "RUB", BaselineMinutes: 10, DepartmentRates: map[string]float64{}}, SLA: SLAPolicy{Clock: "business"},
		ClosureReasons: []Choice{{"solved", "Вопрос решён"}, {"duplicate", "Дубликат обращения"}, {"cancelled", "Отменено сотрудником"}, {"no_response", "Нет ответа сотрудника"}, {"out_of_scope", "Вне зоны поддержки"}, {"other", "Другая причина"}}}
}

var configID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var currencyID = regexp.MustCompile(`^[A-Z]{3}$`)

func (c *PlatformConfig) Validate() error {
	if err := c.Process.Validate(); err != nil {
		return err
	}
	if err := c.AI.Validate(); err != nil {
		return err
	}
	c.Name = strings.TrimSpace(c.Name)
	c.ContactEmail = strings.TrimSpace(c.ContactEmail)
	if c.Name == "" || len([]rune(c.Name)) > 160 || len([]rune(c.Description)) > 4000 || len([]rune(c.Industry)) > 160 {
		return errors.New("Проверьте название и описание организации")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("Неизвестный часовой пояс")
	}
	if c.ContactEmail != "" {
		a, err := mail.ParseAddress(c.ContactEmail)
		if err != nil || a.Address != c.ContactEmail {
			return errors.New("Некорректная контактная почта")
		}
	}
	if len(c.Calendar.Days) == 0 || len(c.Calendar.Days) > 7 || len(c.Calendar.Holidays) > 1000 {
		return errors.New("Проверьте рабочий календарь")
	}
	days := map[int]bool{}
	for _, day := range c.Calendar.Days {
		if day < 0 || day > 6 || days[day] {
			return errors.New("Некорректные рабочие дни")
		}
		days[day] = true
	}
	start, e1 := time.Parse("15:04", c.Calendar.Start)
	end, e2 := time.Parse("15:04", c.Calendar.End)
	if e1 != nil || e2 != nil || !start.Before(end) {
		return errors.New("Рабочее время должно быть в пределах одних суток")
	}
	for _, date := range c.Calendar.Holidays {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return errors.New("Некорректная дата выходного дня")
		}
	}
	validCost := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1e12 }
	e := c.Economics
	if !currencyID.MatchString(e.Currency) || !validCost(e.HourlyRate) || !validCost(e.MonthlyInfrastructure) || !validCost(e.Implementation) || !validCost(e.MonthlyMaintenance) || e.BaselineMinutes <= 0 || e.BaselineMinutes > 480 || len(e.DepartmentRates) > 1000 {
		return errors.New("Проверьте экономические параметры")
	}
	for dept, rate := range e.DepartmentRates {
		if len(dept) == 0 || len(dept) > 100 || !validCost(rate) {
			return errors.New("Некорректная ставка подразделения")
		}
	}
	if c.SLA.Clock != "calendar" && c.SLA.Clock != "business" {
		return errors.New("Неизвестный режим SLA")
	}
	if c.SLA.FirstResponseMinutes < 0 || c.SLA.ResolutionMinutes < 0 || c.SLA.FirstResponseMinutes > 525600 || c.SLA.ResolutionMinutes > 525600 {
		return errors.New("Некорректные сроки SLA")
	}
	if len(c.ClosureReasons) == 0 || len(c.ClosureReasons) > 100 {
		return errors.New("Нужна хотя бы одна причина закрытия")
	}
	seen := map[string]bool{}
	for _, item := range c.ClosureReasons {
		if !configID.MatchString(item.ID) || seen[item.ID] || strings.TrimSpace(item.Label) == "" || len([]rune(item.Label)) > 160 {
			return errors.New("Некорректная или повторная причина закрытия")
		}
		seen[item.ID] = true
	}
	return nil
}

func LoadPlatform(ctx context.Context, db *pgxpool.Pool, tenant string) (PlatformConfig, error) {
	c := DefaultPlatformConfig()
	var raw []byte
	var revision int64
	err := db.QueryRow(ctx, `SELECT platform_config,platform_revision FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, tenant).Scan(&raw, &revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("decode organization config: %w", err)
		}
	}
	c.Revision = revision
	// Workspace name is the existing source of truth and stays compatible with login/profile.
	var name string
	err = db.QueryRow(ctx, `SELECT name FROM workspaces WHERE tenant_id=$1`, tenant).Scan(&name)
	if err == nil {
		c.Name = name
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	return c, nil
}

// ElapsedSeconds measures the organization's actual local business calendar,
// including holidays and DST. Calendar-clock policies bypass business hours.
func (c PlatformConfig) ElapsedSeconds(from, to time.Time) float64 {
	if !to.After(from) {
		return 0
	}
	if c.SLA.Clock == "calendar" {
		return to.Sub(from).Seconds()
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return 0
	}
	days := map[int]bool{}
	for _, d := range c.Calendar.Days {
		days[d] = true
	}
	holidays := map[string]bool{}
	for _, d := range c.Calendar.Holidays {
		holidays[d] = true
	}
	start, _ := time.Parse("15:04", c.Calendar.Start)
	end, _ := time.Parse("15:04", c.Calendar.End)
	cursor := from.In(loc)
	cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, loc)
	total := float64(0)
	for cursor.Before(to) {
		if days[int(cursor.Weekday())] && !holidays[cursor.Format("2006-01-02")] {
			a := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), start.Hour(), start.Minute(), 0, 0, loc)
			b := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), end.Hour(), end.Minute(), 0, 0, loc)
			if a.Before(from) {
				a = from
			}
			if b.After(to) {
				b = to
			}
			if b.After(a) {
				total += b.Sub(a).Seconds()
			}
		}
		cursor = cursor.AddDate(0, 0, 1)
	}
	return total
}
