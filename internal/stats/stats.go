// Package stats collects per-request statistics (aggregate + per-model +
// per-account + 72h hourly buckets) and serialises to stats.json with a
// debounced writer. Mirrors src/dashboard/stats.js.
package stats

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"windsurfapi/internal/atomicfile"
	"windsurfapi/internal/models"
)

type ModelCounts struct {
	Requests     int     `json:"requests"`
	Success      int     `json:"success"`
	Errors       int     `json:"errors"`
	TotalMs      int64   `json:"totalMs"`
	AvgMs        int64   `json:"avgMs"`
	P50Ms        int64   `json:"p50Ms"`
	P95Ms        int64   `json:"p95Ms"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	// First / Last request tracking — operators asked for this on the Stats
	// page so they can see "when did this model first appear" and "what
	// was the most recent call's latency" at a glance without trawling logs.
	// Timestamps are unix-ms; durations are in ms.
	FirstAt int64 `json:"firstAt,omitempty"`
	FirstMs int64 `json:"firstMs,omitempty"`
	LastAt  int64 `json:"lastAt,omitempty"`
	LastMs  int64 `json:"lastMs,omitempty"`
	recent  []int64 `json:"-"`
}

type AccountCounts struct {
	Requests int `json:"requests"`
	Success  int `json:"success"`
	Errors   int `json:"errors"`
}

type HourBucket struct {
	Hour     string `json:"hour"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
}

type internalState struct {
	StartedAt         int64                     `json:"startedAt"`
	TotalRequests     int                       `json:"totalRequests"`
	SuccessCount      int                       `json:"successCount"`
	ErrorCount        int                       `json:"errorCount"`
	TotalInputTokens  int64                     `json:"totalInputTokens"`
	TotalOutputTokens int64                     `json:"totalOutputTokens"`
	TotalCostUSD      float64                   `json:"totalCostUsd"`
	// UpstreamStatus counts HTTP status codes returned by the upstream to
	// this service. Keys are string for easy JSON consumption ("200", "429",
	// "5xx_transport" for socket-level errors with no HTTP status).
	UpstreamStatus map[string]int            `json:"upstreamStatus"`
	ModelCounts    map[string]*ModelCounts   `json:"modelCounts"`
	AccountCounts  map[string]*AccountCounts `json:"accountCounts"`
	HourlyBuckets  []HourBucket              `json:"hourlyBuckets"`
}

var (
	mu    sync.Mutex
	state = internalState{
		StartedAt:      time.Now().UnixMilli(),
		UpstreamStatus: map[string]int{},
		ModelCounts:    map[string]*ModelCounts{},
		AccountCounts:  map[string]*AccountCounts{},
	}
	path    = "stats.json"
	saveTmr *time.Timer
)

// Init loads stats.json — call once at startup.
func Init() {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &state)
	if state.ModelCounts == nil {
		state.ModelCounts = map[string]*ModelCounts{}
	}
	if state.AccountCounts == nil {
		state.AccountCounts = map[string]*AccountCounts{}
	}
	if state.UpstreamStatus == nil {
		state.UpstreamStatus = map[string]int{}
	}
	if state.StartedAt == 0 {
		state.StartedAt = time.Now().UnixMilli()
	}

	// One-shot migration: before 1.4.3 we mistakenly recorded AccountCounts
	// keyed by `acct.APIKey[:8]` instead of `acct.ID`. Those garbled keys
	// (API-key prefixes like "devin-se", "sk-ws-01", …) clutter the
	// dashboard account-dimension table because they never match any real
	// account ID. auth.newID() produces exactly 8 lowercase hex chars; any
	// key that doesn't fit that shape is legacy garbage and we drop it.
	// Idempotent — on a clean post-1.4.3 stats.json this loop is a no-op.
	dropped := 0
	for k := range state.AccountCounts {
		if !isHex8(k) {
			delete(state.AccountCounts, k)
			dropped++
		}
	}
	if dropped > 0 {
		// Re-save immediately so the cleanup survives even if the service
		// is killed before the next Record triggers scheduleSave().
		if out, err := json.MarshalIndent(&state, "", "  "); err == nil {
			_ = atomicfile.Write(path, out)
		}
	}
}

func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func scheduleSave() {
	if saveTmr != nil {
		saveTmr.Stop()
	}
	saveTmr = time.AfterFunc(5*time.Second, func() {
		mu.Lock()
		data, _ := json.MarshalIndent(&state, "", "  ")
		mu.Unlock()
		_ = atomicfile.Write(path, data)
	})
}

func getHourKey() string {
	d := time.Now().UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), d.Hour(), 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// Record one completed request. token counts + upstreamStatus are optional
// (pass 0 / 0 / 0 when unavailable).
func Record(model string, success bool, durationMs int64, accountID string,
	inputTokens, outputTokens int64, upstreamStatus int,
) {
	mu.Lock()
	defer mu.Unlock()

	state.TotalRequests++
	if success {
		state.SuccessCount++
	} else {
		state.ErrorCount++
	}

	// Upstream status code histogram — 0 means "didn't reach upstream"
	// (treated as a transport_error bucket).
	if state.UpstreamStatus == nil {
		state.UpstreamStatus = map[string]int{}
	}
	statusKey := "0"
	if upstreamStatus > 0 {
		statusKey = strconv.Itoa(upstreamStatus)
	}
	state.UpstreamStatus[statusKey]++

	// Token + cost accounting at the global level.
	if inputTokens > 0 {
		state.TotalInputTokens += inputTokens
	}
	if outputTokens > 0 {
		state.TotalOutputTokens += outputTokens
	}
	cost := models.PriceFor(model, inputTokens, outputTokens)
	state.TotalCostUSD += cost

	mc, ok := state.ModelCounts[model]
	if !ok {
		mc = &ModelCounts{}
		state.ModelCounts[model] = mc
	}
	nowMs := time.Now().UnixMilli()
	if mc.FirstAt == 0 {
		mc.FirstAt = nowMs
		mc.FirstMs = durationMs
	}
	mc.LastAt = nowMs
	mc.LastMs = durationMs
	mc.Requests++
	if success {
		mc.Success++
	} else {
		mc.Errors++
	}
	mc.TotalMs += durationMs
	if durationMs > 0 {
		mc.recent = append(mc.recent, durationMs)
		if len(mc.recent) > 200 {
			mc.recent = mc.recent[len(mc.recent)-200:]
		}
	}
	mc.InputTokens += inputTokens
	mc.OutputTokens += outputTokens
	mc.CostUSD += cost

	if accountID != "" {
		key := accountID
		if len(key) > 8 {
			key = key[:8]
		}
		ac, ok := state.AccountCounts[key]
		if !ok {
			ac = &AccountCounts{}
			state.AccountCounts[key] = ac
		}
		ac.Requests++
		if success {
			ac.Success++
		} else {
			ac.Errors++
		}
	}

	hk := getHourKey()
	var bucket *HourBucket
	for i := range state.HourlyBuckets {
		if state.HourlyBuckets[i].Hour == hk {
			bucket = &state.HourlyBuckets[i]
			break
		}
	}
	if bucket == nil {
		state.HourlyBuckets = append(state.HourlyBuckets, HourBucket{Hour: hk})
		if len(state.HourlyBuckets) > 72 {
			state.HourlyBuckets = state.HourlyBuckets[len(state.HourlyBuckets)-72:]
		}
		bucket = &state.HourlyBuckets[len(state.HourlyBuckets)-1]
	}
	bucket.Requests++
	if !success {
		bucket.Errors++
	}

	scheduleSave()
}

// Snapshot returns a deep-copy view with percentiles computed.
type Snapshot struct {
	StartedAt         int64                     `json:"startedAt"`
	TotalRequests     int                       `json:"totalRequests"`
	SuccessCount      int                       `json:"successCount"`
	ErrorCount        int                       `json:"errorCount"`
	TotalInputTokens  int64                     `json:"totalInputTokens"`
	TotalOutputTokens int64                     `json:"totalOutputTokens"`
	TotalCostUSD      float64                   `json:"totalCostUsd"`
	UpstreamStatus    map[string]int            `json:"upstreamStatus"`
	ModelCounts       map[string]*ModelCounts   `json:"modelCounts"`
	AccountCounts     map[string]*AccountCounts `json:"accountCounts"`
	HourlyBuckets     []HourBucket              `json:"hourlyBuckets"`
}

func Get() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	s := Snapshot{
		StartedAt:         state.StartedAt,
		TotalRequests:     state.TotalRequests,
		SuccessCount:      state.SuccessCount,
		ErrorCount:        state.ErrorCount,
		TotalInputTokens:  state.TotalInputTokens,
		TotalOutputTokens: state.TotalOutputTokens,
		TotalCostUSD:      state.TotalCostUSD,
		UpstreamStatus:    map[string]int{},
		HourlyBuckets:     append([]HourBucket(nil), state.HourlyBuckets...),
		ModelCounts:       map[string]*ModelCounts{},
		AccountCounts:     map[string]*AccountCounts{},
	}
	for k, v := range state.UpstreamStatus {
		s.UpstreamStatus[k] = v
	}
	for k, v := range state.ModelCounts {
		sorted := append([]int64(nil), v.recent...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		out := *v
		if v.Requests > 0 {
			out.AvgMs = v.TotalMs / int64(v.Requests)
		}
		out.P50Ms = pct(sorted, 0.5)
		out.P95Ms = pct(sorted, 0.95)
		s.ModelCounts[k] = &out
	}
	for k, v := range state.AccountCounts {
		cp := *v
		s.AccountCounts[k] = &cp
	}
	return s
}

func pct(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * p)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Reset wipes all counters.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	state = internalState{
		StartedAt:      time.Now().UnixMilli(),
		UpstreamStatus: map[string]int{},
		ModelCounts:    map[string]*ModelCounts{},
		AccountCounts:  map[string]*AccountCounts{},
	}
	scheduleSave()
}
