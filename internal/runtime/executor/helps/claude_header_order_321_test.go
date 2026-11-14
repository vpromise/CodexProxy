package helps

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
)

func TestClaudeCode321MeasuredHeaderOrder(t *testing.T) {
	// Recorded from macOS arm64 Claude Code 2.1.321, including a tool-result
	// continuation. The API-key sample differs only at the authentication slot.
	const beforeAuth = "Accept"
	const common = "Content-Type,User-Agent,X-Claude-Code-Session-Id,X-Stainless-Arch,X-Stainless-Lang,X-Stainless-OS,X-Stainless-Package-Version,X-Stainless-Retry-Count,X-Stainless-Runtime,X-Stainless-Runtime-Version,X-Stainless-Timeout,anthropic-beta,anthropic-dangerous-direct-browser-access,anthropic-version"
	const suffix = "x-app,x-claude-code-prev-tool-durations,x-claude-code-prompt-id,x-claude-code-request-class,x-claude-code-sandy-plover,x-client-request-id,Connection,Host,Accept-Encoding,Content-Length"
	for _, apiKey := range []bool{false, true} {
		for _, count := range []bool{false, true} {
			name, path := "bearer", "/v1/messages"
			ordered := beforeAuth + ",Authorization," + common + "," + suffix
			if apiKey {
				name = "api-key"
				ordered = beforeAuth + "," + common + ",x-api-key," + suffix
			}
			if count {
				name += "/count"
				path += "/count_tokens"
				ordered = strings.ReplaceAll(ordered, ",X-Stainless-Timeout", "")
			}
			t.Run(name, func(t *testing.T) {
				want := strings.Split(ordered, ",")
				req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com"+path, strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range want {
					if key != "Host" && key != "Content-Length" {
						req.Header[key] = []string{"fixture"}
					}
				}
				left, right := net.Pipe()
				done := make(chan error, 1)
				go func() {
					done <- req.Write(httpwire.NewOrderedRequestConn(left, claudeCodeRequestHeaderOrder))
					left.Close()
				}()
				wire, err := io.ReadAll(right)
				right.Close()
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				header, body, _ := bytes.Cut(wire, []byte("\r\n\r\n"))
				var actual []string
				for _, line := range strings.Split(string(header), "\r\n")[1:] {
					key, _, _ := strings.Cut(line, ":")
					actual = append(actual, key)
				}
				if strings.Join(actual, ",") != ordered {
					t.Errorf("wire order = %v, want %v", actual, want)
				}
				if string(body) != "{}" {
					t.Fatalf("changed body bytes %q", body)
				}
			})
		}
	}
}
