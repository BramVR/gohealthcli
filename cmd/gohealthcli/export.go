package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

type dailyStepsExportRow struct {
	ProviderName          string `json:"provider_name"`
	ConnectionID          string `json:"connection_id"`
	CivilDate             string `json:"civil_date"`
	StepCount             int64  `json:"step_count"`
	SourceKind            string `json:"source_kind"`
	SourceFamilyFilter    string `json:"source_family_filter"`
	SourceRecordCount     int64  `json:"source_record_count"`
	LatestSourceTimestamp string `json:"latest_source_timestamp"`
}

type exportDatasetSpec struct {
	name             string
	view             string
	viewSQL          string
	migrationVersion int
	fields           []exportFieldSpec
	orderBy          string
}

type exportFieldSpec struct {
	name string
	kind string
}

type exportRow []string

// exportDatasetDefinitions is the canonical Normalized View registry.
// It lives next to the export writer for historical reasons (this
// package shipped exports before the Registry concept existed); the
// follow-up PR for #109 (describe-schema --json) splits these into
// per-category files (views_steps.go, views_sleep.go, views_identity.go,
// …) and the Registry becomes the only entry point. Until then, treat
// this slice and `normalizedViewsRegistry()` as the same thing — every
// consumer should go through the Registry, never read the slice
// directly.
var exportDatasetDefinitions = []exportDatasetSpec{
	{
		name:             "daily-steps",
		view:             "daily_steps",
		migrationVersion: 4,
		viewSQL: `WITH data_point_days AS (
			SELECT
				provider_name,
				connection_id,
				COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(end_civil_time, 1, 10), substr(start_time_utc, 1, 10), substr(end_time_utc, 1, 10)) AS civil_date,
				IFNULL(source_family_filter, '') AS source_family_filter,
				SUM(CAST(json_extract(raw_json, '$.steps.count') AS INTEGER)) AS step_count,
				COUNT(*) AS source_record_count,
				MAX(COALESCE(end_time_utc, start_time_utc, updated_at, '')) AS latest_source_timestamp
			FROM data_points
			WHERE data_type = 'steps'
				AND json_extract(raw_json, '$.steps.count') IS NOT NULL
				AND COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(end_civil_time, 1, 10), substr(start_time_utc, 1, 10), substr(end_time_utc, 1, 10)) IS NOT NULL
			GROUP BY provider_name, connection_id, civil_date, source_family_filter
		),
		rollup_days AS (
			SELECT
				provider_name,
				connection_id,
				civil_date,
				'' AS source_family_filter,
				CAST(json_extract(raw_json, '$.steps.countSum') AS INTEGER) AS step_count,
				1 AS source_record_count,
				COALESCE(window_end_utc, window_start_utc, civil_date, updated_at, '') AS latest_source_timestamp
			FROM rollups
			WHERE data_type = 'steps'
				AND rollup_kind = 'dailyRollUp'
				AND civil_date IS NOT NULL
				AND json_extract(raw_json, '$.steps.countSum') IS NOT NULL
		)
		SELECT
			provider_name,
			connection_id,
			civil_date,
			source_family_filter,
			step_count,
			'dailyRollUp' AS source_kind,
			source_record_count,
			latest_source_timestamp
		FROM rollup_days
		UNION ALL
		SELECT
			provider_name,
			connection_id,
			civil_date,
			source_family_filter,
			step_count,
			'dataPoints' AS source_kind,
			source_record_count,
			latest_source_timestamp
		FROM data_point_days
		WHERE NOT EXISTS (
			SELECT 1
			FROM rollup_days
				WHERE rollup_days.provider_name = data_point_days.provider_name
					AND rollup_days.connection_id = data_point_days.connection_id
					AND rollup_days.civil_date = data_point_days.civil_date
					AND rollup_days.source_family_filter = data_point_days.source_family_filter
			)`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "civil_date"},
			{name: "step_count", kind: "int"},
			{name: "source_kind"},
			{name: "source_family_filter"},
			{name: "source_record_count", kind: "int"},
			{name: "latest_source_timestamp"},
		},
		orderBy: "civil_date, provider_name, connection_id, source_kind, source_family_filter",
	},
	{
		name:             "heart-rate-samples",
		view:             "heart_rate_samples",
		migrationVersion: 5,
		orderBy:          "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			CAST(json_extract(raw_json, '$.heartRate.beatsPerMinute') AS TEXT) AS beats_per_minute,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'heart-rate'
			AND record_kind = 'sample'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.heartRate.beatsPerMinute') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "sample_time_utc"},
			{name: "sample_civil_time"},
			{name: "civil_date"},
			{name: "beats_per_minute"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "resting-heart-rate-by-day",
		view:             "resting_heart_rate_by_day",
		migrationVersion: 5,
		orderBy:          "civil_date, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			provider_civil_date AS civil_date,
			CAST(json_extract(raw_json, '$.dailyRestingHeartRate.beatsPerMinute') AS TEXT) AS beats_per_minute,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'daily-resting-heart-rate'
			AND record_kind = 'daily'
			AND provider_civil_date IS NOT NULL
			AND json_extract(raw_json, '$.dailyRestingHeartRate.beatsPerMinute') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "civil_date"},
			{name: "beats_per_minute"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "sleep-sessions",
		view:             "sleep_sessions",
		migrationVersion: 5,
		orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'sleep'
			AND record_kind = 'session'
			AND start_time_utc IS NOT NULL
			AND end_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "exercise-sessions",
		view:             "exercise_sessions",
		migrationVersion: 5,
		orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.exercise.exerciseType'), '') AS exercise_type,
			IFNULL(json_extract(raw_json, '$.exercise.activeDuration'), '') AS active_duration,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'exercise'
			AND record_kind = 'session'
			AND start_time_utc IS NOT NULL
			AND end_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "exercise_type"},
			{name: "active_duration"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		name:             "weight-samples",
		view:             "weight_samples",
		migrationVersion: 5,
		orderBy:          "sample_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			IFNULL(provider_civil_date, '') AS civil_date,
			CAST(json_extract(raw_json, '$.weight.weightGrams') AS TEXT) AS weight_grams,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'weight'
			AND record_kind = 'sample'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.weight.weightGrams') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "sample_time_utc"},
			{name: "sample_civil_time"},
			{name: "civil_date"},
			{name: "weight_grams"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// active_minutes_intervals explodes the activeMinutesByActivityLevel
		// array Google returns under $.activeMinutes — one row per
		// activity-level slice per parent interval.
		name:             "active-minutes-intervals",
		view:             "active_minutes_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id, activity_level",
		viewSQL: `SELECT
			data_points.provider_name,
			data_points.connection_id,
			data_points.start_time_utc,
			data_points.end_time_utc,
			COALESCE(data_points.provider_civil_date, substr(data_points.start_civil_time, 1, 10), substr(data_points.start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(level.value, '$.activityLevel'), '') AS activity_level,
			CAST(json_extract(level.value, '$.activeMinutes') AS INTEGER) AS active_minutes,
			IFNULL(data_points.source_family_filter, '') AS source_family_filter,
			IFNULL(data_points.upstream_resource_name, '') AS upstream_resource_name
		FROM data_points, json_each(data_points.raw_json, '$.activeMinutes.activeMinutesByActivityLevel') AS level
		WHERE data_points.data_type = 'active-minutes'
			AND json_extract(data_points.raw_json, '$.activeMinutes.activeMinutesByActivityLevel') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "activity_level"}, {name: "active_minutes"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// active_zone_minutes_intervals projects $.activeZoneMinutes
		// (one heart-rate-zone + duration per archived interval).
		name:             "active-zone-minutes-intervals",
		view:             "active_zone_minutes_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id, heart_rate_zone",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.activeZoneMinutes.heartRateZone'), '') AS heart_rate_zone,
			CAST(json_extract(raw_json, '$.activeZoneMinutes.activeZoneMinutes') AS INTEGER) AS active_zone_minutes,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'active-zone-minutes'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "heart_rate_zone"}, {name: "active_zone_minutes"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// altitude_intervals exposes Google's $.altitude.gainMillimeters
		// in both raw millimeters and derived meters.
		name:             "altitude-intervals",
		view:             "altitude_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.altitude.gainMillimeters') AS INTEGER) / 1000 AS gain_meters,
			CAST(json_extract(raw_json, '$.altitude.gainMillimeters') AS INTEGER) AS gain_millimeters,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'altitude'
			AND json_extract(raw_json, '$.altitude.gainMillimeters') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "gain_meters"}, {name: "gain_millimeters"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// activity_level_intervals exposes Google's enum
		// $.activityLevel.activityLevelType (SEDENTARY/LIGHT/...) plus
		// a derived duration in seconds.
		name:             "activity-level-intervals",
		view:             "activity_level_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.activityLevel.activityLevelType'), '') AS activity_level_type,
			CAST((strftime('%s', end_time_utc) - strftime('%s', start_time_utc)) AS INTEGER) AS duration_seconds,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'activity-level'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "activity_level_type"}, {name: "duration_seconds"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// sedentary_period_intervals: no scalar; expose the interval as
		// a derived duration in seconds.
		name:             "sedentary-period-intervals",
		view:             "sedentary_period_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST((strftime('%s', end_time_utc) - strftime('%s', start_time_utc)) AS INTEGER) AS duration_seconds,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'sedentary-period'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "duration_seconds"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// time_in_heart_rate_zone_intervals: heartRateZoneType + derived duration.
		name:             "time-in-heart-rate-zone-intervals",
		view:             "time_in_heart_rate_zone_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id, heart_rate_zone_type",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.timeInHeartRateZone.heartRateZoneType'), '') AS heart_rate_zone_type,
			CAST((strftime('%s', end_time_utc) - strftime('%s', start_time_utc)) AS INTEGER) AS duration_seconds,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'time-in-heart-rate-zone'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "heart_rate_zone_type"}, {name: "duration_seconds"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// swim_lengths_data_intervals: strokeCount per interval.
		name:             "swim-lengths-data-intervals",
		view:             "swim_lengths_data_intervals",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.swimLengthsData.strokeCount') AS INTEGER) AS stroke_count,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'swim-lengths-data'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "start_time_utc"}, {name: "end_time_utc"}, {name: "civil_date"},
			{name: "stroke_count"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// vo2_max_samples: the live response stores the scalar as a
		// floating-point number at $.vo2Max.vo2Max (Google's repeated
		// data-type name nesting). Stored as TEXT to preserve precision.
		name:             "vo2-max-samples",
		view:             "vo2_max_samples",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.vo2Max.vo2Max') AS TEXT) AS vo2_max,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'vo2-max'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "vo2_max"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// run_vo2_max_samples: same shape as vo2-max but the scalar
		// lives at $.runVo2Max.runVo2Max (run-specific VO₂ max estimate).
		name:             "run-vo2-max-samples",
		view:             "run_vo2_max_samples",
		migrationVersion: 17,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.runVo2Max.runVo2Max') AS TEXT) AS run_vo2_max,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'run-vo2-max'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "run_vo2_max"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// daily_vo2_max projects archived daily-vo2-max Data Points into
		// one row per civil date with the principal vo2Max scalar, the
		// cardio-fitness-level enum, and the covariance scalar. vo2Max
		// stored as TEXT to preserve floating-point precision; the raw
		// JSON path lives at $.dailyVo2Max.vo2Max (Google's repeated
		// data-type name nesting).
		name:             "daily-vo2-max",
		view:             "daily_vo2_max",
		migrationVersion: 19,
		orderBy:          "civil_date, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			provider_civil_date AS civil_date,
			CAST(json_extract(raw_json, '$.dailyVo2Max.vo2Max') AS TEXT) AS vo2_max,
			IFNULL(json_extract(raw_json, '$.dailyVo2Max.cardioFitnessLevel'), '') AS cardio_fitness_level,
			CAST(json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance') AS TEXT) AS vo2_max_covariance,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'daily-vo2-max'
			AND record_kind = 'daily'
			AND provider_civil_date IS NOT NULL
			AND json_extract(raw_json, '$.dailyVo2Max.vo2Max') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "civil_date"},
			{name: "vo2_max"}, {name: "cardio_fitness_level"}, {name: "vo2_max_covariance"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// daily_heart_rate_zones explodes the heartRateZones[] array
		// Google returns under $.dailyHeartRateZones — one row per
		// per-day zone slice with the enum and the min/max BPM scalars
		// (the live API returns those as strings, hence the CAST).
		name:             "daily-heart-rate-zones",
		view:             "daily_heart_rate_zones",
		migrationVersion: 19,
		orderBy:          "civil_date, provider_name, connection_id, heart_rate_zone_type",
		viewSQL: `SELECT
			data_points.provider_name,
			data_points.connection_id,
			data_points.provider_civil_date AS civil_date,
			IFNULL(json_extract(zone.value, '$.heartRateZoneType'), '') AS heart_rate_zone_type,
			CAST(json_extract(zone.value, '$.minBeatsPerMinute') AS INTEGER) AS min_beats_per_minute,
			CAST(json_extract(zone.value, '$.maxBeatsPerMinute') AS INTEGER) AS max_beats_per_minute,
			IFNULL(data_points.source_family_filter, '') AS source_family_filter,
			IFNULL(data_points.upstream_resource_name, '') AS upstream_resource_name
		FROM data_points, json_each(data_points.raw_json, '$.dailyHeartRateZones.heartRateZones') AS zone
		WHERE data_points.data_type = 'daily-heart-rate-zones'
			AND data_points.record_kind = 'daily'
			AND data_points.provider_civil_date IS NOT NULL
			AND json_extract(data_points.raw_json, '$.dailyHeartRateZones.heartRateZones') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "civil_date"},
			{name: "heart_rate_zone_type"}, {name: "min_beats_per_minute"}, {name: "max_beats_per_minute"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// daily_sleep_temperature_derivations projects the nightly
		// temperature, the baseline, and the relative stddev (all
		// Celsius) for each archived daily Data Point. All scalars
		// stored as TEXT to preserve floating-point precision.
		name:             "daily-sleep-temperature-derivations",
		view:             "daily_sleep_temperature_derivations",
		migrationVersion: 19,
		orderBy:          "civil_date, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			provider_civil_date AS civil_date,
			CAST(json_extract(raw_json, '$.dailySleepTemperatureDerivations.nightlyTemperatureCelsius') AS TEXT) AS nightly_temperature_celsius,
			CAST(json_extract(raw_json, '$.dailySleepTemperatureDerivations.baselineTemperatureCelsius') AS TEXT) AS baseline_temperature_celsius,
			CAST(json_extract(raw_json, '$.dailySleepTemperatureDerivations.relativeNightlyStddev30dCelsius') AS TEXT) AS relative_nightly_stddev_30d_celsius,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'daily-sleep-temperature-derivations'
			AND record_kind = 'daily'
			AND provider_civil_date IS NOT NULL
			AND json_extract(raw_json, '$.dailySleepTemperatureDerivations.nightlyTemperatureCelsius') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "civil_date"},
			{name: "nightly_temperature_celsius"}, {name: "baseline_temperature_celsius"}, {name: "relative_nightly_stddev_30d_celsius"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// respiratory_rate_sleep_summary projects the principal
		// full-sleep breathsPerMinute scalar plus the per-stage
		// (deep/light/REM) scalars Google returns under
		// $.respiratoryRateSleepSummary. All scalars stored as TEXT to
		// preserve floating-point precision.
		name:             "respiratory-rate-sleep-summary",
		view:             "respiratory_rate_sleep_summary",
		migrationVersion: 19,
		orderBy:          "sample_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.respiratoryRateSleepSummary.fullSleepStats.breathsPerMinute') AS TEXT) AS full_sleep_breaths_per_minute,
			CAST(json_extract(raw_json, '$.respiratoryRateSleepSummary.deepSleepStats.breathsPerMinute') AS TEXT) AS deep_sleep_breaths_per_minute,
			CAST(json_extract(raw_json, '$.respiratoryRateSleepSummary.lightSleepStats.breathsPerMinute') AS TEXT) AS light_sleep_breaths_per_minute,
			CAST(json_extract(raw_json, '$.respiratoryRateSleepSummary.remSleepStats.breathsPerMinute') AS TEXT) AS rem_sleep_breaths_per_minute,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'respiratory-rate-sleep-summary'
			AND record_kind = 'sample'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "full_sleep_breaths_per_minute"},
			{name: "deep_sleep_breaths_per_minute"},
			{name: "light_sleep_breaths_per_minute"},
			{name: "rem_sleep_breaths_per_minute"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// floors_intervals projects archived floors interval Data Points
		// into one row per source-interval with civil_date, count, and
		// source attribution. Same pattern as the steps interval flow.
		name:             "floors-intervals",
		view:             "floors_intervals",
		migrationVersion: 16,
		orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.floors.count') AS INTEGER) AS count,
			IFNULL(json_extract(data_source_json, '$.platform'), '') AS source_platform,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'floors'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.floors.count') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "civil_date"},
			{name: "count"},
			{name: "source_platform"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// electrocardiogram_sessions projects archived Tier 2 ECG
		// session Data Points (#104) into one row per session with
		// the classification enum and the average heart-rate scalar.
		// Civil_date prefers the upstream provider_civil_date so a
		// session that straddles midnight in the user's tz lands on
		// its civil day. Stays a row-projection (not an explode) —
		// the upstream payload is one session per Data Point.
		name:             "electrocardiogram-sessions",
		view:             "electrocardiogram_sessions",
		migrationVersion: 20,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.electrocardiogram.classification'), '') AS classification,
			CAST(json_extract(raw_json, '$.electrocardiogram.averageHeartRateBpm') AS INTEGER) AS average_heart_rate_bpm,
			IFNULL(json_extract(data_source_json, '$.platform'), '') AS source_platform,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'electrocardiogram'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "classification"},
			{name: "average_heart_rate_bpm"},
			{name: "source_platform"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// irregular_rhythm_notifications projects archived Tier 2 IRN
		// session Data Points (#104). The upstream payload carries a
		// single classification enum per session — the view exposes
		// that as a categorical column the LLM can filter on without
		// reaching into raw_json.
		name:             "irregular-rhythm-notifications",
		view:             "irregular_rhythm_notifications",
		migrationVersion: 20,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(raw_json, '$.irregularRhythmNotification.classification'), '') AS classification,
			IFNULL(json_extract(data_source_json, '$.platform'), '') AS source_platform,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'irregular-rhythm-notification'
			AND start_time_utc IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "classification"},
			{name: "source_platform"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// hydration_log_sessions projects archived hydration-log session
		// Data Points (#103) into one row per logged volume. Google
		// Health's HydrationLog proto carries the principal volume under
		// $.hydrationLog.volume.liters (double); stored as TEXT to
		// preserve floating-point precision the same way vo2_max_samples
		// does. Civil_date prefers the upstream provider_civil_date so a
		// log entry that straddles midnight in the user's tz lands on
		// its civil day.
		name:             "hydration-log-sessions",
		view:             "hydration_log_sessions",
		migrationVersion: 21,
		orderBy:          "start_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.hydrationLog.volume.liters') AS TEXT) AS volume_liters,
			IFNULL(json_extract(data_source_json, '$.platform'), '') AS source_platform,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'hydration-log'
			AND record_kind = 'session'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.hydrationLog.volume.liters') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "start_civil_time"},
			{name: "end_civil_time"},
			{name: "civil_date"},
			{name: "volume_liters"},
			{name: "source_platform"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// searchable_text is the LLM's one-target free-text needle path.
		// UNIONs categorical text from paired devices, Data Point data
		// source JSON, the latest profile snapshot, and exercise labels,
		// each tagged with a kind discriminator. WHERE text LIKE
		// '%needle%' answers across all four without the caller knowing
		// which underlying column to read. The view name is the stable
		// contract; backing can swap to FTS5 later without affecting
		// prompts that read it.
		name:             "searchable-text",
		view:             "searchable_text",
		migrationVersion: 13,
		orderBy:          "kind, text",
		viewSQL: `WITH latest_profile AS (
	SELECT id, raw_json, ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
	FROM identity_snapshots
	WHERE snapshot_kind = 'profile'
)
SELECT 'device' AS kind, model AS text, 'paired_devices' AS ref_table, connection_id AS ref_id FROM paired_devices WHERE model != ''
UNION ALL
SELECT 'device' AS kind, manufacturer AS text, 'paired_devices' AS ref_table, connection_id AS ref_id FROM paired_devices WHERE manufacturer != ''
UNION ALL
SELECT 'data_source' AS kind, json_extract(data_source_json, '$.applicationName') AS text, 'data_points' AS ref_table, CAST(id AS TEXT) AS ref_id FROM data_points WHERE IFNULL(json_extract(data_source_json, '$.applicationName'), '') != ''
UNION ALL
SELECT 'data_source' AS kind, json_extract(data_source_json, '$.device.displayName') AS text, 'data_points' AS ref_table, CAST(id AS TEXT) AS ref_id FROM data_points WHERE IFNULL(json_extract(data_source_json, '$.device.displayName'), '') != ''
UNION ALL
SELECT 'data_source' AS kind, json_extract(data_source_json, '$.device.model') AS text, 'data_points' AS ref_table, CAST(id AS TEXT) AS ref_id FROM data_points WHERE IFNULL(json_extract(data_source_json, '$.device.model'), '') != ''
UNION ALL
-- The profile kind covers any free-text field Google Health emits in
-- users.getProfile. As of 2026-06 the API only emits 'name' (the
-- resource path), age (number), membership date, and stride lengths;
-- the user's first/last name is NOT in the response. firstName /
-- lastName extractions stay in case Google adds them later — they're
-- harmless on current data (filtered out by the != '' guard). Limited
-- to the latest profile snapshot per Connection so historical name
-- values from older snapshots don't pollute search.
SELECT 'profile' AS kind, json_extract(raw_json, '$.firstName') AS text, 'identity_snapshots' AS ref_table, CAST(id AS TEXT) AS ref_id FROM latest_profile WHERE rank = 1 AND IFNULL(json_extract(raw_json, '$.firstName'), '') != ''
UNION ALL
SELECT 'profile' AS kind, json_extract(raw_json, '$.lastName') AS text, 'identity_snapshots' AS ref_table, CAST(id AS TEXT) AS ref_id FROM latest_profile WHERE rank = 1 AND IFNULL(json_extract(raw_json, '$.lastName'), '') != ''
UNION ALL
SELECT 'exercise_type' AS kind, json_extract(raw_json, '$.exercise.exerciseType') AS text, 'data_points' AS ref_table, CAST(id AS TEXT) AS ref_id FROM data_points WHERE data_type = 'exercise' AND IFNULL(json_extract(raw_json, '$.exercise.exerciseType'), '') != ''
UNION ALL
SELECT 'exercise_type' AS kind, json_extract(raw_json, '$.exercise.displayName') AS text, 'data_points' AS ref_table, CAST(id AS TEXT) AS ref_id FROM data_points WHERE data_type = 'exercise' AND IFNULL(json_extract(raw_json, '$.exercise.displayName'), '') != ''`,
		fields: []exportFieldSpec{
			{name: "kind"},
			{name: "text"},
			{name: "ref_table"},
			{name: "ref_id"},
		},
	},
	{
		// sleep_stages explodes the stages[] array inside every archived
		// sleep Data Point into one row per stage. Pure SQL over the
		// existing raw_json — no new sync required.
		name:             "sleep-stages",
		view:             "sleep_stages",
		migrationVersion: 11,
		orderBy:          "start_time_utc, upstream_resource_name",
		viewSQL: `SELECT
			data_points.provider_name,
			data_points.connection_id,
			IFNULL(json_extract(stage.value, '$.startTime'), '') AS start_time_utc,
			IFNULL(json_extract(stage.value, '$.endTime'), '') AS end_time_utc,
			COALESCE(data_points.provider_civil_date, substr(data_points.start_civil_time, 1, 10), substr(data_points.start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(stage.value, '$.type'), '') AS sleep_stage,
			CAST((strftime('%s', json_extract(stage.value, '$.endTime')) - strftime('%s', json_extract(stage.value, '$.startTime'))) AS INTEGER) AS duration_seconds,
			IFNULL(data_points.source_family_filter, '') AS source_family_filter,
			IFNULL(data_points.upstream_resource_name, '') AS upstream_resource_name
		FROM data_points, json_each(data_points.raw_json, '$.sleep.stages') AS stage
		WHERE data_points.data_type = 'sleep'
			AND json_extract(data_points.raw_json, '$.sleep.stages') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "civil_date"},
			{name: "sleep_stage"},
			{name: "duration_seconds"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// exercise_splits explodes the splits[] array inside every
		// archived exercise Data Point. Same pattern as sleep_stages.
		name:             "exercise-splits",
		view:             "exercise_splits",
		migrationVersion: 11,
		orderBy:          "start_time_utc, upstream_resource_name",
		viewSQL: `SELECT
			data_points.provider_name,
			data_points.connection_id,
			IFNULL(json_extract(split.value, '$.startTime'), '') AS start_time_utc,
			IFNULL(json_extract(split.value, '$.endTime'), '') AS end_time_utc,
			COALESCE(data_points.provider_civil_date, substr(data_points.start_civil_time, 1, 10), substr(data_points.start_time_utc, 1, 10), '') AS civil_date,
			IFNULL(json_extract(split.value, '$.splitType'), '') AS split_type,
			CAST(json_extract(split.value, '$.metricsSummary.distanceMillimeters') AS INTEGER) / 1000 AS distance_meters,
			CAST(json_extract(split.value, '$.metricsSummary.distanceMillimeters') AS INTEGER) AS distance_millimeters,
			IFNULL(json_extract(split.value, '$.activeDuration'), '') AS active_duration,
			IFNULL(data_points.source_family_filter, '') AS source_family_filter,
			IFNULL(data_points.upstream_resource_name, '') AS upstream_resource_name
		FROM data_points, json_each(data_points.raw_json, '$.exercise.splits') AS split
		WHERE data_points.data_type = 'exercise'
			AND json_extract(data_points.raw_json, '$.exercise.splits') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "start_time_utc"},
			{name: "end_time_utc"},
			{name: "civil_date"},
			{name: "split_type"},
			{name: "distance_meters"},
			{name: "distance_millimeters"},
			{name: "active_duration"},
			{name: "source_family_filter"},
			{name: "upstream_resource_name"},
		},
	},
	{
		// current_irn_profile projects the latest kind='irn-profile'
		// snapshot per Connection (onboarding state, enrollment state,
		// last update time). Behind the same Identity Snapshot pattern
		// as current_settings.
		name:             "current-irn-profile",
		view:             "current_irn_profile",
		migrationVersion: 10,
		orderBy:          "connection_id",
		viewSQL: `WITH latest AS (
			SELECT
				provider_name,
				connection_id,
				raw_json,
				fetched_at,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
			FROM identity_snapshots
			WHERE snapshot_kind = 'irn-profile'
		)
		SELECT
			provider_name,
			connection_id,
			IFNULL(json_extract(raw_json, '$.onboardingState'), '') AS onboarding_state,
			IFNULL(json_extract(raw_json, '$.enrollmentState'), '') AS enrollment_state,
			IFNULL(json_extract(raw_json, '$.lastUpdateTime'), '') AS last_update_time,
			fetched_at
		FROM latest
		WHERE rank = 1`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "onboarding_state"},
			{name: "enrollment_state"},
			{name: "last_update_time"},
			{name: "fetched_at"},
		},
	},
	{
		// paired_devices explodes the device list inside the latest
		// kind='paired-devices' Identity Snapshot via json_each. One
		// row per device with the contracted columns; new fields land
		// as additional json_extract projections, no re-sync needed.
		name:             "paired-devices",
		view:             "paired_devices",
		migrationVersion: 9,
		orderBy:          "connection_id, model",
		viewSQL: `WITH latest AS (
			SELECT
				provider_name,
				connection_id,
				raw_json,
				fetched_at,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
			FROM identity_snapshots
			WHERE snapshot_kind = 'paired-devices'
		),
		latest_only AS (
			SELECT * FROM latest WHERE rank = 1
		)
		SELECT
			latest_only.provider_name,
			latest_only.connection_id,
			IFNULL(json_extract(device.value, '$.deviceType'), '') AS device_type,
			IFNULL(json_extract(device.value, '$.model'), '') AS model,
			IFNULL(json_extract(device.value, '$.manufacturer'), '') AS manufacturer,
			json_extract(device.value, '$.batteryPercentage') AS battery_percentage,
			IFNULL(json_extract(device.value, '$.lastSyncTime'), '') AS last_sync_time,
			IFNULL(json_extract(device.value, '$.features'), '[]') AS features,
			latest_only.fetched_at
		FROM latest_only, json_each(latest_only.raw_json, '$.devices') AS device`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "device_type"},
			{name: "model"},
			{name: "manufacturer"},
			{name: "battery_percentage"},
			{name: "last_sync_time"},
			{name: "features"},
			{name: "fetched_at"},
		},
	},
	{
		// current_settings projects the most recent Identity Snapshot of
		// kind='settings' for each Connection into a column-shaped view.
		// New fields land here as additional json_extract projections
		// without a re-sync; raw_json stays the source of truth.
		name:             "current-settings",
		view:             "current_settings",
		migrationVersion: 8,
		orderBy:          "connection_id",
		viewSQL: `WITH latest AS (
			SELECT
				provider_name,
				connection_id,
				snapshot_kind,
				raw_json,
				fetched_at,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY fetched_at DESC, id DESC) AS rank
			FROM identity_snapshots
			WHERE snapshot_kind = 'settings'
		)
		SELECT
			provider_name,
			connection_id,
			IFNULL(json_extract(raw_json, '$.measurementSystem'), '') AS measurement_system,
			IFNULL(json_extract(raw_json, '$.timezone'), '') AS timezone,
			IFNULL(json_extract(raw_json, '$.strideLengthType'), '') AS stride_length_type,
			fetched_at
		FROM latest
		WHERE rank = 1`,
		fields: []exportFieldSpec{
			{name: "provider_name"},
			{name: "connection_id"},
			{name: "measurement_system"},
			{name: "timezone"},
			{name: "stride_length_type"},
			{name: "fetched_at"},
		},
	},
	// Tier 1 Health metrics views (#102), migration 18. Each
	// projects the principal scalar Google Health's REST API
	// documents for the corresponding Data Type. Scalars stored as
	// TEXT to preserve upstream precision (matches vo2_max_samples
	// pattern from #101). current_height is the latest-only
	// projection so an LLM can answer "what's my height?" without
	// ordering by hand.
	{
		// body_fat_samples: $.bodyFat.percentage — same shape as
		// oxygenSaturation.percentage (both percentages).
		name:             "body-fat-samples",
		view:             "body_fat_samples",
		migrationVersion: 18,
		orderBy:          "sample_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.bodyFat.percentage') AS TEXT) AS percentage,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'body-fat'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.bodyFat.percentage') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "percentage"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// blood_glucose_samples: principal scalar lives at
		// $.bloodGlucose.bloodGlucoseLevel.milligramsPerDeciliter
		// (UnitValue style). Stored as TEXT to preserve precision.
		name:             "blood-glucose-samples",
		view:             "blood_glucose_samples",
		migrationVersion: 18,
		orderBy:          "sample_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.bloodGlucose.bloodGlucoseLevel.milligramsPerDeciliter') AS TEXT) AS milligrams_per_deciliter,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'blood-glucose'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.bloodGlucose.bloodGlucoseLevel.milligramsPerDeciliter') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "milligrams_per_deciliter"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// core_body_temperature_samples: scalar at
		// $.coreBodyTemperature.celsius (matches Google Health's
		// celsius convention across temperature Data Types).
		name:             "core-body-temperature-samples",
		view:             "core_body_temperature_samples",
		migrationVersion: 18,
		orderBy:          "sample_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.coreBodyTemperature.celsius') AS TEXT) AS celsius,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'core-body-temperature'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.coreBodyTemperature.celsius') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "celsius"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// height_samples: scalar at $.height.heightMeters (mirrors
		// weight.weightGrams). Stored as TEXT to preserve precision.
		name:             "height-samples",
		view:             "height_samples",
		migrationVersion: 18,
		orderBy:          "sample_time_utc, provider_name, connection_id",
		viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.height.heightMeters') AS TEXT) AS height_meters,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'height'
			AND start_time_utc IS NOT NULL
			AND json_extract(raw_json, '$.height.heightMeters') IS NOT NULL`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "height_meters"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
	{
		// current_height: the latest height sample per Connection.
		// Issue #102 calls for this so an LLM can answer "what's my
		// height?" without ordering manually.
		name:             "current-height",
		view:             "current_height",
		migrationVersion: 18,
		orderBy:          "connection_id",
		viewSQL: `WITH ranked AS (
			SELECT
				provider_name,
				connection_id,
				start_time_utc,
				start_civil_time,
				provider_civil_date,
				raw_json,
				source_family_filter,
				upstream_resource_name,
				ROW_NUMBER() OVER (PARTITION BY connection_id ORDER BY start_time_utc DESC, id DESC) AS rank
			FROM data_points
			WHERE data_type = 'height'
				AND start_time_utc IS NOT NULL
				AND json_extract(raw_json, '$.height.heightMeters') IS NOT NULL
		)
		SELECT
			provider_name,
			connection_id,
			start_time_utc AS sample_time_utc,
			IFNULL(start_civil_time, '') AS sample_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CAST(json_extract(raw_json, '$.height.heightMeters') AS TEXT) AS height_meters,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM ranked
		WHERE rank = 1`,
		fields: []exportFieldSpec{
			{name: "provider_name"}, {name: "connection_id"},
			{name: "sample_time_utc"}, {name: "sample_civil_time"}, {name: "civil_date"},
			{name: "height_meters"},
			{name: "source_family_filter"}, {name: "upstream_resource_name"},
		},
	},
}

var exportDatasetSpecs = exportDatasetSpecByName(exportDatasetDefinitions)

func exportDatasetSpecByName(definitions []exportDatasetSpec) map[string]exportDatasetSpec {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	for _, definition := range definitions {
		if definition.name == "" {
			panic("export dataset definition missing name")
		}
		if _, exists := specs[definition.name]; exists {
			panic(fmt.Sprintf("duplicate export dataset definition: %s", definition.name))
		}
		specs[definition.name] = definition
	}
	return specs
}

func exportDatasetViewMigrationStatements(migrationVersion int) []string {
	var statements []string
	for _, definition := range exportDatasetDefinitions {
		if definition.migrationVersion != migrationVersion {
			continue
		}
		statements = append(statements, exportDatasetViewMigrationStatement(definition))
	}
	return statements
}

func exportDatasetViewMigrationStatement(spec exportDatasetSpec) string {
	return fmt.Sprintf("CREATE VIEW %s AS\n%s", spec.view, strings.TrimSpace(spec.viewSQL))
}

// exportDatasetCatalog is the small discovery adapter over the
// exportDatasetDefinitions registry. It owns the three views the read
// surface needs but the registry itself was never shaped to provide:
//
//   - Names() — sorted, deduped list for `export --help` and the
//     README drift guard.
//   - Find(name) — case-sensitive lookup matching the registry's name
//     contract (mirrors `exportDatasetSpecs[name]`).
//   - Suggest(typo) — Levenshtein ≤ 3, top 3 by closeness then
//     alphabetical, for the `export <typo>` did-you-mean line.
//
// PRD #144 slice 3 (issue #157) introduces this seam so consumers
// (--help printer, typo error path, future docs generators) share one
// surface instead of each re-walking the registry. ADR 0007 keeps the
// registry as the source of truth for view SQL / migrations; the
// catalog only *projects* discovery views over it.
type exportDatasetCatalog struct {
	// names is precomputed once at construction time: sorted, deduped.
	// Cached because `export --help` and the typo error path each touch
	// it on every invocation, and the registry never changes at runtime.
	names []string
	specs map[string]exportDatasetSpec
}

// exportSuggestMaxDistance is the Levenshtein cutoff for export typo
// suggestions, fixed at 3 per PRD #144 slice 3. The looser bound (vs
// the top-level command registry's 2) reflects that dataset names are
// longer (averaging 18 chars) so a 2-edit cutoff misses common typos
// like `heart-rate-sample` → `heart-rate-samples` when paired with a
// second transposition.
const exportSuggestMaxDistance = 3

// exportSuggestMax is the hard cap on returned suggestions.
const exportSuggestMax = 3

// newExportDatasetCatalog builds a catalog over the given definitions.
// Duplicate names are tolerated here (only the first wins for Find);
// the registry seam (exportDatasetSpecByName) already panics on
// duplicates, so production callers using exportDatasetDefinitions
// never trigger the dedup branch. Tests pass synthetic registries.
func newExportDatasetCatalog(definitions []exportDatasetSpec) *exportDatasetCatalog {
	specs := make(map[string]exportDatasetSpec, len(definitions))
	names := make([]string, 0, len(definitions))
	for _, def := range definitions {
		if _, exists := specs[def.name]; exists {
			continue
		}
		specs[def.name] = def
		names = append(names, def.name)
	}
	sort.Strings(names)
	return &exportDatasetCatalog{names: names, specs: specs}
}

// Names returns the sorted, deduped list of dataset names. The returned
// slice is a fresh copy so callers may safely mutate it without
// disturbing the cached state.
func (c *exportDatasetCatalog) Names() []string {
	out := make([]string, len(c.names))
	copy(out, c.names)
	return out
}

// Find returns the spec for the given name and ok=true on hit,
// (zero-value spec, false) on miss. Case-sensitive — the registry's
// dataset names are kebab-case ASCII and never mixed-case.
func (c *exportDatasetCatalog) Find(name string) (exportDatasetSpec, bool) {
	spec, ok := c.specs[name]
	return spec, ok
}

// Suggest returns at most exportSuggestMax dataset names whose
// Levenshtein distance from `typo` is ≤ exportSuggestMaxDistance,
// ordered by (distance asc, name asc). An empty slice (not nil)
// indicates no close match; the typo error path falls back to the
// `export --help` pointer in that case.
//
// The algorithm is dependency-free; we reuse the levenshteinDistance
// helper that already lives in commands.go for the top-level
// command-name typo path.
func (c *exportDatasetCatalog) Suggest(typo string) []string {
	type candidate struct {
		name     string
		distance int
	}
	var candidates []candidate
	for _, name := range c.names {
		d := levenshteinDistance(typo, name)
		if d <= exportSuggestMaxDistance {
			candidates = append(candidates, candidate{name: name, distance: d})
		}
	}
	// Sort by (distance asc, name asc). c.names is already alphabetical
	// so stable sort by distance preserves the alphabetical tie-break
	// for free.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].distance < candidates[j].distance
	})
	if len(candidates) > exportSuggestMax {
		candidates = candidates[:exportSuggestMax]
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.name)
	}
	return out
}

// exportDatasetCatalogSingleton is the production catalog over the
// canonical registry. Built once at package init so the help printer
// and typo error path do not pay the construction cost per invocation.
var exportDatasetCatalogSingleton = newExportDatasetCatalog(exportDatasetDefinitions)

func runExport(args []string, configPath, archivePath string, configPathExplicit, archivePathExplicit bool, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(stderr)

	// export accepts the full Common Flag Set ({config, db, json, plain,
	// no-input}) so the "every subcommand accepts the same global flags"
	// invariant (PRD #143) holds. --json is a documented synonym for
	// --format jsonl; --plain is a documented synonym for --format csv.
	// The export-specific --format / --output / --stdout flags are
	// registered AFTER RegisterCommon. --no-input is accepted but unused
	// by export (it's a read-only verb against the local archive); we
	// keep it in the spec so the global-flag pre-scan does not reject it.
	common := RegisterCommon(flags, AllCommonFlagsSpec(), CommonFlagValues{
		ConfigPath:          configPath,
		ArchivePath:         archivePath,
		ArchivePathExplicit: archivePathExplicit,
		ConfigPathExplicit:  configPathExplicit,
	})
	// Override the generic CommonFlagSet Usage strings for the flags
	// whose meaning is export-specific. `export --help` now reflects the
	// documented synonym semantics instead of the misleading "write
	// stable JSON to stdout" wording inherited from the shared module.
	flags.Lookup("json").Usage = "synonym for --format jsonl"
	flags.Lookup("plain").Usage = "synonym for --format csv"
	flags.Lookup("no-input").Usage = "accepted for uniformity; export does no prompting"
	exportFormat := flags.String("format", "csv", "export format: csv or jsonl (synonyms: --json → jsonl, --plain → csv)")
	exportOutputPath := flags.String("output", "", "write export to path")
	exportStdout := flags.Bool("stdout", false, "write export data to stdout")

	// `export --help` is the discovery surface for the 30+ normalized
	// datasets (PRD #144 slice 3). The stdlib default Usage prints the
	// flag block only; we wrap it to append the catalog list so an LLM
	// or script that asks the binary "what can you export?" gets a
	// complete answer from one call. The catalog earns its seam here:
	// the loop is one line because Names() already sorts and dedupes.
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage of %s:\n", flags.Name())
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nSupported datasets:")
		for _, name := range exportDatasetCatalogSingleton.Names() {
			fmt.Fprintf(flags.Output(), "  %s\n", name)
		}
	}

	positionals, parseArgs, err := splitExportArgs(args)
	if err != nil {
		// splitExportArgs runs BEFORE ParseCommon, so common.JSONOutput /
		// common.PlainOutput are not yet populated from inner flags. The
		// only failure shape splitExportArgs surfaces is "flag needs an
		// argument: --foo", which is a flag-shape error that defaults to
		// the bare `<cmd>: <msg>` line on stderr. The multi-positional
		// "export requires exactly one dataset" rejection used to fire
		// here too, but it was deferred to AFTER ParseCommon below so its
		// ReportFailure can honour --json / --plain. Every other
		// ReportFailure in runExport runs AFTER ParseCommon and carries
		// Mode so the unified --json / --plain failure contract is
		// honoured.
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error()}, stdout, stderr)
	}

	if err := ParseCommon(flags, common, parseArgs); err != nil {
		return commonFlagsExitCode(flags, err, stdout, stderr)
	}
	// mode is the unified output-mode the failure_reporter contract pivots
	// on. Until slice 9's export migration is patched, every ReportFailure
	// here dropped Mode and silently fell back to default-mode output —
	// `--json` invocations never saw their JSON envelope. Threading mode
	// once and reusing it on every call site below restores the contract
	// without dragging an `outputMode` parameter through runExport's
	// signature (the runtime adapter still owns the registry-driven shape).
	mode := outputMode{json: common.JSONOutput, plain: common.PlainOutput}
	if len(positionals) == 0 || flags.NArg() != 0 {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires exactly one dataset", Mode: mode}, stdout, stderr)
	}
	if len(positionals) > 1 {
		// Multi-positional rejection deferred from splitExportArgs so the
		// failure surface honours --json / --plain like every other
		// post-ParseCommon ReportFailure below.
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires exactly one dataset", Mode: mode}, stdout, stderr)
	}
	dataset := positionals[0]
	spec, ok := exportDatasetCatalogSingleton.Find(dataset)
	if !ok {
		exit := ReportFailure(FailureReport{
			Command: "export",
			Status:  StatusFlagInvalid,
			Message: fmt.Sprintf("export dataset %q is not supported", dataset),
			Mode:    mode,
		}, stdout, stderr)
		// In --json mode the caller wants a single-line envelope on
		// stdout and nothing on stderr; appending hints would corrupt
		// that shape (the same constraint runUnknownCommand honours).
		// In default/--plain mode, surface the did-you-mean line plus
		// the `export --help` pointer so the human (or scripted LLM
		// retry) can recover without grepping source. The pointer is
		// emitted unconditionally because Suggest() can return an
		// empty slice for gibberish input — the help pointer is the
		// invariant fallback.
		if !mode.json {
			if suggestions := exportDatasetCatalogSingleton.Suggest(dataset); len(suggestions) > 0 {
				fmt.Fprintf(stderr, "Did you mean: %s?\n", strings.Join(suggestions, ", "))
			}
			fmt.Fprintln(stderr, "Run 'gohealthcli export --help' for the full list of supported datasets.")
		}
		return exit
	}
	// Resolve --json / --plain into --format. Mutual exclusion between
	// --plain and --json already fired in ParseCommon above (the
	// CommonFlagSet seam owns that invariant); the conflict between a
	// Common Flag synonym and an explicit --format value is
	// export-specific, so the validator lives here, not in common_flags.go.
	formatExplicit := flagWasProvided(flags, "format")
	resolvedFormat, err := resolveExportFormat(*exportFormat, formatExplicit, common.JSONOutput, common.PlainOutput)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	if *exportOutputPath == "" && !*exportStdout {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export requires --output PATH or --stdout", Mode: mode}, stdout, stderr)
	}
	if *exportOutputPath != "" && *exportStdout {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: "export accepts only one destination: --output or --stdout", Mode: mode}, stdout, stderr)
	}
	if err := validateExportFormat(resolvedFormat); err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusFlagInvalid, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	resolvedArchivePath, err := resolveReadArchivePath(*common)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}
	rows, err := exportRows(resolvedArchivePath, spec)
	if err != nil {
		return ReportFailure(FailureReport{Command: "export", Status: StatusOperationFailed, Message: err.Error(), Mode: mode}, stdout, stderr)
	}

	if *exportStdout {
		if err := writeExport(rows, spec, resolvedFormat, stdout); err != nil {
			return ReportFailure(FailureReport{
				Command: "export",
				Status:  StatusArchiveUnwritable,
				Message: fmt.Sprintf("write output: %v", err),
				Mode:    mode,
			}, stdout, stderr)
		}
		return 0
	}
	if err := writeExportFile(rows, spec, resolvedFormat, *exportOutputPath); err != nil {
		status := StatusArchiveUnwritable
		if errors.Is(err, errExportOutputSymlink) {
			status = StatusFlagInvalid
		}
		return ReportFailure(FailureReport{
			Command: "export",
			Status:  status,
			Message: fmt.Sprintf("write export: %v", err),
			Mode:    mode,
		}, stdout, stderr)
	}
	return 0
}

// resolveExportFormat maps the Common Flag Set synonyms (--json, --plain)
// onto the export-specific --format value. It enforces the conflict
// invariant export.go owns (CommonFlagSet owns --plain --json mutual
// exclusion at the seam above):
//
//   - if --json is set with an explicit --format whose value is not "jsonl",
//     return a "--json conflicts with --format <value>" error;
//   - if --plain is set with an explicit --format whose value is not "csv",
//     return a "--plain conflicts with --format <value>" error;
//   - otherwise, --json overrides the default to "jsonl" and --plain
//     overrides the default to "csv" (when --format was NOT explicit).
//
// formatExplicit comes from flagWasProvided so a user passing the synonym
// alongside `--format jsonl` (redundant but not contradictory) does NOT
// error — only contradictory pairings do.
func resolveExportFormat(format string, formatExplicit, jsonSynonym, plainSynonym bool) (string, error) {
	if jsonSynonym && formatExplicit && format != "jsonl" {
		return "", fmt.Errorf("--json conflicts with --format %s", format)
	}
	if plainSynonym && formatExplicit && format != "csv" {
		return "", fmt.Errorf("--plain conflicts with --format %s", format)
	}
	if jsonSynonym && !formatExplicit {
		return "jsonl", nil
	}
	if plainSynonym && !formatExplicit {
		return "csv", nil
	}
	return format, nil
}

// splitExportArgs separates flag tokens from positional dataset args so
// the inner FlagSet can parse the flag block while runExport keeps the
// positional list intact for the post-ParseCommon dataset-count check.
//
// Returns the positional list (length 0 = missing dataset, length 1 =
// the canonical case, length >= 2 = duplicate-dataset error surfaced
// AFTER ParseCommon so its ReportFailure can honour --json / --plain),
// the flag-token slice ready for ParseCommon, and an error reserved for
// the rare "flag needs an argument: --foo" shape that only surfaces
// when an operator passes a non-bool flag without its value. Multi-
// positional rejection is intentionally NOT raised here: deferring it
// to the post-parse path is what lets the failure_reporter contract
// thread Mode through so `export --json a b` emits the JSON envelope
// instead of falling back to default mode.
func splitExportArgs(args []string) ([]string, []string, error) {
	var positionals []string
	var flagArgs []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if exportFlagNeedsValue(arg) && !strings.Contains(arg, "=") {
				index++
				if index >= len(args) {
					return nil, nil, fmt.Errorf("flag needs an argument: %s", arg)
				}
				flagArgs = append(flagArgs, args[index])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return positionals, flagArgs, nil
}

func exportFlagNeedsValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	switch name {
	case "config", "db", "format", "output":
		return true
	default:
		return false
	}
}

func validateExportFormat(format string) error {
	switch format {
	case "csv", "jsonl":
		return nil
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeExportFile(rows []exportRow, spec exportDatasetSpec, format, path string) error {
	if usesPOSIXPermissions() {
		if err := restrictExistingExportOutput(path); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeExport(rows, spec, format, file)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if usesPOSIXPermissions() {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func writeDailyStepsExportFile(rows []dailyStepsExportRow, format, path string) error {
	return writeExportFile(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], format, path)
}

// errExportOutputSymlink reports that --output names a symbolic link. The
// export writer refuses such paths so it never chmods or truncates the link
// target; the caller surfaces this as a flag-invalid failure.
var errExportOutputSymlink = errors.New("symbolic link")

func restrictExistingExportOutput(path string) error {
	// os.Lstat does not follow symlinks, so it sees the link itself. Check it
	// BEFORE os.Stat (which follows symlinks) so a symlinked --output is
	// refused rather than chmod'd or truncated through the link target.
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: --output %s names a symbolic link; pass the resolved target path explicitly", errExportOutputSymlink, path)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode().Perm() != 0o600 {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func writeExport(rows []exportRow, spec exportDatasetSpec, format string, writer io.Writer) error {
	switch format {
	case "csv":
		return writeExportCSV(rows, spec, writer)
	case "jsonl":
		return writeExportJSONL(rows, spec, writer)
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func writeDailyStepsExport(rows []dailyStepsExportRow, format string, writer io.Writer) error {
	return writeExport(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], format, writer)
}

func writeExportCSV(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(exportFieldNames(spec)); err != nil {
		return err
	}
	for _, row := range rows {
		if err := csvWriter.Write([]string(row)); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func writeDailyStepsCSV(rows []dailyStepsExportRow, writer io.Writer) error {
	return writeExportCSV(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], writer)
}

func writeExportJSONL(rows []exportRow, spec exportDatasetSpec, writer io.Writer) error {
	for _, row := range rows {
		if _, err := fmt.Fprint(writer, "{"); err != nil {
			return err
		}
		for index, field := range spec.fields {
			if index > 0 {
				if _, err := fmt.Fprint(writer, ","); err != nil {
					return err
				}
			}
			name, err := json.Marshal(field.name)
			if err != nil {
				return err
			}
			if _, err := writer.Write(name); err != nil {
				return err
			}
			if _, err := fmt.Fprint(writer, ":"); err != nil {
				return err
			}
			if field.kind == "int" && row[index] != "" {
				value, err := strconv.ParseInt(row[index], 10, 64)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprint(writer, strconv.FormatInt(value, 10)); err != nil {
					return err
				}
				continue
			}
			value, err := json.Marshal(row[index])
			if err != nil {
				return err
			}
			if _, err := writer.Write(value); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(writer, "}"); err != nil {
			return err
		}
	}
	return nil
}

func writeDailyStepsJSONL(rows []dailyStepsExportRow, writer io.Writer) error {
	return writeExportJSONL(dailyStepsExportRowsToExportRows(rows), exportDatasetSpecs["daily-steps"], writer)
}

func dailyStepsExportFields() []string {
	return exportFieldNames(exportDatasetSpecs["daily-steps"])
}

func exportFieldNames(spec exportDatasetSpec) []string {
	fields := make([]string, 0, len(spec.fields))
	for _, field := range spec.fields {
		fields = append(fields, field.name)
	}
	return fields
}

func dailyStepsExportRowsToExportRows(rows []dailyStepsExportRow) []exportRow {
	out := make([]exportRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, exportRow{
			row.ProviderName,
			row.ConnectionID,
			row.CivilDate,
			strconv.FormatInt(row.StepCount, 10),
			row.SourceKind,
			row.SourceFamilyFilter,
			strconv.FormatInt(row.SourceRecordCount, 10),
			row.LatestSourceTimestamp,
		})
	}
	return out
}

func exportRows(archivePath string, spec exportDatasetSpec) ([]exportRow, error) {
	reader, err := openHealthArchiveReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.ExportRows(spec)
}
