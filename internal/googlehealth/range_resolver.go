package googlehealth

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

// RangeTarget describes the timestamp shape required by the selected
// Google Health endpoint.
type RangeTarget string

const (
	RangeTargetPhysical RangeTarget = "physical"
	RangeTargetCivil    RangeTarget = "civil"
	RangeTargetDaily    RangeTarget = "daily"
)

// ResolvedRange is the immutable result of resolving one sync invocation's
// requested boundaries against one captured clock instant.
type ResolvedRange struct {
	From        string
	To          string
	Timezone    string
	ResolvedAt  time.Time
	FromNamed   bool
	ToNamed     bool
	FromInstant time.Time
	ToInstant   time.Time
}

// SyncRangeTarget derives the range shape from the endpoint the planner will
// call. Rollups have an endpoint-defined shape; Data Point list/reconcile
// calls derive it from their catalog filter field.
func SyncRangeTarget(dataType string, rollup *RollupSpec, reconcile bool) (RangeTarget, error) {
	if rollup != nil {
		if rollup.endpointFamily == endpointFamilyDailyRollUp {
			return RangeTargetDaily, nil
		}
		return RangeTargetPhysical, nil
	}
	family := endpointFamilyList
	if reconcile {
		family = endpointFamilyReconcile
	}
	field, err := googleHealthDataTypeFilterField(dataType, family)
	if err != nil {
		return "", err
	}
	switch {
	case strings.HasSuffix(field, ".date"):
		return RangeTargetDaily, nil
	case strings.Contains(field, ".civil_"):
		return RangeTargetCivil, nil
	default:
		return RangeTargetPhysical, nil
	}
}

// ResolveRange resolves exactly now/today/yesterday in an explicit IANA
// location. Existing civil-date and RFC3339 inputs pass through byte-for-byte;
// downstream endpoint normalization therefore retains its established rules.
// An empty --to is the historical "now" default and an empty --from remains
// the cursor-resume signal.
func ResolveRange(from, to, timezone string, resolvedAt time.Time, target RangeTarget) (ResolvedRange, error) {
	return resolveRange("sync", from, to, timezone, resolvedAt, target)
}

// ResolveRawRange applies the same range grammar and target rendering as
// ResolveRange while keeping raw's user-facing validation context accurate.
func ResolveRawRange(from, to, timezone string, resolvedAt time.Time, target RangeTarget) (ResolvedRange, error) {
	return resolveRange("raw", from, to, timezone, resolvedAt, target)
}

func resolveRange(command, from, to, timezone string, resolvedAt time.Time, target RangeTarget) (ResolvedRange, error) {
	locationName, location, err := loadTimezone(timezone)
	if err != nil {
		return ResolvedRange{}, fmt.Errorf("%s --%w", command, err)
	}
	if to == "" {
		to = "now"
	}
	resolvedFrom, fromInstant, fromNamed, err := resolveRangeBoundary(command, from, "--from", location, resolvedAt, target)
	if err != nil {
		return ResolvedRange{}, err
	}
	resolvedTo, toInstant, toNamed, err := resolveRangeBoundary(command, to, "--to", location, resolvedAt, target)
	if err != nil {
		return ResolvedRange{}, err
	}
	return ResolvedRange{
		From:        resolvedFrom,
		To:          resolvedTo,
		Timezone:    locationName,
		ResolvedAt:  resolvedAt,
		FromNamed:   fromNamed,
		ToNamed:     toNamed,
		FromInstant: fromInstant,
		ToInstant:   toInstant,
	}, nil
}

// ValidateTimezone validates the shared config/flag timezone contract without
// consulting the machine's local timezone. An empty value is the implicit UTC
// fallback; callers that distinguish an explicitly empty value reject it first.
func ValidateTimezone(timezone string) error {
	_, _, err := loadTimezone(timezone)
	return err
}

func loadTimezone(timezone string) (string, *time.Location, error) {
	locationName := timezone
	if locationName == "" {
		locationName = "UTC"
	}
	if locationName == "Local" {
		return "", nil, fmt.Errorf("timezone %q is not an explicit IANA timezone", locationName)
	}
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return "", nil, fmt.Errorf("timezone %q: load IANA timezone: %w", locationName, err)
	}
	return locationName, location, nil
}

func resolveRangeBoundary(
	command, value, flag string,
	location *time.Location,
	resolvedAt time.Time,
	target RangeTarget,
) (string, time.Time, bool, error) {
	if value == "" {
		return "", time.Time{}, false, nil
	}
	if parsed, ok := ParseRangeBoundary(value); ok {
		return value, parsed, false, nil
	}

	localNow := resolvedAt.In(location)
	switch value {
	case "now":
		canonicalNow := localNow
		switch target {
		case RangeTargetCivil:
			canonicalNow = localNow.Truncate(time.Second)
		case RangeTargetDaily:
			var err error
			canonicalNow, err = rangeDayStart(localNow.Year(), localNow.Month(), localNow.Day(), location)
			if err != nil {
				return "", time.Time{}, true, fmt.Errorf("%s %s now: %w", command, flag, err)
			}
		}
		return renderNamedBoundary(canonicalNow, target), canonicalNow, true, nil
	case "today":
		start, err := rangeDayStart(localNow.Year(), localNow.Month(), localNow.Day(), location)
		if err != nil {
			return "", time.Time{}, true, fmt.Errorf("%s %s today: %w", command, flag, err)
		}
		return renderNamedBoundary(start, target), start, true, nil
	case "yesterday":
		year, month, day := localNow.Date()
		previous := time.Date(year, month, day-1, 12, 0, 0, 0, time.UTC)
		start, err := rangeDayStart(previous.Year(), previous.Month(), previous.Day(), location)
		if err != nil {
			return "", time.Time{}, true, fmt.Errorf("%s %s yesterday: %w", command, flag, err)
		}
		return renderNamedBoundary(start, target), start, true, nil
	default:
		return "", time.Time{}, false, fmt.Errorf(
			"%s %s %q: expected now, today, yesterday, YYYY-MM-DD, or RFC3339",
			command, flag, value,
		)
	}
}

// IsNamedRangeBoundary reports whether value is one of the three relative
// boundary tokens accepted by ResolveRange.
func IsNamedRangeBoundary(value string) bool {
	switch value {
	case "now", "today", "yesterday":
		return true
	default:
		return false
	}
}

func rangeDayStart(year int, month time.Month, day int, location *time.Location) (time.Time, error) {
	// Model the requested civil day as a UTC-shaped wall-clock interval.
	// For each constant-offset zone interval nearby, translate that wall
	// interval back to real instants and take the earliest non-empty
	// intersection. This handles skipped/repeated midnight transitions:
	// a date beginning at 01:00 remains valid, a repeated 00:00 chooses its
	// first occurrence, and only a wholly absent civil date has no match.
	civilStart := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	civilEnd := civilStart.AddDate(0, 0, 1)
	const searchMargin = 48 * time.Hour
	searchStart := civilStart.Add(-searchMargin)
	searchEnd := civilEnd.Add(searchMargin)

	var earliest time.Time
	for cursor := searchStart; cursor.Before(searchEnd); {
		localCursor := cursor.In(location)
		_, offsetSeconds := localCursor.Zone()
		zoneStart, zoneEnd := localCursor.ZoneBounds()

		intervalStart := searchStart
		if !zoneStart.IsZero() && zoneStart.After(intervalStart) {
			intervalStart = zoneStart
		}
		intervalEnd := searchEnd
		if !zoneEnd.IsZero() && zoneEnd.Before(intervalEnd) {
			intervalEnd = zoneEnd
		}

		offset := time.Duration(offsetSeconds) * time.Second
		candidateStart := civilStart.Add(-offset)
		candidateEnd := civilEnd.Add(-offset)
		if candidateStart.Before(intervalStart) {
			candidateStart = intervalStart
		}
		if candidateEnd.After(intervalEnd) {
			candidateEnd = intervalEnd
		}
		if candidateStart.Before(candidateEnd) {
			gotYear, gotMonth, gotDay := candidateStart.In(location).Date()
			if gotYear == year && gotMonth == month && gotDay == day &&
				(earliest.IsZero() || candidateStart.Before(earliest)) {
				earliest = candidateStart
			}
		}

		if zoneEnd.IsZero() || !zoneEnd.Before(searchEnd) {
			break
		}
		if !zoneEnd.After(cursor) {
			cursor = cursor.Add(time.Second)
		} else {
			cursor = zoneEnd
		}
	}
	if earliest.IsZero() {
		return time.Time{}, fmt.Errorf(
			"skipped date %04d-%02d-%02d in %s",
			year, month, day, location,
		)
	}
	return earliest.In(location), nil
}

func renderNamedBoundary(value time.Time, target RangeTarget) string {
	switch target {
	case RangeTargetCivil:
		return value.Format("2006-01-02T15:04:05")
	case RangeTargetDaily:
		return value.Format("2006-01-02")
	default:
		return value.UTC().Format(time.RFC3339Nano)
	}
}
