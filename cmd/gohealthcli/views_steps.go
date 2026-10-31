package main

// views_steps.go owns the steps-category Normalized View dataset
// specs — view SQL, field order, sort order, and the schema version
// that introduces each view (ADR-0007, issue #276). Consumers never
// read these vars directly; every lookup goes through
// normalizedViewsRegistry().

var dailyStepsViewSpec = exportDatasetSpec{
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
}

var floorsIntervalsViewSpec = exportDatasetSpec{
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
}
