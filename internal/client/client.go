// Package client is the WindsurfClient — it drives the two chat flows
// (Cascade and legacy RawGetChatMessage) against a local language_server
// process. Direct behavioural port of src/client.js.
package client

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"windsurfapi/internal/grpcx"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
	"windsurfapi/internal/windsurf"
)

// Keep sync in use via Entry.Warmup only — no direct refs here.

const lsService = "/exa.language_server_pb.LanguageServerService"

// ChatMsg is the OpenAI-style role/content pair the caller passes in. content
// may be string or []{type,text}; normalise at entry.
type ChatMsg struct {
	Role          string
	Content       string
	ToolCallsText string // rendered prior tool_calls (see handlers/chat)
	ToolCallID    string // for role=tool
	// Images carries base64-encoded inline images for this turn. Surfaced to
	// Cascade via SendUserCascadeMessageRequest.images (proto field 6); legacy
	// RawGetChatMessage has no equivalent field and silently drops them.
	Images []windsurf.ImageData
}

// Chunk is one incremental step produced during a cascade poll.
type Chunk struct {
	Text     string
	Thinking string
	IsError  bool
}

// CascadeOptions carries per-call tunables.
type CascadeOptions struct {
	ToolPreamble           string
	IdentityPrompt         string // stamped into the LS system prompt via proto fields 8 + 13
	ResponseLanguagePrompt string // appended to communication_section, no identity semantics
	ReuseEntry             *ReuseRef

	// OnChunk is invoked for every delta. Safe to nil for non-streaming calls.
	OnChunk func(Chunk)
}

// ReuseRef describes a resumable cascade handed back to the conversation pool.
type ReuseRef struct {
	CascadeID string
	SessionID string
	// populated by the caller on success for the checkin write-back
	APIKey string
	LSPort int
	CreatedAt time.Time
}

// CascadeResult is returned on success.
type CascadeResult struct {
	Text       string
	Thinking   string
	CascadeID  string
	SessionID  string
	ToolCalls  []windsurf.ToolCall
	Usage      *windsurf.Usage
	EndReason  string
}

// Client holds the LS entry + api credentials for one upcoming call.
type Client struct {
	APIKey string
	Entry  *langserver.Entry
	conn   *grpcx.Client
}

// New builds a client. The gRPC transport is reused across calls by keeping
// one per (port).
func New(apiKey string, entry *langserver.Entry) *Client {
	return &Client{
		APIKey: apiKey,
		Entry:  entry,
		conn:   grpcx.New(entry.Port, entry.CSRF),
	}
}

// Close tears down idle HTTP/2 connections.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// WarmupCascade runs the three-RPC workspace init once per LS process. Safe
// to call concurrently.
func (c *Client) WarmupCascade(ctx context.Context) error {
	return c.Entry.Warmup(func() error {
		apiKey := c.APIKey
		if c.Entry.SessionID == "" {
			c.Entry.SessionID = windsurf.NewSessionID()
		}
		session := c.Entry.SessionID

		proto := windsurf.BuildInitializePanelStateRequest(apiKey, session, true)
		if _, err := c.conn.Unary(ctx, lsService+"/InitializeCascadePanelState", grpcx.Frame(proto), 5*time.Second); err != nil {
			logx.Warn("InitializeCascadePanelState: %s", err.Error())
		}
		proto = windsurf.BuildAddTrackedWorkspaceRequest("/tmp/windsurf-workspace")
		if _, err := c.conn.Unary(ctx, lsService+"/AddTrackedWorkspace", grpcx.Frame(proto), 5*time.Second); err != nil {
			// LS rejects duplicate registrations with "path is already tracked".
			// We deliberately re-run WarmupCascade after panel-state eviction,
			// so collisions here are expected and the tracked set stays valid
			// — don't surface them as warnings.
			if strings.Contains(err.Error(), "already tracked") {
				logx.Debug("AddTrackedWorkspace (dup, ignored): %s", err.Error())
			} else {
				logx.Warn("AddTrackedWorkspace: %s", err.Error())
			}
		}
		proto = windsurf.BuildUpdateWorkspaceTrustRequest(apiKey, session, true)
		if _, err := c.conn.Unary(ctx, lsService+"/UpdateWorkspaceTrust", grpcx.Frame(proto), 5*time.Second); err != nil {
			logx.Warn("UpdateWorkspaceTrust: %s", err.Error())
		}
		logx.Info("Cascade workspace init complete for LS port=%d", c.Entry.Port)
		return nil
	})
}

// ─── Legacy RawGetChatMessage ─────────────────────────────

var errRe = regexp.MustCompile(`^(permission_denied|failed_precondition|not_found|unauthenticated):`)

// RawChat streams chunks from RawGetChatMessage. Returns accumulated Chunks.
// `isModelError` on the returned error is signalled via ErrorModel / ErrorTransient.
func (c *Client) RawChat(ctx context.Context, msgs []ChatMsg, modelEnum uint64, modelName string, onChunk func(Chunk)) ([]Chunk, error) {
	proto := windsurf.BuildRawGetChatMessageRequest(c.APIKey, toWindsurf(msgs), modelEnum, modelName)
	body := grpcx.Frame(proto)
	var chunks []Chunk
	var streamErr error

	err := c.conn.Stream(ctx, lsService+"/RawGetChatMessage", body, 5*time.Minute, func(payload []byte) {
		parsed, perr := windsurf.ParseRawResponse(payload)
		if perr != nil {
			logx.Warn("RawGetChatMessage parse: %s", perr.Error())
			return
		}
		if parsed.Text == "" {
			return
		}
		if parsed.IsError || errRe.MatchString(strings.TrimSpace(parsed.Text)) {
			e := &ModelError{Msg: strings.TrimSpace(parsed.Text)}
			streamErr = e
			return
		}
		ch := Chunk{Text: parsed.Text}
		chunks = append(chunks, ch)
		if onChunk != nil {
			onChunk(ch)
		}
	})
	if streamErr != nil {
		return chunks, streamErr
	}
	return chunks, err
}

// ─── Cascade ──────────────────────────────────────────────

// CascadeChat drives StartCascade + SendUserCascadeMessage + the trajectory
// polling loop. Returns on IDLE, stall, or hard error. Verbatim behavioural
// port of the JS cascadeChat().
func (c *Client) CascadeChat(ctx context.Context, msgs []ChatMsg, modelEnum uint64, modelUID string, opts CascadeOptions) (*CascadeResult, error) {
	_ = c.WarmupCascade(ctx) // best-effort; errors logged inside
	sessionID := c.Entry.SessionID
	if opts.ReuseEntry != nil && opts.ReuseEntry.SessionID != "" {
		sessionID = opts.ReuseEntry.SessionID
	}
	if sessionID == "" {
		sessionID = windsurf.NewSessionID()
		c.Entry.SessionID = sessionID
	}

	isPanelMissing := func(err error) bool {
		if err == nil {
			return false
		}
		m := strings.ToLower(err.Error())
		return strings.Contains(m, "panel state not found") || strings.Contains(m, "not_found") && strings.Contains(m, "panel")
	}

	var cascadeID string
	openCascade := func() error {
		if opts.ReuseEntry != nil && opts.ReuseEntry.CascadeID != "" {
			cascadeID = opts.ReuseEntry.CascadeID
			return nil
		}
		proto := windsurf.BuildStartCascadeRequest(c.APIKey, sessionID)
		resp, err := c.conn.Unary(ctx, lsService+"/StartCascade", grpcx.Frame(proto), 0)
		if err != nil {
			return err
		}
		id, err := windsurf.ParseStartCascadeResponse(resp)
		if err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("StartCascade returned empty cascade_id")
		}
		cascadeID = id
		return nil
	}
	if err := openCascade(); err != nil {
		if !isPanelMissing(err) {
			return nil, err
		}
		// Same recovery path as the Send variant below: panel state GC'd
		// before StartCascade. Downgraded to DEBUG.
		logx.Debug("Panel state missing, re-warming LS port=%d", c.Entry.Port)
		c.Entry.ResetWarmup()
		_ = c.WarmupCascade(ctx)
		sessionID = c.Entry.SessionID
		if opts.ReuseEntry != nil {
			opts.ReuseEntry.CascadeID = ""
		}
		if err := openCascade(); err != nil {
			return nil, err
		}
	}

	// Build the single text payload Cascade accepts. For fresh cascades we
	// render system + u/a turns into a labelled transcript; resume mode only
	// forwards the latest user message.
	text := assemblePayload(msgs, opts.ReuseEntry != nil && opts.ReuseEntry.CascadeID != "")

	// Images from the latest user turn (earlier turns are already encoded
	// into assemblePayload text; the Cascade image field only applies to the
	// outbound turn).
	var latestImages []windsurf.ImageData
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].Images) > 0 {
			latestImages = msgs[i].Images
			break
		}
	}

	sendMessage := func() error {
		proto := windsurf.BuildSendCascadeMessageRequest(c.APIKey, cascadeID, text, modelEnum, modelUID, sessionID, windsurf.SendOpts{
			ToolPreamble:           opts.ToolPreamble,
			IdentityPrompt:         opts.IdentityPrompt,
			ResponseLanguagePrompt: opts.ResponseLanguagePrompt,
		}, latestImages)
		_, err := c.conn.Unary(ctx, lsService+"/SendUserCascadeMessage", grpcx.Frame(proto), 0)
		return err
	}
	if err := sendMessage(); err != nil {
		if !isPanelMissing(err) {
			return nil, err
		}
		// Recovery path: Cascade panel state was GC'd between StartCascade and
		// SendUserCascadeMessage. We redo the three-RPC init + reopen the
		// cascade; if the retry succeeds this path is entirely transparent to
		// the caller. Logged at DEBUG so operators aren't misled into thinking
		// the pool is failing when it's handling the eviction correctly.
		logx.Debug("Panel state missing on Send, re-warming + restarting cascade port=%d", c.Entry.Port)
		c.Entry.ResetWarmup()
		_ = c.WarmupCascade(ctx)
		sessionID = c.Entry.SessionID
		if err := openCascade(); err != nil {
			return nil, err
		}
		if err := sendMessage(); err != nil {
			return nil, err
		}
	}

	return c.pollCascade(ctx, cascadeID, sessionID, inputChars(msgs), opts.OnChunk)
}

// ─── Poll loop (stall logic copied from client.js) ────────

const (
	maxWait         = 180 * time.Second
	pollInterval    = 250 * time.Millisecond
	idleGrace       = 8 * time.Second
	noGrowthStall   = 25 * time.Second
	stallRetryMin   = 300
	// Transient-error budget per poll loop. A single dropped TCP connection
	// (LS mid-restart, tmpfs hiccup, h2 frame desync) used to kill the whole
	// conversation here — now we swallow up to pollMaxTransient consecutive
	// errors, sleep pollRetryDelay between them, and keep polling the same
	// cascade_id. The LS server-side session is still alive; we just missed
	// one trajectory snapshot.
	pollMaxTransient = 6
	pollRetryDelay   = 500 * time.Millisecond
)

// isTransientPollErr reports whether err is the kind of gRPC/transport
// hiccup where the cascade on the LS side is probably still running and
// worth retrying. Hard modelling errors / context cancellations escape
// out of the retry wrapper.
func isTransientPollErr(err error) bool {
	if err == nil {
		return false
	}
	// Modelling errors surfaced by windsurf.ParseTrajectorySteps aren't
	// transient — they come from the LS response body, not the transport.
	var mErr *ModelError
	if errors.As(err, &mErr) {
		return false
	}
	s := err.Error()
	// Substrings we know the underlying h2c transport / kernel surface for
	// LS restart + socket reset patterns.
	switch {
	case strings.Contains(s, "connection refused"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "broken pipe"),
		strings.Contains(s, "EOF"),
		strings.Contains(s, "unexpected EOF"),
		strings.Contains(s, "i/o timeout"),
		strings.Contains(s, "use of closed network connection"),
		strings.Contains(s, "http2: client connection lost"),
		strings.Contains(s, "stream error"):
		return true
	}
	return false
}

func (c *Client) pollCascade(ctx context.Context, cascadeID, sessionID string, inputCh int, onChunk func(Chunk)) (*CascadeResult, error) {
	yielded := map[int]int{}
	thinkingBy := map[int]int{}
	usageBy := map[int]windsurf.Usage{}
	seenToolIDs := map[string]struct{}{}
	var allText, allThinking strings.Builder
	var toolCalls []windsurf.ToolCall

	var totalYielded int64
	var totalThinking int64
	idleCount := 0
	sawActive := false
	sawText := false
	lastStatus := uint64(0)
	lastGrowthAt := time.Now()
	lastStepCount := 0
	start := time.Now()
	endReason := "unknown"
	// Consecutive-transient-error counters per RPC. Keep them separate so
	// that "steps call reliably succeeds, status call reliably fails" (or
	// vice-versa) doesn't falsely blow through the budget — each RPC
	// independently tracks its own recent failure streak and resets on its
	// own success. Also kept as two independent numbers so you can tell
	// which leg of the poll is flapping by reading the logs.
	stepsErrs := 0
	statusErrs := 0

	emit := func(ch Chunk) {
		if ch.Text != "" {
			allText.WriteString(ch.Text)
		}
		if ch.Thinking != "" {
			allThinking.WriteString(ch.Thinking)
		}
		if onChunk != nil {
			onChunk(ch)
		}
	}

	for time.Since(start) < maxWait {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		// Transient error budget: LS restarts, h2 stream drops, and kernel
		// EPIPE during LS process flap used to propagate straight up and
		// truncate the user's conversation mid-answer. We swallow up to
		// pollMaxTransient of those in a row, sleeping between retries,
		// then keep walking the same cascade_id — the LS-side session
		// survives a socket bounce and continues producing trajectory
		// steps as long as we ask for them.
		stepsResp, err := c.conn.Unary(ctx, lsService+"/GetCascadeTrajectorySteps", grpcx.Frame(windsurf.BuildGetTrajectoryStepsRequest(cascadeID, 0)), 0)
		if err != nil {
			if !isTransientPollErr(err) {
				return nil, err
			}
			stepsErrs++
			if stepsErrs > pollMaxTransient {
				logx.Warn("Cascade poll steps: gave up after %d transient errors: %s", stepsErrs, err.Error())
				return nil, err
			}
			logx.Debug("Cascade poll steps: transient error #%d (%s) — retrying", stepsErrs, err.Error())
			// A wave of transient errors means we were probably silent on
			// the wire for a few seconds. Treat that silence as "activity"
			// so the stall-detection timer doesn't misread it as the model
			// having stopped — the cascade on the LS side is still alive.
			lastGrowthAt = time.Now()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pollRetryDelay):
			}
			continue
		}
		stepsErrs = 0
		steps, err := windsurf.ParseTrajectorySteps(stepsResp)
		if err != nil {
			return nil, err
		}

		// CORTEX_STEP_TYPE_ERROR_MESSAGE (17).
		for _, s := range steps {
			if s.Type == 17 && s.ErrorText != "" {
				return nil, &ModelError{Msg: strings.TrimSpace(s.ErrorText)}
			}
		}

		// Stall detection.
		elapsed := time.Since(start)
		coldStall := 30*time.Second + time.Duration(inputCh/1500)*5*time.Second
		if coldStall > maxWait {
			coldStall = maxWait
		}
		if elapsed > coldStall && sawActive && !sawText && len(seenToolIDs) == 0 {
			endReason = "stall_cold"
			return nil, &ModelError{Msg: fmt.Sprintf("Cascade planner stalled — no output after %.0fs", coldStall.Seconds())}
		}
		if sawText && lastStatus != 1 && time.Since(lastGrowthAt) > noGrowthStall {
			if atomic.LoadInt64(&totalYielded) < stallRetryMin {
				endReason = "stall_warm_retry"
				return nil, &ModelError{Msg: "Cascade planner stalled after preamble — no progress for 25s"}
			}
			logx.Warn("Cascade warm stall (accepting partial)")
			endReason = "stall_warm"
			break
		}

		if len(steps) > lastStepCount {
			lastStepCount = len(steps)
			lastGrowthAt = time.Now()
		}

		for i, step := range steps {
			if step.Usage != nil {
				usageBy[i] = *step.Usage
			}
			for _, tc := range step.ToolCalls {
				key := tc.ID
				if key == "" {
					key = tc.Name + ":" + tc.ArgumentsJSON
				}
				if _, seen := seenToolIDs[key]; seen {
					continue
				}
				seenToolIDs[key] = struct{}{}
				toolCalls = append(toolCalls, tc)
				lastGrowthAt = time.Now()
			}

			if step.Thinking != "" {
				prev := thinkingBy[i]
				if len(step.Thinking) > prev {
					delta := step.Thinking[prev:]
					thinkingBy[i] = len(step.Thinking)
					atomic.AddInt64(&totalThinking, int64(len(delta)))
					lastGrowthAt = time.Now()
					emit(Chunk{Thinking: delta})
				}
			}

			live := step.ResponseText
			if live == "" {
				live = step.Text
			}
			if live == "" {
				continue
			}
			prev := yielded[i]
			if len(live) > prev {
				delta := live[prev:]
				yielded[i] = len(live)
				atomic.AddInt64(&totalYielded, int64(len(delta)))
				lastGrowthAt = time.Now()
				sawText = true
				emit(Chunk{Text: delta})
			}
		}

		statusResp, err := c.conn.Unary(ctx, lsService+"/GetCascadeTrajectory", grpcx.Frame(windsurf.BuildGetTrajectoryRequest(cascadeID)), 0)
		if err != nil {
			// Same transient budget as the steps poll above — the status
			// RPC races the LS restart window slightly more often because
			// it fires on every loop iteration unconditionally. Separate
			// counter from stepsErrs so a one-sided flap doesn't burn both
			// budgets at once.
			if !isTransientPollErr(err) {
				return nil, err
			}
			statusErrs++
			if statusErrs > pollMaxTransient {
				logx.Warn("Cascade poll status: gave up after %d transient errors: %s", statusErrs, err.Error())
				return nil, err
			}
			logx.Debug("Cascade poll status: transient error #%d (%s) — retrying", statusErrs, err.Error())
			lastGrowthAt = time.Now()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pollRetryDelay):
			}
			continue
		}
		statusErrs = 0
		status, _ := windsurf.ParseTrajectoryStatus(statusResp)
		lastStatus = status
		if status != 1 {
			sawActive = true
		}

		if status == 1 { // IDLE
			if !sawActive && time.Since(start) <= idleGrace {
				continue
			}
			idleCount++
			canBreak := (sawText && idleCount >= 2) || (!sawText && idleCount >= 4)
			if !canBreak {
				continue
			}
			// Final sweep with modified_response top-up.
			finalResp, err := c.conn.Unary(ctx, lsService+"/GetCascadeTrajectorySteps", grpcx.Frame(windsurf.BuildGetTrajectoryStepsRequest(cascadeID, 0)), 0)
			if err == nil {
				finalSteps, _ := windsurf.ParseTrajectorySteps(finalResp)
				for i, step := range finalSteps {
					prev := yielded[i]
					if len(step.ResponseText) > prev {
						delta := step.ResponseText[prev:]
						yielded[i] = len(step.ResponseText)
						atomic.AddInt64(&totalYielded, int64(len(delta)))
						emit(Chunk{Text: delta})
					}
					cursor := yielded[i]
					if len(step.ModifiedText) > cursor && strings.HasPrefix(step.ModifiedText, step.ResponseText) {
						delta := step.ModifiedText[cursor:]
						yielded[i] = len(step.ModifiedText)
						atomic.AddInt64(&totalYielded, int64(len(delta)))
						emit(Chunk{Text: delta})
					}
				}
			}
			if sawText {
				endReason = "idle_done"
			} else {
				endReason = "idle_empty"
			}
			break
		}
		idleCount = 0
	}
	if endReason == "unknown" {
		endReason = "max_wait"
	}

	logx.Info("Cascade done cascadeId=%s reason=%s textLen=%d thinkingLen=%d toolCalls=%d ms=%d",
		truncID(cascadeID), endReason, allText.Len(), allThinking.Len(), len(toolCalls), time.Since(start).Milliseconds())

	// Server-reported token usage.
	var serverUsage *windsurf.Usage
	if metaResp, err := c.conn.Unary(ctx, lsService+"/GetCascadeTrajectoryGeneratorMetadata", grpcx.Frame(windsurf.BuildGetGeneratorMetadataRequest(cascadeID, 0)), 5*time.Second); err == nil {
		serverUsage = windsurf.ParseGeneratorMetadata(metaResp)
	}
	if serverUsage == nil && len(usageBy) > 0 {
		u := windsurf.Usage{}
		for _, v := range usageBy {
			u.Add(v)
		}
		if u.InputTokens|u.OutputTokens|u.CacheReadTokens|u.CacheWriteTokens != 0 {
			serverUsage = &u
		}
	}

	return &CascadeResult{
		Text: allText.String(), Thinking: allThinking.String(),
		CascadeID: cascadeID, SessionID: sessionID,
		ToolCalls: toolCalls, Usage: serverUsage, EndReason: endReason,
	}, nil
}

// ─── Helpers ──────────────────────────────────────────────

func toWindsurf(msgs []ChatMsg) []windsurf.ChatMsg {
	out := make([]windsurf.ChatMsg, len(msgs))
	for i, m := range msgs {
		out[i] = windsurf.ChatMsg{
			Role: m.Role, Content: m.Content,
			ToolCallsText: m.ToolCallsText, ToolCallID: m.ToolCallID,
		}
	}
	return out
}

func inputChars(msgs []ChatMsg) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}

func truncID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// assemblePayload produces the single-text payload Cascade accepts. Fresh
// cascades see a full "[Conversation so far]" + system header; resumes send
// only the latest user message.
func assemblePayload(msgs []ChatMsg, resume bool) string {
	if resume {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				return msgs[i].Content
			}
		}
		return ""
	}

	var sys []string
	var convo []ChatMsg
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m.Content)
			continue
		}
		if m.Role == "user" || m.Role == "assistant" {
			convo = append(convo, m)
		}
	}
	sysText := strings.TrimSpace(strings.Join(sys, "\n"))

	var body string
	if len(convo) <= 1 {
		if len(convo) == 1 {
			body = convo[0].Content
		}
	} else {
		var lines []string
		for i := 0; i < len(convo)-1; i++ {
			label := "User"
			if convo[i].Role == "assistant" {
				label = "Assistant"
			}
			lines = append(lines, fmt.Sprintf("%s: %s", label, convo[i].Content))
		}
		latest := convo[len(convo)-1].Content
		body = fmt.Sprintf("[Conversation so far]\n%s\n\n[Current user message]\n%s", strings.Join(lines, "\n\n"), latest)
	}
	if sysText != "" {
		body = sysText + "\n\n" + body
	}
	return body
}

// ─── Error types ──────────────────────────────────────────

// ModelError is raised for model-level failures (permission_denied, error
// step, stalls). It never counts against the calling account's error counter.
type ModelError struct {
	Msg string
}

func (e *ModelError) Error() string { return e.Msg }
func IsModelError(err error) bool {
	_, ok := err.(*ModelError)
	return ok
}
