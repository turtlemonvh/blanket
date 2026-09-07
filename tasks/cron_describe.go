package tasks

import (
	"fmt"
	"strconv"
	"strings"
)

// describeCron5 is a deliberately small English renderer for the standard
// 5-field cron expression shapes blanket's schedule editor produces and
// the common hand-written ones (a plain "*", "*/n" steps, "a-b" ranges,
// "a-b/n" stepped ranges, "a,b,c" lists, month/day-of-week names, and the
// "@hourly"/"@daily"/"@weekly"/"@monthly"/"@yearly"/"@annually"/"@midnight"
// shorthands robfig/cron's parser recognizes). Anything outside that --
// "L"/"W"/"#" modifiers, a 6-field seconds expression, "?", wraparound or
// stepped day-of-week/month ranges, and a handful of other combinations
// this function doesn't render with confidence -- returns an error rather
// than guessing, so DescribeCron's existing fallback shows the raw
// expression instead of a wrong description.
//
// It replaces github.com/lnquy/cron (the Go port of
// cron-expression-descriptor, a.k.a. cRonstrue), which covers far more of
// the grammar (L/W/# modifiers, seconds, locales, a 24-hour option); if
// blanket ever needs that broader coverage again, that is the reference
// implementation to reach for (turtlemonvh/blanket#146). The output
// wording here was matched by hand against lnquy/cron v1.1.1's English
// locale for every shape this function supports, so this is a
// behaviour-preserving swap for those shapes -- see cron_describe_test.go.
func describeCron5(expr string) (string, error) {
	e := expr
	if exp, ok := cronShorthands[strings.ToLower(strings.TrimSpace(expr))]; ok {
		e = exp
	}

	parts := strings.Fields(e)
	if len(parts) != 5 {
		return "", fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}

	minF, err := parseField(parts[0], 0, 59, nil, false)
	if err != nil {
		return "", fmt.Errorf("minute field: %w", err)
	}
	hourF, err := parseField(parts[1], 0, 23, nil, false)
	if err != nil {
		return "", fmt.Errorf("hour field: %w", err)
	}
	domF, err := parseField(parts[2], 1, 31, nil, false)
	if err != nil {
		return "", fmt.Errorf("day-of-month field: %w", err)
	}
	monF, err := parseField(parts[3], 1, 12, monthNames3, false)
	if err != nil {
		return "", fmt.Errorf("month field: %w", err)
	}
	dowF, err := parseField(parts[4], 0, 7, dayNames3, true)
	if err != nil {
		return "", fmt.Errorf("day-of-week field: %w", err)
	}

	timeClause, err := describeTime(minF, hourF)
	if err != nil {
		return "", err
	}
	domClause, err := describeDom(domF)
	if err != nil {
		return "", err
	}
	monClause, err := describeMonthField(monF)
	if err != nil {
		return "", err
	}
	dowClause, err := describeDow(dowF)
	if err != nil {
		return "", err
	}

	clauses := make([]string, 0, 4)
	clauses = append(clauses, timeClause)
	for _, c := range []string{domClause, monClause, dowClause} {
		if c != "" {
			clauses = append(clauses, c)
		}
	}
	return strings.Join(clauses, ", "), nil
}

// cronShorthands mirrors robfig/cron's parseDescriptor mapping (parser.go),
// expanding each "@" shorthand to the standard 5-field expression it's
// equivalent to. blanket's own cronParser (tasks/schedule.go) is built
// without cron.Descriptor, so it currently rejects these before
// DescribeCron ever reaches describeCron5 -- this map exists so
// describeCron5 renders them correctly on its own if that ever changes,
// and so it can be exercised directly in tests.
var cronShorthands = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// fieldKind classifies a single parsed cron field into the shapes
// describeCron5 knows how to render.
type fieldKind int

const (
	kindStar      fieldKind = iota
	kindStep                // "*/n", or "a-b/n"/"a/n" covering the field's full range
	kindRangeStep           // "a-b/n" not covering the field's full range
	kindRange               // "a-b" not covering the field's full range
	kindSingle
	kindList
)

type field struct {
	kind       fieldKind
	n          int // step (kindStep), or the value (kindSingle)
	start, end int // kindRange, kindRangeStep
	list       []int
}

// parseField parses one cron field (already comma-split by the caller is
// NOT assumed -- lists are handled here) against [min, max], resolving
// names via `names` (nil for purely numeric fields). dow is true only for
// the day-of-week field, where 7 is an accepted alias for 0 (Sunday) --
// both robfig/cron and lnquy/cron treat them the same.
func parseField(raw string, min, max int, names map[string]int, dow bool) (field, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return field{}, fmt.Errorf("empty field")
	}
	if strings.ContainsAny(raw, "?LWlw#") {
		return field{}, fmt.Errorf("unsupported syntax %q (L/W/#/? modifiers aren't rendered by this describer)", raw)
	}

	starMax := max
	if dow {
		starMax = 6 // the "full range" a bare "*" or "0-6"/"0-7" covers
	}

	parseValue := func(tok string) (int, error) {
		tok = strings.TrimSpace(tok)
		if names != nil {
			if v, ok := names[strings.ToUpper(tok)]; ok {
				return v, nil
			}
		}
		v, err := strconv.Atoi(tok)
		if err != nil {
			return 0, fmt.Errorf("invalid value %q", tok)
		}
		if dow && v == 7 {
			v = 0
		}
		if v < min || v > max {
			return 0, fmt.Errorf("value %d out of range [%d,%d]", v, min, max)
		}
		return v, nil
	}

	if strings.Contains(raw, ",") {
		toks := strings.Split(raw, ",")
		vals := make([]int, 0, len(toks))
		for _, t := range toks {
			v, err := parseValue(t)
			if err != nil {
				return field{}, err
			}
			vals = append(vals, v)
		}
		return field{kind: kindList, list: vals}, nil
	}

	if raw == "*" {
		return field{kind: kindStar}, nil
	}

	if strings.HasPrefix(raw, "*/") {
		n, err := strconv.Atoi(raw[2:])
		if err != nil || n <= 0 {
			return field{}, fmt.Errorf("invalid step %q", raw)
		}
		return field{kind: kindStep, n: n}, nil
	}

	if strings.Contains(raw, "/") {
		lr := strings.SplitN(raw, "/", 2)
		n, err := strconv.Atoi(lr[1])
		if err != nil || n <= 0 {
			return field{}, fmt.Errorf("invalid step %q", raw)
		}
		var start, end int
		if strings.Contains(lr[0], "-") {
			ab := strings.SplitN(lr[0], "-", 2)
			start, err = parseValue(ab[0])
			if err != nil {
				return field{}, err
			}
			end, err = parseValue(ab[1])
			if err != nil {
				return field{}, err
			}
		} else {
			// "a/n" means "starting at a, step n through the field's max"
			// (robfig/cron's own reading of the POSIX form).
			start, err = parseValue(lr[0])
			if err != nil {
				return field{}, err
			}
			end = starMax
		}
		if end < start {
			return field{}, fmt.Errorf("unsupported wraparound range %q", raw)
		}
		if start == min && end == starMax {
			return field{kind: kindStep, n: n}, nil
		}
		return field{kind: kindRangeStep, start: start, end: end, n: n}, nil
	}

	if strings.Contains(raw, "-") {
		ab := strings.SplitN(raw, "-", 2)
		start, err := parseValue(ab[0])
		if err != nil {
			return field{}, err
		}
		end, err := parseValue(ab[1])
		if err != nil {
			return field{}, err
		}
		if end < start {
			return field{}, fmt.Errorf("unsupported wraparound range %q", raw)
		}
		if start == min && end == starMax {
			return field{kind: kindStar}, nil
		}
		return field{kind: kindRange, start: start, end: end}, nil
	}

	v, err := parseValue(raw)
	if err != nil {
		return field{}, err
	}
	return field{kind: kindSingle, n: v}, nil
}

// describeTime renders the combined minute+hour clause, the one place
// this describer looks at two fields together (matching how a reader
// says a time out loud, e.g. "every 15 minutes, between 9am and 5pm"
// rather than two separate clauses).
func describeTime(min, hour field) (string, error) {
	switch {
	case min.kind == kindStar && hour.kind == kindStar:
		return "Every minute", nil

	case min.kind == kindStep && hour.kind == kindStar:
		if min.n == 1 {
			return "Every minute", nil
		}
		return fmt.Sprintf("Every %d minutes", min.n), nil

	case min.kind == kindRangeStep && hour.kind == kindStar:
		return fmt.Sprintf("Every %d minutes, minutes %d through %d past the hour", min.n, min.start, min.end), nil

	case min.kind == kindSingle && min.n == 0 && hour.kind == kindStar:
		return "Every hour", nil

	case min.kind == kindSingle && hour.kind == kindStar:
		return fmt.Sprintf("At %d minutes past the hour", min.n), nil

	case min.kind == kindList && hour.kind == kindStar:
		return fmt.Sprintf("At %s minutes past the hour", joinInts(min.list)), nil

	case min.kind == kindSingle && hour.kind == kindSingle:
		return "At " + formatClock(hour.n, min.n), nil

	case min.kind == kindSingle && hour.kind == kindList:
		times := make([]string, len(hour.list))
		for i, h := range hour.list {
			times[i] = formatClock(h, min.n)
		}
		return "At " + joinEnglish(times), nil

	case min.kind == kindStar && hour.kind == kindRange:
		return fmt.Sprintf("Every minute, between %s and %s", formatClock(hour.start, 0), formatClock(hour.end, 59)), nil

	case min.kind == kindSingle && min.n == 0 && hour.kind == kindRange:
		return fmt.Sprintf("Every hour, between %s and %s", formatClock(hour.start, 0), formatClock(hour.end, 59)), nil

	case min.kind == kindStep && hour.kind == kindRange:
		return fmt.Sprintf("Every %d minutes, between %s and %s", min.n, formatClock(hour.start, 0), formatClock(hour.end, 59)), nil

	case min.kind == kindStep && hour.kind == kindSingle:
		return fmt.Sprintf("Every %d minutes, between %s and %s", min.n, formatClock(hour.n, 0), formatClock(hour.n, 59)), nil

	default:
		return "", fmt.Errorf("unsupported minute/hour combination")
	}
}

// describeDom renders the day-of-month clause, or "" when it's "*" (no
// clause needed).
func describeDom(f field) (string, error) {
	switch f.kind {
	case kindStar:
		return "", nil
	case kindSingle:
		return fmt.Sprintf("on day %d of the month", f.n), nil
	case kindList:
		return fmt.Sprintf("on day %s of the month", joinInts(f.list)), nil
	case kindStep:
		return fmt.Sprintf("every %d days", f.n), nil
	default:
		return "", fmt.Errorf("unsupported day-of-month field shape")
	}
}

// describeMonthField renders the month clause, or "" for "*".
func describeMonthField(f field) (string, error) {
	switch f.kind {
	case kindStar:
		return "", nil
	case kindSingle:
		return "only in " + monthName(f.n), nil
	case kindList:
		names := make([]string, len(f.list))
		for i, v := range f.list {
			names[i] = monthName(v)
		}
		return "only in " + joinEnglish(names), nil
	default:
		return "", fmt.Errorf("unsupported month field shape")
	}
}

// describeDow renders the day-of-week clause, or "" for "*". A plain
// range (e.g. "1-5") reads as "Monday through Friday" with no "only on"
// prefix -- matching how a reader would say it, and lnquy/cron's own
// wording for this shape.
func describeDow(f field) (string, error) {
	switch f.kind {
	case kindStar:
		return "", nil
	case kindSingle:
		return "only on " + dayName(f.n), nil
	case kindList:
		names := make([]string, len(f.list))
		for i, v := range f.list {
			names[i] = dayName(v)
		}
		return "only on " + joinEnglish(names), nil
	case kindRange:
		return dayName(f.start) + " through " + dayName(f.end), nil
	default:
		return "", fmt.Errorf("unsupported day-of-week field shape")
	}
}

// formatClock renders hour:minute (24-hour, hour in [0,23]) as a
// zero-padded 12-hour clock string, e.g. (9, 30) -> "09:30 AM",
// (0, 0) -> "12:00 AM", (12, 0) -> "12:00 PM" -- matching
// cron-expression-descriptor's own default (12-hour clock) wording.
func formatClock(hour, minute int) string {
	h12 := hour % 12
	if h12 == 0 {
		h12 = 12
	}
	ampm := "AM"
	if hour >= 12 {
		ampm = "PM"
	}
	return fmt.Sprintf("%02d:%02d %s", h12, minute, ampm)
}

// joinInts renders []int (in the order given, matching lnquy/cron's own
// behaviour of preserving the expression's field order rather than
// sorting it) as an English list, e.g. [1] -> "1", [1,15] -> "1 and 15",
// [5,10,15] -> "5, 10, and 15" (Oxford comma).
func joinInts(vals []int) string {
	strs := make([]string, len(vals))
	for i, v := range vals {
		strs[i] = strconv.Itoa(v)
	}
	return joinEnglish(strs)
}

// joinEnglish joins items the way a person reads a list aloud: "a",
// "a and b", or "a, b, and c" (Oxford comma) for three or more.
func joinEnglish(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// monthNames3 maps three-letter month abbreviations (JAN..DEC) to their
// 1-12 numeric value, for parseField's name resolution.
var monthNames3 = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

// monthFullNames renders a 1-12 month number as its full English name.
var monthFullNames = [...]string{
	"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

func monthName(n int) string {
	if n < 1 || n > 12 {
		return strconv.Itoa(n)
	}
	return monthFullNames[n]
}

// dayNames3 maps three-letter day-of-week abbreviations (SUN..SAT) to
// their 0-6 numeric value (Sunday = 0), for parseField's name resolution.
var dayNames3 = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// dayFullNames renders a 0-6 day-of-week number as its full English name.
var dayFullNames = [...]string{
	"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
}

func dayName(n int) string {
	if n < 0 || n > 6 {
		return strconv.Itoa(n)
	}
	return dayFullNames[n]
}
