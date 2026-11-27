package channels

import (
	"flowops-ai/internal/intake"
	"reflect"
	"testing"
	"time"
)

func TestIntakeReplayedUpdatesDoNotAdvanceQuestions(t *testing.T) {
	d := newIntakeDrafts()
	message := "Не работает проектор"
	draft, question := d.advance(7, message, "m1")
	if draft == nil || question == "" {
		t.Fatal("missing first question")
	}
	_, repeated := d.advance(7, message, "m1")
	if repeated != question || draft.Details.Location != "" {
		t.Fatal("replayed initial update consumed as location")
	}
	_, next := d.advance(7, "Корпус А, аудитория 313", "m2")
	if next == question || draft.Details.Location == "" {
		t.Fatal("valid answer did not advance")
	}
	before := draft.Details
	_, repeated = d.advance(7, message, "m1")
	if repeated != next || !reflect.DeepEqual(draft.Details, before) {
		t.Fatal("older replay advanced another question")
	}
	_, repeated = d.advance(7, "Корпус А, аудитория 313", "m2")
	if repeated != next || !reflect.DeepEqual(draft.Details, before) {
		t.Fatal("last replay advanced another question")
	}
}

func TestIntakeReadyRetryPayloadIsImmutable(t *testing.T) {
	d := newIntakeDrafts()
	d.advance(7, "Не работает проектор", "m1")
	d.advance(7, "Корпус А, аудитория 313", "m2")
	d.advance(7, "Не включается", "m3")
	d.advance(7, "Работа может продолжаться", "m4")
	draft, q := d.advance(7, "Проверили подключение", "m5")
	if q != "" || draft == nil || draft.Details.Skip {
		t.Fatal("not a complete normal submission")
	}
	before := draft.Details
	// Simulate retry after an ambiguous commit/response loss. /skip must not alter
	// the payload under the stable idempotency key and cause a conflict.
	retry, q := d.advance(7, "/skip", "m6")
	if q != "" || retry.Key != "m1" || !reflect.DeepEqual(retry.Details, before) {
		t.Fatal("ready retry changed request payload")
	}
}

func TestMaxDraftsRequirePrivateStableUserIdentity(t *testing.T) {
	var u maxUpdate
	u.Message.Recipient.ChatID = 101
	if maxHasPrivateUser(u) {
		t.Fatal("missing sender accepted as shared draft key zero")
	}
	u.Message.Sender.UserID = 7
	u.Message.Recipient.ChatType = "dialog"
	if !maxHasPrivateUser(u) {
		t.Fatal("private user rejected")
	}
	for _, kind := range []string{"chat", "channel", "CHAT"} {
		u.Message.Recipient.ChatType = kind
		if maxHasPrivateUser(u) {
			t.Fatal("group could modify private user's draft")
		}
	}
}

func TestExpiredIntakePurgesTextButRetainsBoundedLateAnswerTombstone(t *testing.T) {
	d := newIntakeDrafts()
	v, _ := d.advance(7, "Не работает проектор", "m1")
	v.Details.Location = "Корпус А, аудитория 313"
	v.Expires = time.Now().Add(-time.Second)
	d.purgeExpired()
	if v.Message != "" || v.Details != (intake.Details{}) || v.Key != "" || len(v.seenMessageKeys) != 0 {
		t.Fatal("expired polling draft retained requester data")
	}
	if !d.expired(7) {
		t.Fatal("late reply no longer recognized as stale")
	}
	tg := newTelegramDrafts()
	tg.intake = d
	tg.open(7)
	if tg.waiting(7) {
		t.Fatal("late answer accepted after text purge")
	}
}
