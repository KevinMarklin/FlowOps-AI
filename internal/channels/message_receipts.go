package channels

import "time"

// Worker-local replay receipts contain identifiers only, never message text.
// The small TTL-bound cache also covers a final clarification update after its
// draft has closed, when its MID differs from the original ticket's key.
type messageReceipts struct{ keys map[string]time.Time }

func newMessageReceipts() *messageReceipts { return &messageReceipts{keys: make(map[string]time.Time)} }
func (r *messageReceipts) accept(key string) bool {
	if key == "" {
		return true
	}
	now := time.Now()
	if until, ok := r.keys[key]; ok && now.Before(until) {
		return false
	}
	if len(r.keys) >= 4096 {
		var oldest string
		var expiry time.Time
		for k, until := range r.keys {
			if !now.Before(until) {
				delete(r.keys, k)
				continue
			}
			if expiry.IsZero() || until.Before(expiry) {
				oldest = k
				expiry = until
			}
		}
		if len(r.keys) >= 4096 {
			delete(r.keys, oldest)
		}
	}
	r.keys[key] = now.Add(15 * time.Minute)
	return true
}
func (r *messageReceipts) forget(key string) { delete(r.keys, key) }
