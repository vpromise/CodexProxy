package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamBootstrapError struct {
	cause   error
	headers http.Header
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	return &streamBootstrapError{
		cause:   err,
		headers: cloneHTTPHeader(headers),
	}
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

// streamDiagnosticError keeps a bootstrap rejection on the direct error path.
type streamDiagnosticError struct {
	error
	headers http.Header
}

func (e *streamDiagnosticError) Unwrap() error        { return e.error }
func (e *streamDiagnosticError) Headers() http.Header { return cloneHTTPHeader(e.headers) }

func withStreamDiagnosticHeaders(err error, headers http.Header) error {
	connectionScoped := make(map[string]bool)
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			connectionScoped[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}

	diagnostics := make(http.Header)
	for _, name := range []string{"Request-Id", "X-Request-Id", "X-Trace-Id"} {
		if values := headers.Values(name); len(values) > 0 && !connectionScoped[name] {
			diagnostics[name] = append([]string(nil), values...)
		}
	}
	if len(diagnostics) == 0 {
		return err
	}
	var carrier interface{ Headers() http.Header }
	if errors.As(err, &carrier) {
		for name, values := range carrier.Headers() {
			diagnostics[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return &streamDiagnosticError{error: err, headers: diagnostics}
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func validateStreamResult(result *cliproxyexecutor.StreamResult, err error) (*cliproxyexecutor.StreamResult, error) {
	if err != nil {
		return result, err
	}
	if result == nil || result.Chunks == nil {
		return result, &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
	}
	return result, nil
}

func takeReadyStreamChunk(ch <-chan cliproxyexecutor.StreamChunk) (cliproxyexecutor.StreamChunk, bool) {
	select {
	case chunk, open := <-ch:
		return chunk, open
	default:
		return cliproxyexecutor.StreamChunk{}, false
	}
}

func receiveStreamChunk(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) (cliproxyexecutor.StreamChunk, bool) {
	select {
	case chunk, open := <-ch:
		return chunk, open
	case <-ctx.Done():
		// Check the next ready chunk once so an observed upstream error
		// can still be recorded when the caller disconnects.
		return takeReadyStreamChunk(ch)
	}
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) (cliproxyexecutor.StreamChunk, error) {
	for {
		chunk, open := receiveStreamChunk(ctx, ch)
		if chunk.Err != nil {
			return cliproxyexecutor.StreamChunk{}, chunk.Err
		}
		if err := ctx.Err(); err != nil {
			return cliproxyexecutor.StreamChunk{}, err
		}
		if !open || len(chunk.Payload) > 0 {
			return chunk, nil
		}
	}
}

func (m *Manager) classifyStreamFailure(result Result, auth *Auth, err error) (Result, string, bool) {
	result.Error = resultErrorFromError(err)
	result.RetryAfter = retryAfterFromError(err)
	result.CredentialScope = isCredentialScopedError(err)
	action, matched := matchRequestScopedErrorAction(auth, err, m.runtimeConfigSnapshot())
	applyRequestScopedActionToResult(action, matched, &result)
	return result, action, matched
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, result Result, headers http.Header, first cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, ephemeralResult bool, finishAttempt func()) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	streamStart := time.Now()
	go func() {
		defer close(out)
		defer finishAttempt()
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		recordFailure := func(err error) {
			entry := logEntryWithRequestID(ctx)
			warnLogUpstreamFailure(ctx, entry, result.Provider, result.Model, auth, time.Since(streamStart), err)
			failure, _, _ := m.classifyStreamFailure(result, auth, err)
			m.recordExecutionResult(ctx, failure, auth, ephemeralResult)
		}
		recordCanceledChunk := func(chunk cliproxyexecutor.StreamChunk) {
			err := chunk.Err
			if statusCodeFromError(err) == http.StatusTooManyRequests || (errors.Is(err, context.Canceled) && isRequestScopedError(err)) {
				recordFailure(err)
			}
		}
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx.Err() != nil {
				return false
			}
			select {
			case <-ctx.Done():
				return false
			case out <- chunk:
				return true
			}
		}
		sendPayload := func(chunk cliproxyexecutor.StreamChunk) bool {
			chunk.Payload = rewriteForceMappedStreamChunk(rewriter, chunk.Payload)
			return len(chunk.Payload) == 0 || send(chunk)
		}

		if !sendPayload(first) {
			chunk, _ := takeReadyStreamChunk(remaining)
			recordCanceledChunk(chunk)
			return
		}
		for {
			chunk, open := receiveStreamChunk(ctx, remaining)
			if ctx.Err() != nil {
				recordCanceledChunk(chunk)
				return
			}
			if !open {
				break
			}
			if chunk.Err != nil {
				recordFailure(chunk.Err)
				if send(chunk) && ephemeralResult {
					// Home owns the selection until its source finishes or is canceled.
					for ctx.Err() == nil {
						if _, open := receiveStreamChunk(ctx, remaining); !open {
							break
						}
					}
				}
				return
			}
			if !sendPayload(chunk) {
				next, _ := takeReadyStreamChunk(remaining)
				recordCanceledChunk(next)
				return
			}
		}
		if tail := finishForceMappedStreamChunks(rewriter); len(tail) > 0 && !send(cliproxyexecutor.StreamChunk{Payload: tail}) {
			return
		}
		if ctx.Err() == nil {
			result.Success = true
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) replaceHomeExecutionLifecycleAuth(lifecycle cliproxyexecutor.ExecutionLifecycle, auth *Auth) {
	selection, ok := lifecycle.(*HomeDispatchSelection)
	if !ok || selection == nil {
		return
	}
	m.replaceHomeSelectionAuth(selection, auth)
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, routing *apiKeyModelRoutingSnapshot, allowRetry bool, ephemeralResult bool, unauthorizedRefreshTried map[string]struct{}, releaseAdmission func()) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	baseCtx := contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	didRefreshOnUnauthorized := false
	if auth != nil && unauthorizedRefreshTried != nil {
		_, didRefreshOnUnauthorized = unauthorizedRefreshTried[auth.ID]
	}
	for idx, execModel := range execModels {
		ctx := newUpstreamAttemptContext(baseCtx)
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		if executionModel != "" {
			execReq.Model = executionModel
		}
		execOpts := opts
		var errIntercept error
		execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(ctx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
		if errIntercept != nil {
			return nil, errIntercept
		}
		if executionModel == "" {
			execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, execModel)
		}
		entry := logEntryWithRequestID(ctx)
		var attemptStartedAt time.Time
		var cancelAttempt context.CancelFunc
		var allowedWarningObserved bool
		startStream := func() (*cliproxyexecutor.StreamResult, error) {
			attemptCtx, cancel := context.WithCancel(ctx)
			cancelAttempt = cancel
			attemptStartedAt = time.Now()
			if errCtx := attemptCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			return executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
		}
		readBootstrap := func(stream *cliproxyexecutor.StreamResult) (cliproxyexecutor.StreamChunk, error) {
			allowedWarningObserved = !ephemeralResult && ctx.Err() == nil &&
				logClaudeAllowedWarning(ctx, auth.Provider, resultModel, auth.Index, stream.Headers)
			return readStreamBootstrap(ctx, stream.Chunks)
		}
		baseResult := func() Result {
			return Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Options: execOpts, AttemptStartedAt: attemptStartedAt, SkipAllowedWarningObservation: allowedWarningObserved}
		}
		recordCanceledQuota := func(err error) {
			if statusCodeFromError(err) == http.StatusTooManyRequests {
				failure, _, _ := m.classifyStreamFailure(baseResult(), auth, err)
				m.recordExecutionResult(ctx, failure, auth, ephemeralResult)
			}
		}
		tryRefresh := func(err error) (bool, error) {
			if !allowRetry || ctx.Err() != nil || !isUnauthorizedError(err) {
				return false, nil
			}
			alreadyTried := didRefreshOnUnauthorized
			willAttemptHomeRefresh := ephemeralResult && !alreadyTried && auth != nil && auth.AuthKind() == AuthKindOAuth
			refreshCtx := newUpstreamAttemptContext(baseCtx)
			refreshed, okRefresh, errRefresh := m.tryRefreshExecutionAuthAfterUnauthorized(refreshCtx, executor, auth, err, alreadyTried, ephemeralResult)
			if willAttemptHomeRefresh {
				didRefreshOnUnauthorized = true
				if unauthorizedRefreshTried != nil {
					unauthorizedRefreshTried[auth.ID] = struct{}{}
				}
			}
			if errCtx := ctx.Err(); errCtx != nil {
				return false, errCtx
			}
			if errRefresh != nil || !okRefresh {
				return false, errRefresh
			}
			auth = refreshed
			m.replaceHomeExecutionLifecycleAuth(execOpts.ExecutionLifecycle, auth)
			publishSelectedAuthMetadata(execOpts.Metadata, auth)
			auditAccessDecision(ctx, auth, "selected", "credential_refresh_retry")
			didRefreshOnUnauthorized = true
			ctx = newUpstreamAttemptContext(baseCtx)
			return true, nil
		}

		streamResult, errStream := startStream()
		if errStream != nil {
			cancelAttempt()
			if errCtx := ctx.Err(); errCtx != nil {
				recordCanceledQuota(errStream)
				return nil, errCtx
			}
			retry, errRefresh := tryRefresh(errStream)
			if errRefresh != nil {
				errStream = errRefresh
			} else if retry {
				streamResult, errStream = startStream()
			}
			if errStream != nil {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(attemptStartedAt), errStream)
			}
		}
		streamResult, errStream = validateStreamResult(streamResult, errStream)
		if errStream != nil {
			cancelAttempt()
			if errCtx := ctx.Err(); errCtx != nil {
				recordCanceledQuota(errStream)
				return nil, errCtx
			}
			if !ephemeralResult {
				if errCancel := claudeOAuthRequestCancellation(ctx, auth, errStream); errCancel != nil {
					return nil, errCancel
				}
			}
			result, action, matched := m.classifyStreamFailure(baseResult(), auth, errStream)
			result.claudeAccessRejected = claudeauth.IsAccessTokenRejection(errStream)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if isRequestScopedStop(action, matched) {
				return nil, wrapRequestStopError(errStream)
			}
			if result.CredentialScope || (!matched && isRequestInvalidError(errStream)) {
				return nil, errStream
			}
			lastErr = errStream
			continue
		}

		first, bootstrapErr := readBootstrap(streamResult)
		if bootstrapErr != nil {
			cancelAttempt()
			if errCtx := ctx.Err(); errCtx != nil {
				recordCanceledQuota(bootstrapErr)
				return nil, errCtx
			}
			retry, errRefresh := tryRefresh(bootstrapErr)
			if errRefresh != nil {
				bootstrapErr = errRefresh
				streamResult = &cliproxyexecutor.StreamResult{}
			} else if retry {
				streamResult, bootstrapErr = startStream()
				streamResult, bootstrapErr = validateStreamResult(streamResult, bootstrapErr)
				if bootstrapErr != nil {
					streamResult = &cliproxyexecutor.StreamResult{}
				} else {
					first, bootstrapErr = readBootstrap(streamResult)
				}
			}
			if bootstrapErr != nil {
				warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(attemptStartedAt), bootstrapErr)
			}
		}
		if errCtx := ctx.Err(); errCtx != nil {
			cancelAttempt()
			recordCanceledQuota(bootstrapErr)
			return nil, errCtx
		}
		if bootstrapErr != nil {
			cancelAttempt()
			if !ephemeralResult {
				if errCancel := claudeOAuthRequestCancellation(ctx, auth, bootstrapErr); errCancel != nil {
					return nil, errCancel
				}
			}
			result, action, matched := m.classifyStreamFailure(baseResult(), auth, bootstrapErr)
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if isRequestScopedStop(action, matched) {
				return nil, wrapRequestStopError(bootstrapErr)
			}
			if !matched && isRequestInvalidError(bootstrapErr) {
				return nil, withStreamDiagnosticHeaders(bootstrapErr, streamResult.Headers)
			}
			if !result.CredentialScope && (matched || idx < len(execModels)-1) {
				lastErr = bootstrapErr
				continue
			}
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}
		if len(first.Payload) == 0 {
			cancelAttempt()
			emptyErr := &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			warnLogUpstreamFailure(ctx, entry, provider, execModel, auth, time.Since(attemptStartedAt), emptyErr)
			result := baseResult()
			result.Error = emptyErr
			m.recordExecutionResult(ctx, result, auth, ephemeralResult)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, execModel, aliasResult)
		finishAttempt := func() {
			cancelAttempt()
			releaseAdmission()
		}
		return m.wrapStreamResult(ctx, auth.Clone(), baseResult(), streamResult.Headers, first, streamResult.Chunks, attemptAliasResult, ephemeralResult, finishAttempt), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}
