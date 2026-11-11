package logging

import (
	"net/http"
	"strings"
	"testing"
)

func TestFormatLogContentMasksResponseCookies(t *testing.T) {
	secret := "synthetic-legacy-cookie-secret-value"
	headers := map[string][]string{
		"Set-Cookie":   {"session=" + secret + "; HttpOnly"},
		"X-Request-Id": {"legacy-response-marker"},
	}
	logger := &FileRequestLogger{}
	text := logger.formatLogContent("https://example.invalid/responses", http.MethodPost,
		nil, nil, nil, nil, nil, nil, []byte(`{"output":"fixture"}`), http.StatusOK, headers, nil)
	if strings.Contains(text, secret) {
		t.Fatal("legacy response log leaked a synthetic cookie credential")
	}
	if !strings.Contains(text, "Set-Cookie:") || !strings.Contains(text, "legacy-response-marker") {
		t.Fatal("legacy response log omitted expected headers")
	}
	if got := headers["Set-Cookie"][0]; got != "session="+secret+"; HttpOnly" {
		t.Fatalf("logging changed response header: %q", got)
	}
}
