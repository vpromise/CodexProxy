package responses

import (
	"context"
	"fmt"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DeltaContentTypedPartsArray(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"deepseek-v4-1-flash-1017"}`)
	chunks := []string{
		`data: {"id":"bf39068555d24f22aaa60dc43231a002","object":"chat.completion.chunk","created":1794374142,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`data: {"id":"bf39068555d24f22aaa60dc43231a002","object":"chat.completion.chunk","created":1794374142,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"The"}],"closed":true}]},"finish_reason":null}]}`,
		`data: {"id":"bf39068555d24f22aaa60dc43231a002","object":"chat.completion.chunk","created":1794374142,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"erse."}],"closed":true},{"type":"text","text":"收到，链路"}]},"finish_reason":null}]}`,
		`data: {"id":"bf39068555d24f22aaa60dc43231a002","object":"chat.completion.chunk","created":1794374142,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":"正常"},"finish_reason":null}]}`,
		`data: {"id":"bf39068555d24f22aaa60dc43231a002","object":"chat.completion.chunk","created":1794374142,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":31,"total_tokens":62,"completion_tokens":31,"completion_tokens_details":{"reasoning_tokens":21}}}`,
		`data: [DONE]`,
	}

	var param any
	var itemTransitions []string
	var reasoningDeltas []string
	var textDeltas []string
	var reasoningDoneText string
	var textDoneText string
	var completed gjson.Result

	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-1-flash-1017", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				itemTransitions = append(itemTransitions, fmt.Sprintf("added:%s:%d", data.Get("item.type").String(), data.Get("output_index").Int()))
			case "response.output_item.done":
				itemTransitions = append(itemTransitions, fmt.Sprintf("done:%s:%d", data.Get("item.type").String(), data.Get("output_index").Int()))
			case "response.reasoning_summary_text.delta":
				reasoningDeltas = append(reasoningDeltas, data.Get("delta").String())
			case "response.reasoning_summary_text.done":
				reasoningDoneText = data.Get("text").String()
			case "response.output_text.delta":
				textDeltas = append(textDeltas, data.Get("delta").String())
			case "response.output_text.done":
				textDoneText = data.Get("text").String()
			case "response.completed":
				completed = data
			}
		}
	}

	wantTransitions := []string{
		"added:reasoning:0",
		"done:reasoning:0",
		"added:message:1",
		"done:message:1",
	}
	if strings.Join(itemTransitions, ",") != strings.Join(wantTransitions, ",") {
		t.Fatalf("item transitions = %v, want %v", itemTransitions, wantTransitions)
	}
	if len(reasoningDeltas) != 2 || reasoningDeltas[0] != "The" || reasoningDeltas[1] != "erse." {
		t.Fatalf("reasoning deltas = %v, want [The erse.]", reasoningDeltas)
	}
	if reasoningDoneText != "Theerse." {
		t.Fatalf("reasoning_summary_text.done = %q, want %q", reasoningDoneText, "Theerse.")
	}
	if len(textDeltas) != 2 || textDeltas[0] != "收到，链路" || textDeltas[1] != "正常" {
		t.Fatalf("output_text deltas = %v, want [收到，链路 正常]", textDeltas)
	}
	if textDoneText != "收到，链路正常" {
		t.Fatalf("output_text.done = %q, want %q", textDoneText, "收到，链路正常")
	}
	if got := completed.Get("response.output.#").Int(); got != 2 {
		t.Fatalf("response.output len = %d, want 2; completed=%s", got, completed.Raw)
	}
	if got := completed.Get("response.output.0.type").String(); got != "reasoning" {
		t.Fatalf("response.output.0.type = %q, want reasoning", got)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "Theerse." {
		t.Fatalf("response.output.0.summary.0.text = %q, want %q", got, "Theerse.")
	}
	if got := completed.Get("response.output.1.type").String(); got != "message" {
		t.Fatalf("response.output.1.type = %q, want message", got)
	}
	if got := completed.Get("response.output.1.content.0.text").String(); got != "收到，链路正常" {
		t.Fatalf("response.output.1.content.0.text = %q, want %q", got, "收到，链路正常")
	}
	if got := completed.Get("response.usage.output_tokens_details.reasoning_tokens").Int(); got != 21 {
		t.Fatalf("response.usage.output_tokens_details.reasoning_tokens = %d, want 21", got)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DeltaContentInterleavedTextThinkingText(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"zai-glm-5-3"}`)
	chunks := []string{
		`data: {"id":"chatcmpl_interleaved","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"First. "},{"type":"thinking","thinking":"Reconsidering..."},{"type":"text","text":"Second."}]},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}

	var param any
	var transitions []string
	var addedIDs []string
	var completed gjson.Result

	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "zai-glm-5-3", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				transitions = append(transitions, fmt.Sprintf("added:%s:%d:%s", data.Get("item.type").String(), data.Get("output_index").Int(), data.Get("item.id").String()))
				addedIDs = append(addedIDs, data.Get("item.id").String())
			case "response.output_item.done":
				transitions = append(transitions, fmt.Sprintf("done:%s:%d:%s", data.Get("item.type").String(), data.Get("output_index").Int(), data.Get("item.id").String()))
			case "response.completed":
				completed = data
			}
		}
	}

	wantTransitions := []string{
		"added:message:0:msg_chatcmpl_interleaved_0",
		"done:message:0:msg_chatcmpl_interleaved_0",
		"added:reasoning:1:rs_chatcmpl_interleaved_0",
		"done:reasoning:1:rs_chatcmpl_interleaved_0",
		"added:message:2:msg_chatcmpl_interleaved_0_1",
		"done:message:2:msg_chatcmpl_interleaved_0_1",
	}
	if strings.Join(transitions, ",") != strings.Join(wantTransitions, ",") {
		t.Fatalf("transitions = %v, want %v", transitions, wantTransitions)
	}
	if len(addedIDs) != 3 || addedIDs[0] == addedIDs[2] {
		t.Fatalf("expected 3 distinct item IDs, got %v", addedIDs)
	}
	if got := completed.Get("response.output.#").Int(); got != 3 {
		t.Fatalf("response.output len = %d, want 3; completed=%s", got, completed.Raw)
	}
	if got := completed.Get("response.output.0.content.0.text").String(); got != "First. " {
		t.Fatalf("output.0 text = %q, want %q", got, "First. ")
	}
	if got := completed.Get("response.output.1.summary.0.text").String(); got != "Reconsidering..." {
		t.Fatalf("output.1 summary = %q, want %q", got, "Reconsidering...")
	}
	if got := completed.Get("response.output.2.content.0.text").String(); got != "Second." {
		t.Fatalf("output.2 text = %q, want %q", got, "Second.")
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_InterleavedTextThinkingTextTruncatedPreservesClosedMessageStatus(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		finishReason     string
		incompleteReason string
	}{
		{name: "length", finishReason: "length", incompleteReason: "max_output_tokens"},
		{name: "content_filter", finishReason: "content_filter", incompleteReason: "content_filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := []byte(`{"model":"zai-glm-5-3"}`)
			chunks := []string{
				`data: {"id":"chatcmpl_trunc","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"Completed prefix."},{"type":"thinking","thinking":"Thinking..."}]},"finish_reason":null}]}`,
				fmt.Sprintf(`data: {"id":"chatcmpl_trunc","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"Truncated tail"}]},"finish_reason":%q}]}`, tc.finishReason),
				`data: [DONE]`,
			}

			var param any
			doneStatusByID := make(map[string]string)
			var terminal gjson.Result

			for _, line := range chunks {
				for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "zai-glm-5-3", request, request, []byte(line), &param) {
					event, data := parseOpenAIResponsesSSEEvent(t, chunk)
					switch event {
					case "response.output_item.done":
						if data.Get("item.type").String() == "message" {
							doneStatusByID[data.Get("item.id").String()] = data.Get("item.status").String()
						}
					case "response.incomplete":
						terminal = data
					}
				}
			}

			if !terminal.Exists() {
				t.Fatalf("expected response.incomplete terminal event for finish_reason=%s", tc.finishReason)
			}
			if got := terminal.Get("response.incomplete_details.reason").String(); got != tc.incompleteReason {
				t.Fatalf("incomplete_details.reason = %q, want %q", got, tc.incompleteReason)
			}
			if got := doneStatusByID["msg_chatcmpl_trunc_0"]; got != "completed" {
				t.Fatalf("first message output_item.done status = %q, want completed", got)
			}
			if got := doneStatusByID["msg_chatcmpl_trunc_0_1"]; got != "incomplete" {
				t.Fatalf("second message output_item.done status = %q, want incomplete", got)
			}
			if got := terminal.Get("response.output.0.status").String(); got != "completed" {
				t.Fatalf("terminal response.output.0.status = %q, want completed (must match output_item.done)", got)
			}
			if got := terminal.Get("response.output.2.status").String(); got != "incomplete" {
				t.Fatalf("terminal response.output.2.status = %q, want incomplete", got)
			}
		})
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DeltaContentPartVariants(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"zai-glm-5-3"}`)
	chunks := []string{
		`data: {"id":"chatcmpl_variants","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{"content":[]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_variants","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"step 1"},{"type":"reasoning","text":" step 2"},{"type":"output_text","text":"answer: "},{"type":"refusal","refusal":"blocked "},"plain ",{"type":"custom_part","payload":"raw"}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_variants","object":"chat.completion.chunk","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}

	var param any
	var reasoningDeltas []string
	var textDeltas []string

	for _, line := range chunks {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "zai-glm-5-3", request, request, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.reasoning_summary_text.delta":
				reasoningDeltas = append(reasoningDeltas, data.Get("delta").String())
			case "response.output_text.delta":
				textDeltas = append(textDeltas, data.Get("delta").String())
			}
		}
	}

	if got := strings.Join(reasoningDeltas, ""); got != "step 1 step 2" {
		t.Fatalf("reasoning text = %q, want %q (deltas=%v)", got, "step 1 step 2", reasoningDeltas)
	}
	wantTextDeltas := []string{"answer: ", "blocked ", "plain "}
	if strings.Join(textDeltas, "|") != strings.Join(wantTextDeltas, "|") {
		t.Fatalf("text deltas = %v, want %v", textDeltas, wantTextDeltas)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_DeltaContentReasoningOnlyTermination(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"deepseek-v4-1-flash-1017"}`)

	t.Run("finish_reason length emits response.incomplete", func(t *testing.T) {
		chunks := []string{
			`data: {"id":"chatcmpl_len_thinking","object":"chat.completion.chunk","created":1773896263,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"exhausted budget"}],"closed":true}]},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl_len_thinking","object":"chat.completion.chunk","created":1773896263,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"completion_tokens_details":{"reasoning_tokens":20}}}`,
		}

		var param any
		for _, line := range chunks {
			for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-1-flash-1017", request, request, []byte(line), &param) {
				event, _ := parseOpenAIResponsesSSEEvent(t, chunk)
				if event == "response.completed" {
					t.Fatalf("reasoning-only stream with finish_reason=length emitted response.completed: %s", chunk)
				}
			}
		}

		fin, ok := param.(interface{ CanFinalizeResponseStream() bool })
		if !ok || !fin.CanFinalizeResponseStream() {
			t.Fatalf("expected CanFinalizeResponseStream() == true after explicit finish_reason=length")
		}

		var incomplete gjson.Result
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-1-flash-1017", request, request, []byte("data: [DONE]"), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if event == "response.incomplete" {
				incomplete = data
			}
		}

		if !incomplete.Exists() {
			t.Fatal("expected response.incomplete event on [DONE] for reasoning-only stream with finish_reason=length")
		}
		if got := incomplete.Get("response.status").String(); got != "incomplete" {
			t.Fatalf("response.status = %q, want incomplete", got)
		}
		if got := incomplete.Get("response.incomplete_details.reason").String(); got != "max_output_tokens" {
			t.Fatalf("response.incomplete_details.reason = %q, want max_output_tokens", got)
		}
		if got := incomplete.Get("response.output.#").Int(); got != 1 {
			t.Fatalf("response.output len = %d, want 1; incomplete=%s", got, incomplete.Raw)
		}
		if got := incomplete.Get("response.output.0.type").String(); got != "reasoning" {
			t.Fatalf("response.output.0.type = %q, want reasoning", got)
		}
		if got := incomplete.Get("response.output.0.summary.0.text").String(); got != "exhausted budget" {
			t.Fatalf("response.output.0.summary.0.text = %q, want %q", got, "exhausted budget")
		}
	})

	t.Run("missing finish_reason does not complete reasoning-only stream", func(t *testing.T) {
		chunks := []string{
			`data: {"id":"chatcmpl_unfinished_thinking","object":"chat.completion.chunk","created":1773896263,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"still thinking"}],"closed":true}]},"finish_reason":null}]}`,
			`data: [DONE]`,
		}

		var param any
		reasoningSeen := false
		for _, line := range chunks {
			for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-1-flash-1017", request, request, []byte(line), &param) {
				event, _ := parseOpenAIResponsesSSEEvent(t, chunk)
				if event == "response.reasoning_summary_text.delta" {
					reasoningSeen = true
				}
				if event == "response.completed" {
					t.Fatalf("unfinished reasoning-only stream emitted response.completed: %s", chunk)
				}
			}
		}
		if !reasoningSeen {
			t.Fatal("expected reasoning delta to be emitted before [DONE]")
		}
	})
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_ContentPartsArray(t *testing.T) {
	t.Parallel()

	t.Run("nested thinking array and text part", func(t *testing.T) {
		request := []byte(`{"model":"deepseek-v4-1-flash-1017"}`)
		raw := []byte(`{"id":"bd5377d8250c4022b9a1b4dcb258ec4b","object":"chat.completion","created":1773896263,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","tool_calls":null,"content":[{"type":"thinking","thinking":[{"type":"text","text":"We need answer to \"test\". "},{"type":"text","text":"Respond concise."}],"closed":true},{"type":"text","text":"Test received. How can I help?"}]}}]}`)

		resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v4-1-flash-1017", request, request, raw, nil)
		data := gjson.ParseBytes(resp)

		if got := data.Get("output.#").Int(); got != 2 {
			t.Fatalf("output len = %d, want 2; resp=%s", got, resp)
		}
		if got := data.Get("output.0.type").String(); got != "reasoning" {
			t.Fatalf("output.0.type = %q, want reasoning; resp=%s", got, resp)
		}
		if got := data.Get("output.0.summary.0.text").String(); got != `We need answer to "test". Respond concise.` {
			t.Fatalf("output.0.summary.0.text = %q, want %q", got, `We need answer to "test". Respond concise.`)
		}
		if got := data.Get("output.1.type").String(); got != "message" {
			t.Fatalf("output.1.type = %q, want message; resp=%s", got, resp)
		}
		if got := data.Get("output.1.content.0.text").String(); got != "Test received. How can I help?" {
			t.Fatalf("output.1.content.0.text = %q, want %q", got, "Test received. How can I help?")
		}
	})

	t.Run("top-level reasoning_content takes priority without leaking thinking part into message", func(t *testing.T) {
		request := []byte(`{"model":"zai-glm-5-3"}`)
		raw := []byte(`{"id":"chatcmpl_dedup","object":"chat.completion","created":1773896263,"model":"zai-glm-5-3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","reasoning_content":"top-level reasoning","content":[{"type":"thinking","thinking":"duplicate thinking"},{"type":"text","text":"Clean answer."}]}}]}`)

		resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "zai-glm-5-3", request, request, raw, nil)
		data := gjson.ParseBytes(resp)

		if got := data.Get("output.#").Int(); got != 2 {
			t.Fatalf("output len = %d, want 2; resp=%s", got, resp)
		}
		if got := data.Get("output.0.summary.0.text").String(); got != "top-level reasoning" {
			t.Fatalf("output.0.summary.0.text = %q, want %q", got, "top-level reasoning")
		}
		if got := data.Get("output.1.content.0.text").String(); got != "Clean answer." {
			t.Fatalf("output.1.content.0.text = %q, want %q", got, "Clean answer.")
		}
	})

	t.Run("thinking-only array with finish_reason length omits empty message item", func(t *testing.T) {
		request := []byte(`{"model":"deepseek-v4-1-flash-1017"}`)
		raw := []byte(`{"id":"chatcmpl_think_len","object":"chat.completion","created":1773896263,"model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"truncated thought"}],"closed":true}]}}]}`)

		resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v4-1-flash-1017", request, request, raw, nil)
		data := gjson.ParseBytes(resp)

		if got := data.Get("status").String(); got != "incomplete" {
			t.Fatalf("status = %q, want incomplete; resp=%s", got, resp)
		}
		if got := data.Get("output.#").Int(); got != 1 {
			t.Fatalf("output len = %d, want 1; resp=%s", got, resp)
		}
		if got := data.Get("output.0.type").String(); got != "reasoning" {
			t.Fatalf("output.0.type = %q, want reasoning; resp=%s", got, resp)
		}
		if got := data.Get("output.0.summary.0.text").String(); got != "truncated thought" {
			t.Fatalf("output.0.summary.0.text = %q, want %q", got, "truncated thought")
		}
	})
}

func TestOpenAIResponsesTypedContentRejectsStructuredText(t *testing.T) {
	for _, content := range []string{
		`{"type":"text","text":"do-not-leak"}`,
		`[{"type":"unknown","text":"do-not-leak"}]`,
		`[{"type":"text","text":{"secret":"do-not-leak"}}]`,
		`[{"type":"thinking","thinking":[{"text":{"secret":"do-not-leak"}}]}]`,
		`[null,2,true,{},["do-not-leak"]]`,
	} {
		t.Run(content, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"id":"unknown","object":"chat.completion","choices":[{"index":0,"message":{"content":%s},"finish_reason":"stop"}]}`, content))
			response := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "model", nil, nil, raw, nil)
			if gjson.GetBytes(response, "output.#").Int() != 0 {
				t.Fatalf("structured content was exposed: %s", response)
			}
			var param any
			stream := []byte(fmt.Sprintf(`{"id":"unknown","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":%s},"finish_reason":"stop"}]}`, content))
			for _, event := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", nil, nil, stream, &param) {
				_, data := parseOpenAIResponsesSSEEvent(t, event)
				if data.Get("delta").Exists() {
					t.Fatalf("structured delta was exposed: %s", event)
				}
			}
		})
	}
}

func TestOpenAIResponsesTypedContentRepeatedReasoningIDs(t *testing.T) {
	request := []byte(`{"model":"model"}`)
	chunks := []string{
		`{"id":"repeat","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"first","content":[{"type":"thinking","thinking":"duplicate"},{"type":"text","text":"one"}]}}]}`,
		`{"id":"repeat","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"second"},{"type":"text","text":"two"},{"type":"thinking","thinking":"third"}]},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}
	var param any
	added := map[string]int64{}
	done := map[string]int64{}
	var completed gjson.Result
	for _, raw := range chunks {
		for _, event := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "model", request, request, []byte(raw), &param) {
			name, data := parseOpenAIResponsesSSEEvent(t, event)
			id := data.Get("item.id").String()
			switch name {
			case "response.output_item.added":
				if _, ok := added[id]; ok {
					t.Fatalf("reused item ID %q", id)
				}
				added[id] = data.Get("output_index").Int()
			case "response.output_item.done":
				done[id] = data.Get("output_index").Int()
			case "response.completed":
				completed = data
			}
		}
	}
	outputs := completed.Get("response.output").Array()
	if len(outputs) != 5 || len(added) != 5 || len(done) != 5 {
		t.Fatalf("items mismatch added=%v done=%v terminal=%s", added, done, completed.Raw)
	}
	for i, item := range outputs {
		id := item.Get("id").String()
		addedIndex, okAdded := added[id]
		doneIndex, okDone := done[id]
		if !okAdded || !okDone || addedIndex != int64(i) || doneIndex != int64(i) {
			t.Fatalf("item %q lost stable index %d", id, i)
		}
	}
	if got := outputs[0].Get("summary.0.text").String(); got != "first" {
		t.Fatalf("reasoning deduplication = %q", got)
	}
	if got := outputs[2].Get("summary.0.text").String(); got != "second" {
		t.Fatalf("second reasoning = %q", got)
	}
	if got := outputs[4].Get("summary.0.text").String(); got != "third" {
		t.Fatalf("third reasoning = %q", got)
	}
}
