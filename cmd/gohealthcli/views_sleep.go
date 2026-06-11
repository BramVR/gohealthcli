package main

// views_sleep.go owns the sleep-category Normalized View dataset
// specs — sleep sessions, the exploded per-stage view, nightly
// temperature derivations, and the respiratory-rate sleep summary
// (ADR-0007, issue #276). Consumers never read these vars directly;
// every lookup goes through normalizedViewsRegistry().

var sleepSessionsViewSpec = exportDatasetSpec{
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
}

var dailySleepTemperatureDerivationsViewSpec = exportDatasetSpec{
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
}

var respiratoryRateSleepSummaryViewSpec = exportDatasetSpec{
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
}

var sleepStagesViewSpec = exportDatasetSpec{
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
}
