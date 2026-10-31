package main

// views_exercise.go owns the exercise-category Normalized View
// dataset specs — exercise sessions and the exploded per-split view
// (ADR-0007, issue #276). Consumers never read these vars directly;
// every lookup goes through normalizedViewsRegistry().

var exerciseSessionsViewSpec = exportDatasetSpec{
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
}

var exerciseSplitsViewSpec = exportDatasetSpec{
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
}
