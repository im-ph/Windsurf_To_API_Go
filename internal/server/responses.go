// OpenAI Responses API compatibility layer — translates the post-2025
// `/v1/responses` request/response shape into our internal OpenAI Chat
// Completions path and back. Clients currently targeting this surface
// include Claude Code's OpenAI backend, Codex, the new Agents SDK, and
// downstream orchestrators migrating from Chat Completions.
//
// The translator runs the request through `ChatCompletions` (same account
// pool + cascade flow as `/v1/chat/completions`) so every reliability
// improvement (pollCascade retries, eligibility fast path, tool
// emulation, proxy test, etc.) applies to this endpoint for free.

package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"windsurfapi/internal/logx"
	"windsurfapi/internal/toolemu"
)

// Responses handles POST /v1/responses.
func (d *Deps) Responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, openAIErrBody("method not allowed", "invalid_request_error"))
		return
	}
	if !d.Pool.IsAuthenticated() {
		writeJSON(w, http.StatusServiceUnavailable, openAIErrBody("No active accounts", "service_unavailable"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, openAIErrBody("body read failed", "invalid_request_error"))
		return
	}
	var body responsesRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, openAIErrBody("Invalid JSON", "invalid_request_error"))
		return
	}

	requested := body.Model
	if requested == "" {
		requested = "gpt-5"
	}
	chatBody, err := responsesToChatRequest(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, openAIErrBody(err.Error(), "invalid_request_error"))
		return
	}
	respID := newResponsesID()

	translated, _ := json.Marshal(chatBody)
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(translated))

	if !chatBody.Stream {
		capture := &responseCapture{header: http.Header{}}
		d.ChatCompletions(capture, r2)
		if capture.status != http.StatusOK {
			// Pass upstream error body through unchanged — OpenAI-compatible
			// clients already understand the `{error: {type, message}}` shape
			// ChatCompletions emits.
			if capture.status == 0 {
				capture.status = http.StatusBadGateway
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(capture.status)
			_, _ = w.Write(capture.buf.Bytes())
			return
		}
		var parsed openAIResponse
		if err := json.Unmarshal(capture.buf.Bytes(), &parsed); err != nil {
			writeJSON(w, http.StatusBadGateway, openAIErrBody("translate: "+err.Error(), "api_error"))
			return
		}
		writeJSON(w, http.StatusOK, chatToResponsesBody(parsed, requested, respID))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, openAIErrBody("streaming not supported", "api_error"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	tr := &responsesTranslator{w: w, flusher: flusher, respID: respID, model: requested}
	shim := &responsesStreamShim{tr: tr}
	d.ChatCompletions(shim, r2)
	shim.drain()
	tr.finish()
}

// ─── Request shapes ───────────────────────────────────────

type responsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	Tools           []responsesTool `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	// `previous_response_id`, `store`, `reasoning`, `response_format` etc.
	// are accepted but not routed — we have no multi-turn memory backend
	// of our own (clients pass history via `input`), and the chat layer
	// doesn't expose reasoning-effort toggles. Leaving them unmapped is
	// fine: Responses API treats unknown-to-backend fields as advisory.
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// Strict mode is ignored — we're proxying to Cascade which doesn't
	// honour schema-strict tool calls yet; forwarding the flag would
	// mislead the caller.
	Strict *bool `json:"strict,omitempty"`
}

// ─── Request translation ──────────────────────────────────

func responsesToChatRequest(in responsesRequest) (openAIRequest, error) {
	out := openAIRequest{
		Model: in.Model, Stream: in.Stream,
		MaxTokens:   in.MaxOutputTokens,
		Temperature: in.Temperature, TopP: in.TopP,
	}
	if in.Instructions != "" {
		raw, _ := json.Marshal(in.Instructions)
		out.Messages = append(out.Messages, toolemu.OAIMessage{Role: "system", Content: raw})
	}
	msgs, err := parseResponsesInput(in.Input)
	if err != nil {
		return out, err
	}
	out.Messages = append(out.Messages, msgs...)
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			// Non-function tools (e.g. "web_search", "file_search",
			// "computer_use") aren't implemented on the Cascade backend.
			// Silently drop them — the model will work around without the
			// tool rather than hard-error.
			continue
		}
		out.Tools = append(out.Tools, toolemu.Tool{
			Type: "function",
			Function: toolemu.ToolFunction{
				Name: t.Name, Description: t.Description, Parameters: t.Parameters,
			},
		})
	}
	if len(in.ToolChoice) > 0 {
		out.ToolChoice = in.ToolChoice
	}
	return out, nil
}

// parseResponsesInput handles the three shapes Responses API accepts:
//   1. A bare string — equivalent to a single user turn with plain text.
//   2. A string array — rare; treated as sequential user turns.
//   3. An array of message-shaped items with `role` and `content` where
//      content is an array of typed parts (`input_text`, `input_image`,
//      `output_text`, `tool_use`, `tool_result`, `refusal`, …).
// Anything unrecognised is skipped with a debug log rather than failing
// the whole request.
func parseResponsesInput(raw json.RawMessage) ([]toolemu.OAIMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// Case 1: bare string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		b, _ := json.Marshal(s)
		return []toolemu.OAIMessage{{Role: "user", Content: b}}, nil
	}
	// Case 2 + 3: array.
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("input must be string or array, got: %s", snippet(string(raw), 60))
	}
	out := make([]toolemu.OAIMessage, 0, len(arr))
	for _, item := range arr {
		var asStr string
		if err := json.Unmarshal(item, &asStr); err == nil {
			b, _ := json.Marshal(asStr)
			out = append(out, toolemu.OAIMessage{Role: "user", Content: b})
			continue
		}
		var obj struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			// function_call / function_call_output items live at the top level,
			// not inside a message — surface them as tool messages.
			Name      string          `json:"name,omitempty"`
			CallID    string          `json:"call_id,omitempty"`
			Arguments string          `json:"arguments,omitempty"`
			Output    json.RawMessage `json:"output,omitempty"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			logx.Debug("responses: dropped unparseable input item: %s", snippet(string(item), 60))
			continue
		}
		switch obj.Type {
		case "function_call":
			// Previous assistant turn emitted a tool call — encode it the
			// chat-completions way (role=assistant with tool_calls array).
			id := obj.CallID
			if id == "" {
				id = "call_" + hex.EncodeToString(randomBytes(4))
			}
			tc := toolemu.OAIToolCall{ID: id, Type: "function"}
			tc.Function.Name = obj.Name
			tc.Function.Arguments = obj.Arguments
			out = append(out, toolemu.OAIMessage{
				Role: "assistant", ToolCalls: []toolemu.OAIToolCall{tc},
			})
			continue
		case "function_call_output":
			id := obj.CallID
			content := ""
			// `output` can be a string or an array of content parts.
			var asStr2 string
			if err := json.Unmarshal(obj.Output, &asStr2); err == nil {
				content = asStr2
			} else {
				content = string(obj.Output)
			}
			rc, _ := json.Marshal(content)
			out = append(out, toolemu.OAIMessage{
				Role: "tool", ToolCallID: id, Content: rc,
			})
			continue
		}
		// Message item with role + content parts.
		role := obj.Role
		if role == "" {
			role = "user"
		}
		text, _ := extractResponsesContentText(obj.Content)
		if text == "" {
			continue
		}
		b, _ := json.Marshal(text)
		out = append(out, toolemu.OAIMessage{Role: role, Content: b})
	}
	return out, nil
}

// extractResponsesContentText concatenates text parts from a Responses API
// content array. Part shapes: `{type:"input_text", text:"..."}` /
// `{type:"output_text", text:"..."}` / `{type:"refusal", refusal:"..."}`.
// Image parts are acknowledged but not yet routed — vision support for
// the Responses path is a follow-up (the imagex plumbing exists on the
// chat path; needs its own content-array injection here).
func extractResponsesContentText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	var b strings.Builder
	sawAny := false
	for _, p := range parts {
		var typ string
		_ = json.Unmarshal(p["type"], &typ)
		switch typ {
		case "input_text", "output_text", "text":
			var t string
			_ = json.Unmarshal(p["text"], &t)
			if t != "" {
				b.WriteString(t)
				sawAny = true
			}
		case "refusal":
			var t string
			_ = json.Unmarshal(p["refusal"], &t)
			if t != "" {
				b.WriteString(t)
				sawAny = true
			}
		}
	}
	return b.String(), sawAny
}

// ─── Non-stream response translation ──────────────────────

func chatToResponsesBody(in openAIResponse, model, respID string) map[string]any {
	now := time.Now().Unix()
	out := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": now,
		"status":     "completed",
		"model":      model,
		"output":     []any{},
		"usage": map[string]any{
			"input_tokens":  in.Usage.PromptTokens,
			"output_tokens": in.Usage.CompletionTokens,
			"total_tokens":  in.Usage.PromptTokens + in.Usage.CompletionTokens,
		},
		"error": nil,
	}
	if len(in.Choices) == 0 {
		return out
	}
	choice := in.Choices[0]
	var outputs []any

	// Message item (may have text content and/or reasoning content).
	var contentParts []any
	if choice.Message.ReasoningContent != "" {
		// Reasoning gets its own output item — the official API surfaces it
		// as `{type:"reasoning", summary:[...]}`. We emit a minimal version
		// with a single summary part so consumers that collect reasoning
		// traces (Codex, some Agents SDK flows) pick it up.
		outputs = append(outputs, map[string]any{
			"id":   "rs_" + respID[5:],
			"type": "reasoning",
			"summary": []any{map[string]any{
				"type": "summary_text",
				"text": choice.Message.ReasoningContent,
			}},
		})
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		contentParts = append(contentParts, map[string]any{
			"type":        "output_text",
			"text":        *choice.Message.Content,
			"annotations": []any{},
		})
	}
	if len(contentParts) > 0 {
		outputs = append(outputs, map[string]any{
			"id":      "msg_" + respID[5:],
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": contentParts,
		})
	}
	// Tool calls become `function_call` top-level output items.
	for i, tc := range choice.Message.ToolCalls {
		outputs = append(outputs, map[string]any{
			"id":        fmt.Sprintf("fc_%s_%d", respID[5:], i),
			"type":      "function_call",
			"status":    "completed",
			"call_id":   tc.ID,
			"name":      tc.Function.Name,
			"arguments": tc.Function.Arguments,
		})
	}
	if outputs == nil {
		outputs = []any{}
	}
	out["output"] = outputs
	out["status"] = mapFinishReason(choice.FinishReason)
	return out
}

func mapFinishReason(r string) string {
	switch r {
	case "stop", "":
		return "completed"
	case "length":
		return "incomplete"
	case "tool_calls", "function_call":
		return "completed"
	case "content_filter":
		return "incomplete"
	}
	return "completed"
}

// ─── Stream translator ────────────────────────────────────

// responsesTranslator re-emits OpenAI Chat Completions SSE chunks as the
// Responses API event stream. State machine roughly:
//
//	response.created
//	response.in_progress
//	for each text chunk:
//	  (first time) response.output_item.added (message) + response.content_part.added (output_text)
//	  response.output_text.delta
//	(end of text) response.output_text.done + response.content_part.done + response.output_item.done
//	for each tool_call:
//	  response.output_item.added (function_call)
//	  response.function_call_arguments.delta ...
//	  response.function_call_arguments.done
//	  response.output_item.done
//	response.completed
type responsesTranslator struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
	model   string
	respID  string

	started bool
	stopped bool

	seq            int // monotonic sequence_number — clients use it for event ordering
	pendingSSE     strings.Builder
	outputIdx      int
	msgItemOpen    bool
	msgContentOpen bool
	msgItemID      string
	accText        strings.Builder

	toolItems map[int]*responsesToolItem // keyed by upstream tool_calls index
	finalU    openAIUsage
	stopReason string
}

type responsesToolItem struct {
	ItemID    string
	CallID    string
	Name      string
	Args      strings.Builder
	OutputIdx int
	Opened    bool
	Closed    bool
}

func (t *responsesTranslator) send(event string, data map[string]any) {
	data["sequence_number"] = t.seq
	t.seq++
	b, _ := json.Marshal(data)
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", event, b)
	t.flusher.Flush()
}

func (t *responsesTranslator) startResponse() {
	if t.started {
		return
	}
	t.started = true
	t.toolItems = map[int]*responsesToolItem{}
	t.stopReason = "completed"
	t.msgItemID = "msg_" + t.respID[5:]

	baseResp := map[string]any{
		"id":         t.respID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     "in_progress",
		"model":      t.model,
		"output":     []any{},
		"usage":      nil,
		"error":      nil,
	}
	t.send("response.created", map[string]any{
		"type":     "response.created",
		"response": baseResp,
	})
	t.send("response.in_progress", map[string]any{
		"type":     "response.in_progress",
		"response": baseResp,
	})
}

func (t *responsesTranslator) ensureMessageOpen() {
	if t.msgItemOpen {
		return
	}
	t.msgItemOpen = true
	t.send("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": t.outputIdx,
		"item": map[string]any{
			"id":      t.msgItemID,
			"type":    "message",
			"role":    "assistant",
			"status":  "in_progress",
			"content": []any{},
		},
	})
	t.send("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       t.msgItemID,
		"output_index":  t.outputIdx,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	})
	t.msgContentOpen = true
}

func (t *responsesTranslator) emitTextDelta(text string) {
	if text == "" {
		return
	}
	t.ensureMessageOpen()
	t.accText.WriteString(text)
	t.send("response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       t.msgItemID,
		"output_index":  t.outputIdx,
		"content_index": 0,
		"delta":         text,
	})
}

func (t *responsesTranslator) closeMessage() {
	if !t.msgItemOpen {
		return
	}
	if t.msgContentOpen {
		t.send("response.output_text.done", map[string]any{
			"type":          "response.output_text.done",
			"item_id":       t.msgItemID,
			"output_index":  t.outputIdx,
			"content_index": 0,
			"text":          t.accText.String(),
		})
		t.send("response.content_part.done", map[string]any{
			"type":          "response.content_part.done",
			"item_id":       t.msgItemID,
			"output_index":  t.outputIdx,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        t.accText.String(),
				"annotations": []any{},
			},
		})
		t.msgContentOpen = false
	}
	t.send("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": t.outputIdx,
		"item": map[string]any{
			"id":     t.msgItemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        t.accText.String(),
				"annotations": []any{},
			}},
		},
	})
	t.outputIdx++
	t.msgItemOpen = false
}

func (t *responsesTranslator) emitToolCallDelta(idx int, id, name, argsChunk string) {
	item, ok := t.toolItems[idx]
	if !ok {
		item = &responsesToolItem{ItemID: fmt.Sprintf("fc_%s_%d", t.respID[5:], idx)}
		t.toolItems[idx] = item
	}
	if id != "" && item.CallID == "" {
		item.CallID = id
	}
	if name != "" && item.Name == "" {
		item.Name = name
	}
	if !item.Opened && item.CallID != "" && item.Name != "" {
		// Before opening a function_call output item, close any pending
		// message item so output_index stays contiguous — Responses clients
		// (Codex, Agents SDK) assert that once an item is "done", no more
		// events arrive for it.
		t.closeMessage()
		item.Opened = true
		item.OutputIdx = t.outputIdx
		t.send("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": item.OutputIdx,
			"item": map[string]any{
				"id":        item.ItemID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   item.CallID,
				"name":      item.Name,
				"arguments": "",
			},
		})
	}
	if argsChunk != "" && item.Opened {
		item.Args.WriteString(argsChunk)
		t.send("response.function_call_arguments.delta", map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      item.ItemID,
			"output_index": item.OutputIdx,
			"delta":        argsChunk,
		})
	}
}

func (t *responsesTranslator) closeToolItems() {
	// Sort by outputIdx so done events fire in deterministic order.
	for _, item := range t.toolItems {
		if !item.Opened || item.Closed {
			continue
		}
		item.Closed = true
		t.send("response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      item.ItemID,
			"output_index": item.OutputIdx,
			"arguments":    item.Args.String(),
		})
		t.send("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": item.OutputIdx,
			"item": map[string]any{
				"id":        item.ItemID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   item.CallID,
				"name":      item.Name,
				"arguments": item.Args.String(),
			},
		})
		if item.OutputIdx >= t.outputIdx {
			t.outputIdx = item.OutputIdx + 1
		}
	}
}

func (t *responsesTranslator) processChunk(chunk map[string]json.RawMessage) {
	t.startResponse()
	var choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string `json:"role"`
			Content          *string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	_ = json.Unmarshal(chunk["choices"], &choices)
	for _, c := range choices {
		if c.Delta.Content != nil && *c.Delta.Content != "" {
			t.emitTextDelta(*c.Delta.Content)
		}
		for _, tc := range c.Delta.ToolCalls {
			t.emitToolCallDelta(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		if c.FinishReason != nil {
			t.stopReason = mapFinishReason(*c.FinishReason)
		}
	}
	if rawUsage, ok := chunk["usage"]; ok && len(rawUsage) > 0 {
		var u openAIUsage
		if err := json.Unmarshal(rawUsage, &u); err == nil {
			t.finalU = u
		}
	}
}

func (t *responsesTranslator) feed(raw []byte) {
	t.pendingSSE.Write(raw)
	s := t.pendingSSE.String()
	for {
		idx := strings.Index(s, "\n\n")
		if idx < 0 {
			break
		}
		frame := s[:idx]
		s = s[idx+2:]
		sawPing := false
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, ": ") {
				sawPing = true
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := line[6:]
			if payload == "[DONE]" {
				continue
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(payload), &m); err == nil {
				t.processChunk(m)
			}
		}
		// Forward upstream heartbeat so Responses API clients (Agents SDK)
		// don't time out on long reasoning phases. Responses API uses
		// `event: response.ping` as the canonical keepalive.
		if sawPing && t.started {
			t.send("response.ping", map[string]any{"type": "response.ping"})
		}
	}
	t.pendingSSE.Reset()
	t.pendingSSE.WriteString(s)
}

func (t *responsesTranslator) finish() {
	if t.stopped {
		return
	}
	t.stopped = true
	// Same rationale as the Anthropic translator: guarantee the initial
	// events exist even if ChatCompletions produced zero chunks. Without
	// this a transient-error zero-chunk stream emits response.completed
	// with no prior events, which the Responses SDK rejects → blank output
	// + lost turn on the client side.
	t.startResponse()
	t.closeMessage()
	t.closeToolItems()
	final := map[string]any{
		"id":         t.respID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     t.stopReason,
		"model":      t.model,
		"output":     []any{},
		"usage": map[string]any{
			"input_tokens":  t.finalU.PromptTokens,
			"output_tokens": t.finalU.CompletionTokens,
			"total_tokens":  t.finalU.PromptTokens + t.finalU.CompletionTokens,
		},
		"error": nil,
	}
	t.send("response.completed", map[string]any{
		"type":     "response.completed",
		"response": final,
	})
}

// ─── Stream shim ──────────────────────────────────────────

type responsesStreamShim struct {
	tr     *responsesTranslator
	header http.Header
	status int
	errBuf bytes.Buffer
}

func (s *responsesStreamShim) Header() http.Header {
	if s.header == nil {
		s.header = http.Header{}
	}
	return s.header
}
func (s *responsesStreamShim) WriteHeader(status int) { s.status = status }
func (s *responsesStreamShim) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if s.status != http.StatusOK {
		s.errBuf.Write(b)
		return len(b), nil
	}
	s.tr.feed(b)
	return len(b), nil
}
func (s *responsesStreamShim) Flush() {}

func (s *responsesStreamShim) drain() {
	if s.status == 0 || s.status == http.StatusOK {
		return
	}
	msg := extractErrMsg(s.errBuf.Bytes())
	if msg == "" {
		msg = fmt.Sprintf("upstream error (HTTP %d)", s.status)
	}
	s.tr.emitTextDelta("[Error: " + msg + "]")
}

// ─── Helpers ──────────────────────────────────────────────

func newResponsesID() string {
	return "resp_" + hex.EncodeToString(randomBytes(12))
}

func openAIErrBody(msg, typ string) map[string]any {
	return map[string]any{"error": map[string]any{"type": typ, "message": msg}}
}

func snippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
