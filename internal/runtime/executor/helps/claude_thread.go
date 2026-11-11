package helps

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeThreadContinues identifies requests whose prefix is owned by the server.
func ClaudeThreadContinues(body []byte) bool {
	thread := gjson.GetBytes(body, "thread")
	return thread.IsObject() && thread.Get("type").String() == "continue"
}

type claudeThreadRequestError string

func (e claudeThreadRequestError) Error() string       { return string(e) }
func (claudeThreadRequestError) StatusCode() int       { return http.StatusBadRequest }
func (claudeThreadRequestError) IsRequestScoped() bool { return true }

// ValidateClaudeThreadRequest checks endpoint and prefix constraints without
// changing the caller's history or introducing credential retry behavior.
func ValidateClaudeThreadRequest(body []byte, countTokens bool) error {
	thread := gjson.GetBytes(body, "thread")
	if countTokens && thread.Exists() && thread.Type != gjson.Null {
		return claudeThreadRequestError("invalid_request_error: count_tokens does not support thread; supply the full conversation without thread")
	}
	if !ClaudeThreadContinues(body) {
		return nil
	}
	if strings.TrimSpace(thread.Get("previous_message_id").String()) == "" {
		return claudeThreadRequestError("invalid_request_error: thread continuation requires previous_message_id")
	}
	if gjson.GetBytes(body, "system").Exists() || gjson.GetBytes(body, "tools").Exists() {
		return claudeThreadRequestError("invalid_request_error: thread continuation must omit system and tools; these are retained from the create request")
	}
	return nil
}
