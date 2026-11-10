package cliproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestPoolEgressServiceSwitchPreservesActiveStreamAndOtherPool(t *testing.T) {
	secondary := "127.0.0.2"
	secondaryAvailable := true
	probe, errProbe := net.Listen("tcp4", net.JoinHostPort(secondary, "0"))
	if errProbe != nil {
		secondary, secondaryAvailable = "203.0.113.250", false
	} else if errClose := probe.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	started, release := make(chan string, 1), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
			return
		}
		source, _, errSplit := net.SplitHostPort(r.RemoteAddr)
		if errSplit != nil {
			t.Error(errSplit)
			return
		}
		marker := fmt.Sprintf("source=%s;proxy=%t", source, r.Header.Get("X-Forwarded-For") != "")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_pool\",\"model\":\"gpt-5.4\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"synthetic answer\"}\n\n")
		w.(http.Flusher).Flush()
		if strings.Contains(string(payload), "hold-active") {
			started <- marker
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		response := map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "resp_pool", "status": "completed", "model": "gpt-5.4",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": marker}}}},
			"usage":  map[string]any{"input_tokens": 1, "output_tokens": 1},
		}}
		data, errMarshal := json.Marshal(response)
		if errMarshal != nil {
			t.Error(errMarshal)
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	}))
	t.Cleanup(upstream.Close)
	service := newAccessReloadService(t, upstream.URL)
	t.Cleanup(finish)
	status, body := service.request(t, http.MethodPut, "/v0/management/api-keys", "synthetic-management-key", `["synthetic-old-key","synthetic-pool-b"]`)
	if status != http.StatusOK {
		t.Fatalf("add second caller: %d %s", status, body)
	}
	service.waitForKey(t, "synthetic-pool-b", http.StatusOK)
	status, body = service.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
	identity := gjson.Get(body, "accounts.0.id").String()
	if status != http.StatusOK || identity == "" {
		t.Fatalf("native inventory: %d %s", status, body)
	}
	policy := config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{
			{ID: "a", Name: "A", Provider: "codex", Accounts: []string{identity}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}},
			{ID: "b", Name: "B", Provider: "codex", Accounts: []string{identity}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}},
		},
		Keys: []config.ClientKeyBinding{{Key: "synthetic-old-key", Pool: "a"}, {Key: "synthetic-pool-b", Pool: "b"}},
	}
	service.saveIsolationPolicy(t, policy)
	type result struct {
		status int
		body   string
		err    error
	}
	request := func(key, input string, stream bool, visible chan<- struct{}) result {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		payload, errMarshal := json.Marshal(map[string]any{"model": accessReloadModel, "input": input, "stream": stream})
		if errMarshal != nil {
			return result{err: errMarshal}
		}
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, service.url+"/v1/responses", strings.NewReader(string(payload)))
		if errRequest != nil {
			return result{err: errRequest}
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, errDo := service.client.Do(req)
		if errDo != nil {
			return result{err: errDo}
		}
		reader := bufio.NewReader(resp.Body)
		first := ""
		if visible != nil {
			first, _ = reader.ReadString('\n')
			close(visible)
		}
		data, errRead := io.ReadAll(reader)
		errClose := resp.Body.Close()
		if errRead == nil {
			errRead = errClose
		}
		return result{status: resp.StatusCode, body: first + string(data), err: errRead}
	}
	active, visible := make(chan result, 1), make(chan struct{})
	go func() { active <- request("synthetic-old-key", "hold-active", true, visible) }()
	select {
	case marker := <-started:
		if marker != "source=127.0.0.1;proxy=false" {
			t.Fatalf("active stream route = %s", marker)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active stream did not reach upstream")
	}
	select {
	case <-visible:
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not visible before policy change")
	}
	policy.Pools[0].Egress.SourceIP = secondary
	service.saveIsolationPolicy(t, policy)
	results := make(chan struct {
		pool string
		result
	}, 32)
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		for _, pool := range []string{"a", "b"} {
			group.Add(1)
			go func(pool string) {
				defer group.Done()
				key := "synthetic-old-key"
				if pool == "b" {
					key = "synthetic-pool-b"
				}
				results <- struct {
					pool string
					result
				}{pool, request(key, "new-operation", false, nil)}
			}(pool)
		}
	}
	group.Wait()
	close(results)
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.pool == "a" && !secondaryAvailable {
			if got.status != http.StatusServiceUnavailable {
				t.Fatalf("unavailable source returned %d: %s", got.status, got.body)
			}
			continue
		}
		expected := "127.0.0.1"
		if got.pool == "a" {
			expected = secondary
		}
		marker := "source=" + expected + ";proxy=false"
		if got.status != http.StatusOK || !strings.Contains(got.body, marker) {
			t.Fatalf("pool %s: %d %s, want %s", got.pool, got.status, got.body, marker)
		}
	}
	// Revocation affects new operations while the captured stream may still finish.
	policy.Pools[0].Accounts = nil
	service.saveIsolationPolicy(t, policy)
	if got := request("synthetic-old-key", "revoked-operation", false, nil); got.err != nil || got.status < 400 {
		t.Fatalf("revoked operation: %+v", got)
	}
	finish()
	select {
	case got := <-active:
		if got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "source=127.0.0.1;proxy=false") || !strings.Contains(got.body, `"status":"completed"`) {
			t.Fatalf("captured stream was interrupted: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("captured stream did not complete")
	}
	policy.Pools[0].Accounts = []string{identity}
	policy.Pools[0].Egress = &config.PoolEgress{Mode: "inherit"}
	service.saveIsolationPolicy(t, policy)
	if got := request("synthetic-old-key", "inherited-operation", false, nil); got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "source=127.0.0.1;proxy=true") {
		t.Fatalf("inherit did not restore global proxy: %+v", got)
	}
	if got := request("synthetic-pool-b", "unaffected-operation", false, nil); got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, "source=127.0.0.1;proxy=false") {
		t.Fatalf("other pool changed route: %+v", got)
	}
}
