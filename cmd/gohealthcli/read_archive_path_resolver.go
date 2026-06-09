package main

import "fmt"

// readArchivePathResolver owns Health Archive path resolution for the
// four read commands (`status`, `query`, `export`, `describe-schema`).
// PRD #144 slice 1 (issue #155).
//
// Read commands accept three ergonomic shapes the write side does not:
//   - `--db` alone, with no config file present (open the explicit path
//     without inventing a matching config),
//   - `--db` explicit alongside a *default* config that happens to point
//     somewhere else (the user is steering the binary at a temp archive
//     — `--db` wins, no agreement check fires),
//   - `--db` and `--config` both explicit, where the agreement check
//     still fires and the error names the user-facing flags rather than
//     the internal `archive_path` field.
//
// The resolver collapses each of those shapes into one returned
// (resolvedPath, err) pair so call sites become a single line and the
// `describe-schema --db ignored` bug (issue body, slice 1) becomes
// structurally unrepresentable.
type readArchivePathResolver struct {
	configPath          string
	configPathExplicit  bool
	archivePath         string
	archivePathExplicit bool
}

// Resolve returns the Health Archive path a read command should open.
//
// Rules:
//   - If the config file does not exist, the `--db` value (explicit or
//     default) is used directly. No config is required.
//   - If the config file exists and is valid, the config's `archive_path`
//     wins unless `--db` was explicit. When `--db` was explicit it wins
//     over the config's path (the read-side relaxation).
//   - When BOTH `--config` and `--db` are explicit AND they disagree,
//     resolution fails with a message that names `--db` and `--config`
//     directly — never the internal `archive_path` field.
func (r readArchivePathResolver) Resolve() (string, error) {
	configArchivePath, configExists, err := readConfigArchivePath(r.configPath)
	if err != nil {
		return "", err
	}
	if !configExists {
		return r.archivePath, nil
	}
	if r.archivePathExplicit {
		if r.configPathExplicit && configArchivePath != r.archivePath {
			return "", fmt.Errorf(
				"--db %s points to a different Health Archive than --config %s (%s)",
				r.archivePath, r.configPath, configArchivePath,
			)
		}
		return r.archivePath, nil
	}
	return configArchivePath, nil
}
