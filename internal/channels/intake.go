package channels

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/intake"
)

// Polling workers own their own bounded, expiring drafts. Text never crosses
// bot/tenant boundaries and is discarded on cancellation, expiry or success.
type intakeDraft struct {
	Message         string
	Details         intake.Details
	Key             string
	seenMessageKeys map[string]bool
	Expires         time.Time
}
type intakeDrafts struct{ items map[int64]*intakeDraft }

func newIntakeDrafts() *intakeDrafts     { return &intakeDrafts{items: make(map[int64]*intakeDraft)} }
func (d *intakeDrafts) close(chat int64) { delete(d.items, chat) }
func (d *intakeDrafts) expired(chat int64) bool {
	v := d.items[chat]
	return v != nil && !time.Now().Before(v.Expires)
}

// Keep only an expired identifier tombstone after polling notices expiry. It
// rejects a late answer while discarding all requester text and supplementary
// data. Tombstones share the same bounded 4096-entry capacity as active drafts.
func (d *intakeDrafts) purgeExpired() {
	now := time.Now()
	for _, v := range d.items {
		if !now.Before(v.Expires) {
			v.Message = ""
			v.Details = intake.Details{}
			v.Key = ""
			v.seenMessageKeys = nil
		}
	}
}

func (d *intakeDrafts) get(chat int64) *intakeDraft {
	v := d.items[chat]
	if v != nil && !time.Now().Before(v.Expires) {
		d.close(chat)
		return nil
	}
	return v
}
func (d *intakeDrafts) advance(chat int64, text, key string) (*intakeDraft, string) {
	v := d.get(chat)
	if v == nil {
		if intake.Evaluate(text, intake.Details{}).Ready {
			return nil, ""
		}
		if len(d.items) >= 4096 {
			var oldest int64
			var expiry time.Time
			for id, x := range d.items {
				if expiry.IsZero() || x.Expires.Before(expiry) {
					oldest = id
					expiry = x.Expires
				}
			}
			d.close(oldest)
		}
		if key == "" {
			key = "intake:" + rand.Text()
		}
		v = &intakeDraft{Message: text, Key: key, seenMessageKeys: map[string]bool{key: true}, Expires: time.Now().Add(15 * time.Minute)}
		d.items[chat] = v
	} else {
		plan := intake.Evaluate(v.Message, v.Details)
		if plan.Ready {
			// A retry may follow a commit whose response was lost: keep its payload frozen.
		} else if key != "" && v.seenMessageKeys[key] {
			// Polling can replay an update; repeat the question without consuming it twice.
		} else if text == "/skip" {
			v.Details.Skip = true
			v.seenMessageKeys[key] = true
		} else if len(plan.Fields) > 0 {
			next := v.Details
			intake.Set(&next, plan.Fields[0].Key, text)
			if err := intake.Validate(v.Message, next); err != nil {
				return v, err.Error() + "\nОтправьте короткий ответ или /skip."
			}
			v.Details = next
			v.seenMessageKeys[key] = true
		}
	}
	if err := intake.Validate(v.Message, v.Details); err != nil {
		d.close(chat)
		return nil, "Не удалось подготовить уточнения. Опишите проблему короче и повторите отправку."
	}
	plan := intake.Evaluate(v.Message, v.Details)
	if plan.Ready {
		return v, ""
	}
	field := plan.Fields[0]
	return v, fmt.Sprintf("%s\n\n%s\n%s\n\nНе присылайте пароли и коды. /skip — передать без оставшихся уточнений. /cancel — отменить.", plan.Title, field.Label, field.Hint)
}
func intakeReceipt(d *intakeDraft) string {
	if d == nil {
		return ""
	}
	if d.Details.Skip {
		return "\nУточнения пропущены; специалист может задать дополнительные вопросы.\n"
	}
	return "\nУточнения сохранены в обращении.\n"
}

func intakeMessageKeys(d *intakeDraft, key string) []string {
	keys := []string{}
	if key != "" {
		keys = append(keys, key)
	}
	if d != nil {
		for previous := range d.seenMessageKeys {
			if previous != "" && previous != key {
				keys = append(keys, previous)
			}
		}
	}
	return keys
}
func maxFeedbackCommand(text string) (int, bool, bool) {
	fields := strings.Fields(text)
	if len(fields) != 2 || (fields[0] != "/yes" && fields[0] != "/no") {
		return 0, false, false
	}
	id, err := strconv.Atoi(fields[1])
	if err != nil || id <= 0 {
		return 0, false, false
	}
	return id, fields[0] == "/yes", true
}
