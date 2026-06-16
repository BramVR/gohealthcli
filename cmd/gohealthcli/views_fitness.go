package main

// views_fitness.go owns the cardio-fitness Normalized View dataset
// specs — VO2 max samples, run-specific VO2 max samples, and the
// daily VO2 max projection (ADR-0007, issue #276). Consumers never
// read these vars directly; every lookup goes through
// normalizedViewsRegistry().

var vo2MaxSamplesViewSpec = exportDatasetSpec{
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
			CASE
				WHEN json_extract(raw_json, '$.vo2Max.vo2Max') IS NULL THEN NULL
				WHEN json_type(raw_json, '$.vo2Max.vo2Max') = 'real'
					AND json_extract(raw_json, '$.vo2Max.vo2Max') = CAST(json_extract(raw_json, '$.vo2Max.vo2Max') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.vo2Max.vo2Max'))
				ELSE printf('%.15g', json_extract(raw_json, '$.vo2Max.vo2Max'))
			END AS vo2_max,
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
}

var runVo2MaxSamplesViewSpec = exportDatasetSpec{
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
			CASE
				WHEN json_extract(raw_json, '$.runVo2Max.runVo2Max') IS NULL THEN NULL
				WHEN json_type(raw_json, '$.runVo2Max.runVo2Max') = 'real'
					AND json_extract(raw_json, '$.runVo2Max.runVo2Max') = CAST(json_extract(raw_json, '$.runVo2Max.runVo2Max') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.runVo2Max.runVo2Max'))
				ELSE printf('%.15g', json_extract(raw_json, '$.runVo2Max.runVo2Max'))
			END AS run_vo2_max,
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
}

var dailyVo2MaxViewSpec = exportDatasetSpec{
	// daily_vo2_max projects archived daily-vo2-max Data Points into
	// one row per civil date with the principal vo2Max scalar, the
	// cardio-fitness-level enum, and the covariance scalar. The float
	// scalars use explicit text formatting so exports stay stable across
	// SQLite driver updates; the raw JSON path lives at
	// $.dailyVo2Max.vo2Max (Google's repeated data-type name nesting).
	name:             "daily-vo2-max",
	view:             "daily_vo2_max",
	migrationVersion: 19,
	orderBy:          "civil_date, provider_name, connection_id",
	viewSQL: `SELECT
			provider_name,
			connection_id,
			provider_civil_date AS civil_date,
			CASE
				WHEN json_extract(raw_json, '$.dailyVo2Max.vo2Max') IS NULL THEN NULL
				WHEN json_type(raw_json, '$.dailyVo2Max.vo2Max') = 'real'
					AND json_extract(raw_json, '$.dailyVo2Max.vo2Max') = CAST(json_extract(raw_json, '$.dailyVo2Max.vo2Max') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.dailyVo2Max.vo2Max'))
				ELSE printf('%.15g', json_extract(raw_json, '$.dailyVo2Max.vo2Max'))
			END AS vo2_max,
			IFNULL(json_extract(raw_json, '$.dailyVo2Max.cardioFitnessLevel'), '') AS cardio_fitness_level,
			CASE
				WHEN json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance') IS NULL THEN NULL
				WHEN json_type(raw_json, '$.dailyVo2Max.vo2MaxCovariance') = 'real'
					AND json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance') = CAST(json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance'))
				ELSE printf('%.15g', json_extract(raw_json, '$.dailyVo2Max.vo2MaxCovariance'))
			END AS vo2_max_covariance,
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
}
