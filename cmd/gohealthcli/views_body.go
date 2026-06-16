package main

// views_body.go owns the body-measurement Normalized View dataset
// specs — weight, body fat, blood glucose, core body temperature,
// height (plus the latest-only current_height projection), and
// hydration logs (ADR-0007, issue #276). Consumers never read these
// vars directly; every lookup goes through normalizedViewsRegistry().

var weightSamplesViewSpec = exportDatasetSpec{
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
			CASE
				WHEN json_type(raw_json, '$.weight.weightGrams') = 'real'
					AND json_extract(raw_json, '$.weight.weightGrams') = CAST(json_extract(raw_json, '$.weight.weightGrams') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.weight.weightGrams'))
				ELSE printf('%.15g', json_extract(raw_json, '$.weight.weightGrams'))
			END AS weight_grams,
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
}

var hydrationLogSessionsViewSpec = exportDatasetSpec{
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
			CASE
				WHEN json_type(raw_json, '$.hydrationLog.volume.liters') = 'real'
					AND json_extract(raw_json, '$.hydrationLog.volume.liters') = CAST(json_extract(raw_json, '$.hydrationLog.volume.liters') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.hydrationLog.volume.liters'))
				ELSE printf('%.15g', json_extract(raw_json, '$.hydrationLog.volume.liters'))
			END AS volume_liters,
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
}

// Tier 1 Health metrics views (#102), migration 18. Each
// projects the principal scalar Google Health's REST API
// documents for the corresponding Data Type. Scalars stored as
// TEXT to preserve upstream precision (matches vo2_max_samples
// pattern from #101). current_height is the latest-only
// projection so an LLM can answer "what's my height?" without
// ordering by hand.
var bodyFatSamplesViewSpec = exportDatasetSpec{
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
}

var bloodGlucoseSamplesViewSpec = exportDatasetSpec{
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
}

var coreBodyTemperatureSamplesViewSpec = exportDatasetSpec{
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
}

var heightSamplesViewSpec = exportDatasetSpec{
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
}

var currentHeightViewSpec = exportDatasetSpec{
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
}
