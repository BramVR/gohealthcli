package main

// views_identity.go owns the Identity Snapshot Normalized View
// dataset specs — the latest-snapshot projections current_settings,
// paired_devices, and current_irn_profile (ADR-0007, issue #276).
// Consumers never read these vars directly; every lookup goes through
// normalizedViewsRegistry().

var currentIrnProfileViewSpec = exportDatasetSpec{
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
}

var pairedDevicesViewSpec = exportDatasetSpec{
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
}

var currentSettingsViewSpec = exportDatasetSpec{
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
}
