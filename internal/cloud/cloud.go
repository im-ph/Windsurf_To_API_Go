// Package cloud contains the Connect-RPC client that talks to
// server.codeium.com / server.self-serve.windsurf.com for account status,
// model catalog, rate-limit preflight, and the register_user handshake.
//
// Direct port of src/windsurf-api.js and the relevant bits of
// src/dashboard/windsurf-login.js (register_user).
package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/models"
)

var serverHosts = []string{
	"server.codeium.com",
	"server.self-serve.windsurf.com",
}

const (
	pathUserStatus   = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"
	pathModelConfigs = "/exa.api_server_pb.ApiServerService/GetCascadeModelConfigs"
	pathRateLimit    = "/exa.api_server_pb.ApiServerService/CheckUserMessageRateLimit"
)

// metadataBody is the JSON envelope every Connect-RPC call needs.
type metadataBody struct {
	Metadata metadataFields `json:"metadata"`
}

type metadataFields struct {
	APIKey           string `json:"apiKey"`
	IdeName          string `json:"ideName"`
	IdeVersion       string `json:"ideVersion"`
	ExtensionName    string `json:"extensionName"`
	ExtensionVersion string `json:"extensionVersion"`
	Locale           string `json:"locale"`
}

func buildMetadata(apiKey string) metadataBody {
	return metadataBody{Metadata: metadataFields{
		APIKey: apiKey, IdeName: "windsurf", IdeVersion: "1.108.2",
		ExtensionName: "windsurf", ExtensionVersion: "1.108.2", Locale: "en",
	}}
}

func standardHeaders() map[string]string {
	return map[string]string{
		"Connect-Protocol-Version": "1",
		"Accept":                   "application/json",
		"User-Agent":               "windsurf/1.108.2",
	}
}

// ─── GetUserStatus ────────────────────────────────────────

// UserStatus is the normalised view of GetUserStatus that the dashboard +
// credit refresh loop care about.
type UserStatus struct {
	PlanName       string   `json:"planName"`
	DailyPercent   *float64 `json:"dailyPercent,omitempty"`
	WeeklyPercent  *float64 `json:"weeklyPercent,omitempty"`
	DailyResetAt   int64    `json:"dailyResetAt,omitempty"`
	WeeklyResetAt  int64    `json:"weeklyResetAt,omitempty"`
	OverageBalance *float64 `json:"overageBalance,omitempty"`
	Prompt         Credit   `json:"prompt"`
	Flex           Credit   `json:"flex"`
	PlanStart      string   `json:"planStart,omitempty"`
	PlanEnd        string   `json:"planEnd,omitempty"`
	Percent        *float64 `json:"percent,omitempty"`
	FetchedAt      int64    `json:"fetchedAt"`
	// Raw is preserved so the caller can peek at fields we haven't normalised.
	Raw json.RawMessage `json:"-"`
}

type Credit struct {
	Limit     *float64 `json:"limit,omitempty"`
	Used      *float64 `json:"used,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
}

// rawUserStatus is the upstream shape we peel apart.
type rawUserStatus struct {
	UserStatus struct {
		PlanStatus struct {
			PlanInfo struct {
				PlanName                        string  `json:"planName"`
				MonthlyPromptCredits            float64 `json:"monthlyPromptCredits"`
				MonthlyFlexCreditPurchaseAmount float64 `json:"monthlyFlexCreditPurchaseAmount"`
			} `json:"planInfo"`
			DailyQuotaRemainingPercent  *float64 `json:"dailyQuotaRemainingPercent"`
			WeeklyQuotaRemainingPercent *float64 `json:"weeklyQuotaRemainingPercent"`
			DailyQuotaResetAtUnix       any      `json:"dailyQuotaResetAtUnix"`
			WeeklyQuotaResetAtUnix      any      `json:"weeklyQuotaResetAtUnix"`
			OverageBalanceMicros        *float64 `json:"overageBalanceMicros"`
			UsedPromptCredits           float64  `json:"usedPromptCredits"`
			AvailablePromptCredits      float64  `json:"availablePromptCredits"`
			UsedFlexCredits             float64  `json:"usedFlexCredits"`
			AvailableFlexCredits        float64  `json:"availableFlexCredits"`
			PlanStart                   string   `json:"planStart"`
			PlanEnd                     string   `json:"planEnd"`
		} `json:"planStatus"`
	} `json:"userStatus"`
}

func asUnix(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func normalizeUserStatus(raw json.RawMessage) UserStatus {
	var r rawUserStatus
	_ = json.Unmarshal(raw, &r)
	ps := r.UserStatus.PlanStatus
	div := func(n float64) *float64 {
		x := n / 100
		return &x
	}
	out := UserStatus{
		PlanName:      ps.PlanInfo.PlanName,
		DailyPercent:  ps.DailyQuotaRemainingPercent,
		WeeklyPercent: ps.WeeklyQuotaRemainingPercent,
		DailyResetAt:  asUnix(ps.DailyQuotaResetAtUnix),
		WeeklyResetAt: asUnix(ps.WeeklyQuotaResetAtUnix),
		Prompt: Credit{
			Limit: div(ps.PlanInfo.MonthlyPromptCredits),
			Used:  div(ps.UsedPromptCredits),
			Remaining: func() *float64 {
				v := ps.AvailablePromptCredits / 100
				return &v
			}(),
		},
		Flex: Credit{
			Limit:     div(ps.PlanInfo.MonthlyFlexCreditPurchaseAmount),
			Used:      div(ps.UsedFlexCredits),
			Remaining: div(ps.AvailableFlexCredits),
		},
		PlanStart: ps.PlanStart,
		PlanEnd:   ps.PlanEnd,
		Raw:       raw,
		FetchedAt: time.Now().UnixMilli(),
	}
	if ps.OverageBalanceMicros != nil {
		v := *ps.OverageBalanceMicros / 1_000_000
		out.OverageBalance = &v
	}
	if out.PlanName == "" {
		out.PlanName = "Unknown"
	}

	// Derive single display percent: prefer daily, else prompt ratio.
	if out.DailyPercent != nil {
		cp := *out.DailyPercent
		out.Percent = &cp
	} else if out.Prompt.Limit != nil && *out.Prompt.Limit > 0 && out.Prompt.Remaining != nil {
		v := (*out.Prompt.Remaining / *out.Prompt.Limit) * 100
		out.Percent = &v
	}
	return out
}

// GetUserStatus fetches plan info and credit balance.
func GetUserStatus(apiKey string, proxy *langserver.Proxy) (UserStatus, error) {
	body, _ := json.Marshal(buildMetadata(apiKey))
	var lastErr error
	for _, host := range serverHosts {
		status, raw, err := postJSON("https://"+host+pathUserStatus, body, proxy, standardHeaders())
		if err != nil {
			lastErr = err
			logx.Debug("GetUserStatus host=%s err=%s", host, err.Error())
			continue
		}
		if status >= 400 {
			lastErr = fmt.Errorf("GetUserStatus %s → %d: %s", host, status, snippet(raw, 160))
			continue
		}
		return normalizeUserStatus(raw), nil
	}
	if lastErr == nil {
		lastErr = errors.New("GetUserStatus: all hosts failed")
	}
	return UserStatus{}, lastErr
}

// ─── GetCascadeModelConfigs ───────────────────────────────

type modelConfigsResp struct {
	ClientModelConfigs []models.CloudModel `json:"clientModelConfigs"`
}

// GetCascadeModelConfigs pulls the live catalog so NEW modelUids can merge
// into the hand-curated seed.
func GetCascadeModelConfigs(apiKey string, proxy *langserver.Proxy) ([]models.CloudModel, error) {
	body, _ := json.Marshal(buildMetadata(apiKey))
	var lastErr error
	for _, host := range serverHosts {
		status, raw, err := postJSON("https://"+host+pathModelConfigs, body, proxy, standardHeaders())
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 400 {
			lastErr = fmt.Errorf("GetCascadeModelConfigs %s → %d: %s", host, status, snippet(raw, 160))
			continue
		}
		var parsed modelConfigsResp
		if err := json.Unmarshal(raw, &parsed); err != nil {
			lastErr = err
			continue
		}
		return parsed.ClientModelConfigs, nil
	}
	return nil, lastErr
}

// ─── CheckUserMessageRateLimit ────────────────────────────

type RateLimit struct {
	HasCapacity       bool  `json:"hasCapacity"`
	MessagesRemaining int64 `json:"messagesRemaining"`
	MaxMessages       int64 `json:"maxMessages"`
}

// CheckMessageRateLimit returns per-account capacity. On transport failure it
// returns HasCapacity=true (fail open) — matches src/windsurf-api.js.
func CheckMessageRateLimit(apiKey string, proxy *langserver.Proxy) (RateLimit, error) {
	body, _ := json.Marshal(buildMetadata(apiKey))
	var lastErr error
	for _, host := range serverHosts {
		status, raw, err := postJSON("https://"+host+pathRateLimit, body, proxy, standardHeaders())
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 400 {
			lastErr = fmt.Errorf("CheckRateLimit %s → %d: %s", host, status, snippet(raw, 160))
			continue
		}
		var parsed struct {
			HasCapacity       *bool  `json:"hasCapacity"`
			MessagesRemaining *int64 `json:"messagesRemaining"`
			MaxMessages       *int64 `json:"maxMessages"`
		}
		_ = json.Unmarshal(raw, &parsed)
		out := RateLimit{HasCapacity: true, MessagesRemaining: -1, MaxMessages: -1}
		if parsed.HasCapacity != nil {
			out.HasCapacity = *parsed.HasCapacity
		}
		if parsed.MessagesRemaining != nil {
			out.MessagesRemaining = *parsed.MessagesRemaining
		}
		if parsed.MaxMessages != nil {
			out.MaxMessages = *parsed.MaxMessages
		}
		return out, nil
	}
	logx.Warn("CheckRateLimit failed: %s", safeErr(lastErr))
	return RateLimit{HasCapacity: true, MessagesRemaining: -1, MaxMessages: -1}, lastErr
}

// ─── register_user ────────────────────────────────────────

// RegisterResult is the response of api.codeium.com/register_user/.
type RegisterResult struct {
	APIKey       string `json:"api_key"`
	Name         string `json:"name"`
	APIServerURL string `json:"api_server_url"`
}

// RegisterUser exchanges a Firebase id token for a Codeium API key.
func RegisterUser(idToken string, proxy *langserver.Proxy) (RegisterResult, error) {
	body, _ := json.Marshal(map[string]string{"firebase_id_token": idToken})
	status, raw, err := postJSON("https://api.codeium.com/register_user/", body, proxy, standardHeaders())
	if err != nil {
		return RegisterResult{}, err
	}
	if status >= 400 {
		return RegisterResult{}, fmt.Errorf("register_user failed (%d): %s", status, snippet(raw, 200))
	}
	var out RegisterResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("register_user parse: %w", err)
	}
	if out.APIKey == "" {
		return out, fmt.Errorf("register_user response missing api_key: %s", snippet(raw, 200))
	}
	return out, nil
}

// ─── helpers ──────────────────────────────────────────────

func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
func safeErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
