package main

// views_search.go owns the search-category Normalized View dataset
// spec — searchable_text, the LLM-facing free-text needle path over
// devices, Data Sources, profile fields, and exercise labels
// (ADR-0007, issue #276). Consumers never read this var directly;
// every lookup goes through normalizedViewsRegistry().

var searchableTextViewSpec = exportDatasetSpec{
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
}
