// Package version is the single source of truth for the service version
// string. Previously the same literal "1.2.0-go" was duplicated in three
// places (cmd/main.go, server/Health, dashapi/overview) and every release
// missed at least one — the dashboard kept showing an old version while
// CHANGELOG was already at 1.3.4. One const, one bump.
package version

// String is what /health, the dashboard overview card, and the CLI banner
// all report. Bump on every release; scripts grep for the literal so don't
// format-change without updating deploy tooling too.
const String = "1.3.7-go"
