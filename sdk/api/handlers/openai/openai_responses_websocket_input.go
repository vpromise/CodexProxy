package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type responsesWebsocketInput struct {
	kind    int
	payload []byte
}

// readResponsesWebsocketInput owns the single downstream reader. Native Codex
// interrupts bypass the bounded turn queue and never become response.create.
func readResponsesWebsocketInput(ctx context.Context, cancel context.CancelCauseFunc, conn *websocket.Conn, writer *responsesWebsocketWriter, interrupt func([]byte) error) <-chan responsesWebsocketInput {
	input := make(chan responsesWebsocketInput, 16)
	go func() {
		defer close(input)
		for {
			kind, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				cancel(errRead)
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			if json.Valid(payload) && gjson.GetBytes(payload, "type").String() == "response.interrupt" {
				if errInterrupt := interrupt(payload); errInterrupt != nil {
					if _, errWrite := writeResponsesWebsocketError(writer, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errInterrupt}); errWrite != nil {
						cancel(errWrite)
						return
					}
				}
				continue
			}
			select {
			case input <- responsesWebsocketInput{kind: kind, payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return input
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocketInterrupt(ctx context.Context, sessionID string, payload []byte) error {
	responseID := gjson.GetBytes(payload, "response_id")
	if responseID.Type != gjson.String || strings.TrimSpace(responseID.String()) == "" {
		return fmt.Errorf("response.interrupt requires response_id")
	}
	if h == nil || h.AuthManager == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	exec, ok := h.AuthManager.Executor("codex")
	if !ok || exec == nil {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	sender, ok := exec.(interface {
		InterruptExecutionSession(context.Context, string, []byte) error
	})
	if !ok {
		return cliproxyexecutor.ErrNoActiveUpstreamWebsocket
	}
	ctx = cliproxyexecutor.WithWebsocketAuthCheck(ctx, func(authID string) bool {
		auth, found := h.AuthManager.GetByID(authID)
		return found && auth != nil && !auth.Disabled && auth.Status != coreauth.StatusDisabled
	})
	return sender.InterruptExecutionSession(ctx, sessionID, payload)
}
