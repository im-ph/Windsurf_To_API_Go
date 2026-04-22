// Chat completions handler — the hot path of the whole service. Mirrors
// src/handlers/chat.js line-for-line where the behaviour is observable.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"windsurfapi/internal/auth"
	"windsurfapi/internal/cache"
	"windsurfapi/internal/client"
	"windsurfapi/internal/cloud"
	"windsurfapi/internal/convpool"
	"windsurfapi/internal/imagex"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/modelaccess"
	"windsurfapi/internal/models"
	"windsurfapi/internal/proxycfg"
	"windsurfapi/internal/runtimecfg"
	"windsurfapi/internal/sanitize"
	"windsurfapi/internal/stats"
	"windsurfapi/internal/toolemu"
	"windsurfapi/internal/windsurf"
)

const (
	heartbeatMS    = 15 * time.Second
	queueRetryMS   = time.Second
	queueMaxWaitMS = 30 * time.Second
	streamMaxTries = 10
)

// ChatRequestBody is a permissive decoder — we care about the
// OpenAI-compatible subset and forward everything else verbatim.
type ChatRequestBody struct {
	Model      string              `json:"model"`
	Stream     bool                `json:"stream"`
	MaxTokens  *int                `json:"max_tokens,omitempty"`
	Messages   []toolemu.OAIMessage `json:"messages"`
	Tools      []toolemu.Tool      `json:"tools,omitempty"`
	ToolChoice json.RawMessage     `json:"tool_choice,omitempty"`
}

// ChatCompletions handles POST /v1/chat/completions.
func (d *Deps) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "method_not_allowed"))
		return
	}
	if !d.Pool.IsAuthenticated() {
		writeJSON(w, http.StatusServiceUnavailable,
			errBody("No active accounts. POST /auth/login to add accounts.", "auth_error"))
		return
	}
	// 8 MB cap — the old 32 MB let 30 concurrent callers OOM a 1 GB VM.
	// Remote images go through imagex (5 MB/image ceiling) and their base64
	// fits comfortably inside this, so no realistic request gets truncated.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("body read failed", "invalid_request"))
		return
	}
	var body ChatRequestBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Invalid JSON", "invalid_request"))
		return
	}
	if len(body.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("messages must be an array", "invalid_request"))
		return
	}

	// Resolve model.
	wanted := body.Model
	if wanted == "" {
		wanted = d.Cfg.DefaultModel
	}
	modelKey := models.Resolve(wanted)
	info := models.Get(modelKey)
	if info == nil {
		writeJSON(w, http.StatusBadRequest, errBody("unknown model: "+wanted, "invalid_model"))
		return
	}
	displayModel := info.Name
	if displayModel == "" {
		displayModel = modelKey
	}

	// Global access policy.
	if dec := modelaccess.Check(modelKey); !dec.Allowed {
		writeJSON(w, http.StatusForbidden, errBody(dec.Reason, "model_blocked"))
		return
	}

	// Fast path: does ANY active account hold entitlement for this model?
	// HasEligible is O(accounts) with no allocations — the previous
	// implementation walked `d.Pool.All()` which deep-copies every account
	// (maps + slices) per request, a 30+ account × 100 rps hot loop that
	// churned ~1 MB/sec of GC pressure on a 1 GB VM for a simple check.
	if !d.Pool.HasEligible(modelKey, models.IsTierAllowed) {
		writeJSON(w, http.StatusForbidden, errBody(
			fmt.Sprintf("模型 %s 在当前账号池中不可用（未订阅或已被封禁）", displayModel),
			"model_not_entitled"))
		return
	}

	useCascade := info.ModelUID != ""
	hasTools := len(body.Tools) > 0
	hasToolHistory := false
	for _, m := range body.Messages {
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			hasToolHistory = true
			break
		}
	}
	emulateTools := useCascade && (hasTools || hasToolHistory)

	toolPreamble := ""
	if emulateTools {
		toolPreamble = toolemu.BuildPreambleForProto(body.Tools, body.ToolChoice)
	}

	var cascadeMsgs []client.ChatMsg
	var legacyMsgs []client.ChatMsg
	if emulateTools {
		normalised := toolemu.Normalize(body.Messages)
		for i, nm := range normalised {
			msg := client.ChatMsg{Role: nm.Role, Content: nm.Content}
			// Normalize re-shuffles tool-typed turns but preserves array order
			// among non-tool ones, so the image payload on OAIMessage i still
			// maps to the NormalMessage at the same index for user turns.
			// Pull them back in by index so vision works under emulateTools too.
			if i < len(body.Messages) && nm.Role == body.Messages[i].Role {
				msg.Images = extractImages(body.Messages[i].Content)
			}
			cascadeMsgs = append(cascadeMsgs, msg)
		}
	}
	for _, m := range body.Messages {
		msg := client.ChatMsg{
			Role:    m.Role,
			Content: contentToString(m.Content),
			Images:  extractImages(m.Content),
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var lines []string
			for _, tc := range m.ToolCalls {
				name := tc.Function.Name
				if name == "" {
					name = "unknown"
				}
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				lines = append(lines, fmt.Sprintf("[called tool %s with %s]", name, args))
			}
			msg.ToolCallsText = strings.Join(lines, "\n")
		}
		if m.Role == "tool" {
			msg.ToolCallID = m.ToolCallID
		}
		legacyMsgs = append(legacyMsgs, msg)
	}
	if !emulateTools {
		cascadeMsgs = legacyMsgs
	}

	// Identity prompt injection — three-layer belt:
	//   1. Cascade proto field 8 (test_section_content, top of system prompt)
	//   2. Cascade proto field 13 (communication_section override)
	//   3. Fallback: prepend as a system-role chat message (legacy + belt)
	identityPrompt := ""
	if runtimecfg.IsEnabled("modelIdentityPrompt") && info.Provider != "" {
		identityPrompt = runtimecfg.BuildIdentityMessage(displayModel, info.Provider)
		if identityPrompt != "" {
			// Still keep the system-message prepend as a belt — if for some
			// reason Cascade drops our proto overrides, the message-level
			// identity can still reach the model via the user text payload.
			sys := client.ChatMsg{Role: "system", Content: identityPrompt}
			cascadeMsgs = append([]client.ChatMsg{sys}, cascadeMsgs...)
			legacyMsgs = append([]client.ChatMsg{sys}, legacyMsgs...)
		}
	}

	// Cache key (only applicable when no streaming or for replay).
	// IdentityPrompt participates so toggling the identity flag off between
	// two otherwise-identical requests doesn't replay a previously-stamped
	// response.
	ckey := cache.Key(cache.RequestBody{
		Model: modelKey, Messages: raw, Tools: rawFromTools(body.Tools), ToolChoice: body.ToolChoice,
		MaxTokens:      body.MaxTokens,
		IdentityPrompt: identityPrompt,
	})

	chatID := genChatID()
	created := time.Now().Unix()

	if body.Stream {
		d.streamChat(w, r, streamInput{
			ChatID: chatID, Created: created, DisplayModel: displayModel,
			ModelKey: modelKey, Info: info, Cascade: useCascade,
			CascadeMsgs: cascadeMsgs, LegacyMsgs: legacyMsgs,
			EmulateTools: emulateTools, ToolPreamble: toolPreamble,
			IdentityPrompt: identityPrompt,
			CacheKey:       ckey,
		})
		return
	}
	d.nonStreamChat(w, r, streamInput{
		ChatID: chatID, Created: created, DisplayModel: displayModel,
		ModelKey: modelKey, Info: info, Cascade: useCascade,
		CascadeMsgs: cascadeMsgs, LegacyMsgs: legacyMsgs,
		EmulateTools: emulateTools, ToolPreamble: toolPreamble,
		CacheKey: ckey,
	})
}

type streamInput struct {
	ChatID         string
	Created        int64
	DisplayModel   string
	ModelKey       string
	Info           *models.Info
	Cascade        bool
	CascadeMsgs    []client.ChatMsg
	LegacyMsgs     []client.ChatMsg
	EmulateTools   bool
	ToolPreamble   string
	IdentityPrompt string
	CacheKey       string
}

// ─── Non-stream ────────────────────────────────────────────

func (d *Deps) nonStreamChat(w http.ResponseWriter, r *http.Request, in streamInput) {
	// Cache hit.
	if v, ok := cache.Get(in.CacheKey); ok {
		logx.Info("Chat: cache HIT model=%s flow=non-stream", in.DisplayModel)
		stats.Record(in.DisplayModel, true, 0, "", 0, 0, 200)
		writeJSON(w, http.StatusOK, nonStreamBody(in, v.Text, v.Thinking, nil, nil, "stop", true))
		return
	}

	reuseEnabled := in.Cascade && !in.EmulateTools && runtimecfg.IsEnabled("cascadeConversationReuse")
	fpBefore := ""
	if reuseEnabled {
		fpBefore = convpool.FingerprintBefore(convMessagesFromClient(in.CascadeMsgs))
	}
	var reuse *convpool.Entry
	if reuseEnabled && fpBefore != "" {
		reuse = convpool.Checkout(fpBefore)
	}

	tried := []string{}
	var last *chatErr
	maxAttempts := clamp(numActive(d.Pool), 3, streamMaxTries)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var acct *auth.Selected
		if reuse != nil && attempt == 0 {
			acct = d.Pool.AcquireByKey(reuse.APIKey, in.ModelKey)
			if acct == nil {
				logx.Info("Chat: cascade reuse skipped — owning account not available")
				reuse = nil
			}
		}
		if acct == nil {
			acct = waitForAccount(r.Context(), d.Pool, tried, queueMaxWaitMS, in.ModelKey)
			if acct == nil {
				break
			}
		}
		tried = append(tried, acct.APIKey)

		px := proxycfg.Effective(acct.ID)

		// Preflight rate-limit check (experimental) — save LS round-trips
		// when the account is already out of capacity.
		if runtimecfg.IsEnabled("preflightRateLimit") {
			if rl, err := cloud.CheckMessageRateLimit(acct.APIKey, px); err == nil && !rl.HasCapacity {
				logx.Warn("Preflight: %s has no capacity (remaining=%d), skipping", acct.Email, rl.MessagesRemaining)
				d.Pool.MarkRateLimited(acct.APIKey, 5*time.Minute, in.ModelKey)
				continue
			}
		}

		ls, err := d.LSP.Ensure(r.Context(), px)
		if err != nil {
			last = &chatErr{status: http.StatusServiceUnavailable, typ: "ls_unavailable", msg: err.Error()}
			break
		}
		entry := d.LSP.Get(px)
		if entry == nil {
			entry = ls
		}
		if reuse != nil && reuse.LSPort != entry.Port {
			logx.Info("Chat: cascade reuse skipped — LS port changed")
			reuse = nil
		}

		logx.Info("Chat: model=%s flow=%s attempt=%d account=%s ls=%d turns=%d chars=%d",
			in.DisplayModel, flowName(in.Cascade), attempt+1, acct.Email, entry.Port,
			len(in.CascadeMsgs), totalChars(in.CascadeMsgs))

		cli := client.New(acct.APIKey, entry)
		res, cerr := d.runOnce(r.Context(), cli, in, reuse)
		cli.Close()

		if cerr != nil {
			if cerr.isRateLimit {
				d.Pool.MarkRateLimited(acct.APIKey, rlDuration(cerr), in.ModelKey)
				d.Pool.UpdateCapability(acct.APIKey, in.ModelKey, false, "rate_limit")
				logx.Warn("Account %s rate-limited on %s (window=%s), trying next",
					acct.Email, in.DisplayModel, rlDuration(cerr))
				last = cerr
				continue
			}
			if cerr.isInternal {
				d.Pool.ReportInternalError(acct.APIKey)
				last = cerr
				continue
			}
			if cerr.isAuthFail {
				d.Pool.ReportError(acct.APIKey)
			}
			if cerr.isModel {
				d.Pool.UpdateCapability(acct.APIKey, in.ModelKey, false, "model_error")
				logx.Warn("Account %s cannot serve %s, trying next", acct.Email, in.DisplayModel)
				last = cerr
				continue
			}
			last = cerr
			break
		}

		d.Pool.ReportSuccess(acct.APIKey)
		d.Pool.UpdateCapability(acct.APIKey, in.ModelKey, true, "success")
		var inTok, outTok int64
		if res.Usage != nil {
			inTok = int64(res.Usage.PromptTokens)
			outTok = int64(res.Usage.CompletionTokens)
		}
		stats.Record(in.DisplayModel, true,
			time.Since(time.Unix(in.Created, 0)).Milliseconds(),
			acct.APIKey, inTok, outTok, 200)

		if reuseEnabled && res.CascadeID != "" && res.Text != "" {
			fpAfter := convpool.FingerprintAfter(convMessagesFromClient(in.CascadeMsgs), res.Text)
			convpool.Checkin(fpAfter, convpool.Entry{
				CascadeID: res.CascadeID, SessionID: res.SessionID,
				LSPort: entry.Port, APIKey: acct.APIKey,
			})
		}

		if len(res.ToolCalls) == 0 && (res.Text != "" || res.Thinking != "") {
			cache.Set(in.CacheKey, cache.Entry{Text: res.Text, Thinking: res.Thinking})
		}

		finish := "stop"
		if len(res.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		writeJSON(w, http.StatusOK, nonStreamBody(in, res.Text, res.Thinking, res.ToolCalls, res.Usage, finish, false))
		return
	}

	// ── Retry exhausted ──
	var lastStatus int
	if last != nil {
		lastStatus = last.status
	}
	stats.Record(in.DisplayModel, false,
		time.Since(time.Unix(in.Created, 0)).Milliseconds(),
		"", 0, 0, lastStatus)
	if last == nil {
		if all, retry := d.Pool.IsAllRateLimited(in.ModelKey); all {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retry/1000+1))
			writeJSON(w, http.StatusTooManyRequests, errBody(
				fmt.Sprintf("%s 所有账号均已达速率限制，请 %d 秒后重试", in.DisplayModel, retry/1000+1),
				"rate_limit_exceeded"))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, errBody("No active accounts available", "pool_exhausted"))
		return
	}
	writeJSON(w, last.status, errBody(sanitize.Text(last.msg), last.typ))
}

// runOnce runs a single Cascade/Legacy attempt and classifies the error.
type chatErr struct {
	status      int
	typ         string
	msg         string
	isModel     bool
	isRateLimit bool
	isInternal  bool
	isAuthFail  bool
	// retryAfter is the server-reported rate-limit window parsed from the
	// error message (e.g. "27m31s"). Zero means "not parseable — use the
	// caller's fallback". Does NOT include the pool's safety grace; the
	// pool layer adds that separately.
	retryAfter time.Duration
}

var (
	reRateLimit = regexp.MustCompile(`(?i)rate limit|rate_limit|too many requests|quota`)
	reInternal  = regexp.MustCompile(`(?i)internal error occurred.*error id`)
	reAuthFail  = regexp.MustCompile(`(?i)unauthenticated|invalid api key|invalid_grant|permission_denied.*account`)

	// Upstream rate-limit messages carry the retry window in several forms.
	// Try each matcher in order — first hit wins.
	//
	//   - Go-duration style appearing anywhere in the message ("27m31s",
	//     "5m", "300s", "1h15m"). This is what Codeium actually emits.
	//   - Loose "retry after 30 seconds" / "wait 5 minutes" prose.
	//   - Bare "retry_after: 30" integer (seconds).
	// The Go-duration regex requires the value to follow a rate-limit-ish
	// keyword ("retry", "wait", "in", "after", "for") — the old pattern
	// `\b(\d+h)?(\d+m)?(\d+s)?\b` would match ANY "5s" token including
	// random log lines like "took 5s before ECONNREFUSED", misreading
	// transient network errors as rate-limit responses and triggering 5-min
	// account quarantine on healthy accounts.
	reRetryGoDuration = regexp.MustCompile(`(?i)(?:retry|wait|after|in|for)[-_ :]+((?:\d+h)?(?:\d+m)?(?:\d+s)+)\b`)
	reRetryProse      = regexp.MustCompile(`(?i)(?:retry\s+after|wait|try\s+again\s+in|please\s+wait|available\s+in)\s+(\d+)\s*(second|minute|hour|s|m|h)\b`)
	reRetrySecondsKey = regexp.MustCompile(`(?i)retry[-_ ]?after[-_ ]?(?:seconds)?\s*[:=]\s*(\d+)`)
)

// rlDuration is the effective rate-limit window for MarkRateLimited: the
// server-reported retryAfter when available, otherwise a 5-minute default
// (matches the JS service's fallback). The pool layer adds its own grace.
func rlDuration(ce *chatErr) time.Duration {
	if ce != nil && ce.retryAfter > 0 {
		return ce.retryAfter
	}
	return 5 * time.Minute
}

// parseRetryAfter scans an upstream error message for the rate-limit window
// the server told us to wait. Returns zero when no window is found — the
// caller should fall back to a sane default like 5 minutes.
func parseRetryAfter(msg string) time.Duration {
	if msg == "" {
		return 0
	}
	// Go-duration — the regex now captures the duration in group 1 and
	// only fires after a keyword (retry/wait/after/in/for), so random "5s"
	// tokens in transient-error logs can't trigger a false ban anymore.
	for _, m := range reRetryGoDuration.FindAllStringSubmatch(msg, -1) {
		if len(m) < 2 || m[1] == "" {
			continue
		}
		if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
			return d
		}
	}
	if m := reRetryProse.FindStringSubmatch(msg); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			return 0
		}
		switch strings.ToLower(m[2]) {
		case "h", "hour":
			return time.Duration(n) * time.Hour
		case "m", "minute":
			return time.Duration(n) * time.Minute
		default:
			return time.Duration(n) * time.Second
		}
	}
	if m := reRetrySecondsKey.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

// runResult bundles everything the attempt produced so we can check into the
// conversation pool with a real cascadeID.
type runResult struct {
	Text      string
	Thinking  string
	ToolCalls []toolemu.ToolCall
	Usage     *usageBody
	CascadeID string
	SessionID string
}

func (d *Deps) runOnce(ctx context.Context, cli *client.Client, in streamInput, reuse *convpool.Entry) (*runResult, *chatErr) {
	var cascadeOpts client.CascadeOptions
	cascadeOpts.ToolPreamble = in.ToolPreamble
	cascadeOpts.IdentityPrompt = in.IdentityPrompt
	if reuse != nil {
		cascadeOpts.ReuseEntry = &client.ReuseRef{CascadeID: reuse.CascadeID, SessionID: reuse.SessionID}
	}

	out := &runResult{}
	if in.Cascade {
		res, err := cli.CascadeChat(ctx, in.CascadeMsgs, in.Info.Enum, in.Info.ModelUID, cascadeOpts)
		if err != nil {
			return nil, classify(err)
		}
		out.CascadeID = res.CascadeID
		out.SessionID = res.SessionID
		text := sanitize.Text(res.Text)
		thinking := sanitize.Text(res.Thinking)
		parsed := toolemu.ParseAll(text)
		out.Text = parsed.Text
		out.Thinking = thinking
		if in.EmulateTools {
			out.ToolCalls = parsed.ToolCalls
		}
		out.Usage = buildUsageFromCascade(res.Usage, in.CascadeMsgs, out.Text, out.Thinking)
		return out, nil
	}
	chunks, err := cli.RawChat(ctx, in.LegacyMsgs, in.Info.Enum, in.Info.ModelUID, nil)
	if err != nil {
		return nil, classify(err)
	}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
	}
	out.Text = sanitize.Text(b.String())
	out.Usage = buildUsageFromCascade(nil, in.LegacyMsgs, out.Text, "")
	return out, nil
}

func classify(err error) *chatErr {
	if err == nil {
		return nil
	}
	msg := err.Error()
	out := &chatErr{msg: msg, status: http.StatusBadGateway, typ: "upstream_error"}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.typ = "client_cancelled"
		return out
	}
	if client.IsModelError(err) {
		out.isModel = true
		out.status = http.StatusForbidden
		out.typ = "model_not_available"
	}
	if reRateLimit.MatchString(msg) {
		out.isRateLimit = true
		out.isModel = true
		out.status = http.StatusTooManyRequests
		out.typ = "rate_limit_exceeded"
		out.retryAfter = parseRetryAfter(msg)
	}
	if reInternal.MatchString(msg) {
		out.isInternal = true
		out.isModel = true
	}
	if reAuthFail.MatchString(msg) {
		out.isAuthFail = true
	}
	return out
}

// ─── Stream ───────────────────────────────────────────────

func (d *Deps) streamChat(w http.ResponseWriter, r *http.Request, in streamInput) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errBody("streaming not supported", "server_error"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// http.ResponseWriter.Write is not safe for concurrent use. The heartbeat
	// goroutine and the main handler both write to w; without this mutex the
	// two can interleave "data: {...}\n\n" frames mid-write and produce
	// malformed SSE, or hit Go's built-in race detector. wmu is the single
	// serialiser every writer must go through.
	var wmu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		wmu.Unlock()
	}
	sendRaw := func(s string) {
		wmu.Lock()
		_, _ = fmt.Fprint(w, s)
		flusher.Flush()
		wmu.Unlock()
	}

	// SSE heartbeat — uses the same wmu so ": ping\n\n" never splits a data
	// frame. The handler must wait for this goroutine to actually exit
	// before returning, otherwise the net/http runtime can reclaim `w` while
	// the heartbeat is mid-write and log "http: superfluous response…"
	// noise (and in degenerate cases write to a closed connection). We
	// couple hbCancel + hbExited so the defer both signals stop AND blocks
	// until the goroutine has left the select loop.
	hbCtx, hbCancel := context.WithCancel(r.Context())
	hbExited := make(chan struct{})
	defer func() {
		hbCancel()
		<-hbExited
	}()
	go func() {
		defer close(hbExited)
		t := time.NewTicker(heartbeatMS)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				if hbCtx.Err() != nil {
					return
				}
				sendRaw(": ping\n\n")
			}
		}
	}()

	// Cache replay path.
	if v, ok := cache.Get(in.CacheKey); ok {
		logx.Info("Chat: cache HIT model=%s flow=stream", in.DisplayModel)
		stats.Record(in.DisplayModel, true, 0, "", 0, 0, 200)
		send(chunkRole(in))
		if v.Thinking != "" {
			send(chunkThinking(in, v.Thinking))
		}
		if v.Text != "" {
			send(chunkContent(in, v.Text))
		}
		send(chunkFinish(in, "stop", cachedUsage(in.CascadeMsgs, v.Text)))
		sendRaw("data: [DONE]\n\n")
		return
	}

	start := time.Now()
	tried := []string{}
	rolePrinted := false
	hadSuccess := false
	collected := []toolemu.ToolCall{}
	var accText, accThink strings.Builder
	var curAPIKey string

	var pathT sanitize.Stream
	var pathK sanitize.Stream
	var parser toolemu.StreamParser

	emitContent := func(s string) {
		if s == "" {
			return
		}
		accText.WriteString(s)
		send(chunkContent(in, s))
	}
	emitThink := func(s string) {
		if s == "" {
			return
		}
		accThink.WriteString(s)
		send(chunkThinking(in, s))
	}
	emitTool := func(tc toolemu.ToolCall, idx int) {
		send(map[string]any{
			"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"tool_calls": []map[string]any{{
					"index": idx, "id": tc.ID, "type": "function",
					"function": map[string]any{"name": tc.Name, "arguments": sanitize.Text(tc.ArgumentsJSON)},
				}}},
				"finish_reason": nil,
			}},
		})
	}

	onChunk := func(c client.Chunk) {
		if !rolePrinted {
			rolePrinted = true
			send(chunkRole(in))
		}
		hadSuccess = true
		if c.Text != "" {
			if in.Cascade {
				out := parser.Feed(c.Text)
				if out.Text != "" {
					emitContent(pathT.Feed(out.Text))
				}
				if in.EmulateTools {
					for _, tc := range out.ToolCalls {
						idx := len(collected)
						collected = append(collected, tc)
						emitTool(tc, idx)
					}
				}
			} else {
				emitContent(pathT.Feed(c.Text))
			}
		}
		if c.Thinking != "" {
			emitThink(pathK.Feed(c.Thinking))
		}
	}

	reuseEnabled := in.Cascade && !in.EmulateTools && runtimecfg.IsEnabled("cascadeConversationReuse")
	var reuse *convpool.Entry
	if reuseEnabled {
		fp := convpool.FingerprintBefore(convMessagesFromClient(in.CascadeMsgs))
		if fp != "" {
			reuse = convpool.Checkout(fp)
		}
	}

	maxAttempts := clamp(numActive(d.Pool), 3, streamMaxTries)
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if r.Context().Err() != nil {
			return
		}
		var acct *auth.Selected
		if reuse != nil && attempt == 0 {
			acct = d.Pool.AcquireByKey(reuse.APIKey, in.ModelKey)
			if acct == nil {
				reuse = nil
			}
		}
		if acct == nil {
			acct = waitForAccount(r.Context(), d.Pool, tried, queueMaxWaitMS, in.ModelKey)
			if acct == nil {
				break
			}
		}
		tried = append(tried, acct.APIKey)
		curAPIKey = acct.APIKey

		px := proxycfg.Effective(acct.ID)

		if runtimecfg.IsEnabled("preflightRateLimit") {
			if rl, err := cloud.CheckMessageRateLimit(acct.APIKey, px); err == nil && !rl.HasCapacity {
				logx.Warn("Preflight: %s has no capacity (remaining=%d), skipping", acct.Email, rl.MessagesRemaining)
				d.Pool.MarkRateLimited(acct.APIKey, 5*time.Minute, in.ModelKey)
				continue
			}
		}

		_, err := d.LSP.Ensure(r.Context(), px)
		if err != nil {
			lastErr = err
			break
		}
		entry := d.LSP.Get(px)
		if entry == nil {
			lastErr = errors.New("No LS instance available")
			break
		}
		logx.Info("Chat: model=%s flow=%s stream=true attempt=%d account=%s ls=%d turns=%d",
			in.DisplayModel, flowName(in.Cascade), attempt+1, acct.Email, entry.Port, len(in.CascadeMsgs))

		cli := client.New(acct.APIKey, entry)

		var cascadeResult *client.CascadeResult
		if in.Cascade {
			opts := client.CascadeOptions{
				ToolPreamble:   in.ToolPreamble,
				IdentityPrompt: in.IdentityPrompt,
				OnChunk:        onChunk,
			}
			if reuse != nil {
				opts.ReuseEntry = &client.ReuseRef{CascadeID: reuse.CascadeID, SessionID: reuse.SessionID}
			}
			cascadeResult, err = cli.CascadeChat(r.Context(), in.CascadeMsgs, in.Info.Enum, in.Info.ModelUID, opts)
		} else {
			_, err = cli.RawChat(r.Context(), in.LegacyMsgs, in.Info.Enum, in.Info.ModelUID, onChunk)
		}
		cli.Close()

		if err != nil {
			lastErr = err
			ce := classify(err)
			if ce == nil {
				break
			}
			if ce.isAuthFail {
				d.Pool.ReportError(acct.APIKey)
			}
			if ce.isRateLimit {
				d.Pool.MarkRateLimited(acct.APIKey, rlDuration(ce), in.ModelKey)
			}
			if ce.isInternal {
				d.Pool.ReportInternalError(acct.APIKey)
			}
			if ce.isModel && !ce.isRateLimit && !ce.isInternal {
				d.Pool.UpdateCapability(acct.APIKey, in.ModelKey, false, "model_error")
			}
			if !hadSuccess && (ce.isModel || ce.isRateLimit) {
				reuse = nil
				continue
			}
			break
		}

		// Tail flush.
		flush := parser.Flush()
		if flush.Text != "" {
			emitContent(pathT.Feed(flush.Text))
		}
		if in.EmulateTools {
			for _, tc := range flush.ToolCalls {
				idx := len(collected)
				collected = append(collected, tc)
				emitTool(tc, idx)
			}
		}
		emitContent(pathT.Flush())
		emitThink(pathK.Flush())

		if hadSuccess {
			d.Pool.ReportSuccess(acct.APIKey)
		}
		d.Pool.UpdateCapability(acct.APIKey, in.ModelKey, true, "success")

		if !rolePrinted {
			send(chunkRole(in))
		}
		finish := "stop"
		if len(collected) > 0 {
			finish = "tool_calls"
		}
		var usage *usageBody
		if cascadeResult != nil {
			usage = buildUsageFromCascade(cascadeResult.Usage, in.CascadeMsgs, accText.String(), accThink.String())
		} else {
			usage = buildUsageFromCascade(nil, in.LegacyMsgs, accText.String(), "")
		}
		// Record after usage is computed so we get real token counts.
		var inTok, outTok int64
		if usage != nil {
			inTok = int64(usage.PromptTokens)
			outTok = int64(usage.CompletionTokens)
		}
		stats.Record(in.DisplayModel, true, time.Since(start).Milliseconds(),
			acct.APIKey, inTok, outTok, 200)
		send(chunkFinish(in, finish, usage))
		// include_usage-style terminal chunk
		send(map[string]any{
			"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
			"choices": []any{}, "usage": usage,
		})
		sendRaw("data: [DONE]\n\n")

		if len(collected) == 0 && (accText.Len() > 0 || accThink.Len() > 0) {
			cache.Set(in.CacheKey, cache.Entry{Text: accText.String(), Thinking: accThink.String()})
		}
		return
	}

	// All attempts failed. lastErr is plain `error` here (not *chatErr), so
	// we can't extract an upstream status — record with 0 to land in the
	// "transport / unknown" bucket on the dashboard.
	stats.Record(in.DisplayModel, false, time.Since(start).Milliseconds(),
		curAPIKey, 0, 0, 0)
	if !rolePrinted {
		send(chunkRole(in))
	}
	msg := "no accounts"
	if lastErr != nil {
		msg = sanitize.Text(lastErr.Error())
	}
	if all, retry := d.Pool.IsAllRateLimited(in.ModelKey); all {
		msg = fmt.Sprintf("%s 所有账号均已达速率限制，请 %d 秒后重试", in.DisplayModel, retry/1000+1)
	}
	send(map[string]any{
		"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"content": fmt.Sprintf("\n[Error: %s]", msg)},
			"finish_reason": "stop",
		}},
	})
	sendRaw("data: [DONE]\n\n")
}

// ─── Response body builders ────────────────────────────────

type usageBody struct {
	PromptTokens            int            `json:"prompt_tokens"`
	CompletionTokens        int            `json:"completion_tokens"`
	TotalTokens             int            `json:"total_tokens"`
	InputTokens             int            `json:"input_tokens"`
	OutputTokens            int            `json:"output_tokens"`
	PromptTokensDetails     map[string]int `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails map[string]int `json:"completion_tokens_details,omitempty"`
	CacheCreationInputTokens int           `json:"cache_creation_input_tokens,omitempty"`
	Cached                  bool           `json:"cached,omitempty"`
}

func estimateTokens(msgs []client.ChatMsg) int {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content)
	}
	n := (chars + 3) / 4
	if n < 1 {
		n = 1
	}
	return n
}

func buildUsageFromCascade(u *windsurf.Usage, msgs []client.ChatMsg, text, thinking string) *usageBody {
	if u != nil && (u.InputTokens != 0 || u.OutputTokens != 0) {
		prompt := int(u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens)
		out := int(u.OutputTokens)
		return &usageBody{
			PromptTokens: prompt, CompletionTokens: out,
			TotalTokens:  prompt + out,
			InputTokens:  prompt, OutputTokens: out,
			PromptTokensDetails:      map[string]int{"cached_tokens": int(u.CacheReadTokens)},
			CacheCreationInputTokens: int(u.CacheWriteTokens),
		}
	}
	pt := estimateTokens(msgs)
	ct := ((len(text) + len(thinking)) + 3) / 4
	if ct < 1 {
		ct = 1
	}
	return &usageBody{
		PromptTokens: pt, CompletionTokens: ct, TotalTokens: pt + ct,
		InputTokens: pt, OutputTokens: ct,
		PromptTokensDetails: map[string]int{"cached_tokens": 0},
	}
}

func cachedUsage(msgs []client.ChatMsg, text string) *usageBody {
	pt := estimateTokens(msgs)
	ct := (len(text) + 3) / 4
	if ct < 1 {
		ct = 1
	}
	return &usageBody{
		PromptTokens: pt, CompletionTokens: ct, TotalTokens: pt + ct,
		InputTokens: pt, OutputTokens: ct,
		PromptTokensDetails: map[string]int{"cached_tokens": pt},
		Cached:              true,
	}
}

func nonStreamBody(in streamInput, text, thinking string, toolCalls []toolemu.ToolCall, usage *usageBody, finish string, cached bool) map[string]any {
	message := map[string]any{"role": "assistant"}
	if len(toolCalls) > 0 {
		message["content"] = nil
		tcs := make([]map[string]any, 0, len(toolCalls))
		for _, tc := range toolCalls {
			tcs = append(tcs, map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": sanitize.Text(tc.ArgumentsJSON),
				},
			})
		}
		message["tool_calls"] = tcs
	} else {
		if text != "" {
			message["content"] = text
		} else {
			message["content"] = nil
		}
	}
	if thinking != "" {
		message["reasoning_content"] = thinking
	}
	if usage == nil {
		if cached {
			usage = cachedUsage(in.CascadeMsgs, text)
		} else {
			usage = buildUsageFromCascade(nil, in.CascadeMsgs, text, thinking)
		}
	}
	return map[string]any{
		"id": in.ChatID, "object": "chat.completion", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	}
}

func chunkRole(in streamInput) map[string]any {
	return map[string]any{
		"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{
			"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil,
		}},
	}
}
func chunkContent(in streamInput, s string) map[string]any {
	return map[string]any{
		"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": s}, "finish_reason": nil}},
	}
}
func chunkThinking(in streamInput, s string) map[string]any {
	return map[string]any{
		"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"reasoning_content": s}, "finish_reason": nil}},
	}
}
func chunkFinish(in streamInput, reason string, usage *usageBody) map[string]any {
	return map[string]any{
		"id": in.ChatID, "object": "chat.completion.chunk", "created": in.Created, "model": in.DisplayModel,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
		"usage":   usage,
	}
}

// ─── Misc helpers ─────────────────────────────────────────

func rawFromTools(t []toolemu.Tool) json.RawMessage {
	b, _ := json.Marshal(t)
	return b
}

// extractImages walks an OpenAI-style content array and pulls every
// `image_url` block through imagex.Resolve. Returns nil when the payload
// is a plain string (common case — hot path stays allocation-free).
// Failures are logged at DEBUG and skipped; we never refuse a request
// because one image URL couldn't be fetched — the model can still reply
// to the text portion.
func extractImages(raw json.RawMessage) []windsurf.ImageData {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var arr []struct {
		Type     string `json:"type"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
		// Anthropic-shaped image block, in case a client routes its
		// /v1/messages payload through /v1/chat/completions by mistake.
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source,omitempty"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	var out []windsurf.ImageData
	for _, p := range arr {
		switch p.Type {
		case "image_url":
			if p.ImageURL == nil || p.ImageURL.URL == "" {
				continue
			}
			im, err := imagex.Resolve(p.ImageURL.URL)
			if err != nil {
				logx.Debug("image_url skipped: %s", err.Error())
				continue
			}
			if im != nil {
				out = append(out, windsurf.ImageData{Base64: im.Base64, Mime: im.Mime})
			}
		case "image":
			if p.Source == nil {
				continue
			}
			if p.Source.Type == "base64" && p.Source.Data != "" {
				out = append(out, windsurf.ImageData{
					Base64: p.Source.Data,
					Mime:   orDefault(p.Source.MediaType, "image/png"),
				})
			}
		}
	}
	return out
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func contentToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var b strings.Builder
		for _, p := range arr {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

func genChatID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "chatcmpl-" + hex.EncodeToString(b[:])[:29]
}

func flowName(cascade bool) string {
	if cascade {
		return "cascade"
	}
	return "legacy"
}

func totalChars(msgs []client.ChatMsg) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}

func numActive(p *auth.Pool) int {
	return p.Counts().Active
}

func waitForAccount(ctx context.Context, p *auth.Pool, tried []string, max time.Duration, modelKey string) *auth.Selected {
	deadline := time.Now().Add(max)
	for {
		if a := p.Acquire(tried, modelKey); a != nil {
			return a
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(queueRetryMS):
		}
	}
}

func convMessagesFromClient(msgs []client.ChatMsg) []convpool.Message {
	out := make([]convpool.Message, len(msgs))
	for i, m := range msgs {
		out[i] = convpool.Message{Role: m.Role, Content: m.Content}
	}
	return out
}
