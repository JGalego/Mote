package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system"`
	Messages      []anthropicMessage `json:"messages"`
	Tools         []anthropicTool    `json:"tools"`
	ToolChoice    json.RawMessage    `json:"tool_choice"`
	StopSequences []string           `json:"stop_sequences"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	Stream        bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		anthropicAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request too large")
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		anthropicAPIError(w, http.StatusBadRequest, "invalid_request_error", "request is not JSON: "+err.Error())
		return
	}
	upstreamBody, err := req.openAI()
	if err != nil {
		anthropicAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	m, err := s.Resolve(req.Model)
	if err != nil {
		anthropicAPIError(w, http.StatusNotFound, "not_found_error", err.Error())
		return
	}
	sl, release, err := s.acquire(r.Context(), m)
	if err != nil {
		anthropicAPIError(w, http.StatusServiceUnavailable, "api_error", err.Error())
		return
	}
	defer release()
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, sl.url+"/v1/chat/completions", bytes.NewReader(upstreamBody))
	if err != nil {
		anthropicAPIError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(up)
	if err != nil {
		anthropicAPIError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("%s: %v", m.ID, err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		kind, message := upstreamError(resp.StatusCode, raw)
		anthropicAPIError(w, resp.StatusCode, kind, message)
		return
	}
	if req.Stream {
		streamAnthropic(r.Context(), w, resp.Body, req.Model)
		return
	}
	var out openAIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil || len(out.Choices) == 0 {
		anthropicAPIError(w, http.StatusBadGateway, "api_error", "model returned an invalid response")
		return
	}
	answer, err := anthropicResponse(req.Model, out)
	if err != nil {
		anthropicAPIError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func (r anthropicRequest) openAI() ([]byte, error) {
	messages := make([]map[string]any, 0, len(r.Messages)+1)
	if len(r.System) > 0 {
		system, err := blockText(r.System)
		if err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
		if system != "" {
			messages = append(messages, map[string]any{"role": "system", "content": system})
		}
	}
	for _, message := range r.Messages {
		converted, err := openAIMessages(message)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}
	body := map[string]any{
		"model": r.Model, "messages": joinTurns(messages), "max_tokens": r.MaxTokens, "stream": r.Stream,
	}
	if r.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if len(r.StopSequences) > 0 {
		body["stop"] = r.StopSequences
	}
	if len(r.Tools) > 0 {
		tools := make([]any, 0, len(r.Tools))
		for _, tool := range r.Tools {
			var parameters any
			if err := json.Unmarshal(tool.InputSchema, &parameters); err != nil {
				return nil, fmt.Errorf("tool %s has an invalid input_schema: %w", tool.Name, err)
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": parameters,
			}})
		}
		body["tools"] = tools
	}
	if len(r.ToolChoice) > 0 {
		choice, err := openAIToolChoice(r.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = choice
	}
	return json.Marshal(body)
}

func openAIMessages(message anthropicMessage) ([]map[string]any, error) {
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return []map[string]any{{"role": message.Role, "content": text}}, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return nil, fmt.Errorf("%s message has invalid content: %w", message.Role, err)
	}
	if message.Role == "assistant" {
		var texts []string
		var calls []any
		for _, block := range blocks {
			switch block.Type {
			case "text":
				texts = append(texts, block.Text)
			case "tool_use":
				args := block.Input
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				calls = append(calls, map[string]any{"id": block.ID, "type": "function", "function": map[string]any{
					"name": block.Name, "arguments": string(args),
				}})
			}
		}
		out := map[string]any{"role": "assistant", "content": strings.Join(texts, "")}
		if len(calls) > 0 {
			out["tool_calls"] = calls
		}
		return []map[string]any{out}, nil
	}
	var out []map[string]any
	var parts []any
	flush := func() {
		if len(parts) == 0 {
			return
		}
		content := any(parts)
		if len(parts) == 1 {
			if part, ok := parts[0].(map[string]any); ok && part["type"] == "text" {
				content = part["text"]
			}
		}
		out = append(out, map[string]any{"role": message.Role, "content": content})
		parts = nil
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
		case "image":
			url := block.Source.URL
			if block.Source.Type == "base64" {
				url = "data:" + block.Source.MediaType + ";base64," + block.Source.Data
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		case "tool_result":
			flush()
			result, err := blockText(block.Content)
			if err != nil {
				return nil, fmt.Errorf("tool result %s: %w", block.ToolUseID, err)
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": block.ToolUseID, "content": result})
		}
	}
	flush()
	return out, nil
}

// joinTurns fits a conversation to what chat templates accept. Anthropic
// allows a system message after the conversation has started, which Claude
// Code sends, and several user messages in a row; most templates take a
// system message only first, and some insist that user and assistant take
// turns. A later system message becomes a user turn where it stands, so the
// prompt llama-server has cached still matches, and adjacent user turns are
// joined.
func joinTurns(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if m["role"] == "system" && len(out) > 0 && out[len(out)-1]["role"] != "system" {
			m["role"] = "user"
		}
		if n := len(out); n > 0 && out[n-1]["role"] == m["role"] && (m["role"] == "system" || m["role"] == "user") {
			out[n-1]["content"] = joinContent(out[n-1]["content"], m["content"])
			continue
		}
		out = append(out, m)
	}
	return out
}

func joinContent(a, b any) any {
	as, aText := a.(string)
	bs, bText := b.(string)
	if aText && bText {
		return as + "\n\n" + bs
	}
	return append(contentParts(a), contentParts(b)...)
}

func contentParts(content any) []any {
	if text, ok := content.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	parts, _ := content.([]any)
	return parts
}

func blockText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var texts []string
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, ""), nil
}

func openAIToolChoice(raw json.RawMessage) (any, error) {
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("invalid tool_choice: %w", err)
	}
	switch choice.Type {
	case "auto", "none":
		return choice.Type, nil
	case "any":
		return "required", nil
	case "tool":
		return map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}, nil
	default:
		return nil, fmt.Errorf("unknown tool_choice type %q", choice.Type)
	}
}

func anthropicResponse(model string, in openAIResponse) (map[string]any, error) {
	if len(in.Choices) == 0 {
		return nil, fmt.Errorf("model returned no choices")
	}
	choice := in.Choices[0]
	id := in.ID
	if id == "" {
		id = nextAnthropicMessageID()
	} else if !strings.HasPrefix(id, "msg_") {
		id = "msg_" + id
	}
	content := []any{}
	if text, err := openAIText(choice.Message.Content); err != nil {
		return nil, err
	} else if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for i, call := range choice.Message.ToolCalls {
		var input any
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = `{}`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return nil, fmt.Errorf("model returned invalid arguments for tool %s: %w", call.Function.Name, err)
		}
		toolID := call.ID
		if toolID == "" {
			toolID = fmt.Sprintf("toolu_%s_%d", id, i)
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": toolID, "name": call.Function.Name, "input": input,
		})
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": anthropicStopReason(choice.FinishReason), "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": in.Usage.PromptTokens, "output_tokens": in.Usage.CompletionTokens},
	}, nil
}

type openAIStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Timings struct {
		CacheN     int `json:"cache_n"`
		PromptN    int `json:"prompt_n"`
		PredictedN int `json:"predicted_n"`
	} `json:"timings"`
}

type streamedTool struct {
	id, name string
	parts    []string
}

var anthropicMessageSequence atomic.Uint64

func nextAnthropicMessageID() string {
	return fmt.Sprintf("msg_mote_%x", anthropicMessageSequence.Add(1))
}

func streamAnthropic(ctx context.Context, w http.ResponseWriter, r io.Reader, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	started := false
	messageID := nextAnthropicMessageID()
	start := func() {
		if started {
			return
		}
		started = true
		writeAnthropicEvent(w, flusher, "message_start", struct {
			Type    string `json:"type"`
			Message any    `json:"message"`
		}{"message_start", map[string]any{
			"id": messageID, "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		}})
	}
	textIndex := -1
	var tools = map[int]*streamedTool{}
	var toolOrder []int
	stopReason := ""
	inputTokens, outputTokens, counted := 0, 0, false

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			start()
			writeAnthropicEvent(w, flusher, "error", map[string]any{"type": "error", "error": map[string]any{
				"type": "api_error", "message": "model returned an invalid stream chunk",
			}})
			return
		}
		if !started && chunk.ID != "" {
			messageID = chunk.ID
			if !strings.HasPrefix(messageID, "msg_") {
				messageID = "msg_" + messageID
			}
		}
		start()
		// Claude Code compacts the conversation by the input it is told
		// about, so it must include the prompt llama-server had cached,
		// which timings' prompt_n leaves out.
		if u := chunk.Usage; u.PromptTokens > 0 || u.CompletionTokens > 0 {
			inputTokens, outputTokens, counted = u.PromptTokens, u.CompletionTokens, true
		} else if t := chunk.Timings; !counted && (t.PromptN > 0 || t.PredictedN > 0) {
			inputTokens, outputTokens = t.CacheN+t.PromptN, t.PredictedN
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if textIndex < 0 {
					textIndex = 0
					writeAnthropicEvent(w, flusher, "content_block_start", struct {
						Type         string `json:"type"`
						Index        int    `json:"index"`
						ContentBlock any    `json:"content_block"`
					}{"content_block_start", textIndex, struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}{"text", ""}})
				}
				writeAnthropicEvent(w, flusher, "content_block_delta", struct {
					Type  string `json:"type"`
					Index int    `json:"index"`
					Delta any    `json:"delta"`
				}{"content_block_delta", textIndex, struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{"text_delta", choice.Delta.Content}})
			}
			for _, call := range choice.Delta.ToolCalls {
				tool := tools[call.Index]
				if tool == nil {
					tool = &streamedTool{}
					tools[call.Index] = tool
					toolOrder = append(toolOrder, call.Index)
				}
				if call.ID != "" {
					tool.id = call.ID
				}
				if call.Function.Name != "" {
					tool.name = call.Function.Name
				}
				if call.Function.Arguments != "" {
					tool.parts = append(tool.parts, call.Function.Arguments)
				}
			}
			if choice.FinishReason != "" {
				stopReason = anthropicStopReason(choice.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		start()
		writeAnthropicEvent(w, flusher, "error", map[string]any{"type": "error", "error": map[string]any{
			"type": "api_error", "message": err.Error(),
		}})
		return
	}
	start()
	if textIndex >= 0 {
		writeAnthropicEvent(w, flusher, "content_block_stop", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}{"content_block_stop", textIndex})
	}
	nextIndex := 0
	if textIndex >= 0 {
		nextIndex = 1
	}
	for _, upstreamIndex := range toolOrder {
		tool := tools[upstreamIndex]
		id := tool.id
		if id == "" {
			id = fmt.Sprintf("toolu_%s_%d", messageID, upstreamIndex)
		}
		writeAnthropicEvent(w, flusher, "content_block_start", struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock any    `json:"content_block"`
		}{"content_block_start", nextIndex, struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}{"tool_use", id, tool.name, map[string]any{}}})
		for _, part := range tool.parts {
			writeAnthropicEvent(w, flusher, "content_block_delta", struct {
				Type  string `json:"type"`
				Index int    `json:"index"`
				Delta any    `json:"delta"`
			}{"content_block_delta", nextIndex, struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			}{"input_json_delta", part}})
		}
		writeAnthropicEvent(w, flusher, "content_block_stop", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}{"content_block_stop", nextIndex})
		nextIndex++
	}
	if stopReason == "" {
		if len(toolOrder) > 0 {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}
	writeAnthropicEvent(w, flusher, "message_delta", struct {
		Type  string `json:"type"`
		Delta any    `json:"delta"`
		Usage any    `json:"usage"`
	}{"message_delta", struct {
		StopReason   string  `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	}{stopReason, nil}, map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens}})
	writeAnthropicEvent(w, flusher, "message_stop", struct {
		Type string `json:"type"`
	}{"message_stop"})
}

func writeAnthropicEvent(w io.Writer, flusher http.Flusher, event string, value any) {
	b, _ := json.Marshal(value)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	if flusher != nil {
		flusher.Flush()
	}
}

func openAIText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("model returned invalid content: %w", err)
	}
	var texts []string
	for _, part := range parts {
		if part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, ""), nil
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop":
		return "end_turn"
	default:
		return reason
	}
}

func (s *Server) anthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		anthropicAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request too large")
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		anthropicAPIError(w, http.StatusBadRequest, "invalid_request_error", "request is not JSON: "+err.Error())
		return
	}
	converted, err := req.openAI()
	if err != nil {
		anthropicAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	// This is deliberately conservative. Claude Code uses the count to decide
	// when to compact; exact tokenization remains model-specific.
	tokens := (len(converted) + 2) / 3
	if tokens == 0 {
		tokens = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": tokens})
}

func anthropicAPIError(w http.ResponseWriter, code int, kind, message string) {
	writeJSON(w, code, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}

// upstreamError turns llama-server's error into Anthropic's terms. A prompt
// longer than the context is worded the way Anthropic words it, which is
// what makes Claude Code compact the conversation and try again.
func upstreamError(code int, raw []byte) (kind, message string) {
	var body struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			PromptTokens int    `json:"n_prompt_tokens"`
			Context      int    `json:"n_ctx"`
		} `json:"error"`
	}
	message = strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &body) == nil && body.Error.Message != "" {
		message = body.Error.Message
	}
	if body.Error.Type == "exceed_context_size_error" && body.Error.Context > 0 {
		return "invalid_request_error", fmt.Sprintf("prompt is too long: %d tokens > %d maximum", body.Error.PromptTokens, body.Error.Context)
	}
	switch code {
	case http.StatusBadRequest:
		kind = "invalid_request_error"
	case http.StatusNotFound:
		kind = "not_found_error"
	case http.StatusRequestEntityTooLarge:
		kind = "request_too_large"
	case http.StatusTooManyRequests:
		kind = "rate_limit_error"
	case http.StatusServiceUnavailable:
		kind = "overloaded_error"
	default:
		kind = "api_error"
	}
	return kind, message
}
