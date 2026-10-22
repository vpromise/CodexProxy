package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type revocableServerAccessProvider struct {
	revoked  atomic.Bool
	mu       sync.Mutex
	requests []recordedAccessRequest
}

type recordedAccessRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

func (p *revocableServerAccessProvider) Identifier() string {
	return "revocable-test-provider"
}

func (p *revocableServerAccessProvider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	var body []byte
	if r != nil && r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	if r != nil {
		p.mu.Lock()
		p.requests = append(p.requests, recordedAccessRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    append([]byte(nil), body...),
		})
		p.mu.Unlock()
	}
	if r == nil || r.Header.Get("Authorization") != "Bearer key-1" {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	if p.revoked.Load() {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{
		Provider:  p.Identifier(),
		Principal: "key-1",
	}, nil
}

type serverResponsesWebsocketCaptureExecutor struct {
	calls atomic.Int32
}

func (e *serverResponsesWebsocketCaptureExecutor) Identifier() string {
	return "codex"
}

func (e *serverResponsesWebsocketCaptureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *serverResponsesWebsocketCaptureExecutor) ExecuteStream(_ context.Context, _ *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	n := e.calls.Add(1)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{
		Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","output":[]}}`, n)),
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *serverResponsesWebsocketCaptureExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *serverResponsesWebsocketCaptureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *serverResponsesWebsocketCaptureExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestSyncOctober23ServerResponsesWebsocketReauthorizesEachTurn(t *testing.T) {
	server := newTestServer(t)
	provider := &revocableServerAccessProvider{}
	server.accessManager.SetProviders([]sdkaccess.Provider{provider})

	wsExec := &serverResponsesWebsocketCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(wsExec)
	authEntry := &auth.Auth{
		ID:         "ws-reauth-auth",
		Provider:   "codex",
		Status:     auth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, errReg := server.handlers.AuthManager.Register(context.Background(), authEntry); errReg != nil {
		t.Fatalf("Register auth: %v", errReg)
	}
	modelName := "ws-reauth-model"
	registry.GetGlobalRegistry().RegisterClient(authEntry.ID, authEntry.Provider, []*registry.ModelInfo{{ID: modelName}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authEntry.ID)
	})

	httpServer := httptest.NewServer(server.engine)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/responses"
	headers := http.Header{"Authorization": []string{"Bearer key-1"}}
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, headers)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	turn1 := []byte(`{"type":"response.create","model":"ws-reauth-model","input":[{"type":"message","role":"user","content":"turn 1"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, turn1); errWrite != nil {
		t.Fatalf("write turn 1: %v", errWrite)
	}
	_, msg1, errRead1 := conn.ReadMessage()
	if errRead1 != nil {
		t.Fatalf("read turn 1: %v", errRead1)
	}
	if got := gjson.GetBytes(msg1, "type").String(); got != "response.completed" {
		t.Fatalf("turn 1 event type = %q, want response.completed (%s)", got, msg1)
	}

	provider.revoked.Store(true)

	// Control: fresh dial with the revoked key must fail at the handshake with 401.
	freshConn, resp, errFreshDial := websocket.DefaultDialer.Dial(wsURL, headers)
	if errFreshDial == nil {
		_ = freshConn.Close()
		t.Fatal("expected fresh dial with revoked key to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("fresh dial status = %d, want %d", status, http.StatusUnauthorized)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}

	// Second turn on the already-open socket must be rejected with an error event and not execute.
	turn2 := []byte(`{"type":"response.create","model":"ws-reauth-model","input":[{"type":"message","role":"user","content":"turn 2"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, turn2); errWrite != nil {
		t.Fatalf("write turn 2: %v", errWrite)
	}
	_, msg2, errRead2 := conn.ReadMessage()
	if errRead2 != nil {
		t.Fatalf("read turn 2: %v", errRead2)
	}
	if got := gjson.GetBytes(msg2, "type").String(); got != "error" {
		t.Fatalf("turn 2 event type = %q, want error (%s)", got, msg2)
	}
	if got := int(gjson.GetBytes(msg2, "status").Int()); got != http.StatusUnauthorized {
		t.Fatalf("turn 2 error status = %d, want %d (%s)", got, http.StatusUnauthorized, msg2)
	}
	if gotCalls := wsExec.calls.Load(); gotCalls != 1 {
		t.Fatalf("executor calls after revoked turn = %d, want 1", gotCalls)
	}
}
