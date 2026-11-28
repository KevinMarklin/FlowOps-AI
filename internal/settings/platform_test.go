package settings

import (
	"testing"
	"time"
)

func TestPlatformValidationAndBusinessCalendar(t *testing.T) {
	c := DefaultPlatformConfig()
	c.Name = "Test"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	parse := func(s string) time.Time {
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	// Friday 17:00 Moscow -> Monday 10:00: two working hours.
	if got := c.ElapsedSeconds(parse("2026-11-27T14:00:00Z"), parse("2026-11-30T07:00:00Z")); got != 7200 {
		t.Fatal(got)
	}
	c.Calendar.Holidays = []string{"2026-11-30"}
	if got := c.ElapsedSeconds(parse("2026-11-27T14:00:00Z"), parse("2026-11-30T07:00:00Z")); got != 3600 {
		t.Fatal(got)
	}
	c.Timezone = "America/New_York"
	c.Calendar.Days = []int{0}
	c.Calendar.Start = "00:00"
	c.Calendar.End = "04:00"
	c.Calendar.Holidays = nil
	if got := c.ElapsedSeconds(parse("2026-03-08T05:00:00Z"), parse("2026-03-08T08:00:00Z")); got != 10800 {
		t.Fatal("DST", got)
	}
	c.Timezone = "Bogus/Zone"
	if c.Validate() == nil {
		t.Fatal("invalid zone")
	}
	c = DefaultPlatformConfig()
	c.Name = "Test"
	c.ClosureReasons = append(c.ClosureReasons, c.ClosureReasons[0])
	if c.Validate() == nil {
		t.Fatal("duplicate reason")
	}
}
