package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeExecutorFastHTTPErrorPassesThroughWithoutRetry(t *testing.T) {
	testCases := []struct {
		name       string
		status     int
		stream     bool
		oauth      bool
		compressed bool
		betaOnly   bool
		errorType  string
		credential bool
		request    bool
	}{
		{name: "non-stream OAuth bad request", status: http.StatusBadRequest, oauth: true, request: true},
		{name: "stream OAuth unauthorized", status: http.StatusUnauthorized, stream: true, oauth: true, credential: true},
		{name: "non-stream payment required", status: http.StatusPaymentRequired, oauth: true, credential: true},
		{name: "non-stream API key forbidden", status: http.StatusForbidden, credential: true},
		{name: "explicit authentication error", status: http.StatusBadRequest, oauth: true, errorType: "authentication_error", credential: true},
		{name: "stream OAuth credits refusal", status: http.StatusTooManyRequests, stream: true, oauth: true, compressed: true, request: true},
		{name: "non-stream OAuth server error", status: http.StatusInternalServerError, oauth: true},
		{name: "stream OAuth beta-only Fast refusal", status: http.StatusServiceUnavailable, stream: true, oauth: true, betaOnly: true},
		{name: "upstream overloaded", status: 529, oauth: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var attempts atomic.Int32
			errorType := testCase.errorType
			if errorType == "" {
				errorType = "upstream_error"
			}
			errorJSON := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":"Fast request rejected"}}`, errorType)
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts.Add(1)
				requestBody, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if !testCase.betaOnly && !bytes.Contains(requestBody, []byte(`"speed":"fast"`)) {
					t.Fatalf("upstream request does not contain speed=fast: %s", requestBody)
				}
				if testCase.betaOnly && bytes.Contains(requestBody, []byte(`"speed"`)) {
					t.Fatalf("beta-only Fast request unexpectedly gained speed: %s", requestBody)
				}
				var wireBetas string
				for name, values := range req.Header {
					if strings.EqualFold(name, "Anthropic-Beta") {
						wireBetas = strings.Join(values, ",")
						break
					}
				}
				if !strings.Contains(wireBetas, claudeFastModeBeta) {
					t.Fatalf("upstream request is missing %s", claudeFastModeBeta)
				}

				body := []byte(errorJSON)
				headers := http.Header{"Content-Type": []string{"application/json"}}
				if testCase.compressed {
					var compressed bytes.Buffer
					writer := gzip.NewWriter(&compressed)
					if _, errWrite := writer.Write(body); errWrite != nil {
						t.Fatal(errWrite)
					}
					if errClose := writer.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					body = compressed.Bytes()
					headers.Set("Content-Encoding", "gzip")
				}
				return &http.Response{
					StatusCode: testCase.status,
					Header:     headers,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Request:    req,
				}, nil
			})

			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{ID: "fast-error-test", Metadata: claudeOAuthTestMetadata()}
			if testCase.oauth {
				auth.Attributes = map[string]string{"api_key": "sk-ant-oat-fast-error"}
			} else {
				auth.Attributes = map[string]string{"api_key": "sk-ant-api03-fast-error"}
				auth.Metadata = nil
			}
			requestPayload := []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"reply OK"}]}`)
			options := cliproxyexecutor.Options{
				Stream:         testCase.stream,
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			}
			if testCase.betaOnly {
				requestPayload = []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"reply OK"}]}`)
				options.Headers = http.Header{"Anthropic-Beta": []string{claudeFastModeBeta}}
			}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: requestPayload}

			executor := NewClaudeExecutor(&config.Config{})
			var errExecute error
			if testCase.stream {
				_, errExecute = executor.ExecuteStream(ctx, auth, request, options)
			} else {
				_, errExecute = executor.Execute(ctx, auth, request, options)
			}
			if errExecute == nil {
				t.Fatal("Fast request error = nil")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("upstream attempts = %d, want 1", got)
			}
			var direct *cliproxyexecutor.RequestTerminatedError
			if !errors.As(errExecute, &direct) || direct == nil {
				t.Fatalf("error = %T %v, want direct response", errExecute, errExecute)
			}
			if got := direct.StatusCode(); got != testCase.status {
				t.Fatalf("direct status = %d, want %d", got, testCase.status)
			}
			if got := string(direct.ResponseBody()); got != errorJSON {
				t.Fatalf("direct body = %q, want %q", got, errorJSON)
			}
			if got := direct.ResponseHeaders().Get("Content-Encoding"); got != "" {
				t.Fatalf("direct Content-Encoding = %q, want absent after decode", got)
			}
			var credentialScoped interface{ IsCredentialScoped() bool }
			gotCredential := errors.As(errExecute, &credentialScoped) && credentialScoped.IsCredentialScoped()
			if gotCredential != testCase.credential {
				t.Fatalf("Fast direct response error = %T, credential scoped = %v, want %v", errExecute, gotCredential, testCase.credential)
			}
			var requestScoped cliproxyexecutor.RequestScopedError
			gotRequest := errors.As(errExecute, &requestScoped) && requestScoped.IsRequestScoped()
			if gotRequest != testCase.request {
				t.Fatalf("Fast direct response error = %T, request scoped = %v, want %v", errExecute, gotRequest, testCase.request)
			}
		})
	}
}

func TestClaudeFastRequestErrorInheritsCredentialStatusFromSSECause(t *testing.T) {
	cause := claudeCredentialError{statusErr: statusErr{
		code: http.StatusUnauthorized,
		msg:  `{"type":"error","error":{"type":"authentication_error","message":"expired"}}`,
	}}
	for _, outerStatus := range []int{0, http.StatusOK} {
		errWrapped := wrapClaudeFastRequestError(true, outerStatus, cause)
		var statusProvider interface{ StatusCode() int }
		if !errors.As(errWrapped, &statusProvider) || statusProvider.StatusCode() != http.StatusUnauthorized {
			t.Fatalf("outer status %d: wrapped error = %T %v, want inherited status 401", outerStatus, errWrapped, errWrapped)
		}
		var credentialProvider interface{ IsCredentialScoped() bool }
		if !errors.As(errWrapped, &credentialProvider) || !credentialProvider.IsCredentialScoped() {
			t.Fatalf("outer status %d: wrapped error = %T %v, want credential scope", outerStatus, errWrapped, errWrapped)
		}
		var requestProvider cliproxyexecutor.RequestScopedError
		if errors.As(errWrapped, &requestProvider) && requestProvider.IsRequestScoped() {
			t.Fatalf("outer status %d: wrapped error = %T %v, must not be request scoped", outerStatus, errWrapped, errWrapped)
		}
	}
}

func TestClaudeExecutorFastSuccessfulHTTPDecodeErrorDoesNotExposeSuccessStatus(t *testing.T) {
	testCases := []struct {
		name string
		run  func(context.Context, *ClaudeExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := executor.Execute(ctx, auth, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				return errStream
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var attempts atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				attempts.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{"gzip"}},
					Body:       io.NopCloser(strings.NewReader("not-a-gzip-stream")),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{
				ID:         "fast-success-decode-error",
				Attributes: map[string]string{"api_key": "sk-ant-oat-fast-success-decode-error"},
				Metadata:   claudeOAuthTestMetadata(),
			}
			request := cliproxyexecutor.Request{
				Model:   "claude-opus-5",
				Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"reply OK"}]}`),
			}
			errRun := testCase.run(ctx, NewClaudeExecutor(&config.Config{}), auth, request, cliproxyexecutor.Options{
				Stream:         testCase.name == "stream",
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			})
			if errRun == nil {
				t.Fatal("Fast decode error = nil")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("upstream attempts = %d, want 1", got)
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if errors.As(errRun, &requestErr) && requestErr != nil && requestErr.IsRequestScoped() {
				t.Fatalf("Fast decode error = %T %v, must allow credential failover", errRun, errRun)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errRun, &statusErr) || statusErr == nil {
				t.Fatalf("Fast decode error = %T %v, want status provider", errRun, errRun)
			}
			if got := statusErr.StatusCode(); got != 0 {
				t.Fatalf("Fast decode status = %d, want 0 instead of upstream success", got)
			}
		})
	}
}

func TestClaudeExecutorFastTransportErrorAllowsCredentialFailover(t *testing.T) {
	upstreamErr := errors.New("transport unavailable")
	var attempts atomic.Int32
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, upstreamErr
	})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "fast-transport-error",
		Attributes: map[string]string{"api_key": "sk-ant-oat-fast-transport"},
		Metadata:   claudeOAuthTestMetadata(),
	}
	request := cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"reply OK"}]}`),
	}

	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, request, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if !errors.Is(errExecute, upstreamErr) {
		t.Fatalf("error = %v, want wrapped transport error", errExecute)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("upstream attempts = %d, want 1", got)
	}
	var requestScoped cliproxyexecutor.RequestScopedError
	if errors.As(errExecute, &requestScoped) && requestScoped.IsRequestScoped() {
		t.Fatalf("Fast transport error = %T, must allow credential failover", errExecute)
	}
}

type claudeFastRoundTripperProvider map[string]http.RoundTripper

func (p claudeFastRoundTripperProvider) RoundTripperFor(auth *cliproxyauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	return p[auth.ID]
}

func TestClaudeExecutorFastInfrastructureFailuresRotateCredentials(t *testing.T) {
	tests := []struct {
		name      string
		firstCall func(*http.Request) (*http.Response, error)
	}{
		{
			name: "transport error",
			firstCall: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport unavailable")
			},
		},
		{
			name: "server error",
			firstCall: func(req *http.Request) (*http.Response, error) {
				return claudeFastTestErrorResponse(req, http.StatusInternalServerError), nil
			},
		},
		{
			name: "overloaded",
			firstCall: func(req *http.Request) (*http.Response, error) {
				return claudeFastTestErrorResponse(req, 529), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var firstAttempts atomic.Int32
			var secondAttempts atomic.Int32
			firstTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				firstAttempts.Add(1)
				return test.firstCall(req)
			})
			secondTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				secondAttempts.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"id":"msg_fast","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
					Request:    req,
				}, nil
			})

			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
			baseID := uuid.NewString()
			firstAuth := &cliproxyauth.Auth{ID: baseID + "-a", Provider: "claude", Attributes: map[string]string{"api_key": "first-key"}}
			secondAuth := &cliproxyauth.Auth{ID: baseID + "-b", Provider: "claude", Attributes: map[string]string{"api_key": "second-key"}}
			manager.SetRoundTripperProvider(claudeFastRoundTripperProvider{
				firstAuth.ID:  firstTransport,
				secondAuth.ID: secondTransport,
			})
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(firstAuth.ID, firstAuth.Provider, []*registry.ModelInfo{{ID: "claude-opus-5"}})
			modelRegistry.RegisterClient(secondAuth.ID, secondAuth.Provider, []*registry.ModelInfo{{ID: "claude-opus-5"}})
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(firstAuth.ID)
				modelRegistry.UnregisterClient(secondAuth.ID)
			})
			if _, errRegister := manager.Register(context.Background(), firstAuth); errRegister != nil {
				t.Fatalf("register first auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), secondAuth); errRegister != nil {
				t.Fatalf("register second auth: %v", errRegister)
			}

			payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"reply OK"}]}`)
			response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
				Model:   "claude-opus-5",
				Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if firstAttempts.Load() != 1 || secondAttempts.Load() != 1 {
				t.Fatalf("attempts = first:%d second:%d, want 1 each", firstAttempts.Load(), secondAttempts.Load())
			}
			if !bytes.Contains(response.Payload, []byte(`"text":"ok"`)) {
				t.Fatalf("failover response = %s", response.Payload)
			}
		})
	}
}

func claudeFastTestErrorResponse(req *http.Request, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"upstream failed"}}`)),
		Request:    req,
	}
}

func TestClaudeExecutorNonFastErrorKeepsCredentialScopedBehavior(t *testing.T) {
	var attempts atomic.Int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:         "standard-rate-limit",
		Attributes: map[string]string{"api_key": "sk-ant-oat-standard-rate-limit"},
		Metadata:   claudeOAuthTestMetadata(),
	}
	request := cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"reply OK"}]}`),
	}

	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, request, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	var statusError interface{ StatusCode() int }
	if !errors.As(errExecute, &statusError) || statusError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want status 429", errExecute)
	}
	var direct *cliproxyexecutor.RequestTerminatedError
	if errors.As(errExecute, &direct) {
		t.Fatal("non-Fast error unexpectedly became a direct response")
	}
	if requestScoped, ok := errExecute.(cliproxyexecutor.RequestScopedError); ok && requestScoped.IsRequestScoped() {
		t.Fatal("non-Fast rate limit unexpectedly became request-scoped")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("upstream attempts = %d, want 1", got)
	}
}
