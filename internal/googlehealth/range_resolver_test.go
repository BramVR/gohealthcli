package googlehealth

import (
	"strings"
	"testing"
	"time"
)

func TestResolveRangeNamedBoundariesByTarget(t *testing.T) {
	t.Parallel()
	resolvedAt := time.Date(2026, 3, 30, 10, 15, 30, 0, time.UTC)

	tests := []struct {
		name     string
		target   RangeTarget
		from     string
		to       string
		wantFrom string
		wantTo   string
	}{
		{
			name:     "physical",
			target:   RangeTargetPhysical,
			from:     "yesterday",
			to:       "now",
			wantFrom: "2026-03-28T23:00:00Z",
			wantTo:   "2026-03-30T10:15:30Z",
		},
		{
			name:     "civil",
			target:   RangeTargetCivil,
			from:     "today",
			to:       "now",
			wantFrom: "2026-03-30T00:00:00",
			wantTo:   "2026-03-30T12:15:30",
		},
		{
			name:     "daily",
			target:   RangeTargetDaily,
			from:     "yesterday",
			to:       "today",
			wantFrom: "2026-03-29",
			wantTo:   "2026-03-30",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveRange(test.from, test.to, "Europe/Brussels", resolvedAt, test.target)
			if err != nil {
				t.Fatalf("ResolveRange: %v", err)
			}
			if got.From != test.wantFrom || got.To != test.wantTo {
				t.Fatalf("range = %q..%q, want %q..%q", got.From, got.To, test.wantFrom, test.wantTo)
			}
			if got.Timezone != "Europe/Brussels" {
				t.Errorf("timezone = %q, want Europe/Brussels", got.Timezone)
			}
			if !got.ResolvedAt.Equal(resolvedAt) {
				t.Errorf("resolved_at = %s, want %s", got.ResolvedAt, resolvedAt)
			}
			if !got.FromNamed || !got.ToNamed {
				t.Errorf("named provenance = from:%v to:%v, want both true", got.FromNamed, got.ToNamed)
			}
		})
	}
}

func TestResolveRangeDefaultsEmptyTimezoneAndToToUTCNow(t *testing.T) {
	t.Parallel()
	resolvedAt := time.Date(2026, 6, 8, 12, 34, 56, 0, time.FixedZone("input", 2*60*60))

	got, err := ResolveRange("today", "", "", resolvedAt, RangeTargetPhysical)
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	if got.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", got.Timezone)
	}
	if got.From != "2026-06-08T00:00:00Z" || got.To != "2026-06-08T10:34:56Z" {
		t.Errorf("range = %q..%q, want UTC today..now", got.From, got.To)
	}
}

func TestResolveRangePreservesExplicitBoundaries(t *testing.T) {
	t.Parallel()
	resolvedAt := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	const from = "2026-06-07T03:30:00+05:30"
	const to = "2026-06-08"

	got, err := ResolveRange(from, to, "America/New_York", resolvedAt, RangeTargetPhysical)
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	if got.From != from || got.To != to {
		t.Fatalf("range = %q..%q, want explicit inputs unchanged", got.From, got.To)
	}
	if got.FromNamed || got.ToNamed {
		t.Errorf("named provenance = from:%v to:%v, want both false", got.FromNamed, got.ToNamed)
	}
}

func TestResolveRangeCalendarYesterdaySpansDST(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		resolvedAt time.Time
		want       time.Duration
	}{
		{
			name:       "spring forward is 23 hours",
			resolvedAt: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
			want:       23 * time.Hour,
		},
		{
			name:       "fall back is 25 hours",
			resolvedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
			want:       25 * time.Hour,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveRange("yesterday", "today", "Europe/Brussels", test.resolvedAt, RangeTargetPhysical)
			if err != nil {
				t.Fatalf("ResolveRange: %v", err)
			}
			from, err := time.Parse(time.RFC3339, got.From)
			if err != nil {
				t.Fatalf("parse from: %v", err)
			}
			to, err := time.Parse(time.RFC3339, got.To)
			if err != nil {
				t.Fatalf("parse to: %v", err)
			}
			if duration := to.Sub(from); duration != test.want {
				t.Fatalf("duration = %s, want %s (%s..%s)", duration, test.want, got.From, got.To)
			}
		})
	}
}

func TestRangeDayStartHandlesMidnightTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		timezone string
		year     int
		month    time.Month
		day      int
		wantUTC  time.Time
		wantHour int
	}{
		{
			name:     "skipped midnight keeps valid date",
			timezone: "America/Sao_Paulo",
			year:     2018,
			month:    time.November,
			day:      4,
			wantUTC:  time.Date(2018, 11, 4, 3, 0, 0, 0, time.UTC),
			wantHour: 1,
		},
		{
			name:     "repeated midnight chooses earliest occurrence",
			timezone: "America/Havana",
			year:     2020,
			month:    time.November,
			day:      1,
			wantUTC:  time.Date(2020, 11, 1, 4, 0, 0, 0, time.UTC),
			wantHour: 0,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			location, err := time.LoadLocation(test.timezone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := rangeDayStart(test.year, test.month, test.day, location)
			if err != nil {
				t.Fatalf("rangeDayStart: %v", err)
			}
			if !got.Equal(test.wantUTC) {
				t.Fatalf("start = %s, want %s", got, test.wantUTC)
			}
			if local := got.In(location); local.Day() != test.day || local.Hour() != test.wantHour {
				t.Fatalf("local start = %s, want day %d hour %d", local, test.day, test.wantHour)
			}
		})
	}
}

func TestResolveRangeRejectsInvalidInputsBeforeEffects(t *testing.T) {
	t.Parallel()
	resolvedAt := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		from     string
		to       string
		timezone string
		want     string
	}{
		{name: "invalid zone", from: "today", to: "now", timezone: "Mars/Olympus_Mons", want: `timezone "Mars/Olympus_Mons"`},
		{name: "unsupported name", from: "tomorrow", to: "now", timezone: "UTC", want: `--from "tomorrow"`},
		{
			name:     "skipped yesterday",
			from:     "yesterday",
			to:       "today",
			timezone: "Pacific/Apia",
			want:     "skipped date 2011-12-30",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			at := resolvedAt
			if test.name == "skipped yesterday" {
				// 2011-12-30T12:00Z is 2011-12-31 in Pacific/Apia,
				// immediately after the zone skipped civil 2011-12-30.
				at = time.Date(2011, 12, 30, 12, 0, 0, 0, time.UTC)
			}
			_, err := ResolveRange(test.from, test.to, test.timezone, at, RangeTargetPhysical)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSyncRangeTargetUsesProviderShape(t *testing.T) {
	t.Parallel()
	daily, err := ParseRollupSpec("daily")
	if err != nil {
		t.Fatal(err)
	}
	hourly, err := ParseRollupSpec("hourly")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		dataType   string
		rollup     *RollupSpec
		reconcile  bool
		wantTarget RangeTarget
	}{
		{name: "physical list", dataType: "heart-rate", wantTarget: RangeTargetPhysical},
		{name: "civil list", dataType: "sleep", wantTarget: RangeTargetCivil},
		{name: "daily list", dataType: "daily-resting-heart-rate", wantTarget: RangeTargetDaily},
		{name: "daily rollup overrides list", dataType: "steps", rollup: &daily, wantTarget: RangeTargetDaily},
		{name: "windowed rollup is physical", dataType: "heart-rate", rollup: &hourly, wantTarget: RangeTargetPhysical},
		{name: "reconcile uses its filter shape", dataType: "steps", reconcile: true, wantTarget: RangeTargetPhysical},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := SyncRangeTarget(test.dataType, test.rollup, test.reconcile)
			if err != nil {
				t.Fatalf("SyncRangeTarget: %v", err)
			}
			if got != test.wantTarget {
				t.Fatalf("target = %q, want %q", got, test.wantTarget)
			}
		})
	}
}

func TestResolveRangePreservesExactBoundaryInstants(t *testing.T) {
	t.Parallel()
	explicit, err := ResolveRange(
		"2026-01-01",
		"2026-01-01T07:00:00Z",
		"America/Los_Angeles",
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		RangeTargetCivil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.FromInstant.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("explicit date instant = %s, want UTC midnight", explicit.FromInstant)
	}
	if !explicit.ToInstant.Equal(time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("explicit RFC3339 instant = %s", explicit.ToInstant)
	}

	foldInstant := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	fold, err := ResolveRange("", "now", "Europe/Brussels", foldInstant, RangeTargetCivil)
	if err != nil {
		t.Fatal(err)
	}
	if fold.To != "2026-10-25T02:30:00" || !fold.ToInstant.Equal(foldInstant) {
		t.Fatalf("fold boundary = %q at %s, want first 02:30 occurrence at %s", fold.To, fold.ToInstant, foldInstant)
	}

	daily, err := ResolveRange(
		"",
		"today",
		"America/Sao_Paulo",
		time.Date(2018, 11, 4, 12, 0, 0, 0, time.UTC),
		RangeTargetDaily,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !daily.ToInstant.Equal(time.Date(2018, 11, 4, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("named daily instant = %s, want first valid instant after skipped midnight", daily.ToInstant)
	}

	fractionalInstant := time.Date(2026, 1, 1, 12, 0, 0, 900_000_000, time.UTC)
	fractional, err := ResolveRange("", "now", "UTC", fractionalInstant, RangeTargetPhysical)
	if err != nil {
		t.Fatal(err)
	}
	if fractional.To != "2026-01-01T12:00:00.9Z" || !fractional.ToInstant.Equal(fractionalInstant) {
		t.Fatalf("fractional physical now = %q at %s", fractional.To, fractional.ToInstant)
	}

	dailyNow, err := ResolveRange("", "now", "America/Sao_Paulo", time.Date(2018, 11, 4, 12, 0, 0, 0, time.UTC), RangeTargetDaily)
	if err != nil {
		t.Fatal(err)
	}
	if !dailyNow.ToInstant.Equal(time.Date(2018, 11, 4, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("daily now instant = %s, want exact local day start", dailyNow.ToInstant)
	}
}
