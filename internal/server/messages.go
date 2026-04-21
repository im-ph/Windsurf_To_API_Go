// Anthropic Messages API compatibility — direct port of src/handlers/messages.js.
// Non-streaming: invoke the chat pipeline, translate the shape.
// Streaming: shim an *http.ResponseWriter that parses OpenAI SSE on the fly
// and re-emits Anthropic message_start / content_block_* / message_delta /
// message_stop events.
package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"windsurfapi/internal/logx"
	"windsurfapi/internal/toolemu"
)

// Messages handles POST /v1/messages.
func (d *Deps) Messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, anthropicErr("method not allowed", "invalid_request_error"))
		return
	}
	if !d.Pool.IsAuthenticated() {
		writeJSON(w, http.StatusServiceUnavailable, anthropicErr("No active accounts", "api_error"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // 8 MB cap — matches chat.go
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, anthropicErr("body read failed", "invalid_request_error"))
		return
	}
	var body anthropicRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, anthropicErr("Invalid JSON", "invalid_request_error"))
		return
	}

	requested := body.Model
	if requested == "" {
		requested = "claude-sonnet-4.6"
	}
	openaiBody, err := anthropicToOpenAIRequest(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, anthropicErr(err.Error(), "invalid_request_error"))
		return
	}
	msgID := newMessageID()

	// Reuse the OpenAI handler by re-encoding the translated body and dispatching.
	translated, _ := json.Marshal(openaiBody)
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(translated))

	if !openaiBody.Stream {
		// Non-stream path — capture the OpenAI response then translate.
		capture := &responseCapture{header: http.Header{}}
		d.ChatCompletions(capture, r2)
		if capture.status != http.StatusOK {
			writeJSON(w, capture.status, anthropicErr(extractErrMsg(capture.buf.Bytes()), extractErrType(capture.buf.Bytes())))
			return
		}
		var parsed openAIResponse
		if err := json.Unmarshal(capture.buf.Bytes(), &parsed); err != nil {
			writeJSON(w, http.StatusBadGateway, anthropicErr("translate: "+err.Error(), "api_error"))
			return
		}
		writeJSON(w, http.StatusOK, openAIToAnthropicResponse(parsed, requested, msgID))
		return
	}

	// Streaming path — translate SSE frames on the fly through a shim writer.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, anthropicErr("streaming not supported", "api_error"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	tr := &anthropicTranslator{
		w: w, flusher: flusher, msgID: msgID, model: requested,
	}
	shim := &streamShim{tr: tr}
	d.ChatCompletions(shim, r2)
	tr.finish()
}

// ─── Shapes ────────────────────────────────────────────────

type anthropicRequest struct {
	Model         string              `json:"model"`
	Stream        bool                `json:"stream"`
	MaxTokens     int                 `json:"max_tokens"`
	System        json.RawMessage     `json:"system,omitempty"`
	Messages      []anthropicMsg      `json:"messages"`
	Tools         []anthropicTool     `json:"tools,omitempty"`
	StopSequences []string            `json:"stop_sequences,omitempty"`
	Temperature   *float64            `json:"temperature,omitempty"`
	TopP          *float64            `json:"top_p,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type openAIRequest struct {
	Model       string              `json:"model"`
	Stream      bool                `json:"stream"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Messages    []toolemu.OAIMessage `json:"messages"`
	Tools       []toolemu.Tool      `json:"tools,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
	TopP        *float64            `json:"top_p,omitempty"`
	Stop        []string            `json:"stop,omitempty"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role            string            `json:"role"`
			Content         *string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content,omitempty"`
			ToolCalls       []openAIToolCall  `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIUsage struct {
	PromptTokens             int            `json:"prompt_tokens"`
	CompletionTokens         int            `json:"completion_tokens"`
	PromptTokensDetails      map[string]int `json:"prompt_tokens_details"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
}

// ─── Anthropic → OpenAI request ───────────────────────────

func anthropicToOpenAIRequest(in anthropicRequest) (openAIRequest, error) {
	out := openAIRequest{
		Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens,
		Temperature: in.Temperature, TopP: in.TopP, Stop: in.StopSequences,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8192
	}

	// System: string | [{text}]
	if len(in.System) > 0 {
		if s := decodeAnthropicText(in.System); s != "" {
			rc, _ := json.Marshal(s)
			out.Messages = append(out.Messages, toolemu.OAIMessage{Role: "system", Content: rc})
		}
	}

	for _, m := range in.Messages {
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		var textParts []string
		var imageBlocks []map[string]any
		var toolCalls []toolemu.OAIToolCall
		var toolResults []toolemu.OAIMessage

		if isStringMessage(m.Content) {
			textParts = []string{decodeAnthropicText(m.Content)}
		} else {
			var blocks []map[string]json.RawMessage
			if err := json.Unmarshal(m.Content, &blocks); err == nil {
				for _, blk := range blocks {
					typ := ""
					_ = json.Unmarshal(blk["type"], &typ)
					switch typ {
					case "text":
						var t string
						_ = json.Unmarshal(blk["text"], &t)
						textParts = append(textParts, t)
					case "image":
						// Convert Anthropic's {source:{type:"base64",
						// media_type, data}} into OpenAI's image_url shape so
						// the downstream chat handler's extractImages picks
						// it up through the single uniform path.
						var src struct {
							Type      string `json:"type"`
							MediaType string `json:"media_type"`
							Data      string `json:"data"`
							URL       string `json:"url"`
						}
						_ = json.Unmarshal(blk["source"], &src)
						var url string
						if src.Type == "base64" && src.Data != "" {
							mime := src.MediaType
							if mime == "" {
								mime = "image/png"
							}
							url = "data:" + mime + ";base64," + src.Data
						} else if src.Type == "url" && src.URL != "" {
							url = src.URL
						}
						if url != "" {
							imageBlocks = append(imageBlocks, map[string]any{
								"type":      "image_url",
								"image_url": map[string]any{"url": url},
							})
						}
					case "thinking":
						// Assistant history thinking — skip.
					case "tool_use":
						if role == "assistant" {
							var id, name string
							_ = json.Unmarshal(blk["id"], &id)
							_ = json.Unmarshal(blk["name"], &name)
							var input any
							_ = json.Unmarshal(blk["input"], &input)
							if input == nil {
								input = map[string]any{}
							}
							args, _ := json.Marshal(input)
							if id == "" {
								id = "call_" + hex.EncodeToString(randomBytes(4))
							}
							tc := toolemu.OAIToolCall{ID: id, Type: "function"}
							tc.Function.Name = name
							tc.Function.Arguments = string(args)
							toolCalls = append(toolCalls, tc)
						}
					case "tool_result":
						var id string
						_ = json.Unmarshal(blk["tool_use_id"], &id)
						content := ""
						if raw, ok := blk["content"]; ok {
							if txt := decodeAnthropicText(raw); txt != "" {
								content = txt
							} else {
								content = string(raw)
							}
						}
						rc, _ := json.Marshal(content)
						toolResults = append(toolResults, toolemu.OAIMessage{
							Role: "tool", ToolCallID: id, Content: rc,
						})
					}
				}
			}
		}

		if len(toolCalls) > 0 {
			msg := toolemu.OAIMessage{Role: "assistant", ToolCalls: toolCalls}
			if len(textParts) > 0 {
				rc, _ := json.Marshal(strings.Join(textParts, "\n"))
				msg.Content = rc
			}
			out.Messages = append(out.Messages, msg)
		} else if len(textParts) > 0 || len(imageBlocks) > 0 {
			var rc json.RawMessage
			if len(imageBlocks) == 0 {
				rc, _ = json.Marshal(strings.Join(textParts, "\n"))
			} else {
				// Mixed text+image — emit OpenAI content-array shape so
				// extractImages sees the image_url blocks and contentToString
				// concatenates the text parts.
				arr := make([]map[string]any, 0, len(textParts)+len(imageBlocks))
				for _, t := range textParts {
					if t != "" {
						arr = append(arr, map[string]any{"type": "text", "text": t})
					}
				}
				arr = append(arr, imageBlocks...)
				rc, _ = json.Marshal(arr)
			}
			out.Messages = append(out.Messages, toolemu.OAIMessage{Role: role, Content: rc})
		}
		out.Messages = append(out.Messages, toolResults...)
	}

	for _, t := range in.Tools {
		out.Tools = append(out.Tools, toolemu.Tool{
			Type: "function",
			Function: toolemu.ToolFunction{
				Name: t.Name, Description: t.Description, Parameters: t.InputSchema,
			},
		})
	}
	return out, nil
}

func isStringMessage(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	return json.Unmarshal(raw, &s) == nil
}

// decodeAnthropicText handles:
//   - "hello"
//   - [{"type":"text","text":"hello"}]
//   - [{"type":"text","text":"a"},{"type":"text","text":"b"}]
func decodeAnthropicText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var b strings.Builder
		for _, item := range arr {
			typ := ""
			_ = json.Unmarshal(item["type"], &typ)
			if typ == "text" {
				var t string
				_ = json.Unmarshal(item["text"], &t)
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}

// ─── OpenAI → Anthropic non-stream response ───────────────

var stopMap = map[string]string{
	"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use",
}

func openAIToAnthropicResponse(in openAIResponse, model, msgID string) map[string]any {
	var content []any
	// Defensive: a malformed upstream response (stream aborted immediately,
	// cache miss path that produced only a role chunk, etc.) could surface
	// here with an empty Choices slice. Without the guard, `in.Choices[0]`
	// panics and the whole process dies — we return an empty assistant
	// turn so the caller sees a well-formed but content-less response.
	if len(in.Choices) == 0 {
		return map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		}
	}
	choice := in.Choices[0]
	if t := choice.Message.ReasoningContent; t != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": t})
	}
	if len(choice.Message.ToolCalls) > 0 {
		if choice.Message.Content != nil && *choice.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": *choice.Message.Content})
		}
		for _, tc := range choice.Message.ToolCalls {
			var input any = map[string]any{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			content = append(content, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input,
			})
		}
	} else {
		body := ""
		if choice.Message.Content != nil {
			body = *choice.Message.Content
		}
		content = append(content, map[string]any{"type": "text", "text": body})
	}
	stop := stopMap[choice.FinishReason]
	if stop == "" {
		stop = "end_turn"
	}
	cacheRead := 0
	if v, ok := in.Usage.PromptTokensDetails["cached_tokens"]; ok {
		cacheRead = v
	}
	return map[string]any{
		"id": msgID, "type": "message", "role": "assistant",
		"content": content, "model": model,
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":                in.Usage.PromptTokens,
			"output_tokens":               in.Usage.CompletionTokens,
			"cache_creation_input_tokens": in.Usage.CacheCreationInputTokens,
			"cache_read_input_tokens":     cacheRead,
		},
	}
}

// ─── Streaming translator ─────────────────────────────────

type anthropicTranslator struct {
	w       io.Writer
	flusher http.Flusher
	mu      sync.Mutex

	msgID     string
	model     string
	started   bool
	stopped   bool
	curType   string // "" | "text" | "thinking" | "tool_use"
	blockIdx  int
	toolBufs  map[int]*anthropicToolBuf
	finalU    openAIUsage
	stopReason string
	pendingSSE strings.Builder
}

type anthropicToolBuf struct {
	ID      string
	Name    string
	BlockIx int
}

func (t *anthropicTranslator) send(event string, data any) {
	b, _ := json.Marshal(data)
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", event, b)
	t.flusher.Flush()
}

func (t *anthropicTranslator) startMessage() {
	if t.started {
		return
	}
	t.started = true
	t.toolBufs = map[int]*anthropicToolBuf{}
	t.stopReason = "end_turn"
	t.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": t.msgID, "type": "message", "role": "assistant",
			"content": []any{}, "model": t.model,
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens": 0, "output_tokens": 0,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			},
		},
	})
}

func (t *anthropicTranslator) closeCurrentBlock() {
	if t.curType == "" {
		return
	}
	t.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": t.blockIdx})
	t.blockIdx++
	t.curType = ""
}

func (t *anthropicTranslator) startBlock(typ string, extra map[string]any) {
	t.closeCurrentBlock()
	t.curType = typ
	block := map[string]any{"type": typ}
	switch typ {
	case "text":
		block["text"] = ""
	case "thinking":
		block["thinking"] = ""
	case "tool_use":
		for k, v := range extra {
			block[k] = v
		}
		block["input"] = map[string]any{}
	}
	t.send("content_block_start", map[string]any{"type": "content_block_start", "index": t.blockIdx, "content_block": block})
}

func (t *anthropicTranslator) emitTextDelta(text string) {
	if text == "" {
		return
	}
	if t.curType != "text" {
		t.startBlock("text", nil)
	}
	t.send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": t.blockIdx,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (t *anthropicTranslator) emitThinkingDelta(text string) {
	if text == "" {
		return
	}
	if t.curType != "thinking" {
		t.startBlock("thinking", nil)
	}
	t.send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": t.blockIdx,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	})
}

func (t *anthropicTranslator) emitToolCallDelta(idx int, id, name, argsChunk string) {
	buf, ok := t.toolBufs[idx]
	if !ok {
		// new tool_use — start a new content block.
		useID := id
		useName := name
		if useID == "" {
			useID = "call_" + hex.EncodeToString(randomBytes(4))
		}
		t.startBlock("tool_use", map[string]any{"id": useID, "name": useName})
		buf = &anthropicToolBuf{ID: useID, Name: useName, BlockIx: t.blockIdx}
		t.toolBufs[idx] = buf
	}
	if argsChunk != "" {
		t.send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": buf.BlockIx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": argsChunk},
		})
	}
}

func (t *anthropicTranslator) processChunk(chunk map[string]json.RawMessage) {
	t.startMessage()
	var choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string         `json:"role"`
			Content          *string        `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
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
		if c.Delta.ReasoningContent != "" {
			t.emitThinkingDelta(c.Delta.ReasoningContent)
		}
		if c.Delta.Content != nil && *c.Delta.Content != "" {
			t.emitTextDelta(*c.Delta.Content)
		}
		for _, tc := range c.Delta.ToolCalls {
			t.emitToolCallDelta(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
		if c.FinishReason != nil {
			if v, ok := stopMap[*c.FinishReason]; ok {
				t.stopReason = v
			}
		}
	}
	if raw, ok := chunk["usage"]; ok && len(raw) > 0 {
		var u openAIUsage
		if err := json.Unmarshal(raw, &u); err == nil {
			t.finalU = u
		}
	}
}

// feed consumes raw bytes (OpenAI SSE) and drives message/block events.
func (t *anthropicTranslator) feed(raw []byte) {
	t.pendingSSE.Write(raw)
	s := t.pendingSSE.String()
	for {
		idx := strings.Index(s, "\n\n")
		if idx < 0 {
			break
		}
		frame := s[:idx]
		s = s[idx+2:]
		for _, line := range strings.Split(frame, "\n") {
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
			} else {
				logx.Warn("Messages SSE parse: %s", err.Error())
			}
		}
	}
	t.pendingSSE.Reset()
	t.pendingSSE.WriteString(s)
}

func (t *anthropicTranslator) finish() {
	if t.stopped {
		return
	}
	t.stopped = true
	t.closeCurrentBlock()
	cacheRead := 0
	if v, ok := t.finalU.PromptTokensDetails["cached_tokens"]; ok {
		cacheRead = v
	}
	t.send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": t.stopReason, "stop_sequence": nil},
		"usage": map[string]any{
			"input_tokens":                t.finalU.PromptTokens,
			"output_tokens":               t.finalU.CompletionTokens,
			"cache_creation_input_tokens": t.finalU.CacheCreationInputTokens,
			"cache_read_input_tokens":     cacheRead,
		},
	})
	t.send("message_stop", map[string]any{"type": "message_stop"})
}

// ─── Response-capture helpers ─────────────────────────────

// responseCapture buffers a non-stream JSON response for translation.
type responseCapture struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func (c *responseCapture) Header() http.Header { return c.header }
func (c *responseCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.buf.Write(b)
}
func (c *responseCapture) WriteHeader(status int) { c.status = status }

// streamShim forwards SSE bytes from the chat handler into the translator.
type streamShim struct {
	tr     *anthropicTranslator
	header http.Header
	wrote  bool
	flush  sync.Mutex
	reader *bufio.Scanner
	// Buffer small pieces so we can cleanly parse SSE frame boundaries.
}

func (s *streamShim) Header() http.Header {
	if s.header == nil {
		s.header = http.Header{}
	}
	return s.header
}
func (s *streamShim) WriteHeader(status int) { s.wrote = true }
func (s *streamShim) Write(b []byte) (int, error) {
	s.tr.feed(b)
	return len(b), nil
}
func (s *streamShim) Flush() {
	// Pretend we flushed — the translator already forwarded to the real writer.
}

// ─── Error shapes ────────────────────────────────────────

func anthropicErr(msg, typ string) map[string]any {
	return map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
}

func extractErrMsg(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return "Upstream error"
}
func extractErrType(body []byte) string {
	var parsed struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Error.Type != "" {
		return parsed.Error.Type
	}
	return "api_error"
}

func newMessageID() string {
	return "msg_" + hex.EncodeToString(randomBytes(12))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
