package responses

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

// A duplicate output's status and body must come from the same winning item.
func TestResponsesToolResultStatus(t *testing.T) {
	for _, kind := range []string{"function", "custom_tool"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name      string
				statuses  []string
				wantError bool
			}{
				{"incomplete", []string{"incomplete"}, true},
				{"normalized incomplete", []string{"  InComplete  "}, true},
				{"completed", []string{"completed"}, false},
				{"in progress", []string{"in_progress"}, false},
				{"absent", []string{""}, false},
				{"unknown", []string{"failed"}, false},
				{"last completed wins", []string{"incomplete", "completed"}, false},
				{"last incomplete wins", []string{"completed", "incomplete"}, true},
				{"last missing status wins", []string{"incomplete", ""}, false},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", kind, stream, tc.name), func(t *testing.T) {
					input := []map[string]any{{"type": kind + "_call", "call_id": "call.with space", "name": "lookup", "arguments": "{}", "input": "query"}}
					for i, status := range tc.statuses {
						item := map[string]any{"type": kind + "_call_output", "call_id": "call.with space", "output": fmt.Sprintf("output-%d", i)}
						if status != "" {
							item["status"] = status
						}
						input = append(input, item)
					}
					raw, err := json.Marshal(map[string]any{"input": input})
					if err != nil {
						t.Fatal(err)
					}
					result := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, stream)
					var results []gjson.Result
					for _, message := range gjson.GetBytes(result, "messages").Array() {
						for _, block := range message.Get("content").Array() {
							if block.Get("type").String() == "tool_result" {
								results = append(results, block)
							}
						}
					}
					if len(results) != 1 {
						t.Fatalf("want one paired result, got %s", result)
					}
					got := results[0]
					if got.Get("is_error").Bool() != tc.wantError || got.Get("is_error").Exists() != tc.wantError {
						t.Errorf("is_error = %s, want present true only for incomplete=%t; %s", got.Get("is_error").Raw, tc.wantError, result)
					}
					if got.Get("content").String() != fmt.Sprintf("output-%d", len(tc.statuses)-1) {
						t.Errorf("last output lost: %s", result)
					}
					if got.Get("tool_use_id").String() != "call_with_space" {
						t.Errorf("sanitized pairing lost: %s", result)
					}
				})
			}
		}
	}
}

func TestResponsesIncompleteOrphanRemainsStandaloneText(t *testing.T) {
	for _, kind := range []string{"function", "custom_tool"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				raw := []byte(fmt.Sprintf(`{"input":[{"type":%q,"call_id":"unknown","status":"incomplete","output":"delegation context"}]}`, kind+"_call_output"))
				result := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, stream)
				message := gjson.GetBytes(result, "messages.0")
				if gjson.GetBytes(result, "messages.#").Int() != 1 || message.Get("role").String() != "user" || message.Get("content").Type != gjson.String || message.Get("content").String() != "delegation context" {
					t.Fatalf("orphan must remain text: %s", result)
				}
			})
		}
	}
}
