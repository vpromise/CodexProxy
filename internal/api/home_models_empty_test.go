package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestDecodeHomeModelsEmptyCatalog(t *testing.T) {
	for _, raw := range []string{"{}", " { } ", `{"claude":[],"codex-pro":[]}`} {
		entries, errDecode := decodeHomeModels([]byte(raw))
		if errDecode != nil || entries == nil || len(entries) != 0 {
			t.Errorf("decodeHomeModels(%q) = %v, %v, want empty list", raw, entries, errDecode)
		}
	}
	for _, raw := range []string{
		"", "null", "[]", "{bad}", `{"claude":[{}]}`, `{"claude":null}`, `{"error":null}`,
		`{"error":{"type":"internal_error","message":"unavailable"}}`,
		`{"error":[]}`, `{"error":[],"claude":[]}`,
		`{"error":[{"id":"not-a-model"}]}`, `{"error":[],"claude":[{"id":"model-a"}]}`,
		`{"gemini":[{"id":"out-of-scope"}]}`, `{"claude":null,"codex":[{"id":"model-a"}]}`,
	} {
		if _, errDecode := decodeHomeModels([]byte(raw)); errDecode == nil {
			t.Errorf("decodeHomeModels(%q) accepted malformed or error payload", raw)
		}
	}
}

func TestHomeEmptyModelCatalogResponses(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	client := newHomeCatalogClient(t, "{}")
	previousHome := home.Current()
	home.SetCurrent(client)
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	registry.GetGlobalRegistry().RegisterClient("empty-catalog-local", "codex", []*registry.ModelInfo{{ID: "local-only-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("empty-catalog-local") })

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		field   string
	}{
		{name: "openai", path: "/v1/models", field: "data"},
		{name: "claude", path: "/v1/models", headers: map[string]string{"Anthropic-Version": "2023-06-01"}, field: "data"},
		{name: "codex", path: "/v1/models?client_version=0.154.0", field: "models"},
		{name: "cpa", path: "/v1/models?client_version=cpa", field: "models"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", "Bearer scoped-key")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
			var payload map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if string(payload[test.field]) != "[]" {
				t.Fatalf("%s = %s, want []; body=%s", test.field, payload[test.field], recorder.Body.String())
			}
		})
	}

}

func TestHomeModelCatalogErrorsRemainErrors(t *testing.T) {
	server := newTestServer(t)
	server.cfg.Home.Enabled = true
	engine := gin.New()
	engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
	for _, test := range []struct {
		name    string
		payload string
		status  int
	}{
		{name: "missing credentials", payload: `{"error":{"type":"no_credentials","message":"Missing API key"}}`, status: http.StatusUnauthorized},
		{name: "invalid credentials", payload: `{"error":{"type":"invalid_credential","message":"Invalid API key"}}`, status: http.StatusUnauthorized},
		{name: "policy failure", payload: `{"error":{"type":"internal_error","message":"Model access service error"}}`, status: http.StatusBadGateway},
		{name: "empty array error", payload: `{"error":[]}`, status: http.StatusBadGateway},
		{name: "error alongside empty section", payload: `{"error":[],"claude":[]}`, status: http.StatusBadGateway},
		{name: "error containing model-shaped records", payload: `{"error":[{"id":"not-a-model"}]}`, status: http.StatusBadGateway},
		{name: "error alongside models", payload: `{"error":[],"claude":[{"id":"model-a"}]}`, status: http.StatusBadGateway},
		{name: "empty response", payload: "", status: http.StatusBadGateway},
		{name: "malformed JSON", payload: "{bad}", status: http.StatusBadGateway},
		{name: "null catalog", payload: "null", status: http.StatusBadGateway},
		{name: "null section", payload: `{"claude":null}`, status: http.StatusBadGateway},
		{name: "missing model IDs", payload: `{"claude":[{}]}`, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newHomeCatalogClient(t, test.payload)
			previousHome := home.Current()
			home.SetCurrent(client)
			t.Cleanup(func() { home.SetCurrent(previousHome) })
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			var payload map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if _, exists := payload["error"]; !exists {
				t.Fatalf("expected error response; body=%s", recorder.Body.String())
			}
		})
	}
}

func newHomeCatalogClient(t *testing.T, payload string) *home.Client {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	done := make(chan struct{})
	var connections sync.WaitGroup
	go func() {
		defer close(done)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Errorf("close Home test connection: %v", errClose)
					}
				}()
				reader := bufio.NewReader(conn)
				for {
					args, errReadRESPArrayOfBulkStrings := readRESPArrayOfBulkStrings(reader)
					if errReadRESPArrayOfBulkStrings != nil || len(args) == 0 {
						return
					}
					response := "+OK\r\n"
					switch strings.ToUpper(string(args[0])) {
					case "HELLO":
						response = "-ERR unknown command 'hello'\r\n"
					case "GET":
						var request struct {
							Type string `json:"type"`
						}
						if len(args) != 2 {
							t.Error("invalid Home GET request")
							return
						}
						if errUnmarshal := json.Unmarshal(args[1], &request); errUnmarshal != nil || request.Type != "models" {
							t.Errorf("unexpected Home request %q: %v", args[1], errUnmarshal)
							return
						}
						response = fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
					}
					if _, errWriteString := io.WriteString(conn, response); errWriteString != nil {
						return
					}
				}
			}()
		}
	}()
	host, portText, errSplitHostPort := net.SplitHostPort(listener.Addr().String())
	if errSplitHostPort != nil {
		t.Fatalf("split Home test address: %v", errSplitHostPort)
	}
	port, errAtoi := strconv.Atoi(portText)
	if errAtoi != nil {
		t.Fatalf("parse Home test port: %v", errAtoi)
	}
	client := home.New(proxyconfig.HomeConfig{Enabled: true, Host: host, Port: port, DisableClusterDiscovery: true})
	t.Cleanup(func() {
		client.Close()
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("close Home test listener: %v", errClose)
		}
		<-done
		connections.Wait()
	})
	return client
}
