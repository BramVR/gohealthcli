package main

// views_activity.go owns the activity-category Normalized View
// dataset specs — the interval-shaped projections for active minutes,
// active-zone minutes, altitude, activity level, sedentary periods,
// time in heart-rate zone, and swim lengths (ADR-0007, issue #276).
// Consumers never read these vars directly; every lookup goes through
// normalizedViewsRegistry().

var activeMinutesIntervalsViewSpec = exportDatasetSpec{
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
}

var activeZoneMinutesIntervalsViewSpec = exportDatasetSpec{
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
}

var altitudeIntervalsViewSpec = exportDatasetSpec{
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
}

var activityLevelIntervalsViewSpec = exportDatasetSpec{
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
}

var sedentaryPeriodIntervalsViewSpec = exportDatasetSpec{
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
}

var timeInHeartRateZoneIntervalsViewSpec = exportDatasetSpec{
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
}

var swimLengthsDataIntervalsViewSpec = exportDatasetSpec{
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
}

var basalEnergyBurnedIntervalsViewSpec = exportDatasetSpec{
	name:             "basal-energy-burned-intervals",
	view:             "basal_energy_burned_intervals",
	migrationVersion: 26,
	orderBy:          "start_time_utc, provider_name, connection_id, source_family_filter, upstream_resource_name",
	viewSQL: `SELECT
			provider_name,
			connection_id,
			start_time_utc,
			end_time_utc,
			IFNULL(start_civil_time, '') AS start_civil_time,
			IFNULL(end_civil_time, '') AS end_civil_time,
			COALESCE(provider_civil_date, substr(start_civil_time, 1, 10), substr(start_time_utc, 1, 10), '') AS civil_date,
			CASE
				WHEN json_extract(raw_json, '$.basalEnergyBurned.kcal') IS NULL THEN NULL
				WHEN json_type(raw_json, '$.basalEnergyBurned.kcal') NOT IN ('integer', 'real') THEN NULL
				WHEN json_type(raw_json, '$.basalEnergyBurned.kcal') = 'real'
					AND json_extract(raw_json, '$.basalEnergyBurned.kcal') = CAST(json_extract(raw_json, '$.basalEnergyBurned.kcal') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.basalEnergyBurned.kcal'))
				ELSE printf('%.15g', json_extract(raw_json, '$.basalEnergyBurned.kcal'))
			END AS kcal,
			IFNULL(json_extract(data_source_json, '$.platform'), '') AS source_platform,
			IFNULL(source_family_filter, '') AS source_family_filter,
			IFNULL(upstream_resource_name, '') AS upstream_resource_name
		FROM data_points
		WHERE data_type = 'basal-energy-burned'
			AND record_kind = 'interval'
			AND start_time_utc IS NOT NULL`,
	fields: []exportFieldSpec{
		{name: "provider_name"},
		{name: "connection_id"},
		{name: "start_time_utc"},
		{name: "end_time_utc"},
		{name: "start_civil_time"},
		{name: "end_civil_time"},
		{name: "civil_date"},
		{name: "kcal"},
		{name: "source_platform"},
		{name: "source_family_filter"},
		{name: "upstream_resource_name"},
	},
}

var totalCaloriesRollupsViewSpec = exportDatasetSpec{
	name:             "total-calories-rollups",
	view:             "total_calories_rollups",
	migrationVersion: 27,
	orderBy:          "CASE WHEN civil_date = '' THEN window_start_utc ELSE civil_date END, rollup_kind, provider_name, connection_id",
	viewSQL: `SELECT
			provider_name,
			connection_id,
			rollup_kind,
			IFNULL(window_start_utc, '') AS window_start_utc,
			IFNULL(window_end_utc, '') AS window_end_utc,
			IFNULL(civil_date, '') AS civil_date,
			CASE
				WHEN json_type(raw_json, '$.totalCalories.kcalSum') = 'real'
					AND json_extract(raw_json, '$.totalCalories.kcalSum') = CAST(json_extract(raw_json, '$.totalCalories.kcalSum') AS INTEGER)
				THEN printf('%.1f', json_extract(raw_json, '$.totalCalories.kcalSum'))
				ELSE printf('%.15g', json_extract(raw_json, '$.totalCalories.kcalSum'))
			END AS kcal_sum
		FROM rollups
		WHERE data_type = 'total-calories'
			AND json_type(raw_json, '$.totalCalories.kcalSum') IN ('integer', 'real')`,
	fields: []exportFieldSpec{
		{name: "provider_name"},
		{name: "connection_id"},
		{name: "rollup_kind"},
		{name: "window_start_utc"},
		{name: "window_end_utc"},
		{name: "civil_date"},
		{name: "kcal_sum"},
	},
}
