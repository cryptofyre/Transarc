package human

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
)

// When writes a clock time in words. hour12 follows the locale: 3:00 AM, or 03:00.
func When(t time.Time, hour12 bool) string {
	clock := "15:04"
	if hour12 {
		clock = "3:04 PM"
	}
	return t.Format("Monday, January 2, 2006 at " + clock + " MST")
}

// Remain writes a wait in words. Hours and days are rounded to the minute.
func Remain(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d >= time.Hour {
		d = d.Round(time.Minute)
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	secs := (d - mins*time.Minute) / time.Second

	var parts []string
	if days > 0 {
		parts = append(parts, quantity(int(days), "day"))
	}
	if hours > 0 {
		parts = append(parts, quantity(int(hours), "hour"))
	}
	if mins > 0 {
		parts = append(parts, quantity(int(mins), "minute"))
	}
	if len(parts) == 0 || (days == 0 && hours == 0 && secs > 0) {
		parts = append(parts, quantity(int(secs), "second"))
	}
	return strings.Join(parts, " ")
}

func quantity(n int, name string) string {
	if n == 1 {
		return "1 " + name
	}
	return fmt.Sprintf("%d %ss", n, name)
}

// LocaleHour12 reports whether the process locale prefers a 12-hour clock.
// TRANSARC_CLOCK=12 or TRANSARC_CLOCK=24 wins over the locale.
func LocaleHour12() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TRANSARC_CLOCK"))) {
	case "12", "12h":
		return true
	case "24", "24h":
		return false
	}
	for _, key := range []string{"LC_TIME", "LC_ALL", "LANG"} {
		if v := os.Getenv(key); v != "" {
			return PreferHour12(v)
		}
	}
	return false
}

// PreferHour12 reports the CLDR preferred hour cycle for a POSIX locale tag.
// The world default is 24-hour. Region 001 is 24-hour. en and ar with no
// region follow their default locales, which are 12-hour.
func PreferHour12(locale string) bool {
	lang, region := splitLocale(locale)
	if lang == "" {
		return false
	}
	if region != "" {
		switch lang + "-" + region {
		case "fr-CA":
			return false
		case "en-001", "ar-001":
			return true
		}
		return regionHour12(region)
	}
	switch lang {
	case "en", "ar", "ko":
		return true
	default:
		return false
	}
}

func splitLocale(locale string) (lang, region string) {
	locale = strings.TrimSpace(locale)
	if i := strings.IndexAny(locale, ".@"); i >= 0 {
		locale = locale[:i]
	}
	locale = strings.ReplaceAll(locale, "_", "-")
	if locale == "" || strings.EqualFold(locale, "C") || strings.EqualFold(locale, "POSIX") {
		return "", ""
	}
	parts := strings.Split(locale, "-")
	lang = strings.ToLower(parts[0])
	for _, p := range parts[1:] {
		if len(p) == 2 && isLetters(p) {
			region = strings.ToUpper(p)
		} else if len(p) == 3 && isDigits(p) {
			region = p
		}
	}
	return lang, region
}

func isLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// hour12Regions are CLDR 49 territories whose preferred hour cycle is h.
const hour12Regions = " AE AG AL AS AU BB BD BH BM BN BO BS BT CA CO CR CU CY DJ DM DO DZ EC EG EH ER ET FJ FM GD GH GM GR GT GU GY HK HN IN IQ JM JO KH KI KN KP KR KW KY LB LC LR LS LY MH MO MP MR MW MX MY NA NI NZ OM PA PE PG PH PK PR PS PW QA SA SB SD SG SL SO SS SV SY SZ TC TD TN TO TT TW UM US VC VE VG VI VU WS YE "

func regionHour12(region string) bool {
	return strings.Contains(hour12Regions, " "+region+" ")
}
