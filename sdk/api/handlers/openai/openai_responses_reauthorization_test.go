package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type revocableWebsocketAccessProvider struct {
	revoked      atomic.Bool
	deniedModel  string
	mu           sync.Mutex
	seenRequests []*http.Request
	seenBodies   [][]byte
}

func (*revocableWebsocketAccessProvider) Identifier() string { return "revocable-ws" }

func (p *revocableWebsocketAccessProvider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	var body []byte
	if r != nil && r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	if r != nil {
		cloned := r.Clone(r.Context())
		p.mu.Lock()
		p.seenRequests = append(p.seenRequests, cloned)
		p.seenBodies = append(p.seenBodies, bytes.Clone(body))
		p.mu.Unlock()
	}
	if r == nil || r.Header.Get("Authorization") != "Bearer key-1" {
		return nil, sdkaccess.NewNoCredentialsError()
	}
	if p.revoked.Load() {
		return nil, &sdkaccess.AuthError{Code: "denied", Message: "key revoked", StatusCode: http.StatusForbidden, Cause: errors.New("private provider cause")}
	}
	p.mu.Lock()
	deniedModel := p.deniedModel
	p.mu.Unlock()
	if deniedModel != "" && strings.TrimSpace(gjson.GetBytes(body, "model").String()) == deniedModel {
		return nil, &sdkaccess.AuthError{Code: "denied", Message: "quota exhausted for model", StatusCode: http.StatusTooManyRequests}
	}
	return &sdkaccess.Result{Principal: "key-1", Provider: p.Identifier()}, nil
}

func TestSyncOctober23WebsocketReauthorizesEachTurnAndKeepsSocketOpen(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketDirectCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:         "auth-ws-reauth",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "test-model"},
		{ID: "quota-blocked-model"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	provider := &revocableWebsocketAccessProvider{deniedModel: "quota-blocked-model"}
	access := sdkaccess.NewManager()
	access.SetProviders([]sdkaccess.Provider{provider})

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	if field := reflect.ValueOf(h).Elem().FieldByName("AccessManager"); field.IsValid() {
		field.Set(reflect.ValueOf(access))
	}
	router := gin.New()
	router.GET("/v1/responses", func(c *gin.Context) {
		if _, authErr := access.Authenticate(c.Request.Context(), c.Request); authErr != nil {
			c.AbortWithStatusJSON(authErr.HTTPStatusCode(), gin.H{"error": authErr.Message})
			return
		}
		c.Next()
	}, h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	hdr := http.Header{
		"Authorization": []string{"Bearer key-1"},
		"X-Custom-Auth": []string{"preserve-me"},
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	sendTurn := func(payload string) []byte {
		t.Helper()
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
			t.Fatalf("write: %v", errWrite)
		}
		_, msg, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read: %v", errRead)
		}
		return msg
	}

	turn1Payload := `{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"hi"}]}`
	msg1 := sendTurn(turn1Payload)
	if got := gjson.GetBytes(msg1, "type").String(); got != "response.completed" {
		t.Fatalf("turn 1 type = %q, want response.completed (%s)", got, msg1)
	}

	// Deny the inherited model ("test-model") and send continuation turns with empty/whitespace model:
	// per-turn auth must resolve the inherited model and reject both turns.
	provider.mu.Lock()
	provider.deniedModel = "test-model"
	provider.mu.Unlock()
	for _, rawContinuation := range []string{
		`{"type":"response.create","model":"","input":[{"type":"message","role":"user","content":"empty model"}]}`,
		`{"type":"response.create","model":"   ","input":[{"type":"message","role":"user","content":"blank model"}]}`,
		`{"type":"response.create","input":[{"type":"message","role":"user","content":"missing model"}]}`,
	} {
		msgDeniedInherited := sendTurn(rawContinuation)
		if got := gjson.GetBytes(msgDeniedInherited, "type").String(); got != "error" {
			t.Fatalf("inherited model turn (%s) type = %q, want error (%s)", rawContinuation, got, msgDeniedInherited)
		}
		if got := int(gjson.GetBytes(msgDeniedInherited, "status").Int()); got != http.StatusTooManyRequests {
			t.Fatalf("inherited model turn (%s) status = %d, want %d (%s)", rawContinuation, got, http.StatusTooManyRequests, msgDeniedInherited)
		}
		if got := len(executor.Payloads()); got != 1 {
			t.Fatalf("executor payloads after denied inherited model turn = %d, want 1", got)
		}
	}
	provider.mu.Lock()
	provider.deniedModel = "quota-blocked-model"
	provider.mu.Unlock()

	provider.revoked.Store(true)
	if _, resp, errDial := websocket.DefaultDialer.Dial(wsURL, hdr); errDial == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("fresh handshake after revoke: err=%v resp=%v, want 403", errDial, resp)
	}

	// Revoked key on already open socket must fail with 403 error event and not execute.
	msg2 := sendTurn(turn1Payload)
	if got := gjson.GetBytes(msg2, "type").String(); got != "error" {
		t.Fatalf("turn 2 after revoke type = %q, want error (%s)", got, msg2)
	}
	if got := int(gjson.GetBytes(msg2, "status").Int()); got != http.StatusForbidden {
		t.Fatalf("turn 2 status = %d, want %d (%s)", got, http.StatusForbidden, msg2)
	}
	if got := gjson.GetBytes(msg2, "error.message").String(); got != "key revoked" {
		t.Fatalf("turn 2 error.message = %q, want %q (%s)", got, "key revoked", msg2)
	}
	if got := len(executor.Payloads()); got != 1 {
		t.Fatalf("executor payloads after revoked turn = %d, want 1", got)
	}

	// Un-revoke key, then send a turn for a model denied with 429: must return 429 error event and keep socket open.
	provider.revoked.Store(false)
	msg3 := sendTurn(`{"type":"response.create","model":"quota-blocked-model","input":[{"type":"message","role":"user","content":"blocked"}]}`)
	if got := gjson.GetBytes(msg3, "type").String(); got != "error" {
		t.Fatalf("turn 3 model-denied type = %q, want error (%s)", got, msg3)
	}
	if got := int(gjson.GetBytes(msg3, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("turn 3 status = %d, want %d (%s)", got, http.StatusTooManyRequests, msg3)
	}
	if got := gjson.GetBytes(msg3, "error.message").String(); got != "quota exhausted for model" {
		t.Fatalf("turn 3 error.message = %q, want %q (%s)", got, "quota exhausted for model", msg3)
	}
	if got := len(executor.Payloads()); got != 1 {
		t.Fatalf("executor payloads after model-denied turn = %d, want 1", got)
	}

	// Subsequent turn on the same open socket with allowed model succeeds.
	msg4 := sendTurn(`{"type":"response.create","model":"test-model","input":[{"type":"message","role":"user","content":"allowed again"}]}`)
	if got := gjson.GetBytes(msg4, "type").String(); got != "response.completed" {
		t.Fatalf("turn 4 type = %q, want response.completed (%s)", got, msg4)
	}
	if got := len(executor.Payloads()); got != 2 {
		t.Fatalf("executor payloads after allowed turn = %d, want 2", got)
	}

	// Verify per-turn request shape seen by Authenticate (index 1 is turn 1, after handshake at index 0).
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.seenRequests) < 2 {
		t.Fatalf("seenRequests len = %d, want >= 2", len(provider.seenRequests))
	}
	turnReq := provider.seenRequests[1]
	turnBody := provider.seenBodies[1]
	if turnReq.Method != http.MethodPost {
		t.Fatalf("per-turn auth request method = %q, want POST", turnReq.Method)
	}
	if turnReq.URL.Path != "/v1/responses" {
		t.Fatalf("per-turn auth request path = %q, want /v1/responses", turnReq.URL.Path)
	}
	if string(turnBody) != turn1Payload {
		t.Fatalf("per-turn auth request body = %s, want %s", turnBody, turn1Payload)
	}
	if got := turnReq.Header.Get("X-Custom-Auth"); got != "preserve-me" {
		t.Fatalf("per-turn auth request X-Custom-Auth = %q, want preserve-me", got)
	}
	for _, dropped := range []string{"Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol", "Content-Length"} {
		if val := turnReq.Header.Get(dropped); val != "" {
			t.Fatalf("per-turn auth request kept handshake header %s = %q", dropped, val)
		}
	}
}
