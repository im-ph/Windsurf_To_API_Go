// N20 — Local Windsurf credential discovery.
//
// Scans the host for a local Windsurf desktop install and surfaces the
// state.vscdb path so the operator can do a one-click pool import. The
// actual SQLite read is shelled out to /usr/bin/sqlite3 when available;
// otherwise we report the candidate paths we found and let the operator
// copy the apiKey out manually.
//
// Why not vendor a pure-Go SQLite driver? `modernc.org/sqlite` would add
// ~3 MB and a transitive dep tree. The state.vscdb read is a once-per-
// boot operation that an operator does interactively from the dashboard —
// shelling out is fine and keeps the static-binary footprint tight.
// Operators on a host without sqlite3 still get the path, can copy the
// file out, and feed the value into POST /dashboard/api/accounts manually.
//
// Cross-platform paths:
//   macOS:   ~/Library/Application Support/Windsurf/User/globalStorage/state.vscdb
//   Linux:   ~/.config/Windsurf/User/globalStorage/state.vscdb
//   Windows: %APPDATA%\Windsurf\User\globalStorage\state.vscdb
//
// Each platform also tries the "Windsurf - Next" / "Windsurf-Next" /
// "Windsurf Insiders" variants so users on the canary track aren't
// invisible to the discovery panel.
package dashapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CandidatePath is one row in the discovery response.
type CandidatePath struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	ModifiedAt int64  `json:"modifiedAt,omitempty"` // unix ms
	// Flavor identifies which Windsurf install variant we found. "stable"
	// for the default, "next" / "insiders" for canary builds.
	Flavor string `json:"flavor"`
}

// LocalWindsurfStatus is the GET /dashboard/api/local-windsurf response.
type LocalWindsurfStatus struct {
	Supported    bool            `json:"supported"`
	Reason       string          `json:"reason,omitempty"`
	Platform     string          `json:"platform"`
	SqliteCLI    string          `json:"sqliteCli,omitempty"`
	HasSqliteCLI bool            `json:"hasSqliteCli"`
	Candidates   []CandidatePath `json:"candidates"`
}

// LocalWindsurfImportResult is the POST /dashboard/api/local-windsurf/import
// response. Contains zero or more discovered apiKey rows ready for the
// operator to confirm before injecting into the pool.
type LocalWindsurfImportResult struct {
	Found []LocalWindsurfCredential `json:"found"`
	Error string                    `json:"error,omitempty"`
}

// LocalWindsurfCredential is one apiKey + label pulled from state.vscdb.
type LocalWindsurfCredential struct {
	Path   string `json:"path"`
	Flavor string `json:"flavor"`
	APIKey string `json:"apiKey"`
	Email  string `json:"email,omitempty"`
}

// candidateStateDBPaths returns every plausible state.vscdb location on
// the current host. Order matters: stable first, then -Next / Insiders
// so the dashboard's "first match" UX picks the user's primary install.
func candidateStateDBPaths() []CandidatePath {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	type candidate struct {
		dir    string
		flavor string
	}
	var roots []candidate

	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		for _, p := range []candidate{
			{dir: filepath.Join(base, "Windsurf"), flavor: "stable"},
			{dir: filepath.Join(base, "Windsurf - Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf-Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf Insiders"), flavor: "insiders"},
		} {
			roots = append(roots, p)
		}
	case "linux":
		base := filepath.Join(home, ".config")
		for _, p := range []candidate{
			{dir: filepath.Join(base, "Windsurf"), flavor: "stable"},
			{dir: filepath.Join(base, "Windsurf - Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf-Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf Insiders"), flavor: "insiders"},
		} {
			roots = append(roots, p)
		}
		// Snap install on Ubuntu lives elsewhere.
		roots = append(roots, candidate{
			dir:    filepath.Join(home, "snap", "windsurf", "current", ".config", "Windsurf"),
			flavor: "snap",
		})
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		for _, p := range []candidate{
			{dir: filepath.Join(base, "Windsurf"), flavor: "stable"},
			{dir: filepath.Join(base, "Windsurf - Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf-Next"), flavor: "next"},
			{dir: filepath.Join(base, "Windsurf Insiders"), flavor: "insiders"},
		} {
			roots = append(roots, p)
		}
	}

	out := make([]CandidatePath, 0, len(roots))
	for _, r := range roots {
		full := filepath.Join(r.dir, "User", "globalStorage", "state.vscdb")
		entry := CandidatePath{Path: full, Flavor: r.flavor}
		if st, err := os.Stat(full); err == nil {
			entry.Exists = true
			entry.SizeBytes = st.Size()
			entry.ModifiedAt = st.ModTime().UnixMilli()
		}
		out = append(out, entry)
	}
	return out
}

// findSqliteCLI returns the path to a sqlite3 binary, or "" when none
// is on PATH. Used by the import endpoint to decide whether we can
// actually read state.vscdb or only report it.
func findSqliteCLI() string {
	for _, name := range []string{"sqlite3", "sqlite", "sqlite3.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// HandleLocalWindsurfStatus handles GET /dashboard/api/local-windsurf.
func (d *Deps) HandleLocalWindsurfStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	cli := findSqliteCLI()
	cand := candidateStateDBPaths()
	supported := len(cand) > 0
	resp := LocalWindsurfStatus{
		Supported:    supported,
		Platform:     runtime.GOOS,
		SqliteCLI:    cli,
		HasSqliteCLI: cli != "",
		Candidates:   cand,
	}
	if !supported {
		resp.Reason = "platform_unsupported"
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleLocalWindsurfImport handles POST /dashboard/api/local-windsurf/import
// {path: "..."} and returns any apiKey rows extracted from state.vscdb.
//
// Expects the dashboard to first GET /local-windsurf, present the user
// with the candidate paths, and POST back the chosen one. We read it
// using sqlite3 CLI — see comment at top of file for why.
func (d *Deps) HandleLocalWindsurfImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if body.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path required"})
		return
	}
	// Refuse paths that don't end in state.vscdb to prevent a path-traversal
	// shenanigan that has the operator feed a private file's contents into
	// the SQL probe (e.g. /etc/shadow). The operator already has dashboard
	// auth so this isn't a privilege boundary, but keeping the surface tight
	// is good hygiene.
	if !strings.HasSuffix(body.Path, "state.vscdb") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path must end in state.vscdb"})
		return
	}
	if _, err := os.Stat(body.Path); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "file not found"})
		return
	}
	cli := findSqliteCLI()
	if cli == "" {
		writeJSON(w, http.StatusOK, LocalWindsurfImportResult{
			Error: "sqlite3 CLI not on PATH; copy " + body.Path + " out and feed apiKey via POST /dashboard/api/accounts manually",
		})
		return
	}

	// Bound the read with a short context. state.vscdb is small (<5 MB)
	// and the query is a single SELECT — anything taking >5s is a sign the
	// CLI hung on a foreign mount or a corrupt file.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	creds, err := readWindsurfCredsViaCLI(ctx, cli, body.Path)
	if err != nil {
		writeJSON(w, http.StatusOK, LocalWindsurfImportResult{Error: err.Error()})
		return
	}
	flavor := classifyFlavor(body.Path)
	for i := range creds {
		creds[i].Path = body.Path
		creds[i].Flavor = flavor
	}
	writeJSON(w, http.StatusOK, LocalWindsurfImportResult{Found: creds})
}

// readWindsurfCredsViaCLI shells out to `sqlite3 -readonly state.vscdb
// "SELECT key, value FROM ItemTable WHERE key LIKE '%windsurfAuthStatus%';"`
// and parses the JSON value blob. The shape Windsurf stores is roughly:
//
//	{
//	  "registrationApiKey": "ws_…",
//	  "name":               "alice@example.com",
//	  "subscriptionTier":   "pro",
//	  ...
//	}
func readWindsurfCredsViaCLI(ctx context.Context, cli, path string) ([]LocalWindsurfCredential, error) {
	// `-cmd ".timeout 2000"` keeps the read from blocking forever on a
	// locked DB (the LS service holds a lock while running).
	// `-readonly` + `file:…?mode=ro` belt + braces.
	uri := "file:" + path + "?mode=ro"
	cmd := exec.CommandContext(ctx, cli,
		"-cmd", ".timeout 2000",
		"-readonly",
		uri,
		"SELECT key || '\\t' || value FROM ItemTable WHERE key LIKE '%windsurfAuthStatus%' OR key LIKE '%codeium%';",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 read failed: %s", strings.TrimSpace(string(out)))
	}
	var creds []LocalWindsurfCredential
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Each line is `key\tvalue` where value is a JSON blob.
		tab := strings.IndexByte(line, '\t')
		if tab == -1 {
			continue
		}
		val := line[tab+1:]
		var obj map[string]any
		if err := json.Unmarshal([]byte(val), &obj); err != nil {
			continue
		}
		c := LocalWindsurfCredential{}
		for _, k := range []string{"registrationApiKey", "apiKey", "api_key"} {
			if v, ok := obj[k].(string); ok && v != "" {
				c.APIKey = v
				break
			}
		}
		if c.APIKey == "" {
			continue
		}
		for _, k := range []string{"name", "email", "userEmail"} {
			if v, ok := obj[k].(string); ok && v != "" {
				c.Email = v
				break
			}
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func classifyFlavor(path string) string {
	low := strings.ToLower(path)
	switch {
	case strings.Contains(low, "windsurf-next"), strings.Contains(low, "windsurf - next"):
		return "next"
	case strings.Contains(low, "insiders"):
		return "insiders"
	case strings.Contains(low, "snap"):
		return "snap"
	default:
		return "stable"
	}
}
