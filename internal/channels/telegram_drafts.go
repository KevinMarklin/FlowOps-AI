package channels

import "time"

// Each polling worker owns its own state: chats from different bots never share
// a draft. Restart/expiry closes the draft instead of ingesting unexpected text.
type telegramDrafts struct {
	chats  map[int64]time.Time
	intake *intakeDrafts
}

func newTelegramDrafts() *telegramDrafts {
	return &telegramDrafts{chats: make(map[int64]time.Time), intake: newIntakeDrafts()}
}
func (d *telegramDrafts) close(chat int64) { delete(d.chats, chat); d.intake.close(chat) }
func (d *telegramDrafts) waiting(chat int64) bool {
	expires, ok := d.chats[chat]
	if !ok || !time.Now().Before(expires) || d.intake.expired(chat) {
		d.close(chat)
		return false
	}
	return true
}
func (d *telegramDrafts) open(chat int64) {
	if len(d.chats) >= 4096 {
		var oldest int64
		var expiry time.Time
		for id, until := range d.chats {
			if expiry.IsZero() || until.Before(expiry) {
				oldest, expiry = id, until
			}
		}
		d.close(oldest)
	}
	d.chats[chat] = time.Now().Add(30 * time.Minute)
}
