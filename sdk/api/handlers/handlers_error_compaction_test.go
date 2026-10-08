package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildErrorResponseBodySingleLineJSON(t *testing.T) {
	for _, input := range []string{
		"{\n  \"error\": {\n    \"code\": 500,\n    \"message\": \"Internal error\",\n    \"detail\": \"line one\\nline two\"\n  }\n}",
		"[\r\n {\"message\":\"invalid\"},\r\n 2\r\n]",
	} {
		body := BuildErrorResponseBody(http.StatusBadGateway, input)
		if strings.ContainsAny(string(body), "\r\n") {
			t.Fatalf("SSE data payload must occupy one line: %s", body)
		}
		if !json.Valid(body) {
			t.Fatalf("invalid JSON: %s", body)
		}
		var want, got any
		if err := json.Unmarshal([]byte(input), &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("error fields changed: %s != %s", gotJSON, wantJSON)
		}
	}
	input := "upstream failed\ntry again"
	body := BuildErrorResponseBody(http.StatusBadGateway, input)
	if strings.ContainsAny(string(body), "\r\n") || gjson.GetBytes(body, "error.message").String() != input {
		t.Fatalf("plain-text error was not preserved safely: %s", body)
	}
}
