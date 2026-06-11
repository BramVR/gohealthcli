package main

// views_heart.go owns the heart-category Normalized View dataset
// specs — heart-rate samples, resting heart rate, daily heart-rate
// zones, and the Tier 2 ECG / irregular-rhythm session views
// (ADR-0007, issue #276). Consumers never read these vars directly;
// every lookup goes through normalizedViewsRegistry().

var heartRateSamplesViewSpec = exportDatasetSpec{
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
}

var restingHeartRateByDayViewSpec = exportDatasetSpec{
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
}

var dailyHeartRateZonesViewSpec = exportDatasetSpec{
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
}

var electrocardiogramSessionsViewSpec = exportDatasetSpec{
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
}

var irregularRhythmNotificationsViewSpec = exportDatasetSpec{
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
}
