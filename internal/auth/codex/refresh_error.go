package codex

import (
	"encoding/json"
	"fmt"
	"strings"
)

type refreshHTTPError struct {
	status    int
	message   string
	oauthCode string
}

func (e *refreshHTTPError) Error() string {
	return fmt.Sprintf("token refresh failed with status %d: %s", e.status, e.message)
}

func (e *refreshHTTPError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func newRefreshHTTPError(status int, body []byte) *refreshHTTPError {
	errRefresh := &refreshHTTPError{status: status, message: string(body)}
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return errRefresh
	}
	var code string
	if json.Unmarshal(payload.Error, &code) != nil {
		var nested struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(payload.Error, &nested) == nil {
			code = nested.Code
		}
	}
	errRefresh.oauthCode = strings.ToLower(strings.TrimSpace(code))
	return errRefresh
}
