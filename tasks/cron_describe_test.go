package tasks

import "testing"

// Goldens below were generated from github.com/lnquy/cron v1.1.1 (English
// locale, default options) by feeding it each expression -- see
// describeCron5's doc comment for why: this is a behaviour-preserving
// swap for every shape covered here. The "@" shorthands aren't accepted
// by lnquy/cron directly (it requires 5 explicit fields), so their
// goldens were generated from the standard 5-field expression robfig/cron
// expands each one to (see cronShorthands in cron_describe.go, which
// mirrors robfig/cron's own parseDescriptor mapping).
func TestDescribeCron5_Table(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		// minute/hour: star, step, single, list
		{"* * * * *", "Every minute"},
		{"*/5 * * * *", "Every 5 minutes"},
		{"*/15 * * * *", "Every 15 minutes"},
		{"0 * * * *", "Every hour"},
		{"*/2 * * * *", "Every 2 minutes"},
		{"15 * * * *", "At 15 minutes past the hour"},
		{"5,10,15 * * * *", "At 5, 10, and 15 minutes past the hour"},
		{"10-40/5 * * * *", "Every 5 minutes, minutes 10 through 40 past the hour"},

		// exact times (12-hour clock, matching cron-expression-descriptor's
		// own default)
		{"30 9 * * *", "At 09:30 AM"},
		{"0 3 * * *", "At 03:00 AM"},
		{"0 0 * * *", "At 12:00 AM"},
		{"0 12 * * *", "At 12:00 PM"},
		{"1 0 * * *", "At 12:01 AM"},
		{"0 9,17 * * *", "At 09:00 AM and 05:00 PM"},
		{"30 9,17 * * *", "At 09:30 AM and 05:30 PM"},

		// hour ranges
		{"0 8-10 * * *", "Every hour, between 08:00 AM and 10:59 AM"},
		{"0 9-17 * * *", "Every hour, between 09:00 AM and 05:59 PM"},
		{"* 8-10 * * *", "Every minute, between 08:00 AM and 10:59 AM"},
		{"*/15 9 * * *", "Every 15 minutes, between 09:00 AM and 09:59 AM"},
		{"*/15 9-17 * * 1-5", "Every 15 minutes, between 09:00 AM and 05:59 PM, Monday through Friday"},

		// day-of-week: numeric, names, Sunday=0/7, ranges, lists
		{"30 9 * * 1", "At 09:30 AM, only on Monday"},
		{"30 9 * * MON", "At 09:30 AM, only on Monday"},
		{"0 12 * * 0", "At 12:00 PM, only on Sunday"},
		{"0 12 * * 7", "At 12:00 PM, only on Sunday"},
		{"0 12 * * SUN", "At 12:00 PM, only on Sunday"},
		{"0 9 * * 1-5", "At 09:00 AM, Monday through Friday"},
		{"0 9 * * MON-FRI", "At 09:00 AM, Monday through Friday"},
		{"0 9 * * 2", "At 09:00 AM, only on Tuesday"},
		{"0 22 * * 1-5", "At 10:00 PM, Monday through Friday"},
		{"0 0 * * 0", "At 12:00 AM, only on Sunday"},
		{"0 0 * * 6,0", "At 12:00 AM, only on Saturday and Sunday"},
		{"0 6 * * 1,3,5", "At 06:00 AM, only on Monday, Wednesday, and Friday"},

		// day-of-month: single, list, step
		{"0 0 1 * *", "At 12:00 AM, on day 1 of the month"},
		{"0 0 15 * *", "At 12:00 AM, on day 15 of the month"},
		{"0 0 1,15 * *", "At 12:00 AM, on day 1 and 15 of the month"},
		{"0 0 */2 * *", "At 12:00 AM, every 2 days"},
		{"15 14 1 * *", "At 02:15 PM, on day 1 of the month"},

		// month: single (numeric and name), list
		{"0 0 * 1 *", "At 12:00 AM, only in January"},
		{"0 0 * JAN *", "At 12:00 AM, only in January"},
		{"0 0 1 6,12 *", "At 12:00 AM, on day 1 of the month, only in June and December"},
		{"0 0 1 3,6,9,12 *", "At 12:00 AM, on day 1 of the month, only in March, June, September, and December"},
		{"0 0 29 2 *", "At 12:00 AM, on day 29 of the month, only in February"},

		// dom + month + dow all combined
		{"0 0 1 1 *", "At 12:00 AM, on day 1 of the month, only in January"},

		// robfig/cron shorthands (see cronShorthands)
		{"@hourly", "Every hour"},
		{"@daily", "At 12:00 AM"},
		{"@weekly", "At 12:00 AM, only on Sunday"},
		{"@monthly", "At 12:00 AM, on day 1 of the month"},
		{"@yearly", "At 12:00 AM, on day 1 of the month, only in January"},
		{"@annually", "At 12:00 AM, on day 1 of the month, only in January"},
		{"@midnight", "At 12:00 AM"},

		// case-insensitivity and whitespace
		{"30 9 * * mon", "At 09:30 AM, only on Monday"},
		{"0 0 * jan *", "At 12:00 AM, only in January"},
		{"  */5   *   *   *   * ", "Every 5 minutes"},
	}

	for _, c := range cases {
		got, err := describeCron5(c.expr)
		if err != nil {
			t.Errorf("describeCron5(%q): unexpected error: %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("describeCron5(%q):\n  got  %q\n  want %q", c.expr, got, c.want)
		}
	}
}

// Shapes describeCron5 deliberately doesn't render -- it returns an error
// so DescribeCron's existing fallback shows the raw expression instead of
// guessing wrong. Covers the grammar explicitly out of scope (L/W/#/?
// modifiers, 6-field seconds expressions) plus a few combinations this
// small describer doesn't have a clause for (e.g. a stepped hour range
// alongside a fixed minute).
func TestDescribeCron5_Unsupported(t *testing.T) {
	cases := []string{
		"0 0 L * *",        // last day of month
		"0 0 * * 5L",       // last Friday of month
		"0 0 15W * *",      // nearest weekday to the 15th
		"0 0 * * 5#3",      // third Friday of month
		"0 0 ? * MON",      // "?" wildcard
		"0 30 10-12 * * *", // 6-field (seconds) expression
		"not a cron expr",
		"",
		"* * * *",        // 4 fields
		"0 9-17/2 * * *", // stepped hour range: no clause for this shape
		"1-30 * * * *",   // plain (non-full, non-stepped) minute range
	}
	for _, expr := range cases {
		if got, err := describeCron5(expr); err == nil {
			t.Errorf("describeCron5(%q): expected error, got %q", expr, got)
		}
	}
}

func TestDescribeCron5_InvalidFieldCount(t *testing.T) {
	if _, err := describeCron5("* * * * * *"); err == nil {
		t.Fatal("expected error for a 6-field expression")
	}
}
