package human

import (
	"testing"
	"time"
)

func TestWhenUsesFullDateAndHourCycle(t *testing.T) {
	loc := time.FixedZone("CST", -6*3600)
	morning := time.Date(2026, 11, 15, 3, 0, 0, 0, loc)
	afternoon := time.Date(2026, 11, 15, 15, 4, 0, 0, loc)
	if got := When(morning, true); got != "Sunday, November 15, 2026 at 3:00 AM CST" {
		t.Fatalf("12h morning %q", got)
	}
	if got := When(afternoon, true); got != "Sunday, November 15, 2026 at 3:04 PM CST" {
		t.Fatalf("12h afternoon %q", got)
	}
	if got := When(morning, false); got != "Sunday, November 15, 2026 at 03:00 CST" {
		t.Fatalf("24h morning %q", got)
	}
	if got := When(afternoon, false); got != "Sunday, November 15, 2026 at 15:04 CST" {
		t.Fatalf("24h afternoon %q", got)
	}
	noon := time.Date(2026, 11, 15, 12, 0, 0, 0, loc)
	midnight := time.Date(2026, 11, 15, 0, 0, 0, 0, loc)
	if got := When(noon, true); got != "Sunday, November 15, 2026 at 12:00 PM CST" {
		t.Fatalf("noon %q", got)
	}
	if got := When(midnight, true); got != "Sunday, November 15, 2026 at 12:00 AM CST" {
		t.Fatalf("midnight %q", got)
	}
}

func TestRemainUsesWords(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0 seconds"},
		{time.Second, "1 second"},
		{45 * time.Second, "45 seconds"},
		{time.Minute + 2*time.Second, "1 minute 2 seconds"},
		{6*time.Hour + 3*time.Minute, "6 hours 3 minutes"},
		{time.Hour, "1 hour"},
		{26*time.Hour + 30*time.Second, "1 day 2 hours 1 minute"},
	}
	for _, tc := range cases {
		if got := Remain(tc.d); got != tc.want {
			t.Errorf("Remain(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestPreferHour12(t *testing.T) {
	yes := []string{"en_US.UTF-8", "en-US", "en", "en_AU.UTF-8", "en_CA", "es_MX", "ko_KR", "ar"}
	no := []string{"", "C", "POSIX", "en_GB.UTF-8", "de_DE.UTF-8", "fr_FR", "fr_CA", "es_ES", "ja_JP.UTF-8"}
	for _, tag := range yes {
		if !PreferHour12(tag) {
			t.Errorf("%s should be 12-hour", tag)
		}
	}
	for _, tag := range no {
		if PreferHour12(tag) {
			t.Errorf("%s should be 24-hour", tag)
		}
	}
}
