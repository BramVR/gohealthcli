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
	// Columns follow the real users.pairedDevices.list shape
	// verified against a live archive on 2026-06-11 (#298): the
	// list lives under $.pairedDevices and each device carries
	// name / deviceType / batteryStatus / batteryLevel /
	// deviceVersion — not the $.devices + model/manufacturer shape
	// #98 assumed. Schema migration 23 recreates the view.
	name:             "paired-devices",
	view:             "paired_devices",
	migrationVersion: 9,
	orderBy:          "connection_id, device_version",
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
			IFNULL(json_extract(device.value, '$.name'), '') AS name,
			IFNULL(json_extract(device.value, '$.deviceType'), '') AS device_type,
			IFNULL(json_extract(device.value, '$.deviceVersion'), '') AS device_version,
			IFNULL(json_extract(device.value, '$.batteryStatus'), '') AS battery_status,
			json_extract(device.value, '$.batteryLevel') AS battery_level,
			latest_only.fetched_at
		FROM latest_only, json_each(latest_only.raw_json, '$.pairedDevices') AS device`,
	fields: []exportFieldSpec{
		{name: "provider_name"},
		{name: "connection_id"},
		{name: "name"},
		{name: "device_type"},
		{name: "device_version"},
		{name: "battery_status"},
		{name: "battery_level"},
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
