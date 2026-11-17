package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string `json:"type"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	SequenceNumber int    `json:"sequence_number"`
}

type openAIResponsesStreamFailedChunk struct {
	Type           string                              `json:"type"`
	SequenceNumber int                                 `json:"sequence_number"`
	Response       openAIResponsesStreamFailedResponse `json:"response"`
}

type openAIResponsesStreamFailedResponse struct {
	Status string         `json:"status"`
	Error  map[string]any `json:"error"`
}

func openAIResponsesStreamErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusForbidden:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusRequestTimeout:
		return "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_server_error"
		}
		if status >= http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "unknown_error"
	}
}

// BuildOpenAIResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
//
// Important: OpenAI's HTTP error bodies are shaped like {"error":{...}}; those are valid for
// non-streaming responses, but streaming clients validate SSE `data:` payloads against a union
// of chunks that requires a top-level `type` field.
func BuildOpenAIResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}

	code := openAIResponsesStreamErrorCode(status)

	payload := decodeOpenAIResponsesErrorPayload(errText)
	if payload != nil {
		if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) == "error" {
			if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
				message = strings.TrimSpace(m)
			}
			if v, ok := payload["code"]; ok && v != nil {
				code = strings.TrimSpace(fmt.Sprint(v))
			}
		}
		if v, ok := payload["sequence_number"].(json.Number); ok && sequenceNumber == 0 {
			if seq, errSequence := v.Int64(); errSequence == nil && seq >= 0 && int64(int(seq)) == seq {
				sequenceNumber = int(seq)
			}
		}
		if detail, scalar := nestedOpenAIResponsesErrorDetail(payload); detail != nil {
			detail = applyClaudePromptOverflowCode(detail)
			if m, ok := detail["message"].(string); ok && strings.TrimSpace(m) != "" {
				message = strings.TrimSpace(m)
			}
			if v, ok := detail["code"]; ok && v != nil {
				code = strings.TrimSpace(fmt.Sprint(v))
			}
		} else if strings.TrimSpace(scalar) != "" {
			message = scalar
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	data, err := json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           code,
		Message:        message,
		SequenceNumber: sequenceNumber,
	})
	if err == nil {
		return data
	}

	// Extremely defensive fallback.
	data, _ = json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           "internal_server_error",
		Message:        message,
		SequenceNumber: sequenceNumber,
	})
	if len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","code":"internal_server_error","message":"internal error","sequence_number":0}`)
}

// Decode protocol counters and error parameters without a float64 round trip.
func decodeOpenAIResponsesErrorPayload(text string) map[string]any {
	data := []byte(strings.TrimSpace(text))
	if !json.Valid(data) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if errDecode := decoder.Decode(&payload); errDecode != nil {
		return nil
	}
	return payload
}

func nestedOpenAIResponsesErrorDetail(payload map[string]any) (map[string]any, string) {
	nodes := []any{payload["error"]}
	for _, path := range []string{"response", "body"} {
		if wrapper, ok := payload[path].(map[string]any); ok {
			nodes = append(nodes, wrapper["error"])
		}
	}
	scalar := ""
	for _, node := range nodes {
		if detail, ok := node.(map[string]any); ok {
			return detail, ""
		}
		if message, ok := node.(string); ok && strings.TrimSpace(message) != "" && scalar == "" {
			scalar = message
		}
	}
	return nil, scalar
}

// applyClaudePromptOverflowCode annotates input rejection, never a generated truncation.
func applyClaudePromptOverflowCode(detail map[string]any) map[string]any {
	if existing, ok := detail["code"]; ok && existing != nil && strings.TrimSpace(fmt.Sprint(existing)) != "" {
		return detail
	}
	if message, _ := detail["message"].(string); detail["type"] == "invalid_request_error" && strings.HasPrefix(message, "prompt is too long") {
		detail["code"] = "context_length_exceeded"
	}
	return detail
}

func openAIResponsesStreamFailedErrorDetail(status int, errText, code, message string) map[string]any {
	if payload := decodeOpenAIResponsesErrorPayload(errText); payload != nil {
		if detail, _ := nestedOpenAIResponsesErrorDetail(payload); detail != nil {
			return applyClaudePromptOverflowCode(detail)
		}
	}

	errorType := "invalid_request_error"
	// A Codex stream that ends before response.completed is a transport failure,
	// surfaced as HTTP 408, rather than an invalid client request.
	if status == http.StatusRequestTimeout || status >= http.StatusInternalServerError {
		errorType = "server_error"
	}
	return map[string]any{
		"type":    errorType,
		"code":    code,
		"message": message,
	}
}

// BuildOpenAIResponsesStreamFailedChunk builds the terminal Responses event used by official Codex clients.
// It is intentionally separate from BuildOpenAIResponsesStreamErrorChunk so existing clients keep the legacy shape.
func BuildOpenAIResponsesStreamFailedChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	legacyChunk := BuildOpenAIResponsesStreamErrorChunk(status, errText, sequenceNumber)
	var legacyPayload openAIResponsesStreamErrorChunk
	if errUnmarshal := json.Unmarshal(legacyChunk, &legacyPayload); errUnmarshal != nil {
		legacyPayload.Code = openAIResponsesStreamErrorCode(status)
		legacyPayload.Message = http.StatusText(status)
		legacyPayload.SequenceNumber = sequenceNumber
	}
	if sequenceNumber == 0 && legacyPayload.SequenceNumber > 0 {
		sequenceNumber = legacyPayload.SequenceNumber
	}

	data, errMarshal := json.Marshal(openAIResponsesStreamFailedChunk{
		Type:           "response.failed",
		SequenceNumber: sequenceNumber,
		Response: openAIResponsesStreamFailedResponse{
			Status: "failed",
			Error:  openAIResponsesStreamFailedErrorDetail(status, errText, legacyPayload.Code, legacyPayload.Message),
		},
	})
	if errMarshal == nil {
		return data
	}

	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"status":"failed","error":{"type":"server_error","code":"internal_server_error","message":"internal error"}}}`)
}
