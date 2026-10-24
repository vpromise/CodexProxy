package pluginhost

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHostHTTPClientStripsCredentialHeadersOnCrossOriginRedirect(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		originHost      string
		destinationHost string
	}{
		{name: "same hostname different port", originHost: "127.0.0.1", destinationHost: "127.0.0.1"},
		{name: "different hostname", originHost: "localhost", destinationHost: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(destination.Close)
			destinationURL := strings.Replace(destination.URL, "127.0.0.1", tc.destinationHost, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destinationURL, http.StatusFound)
			}))
			t.Cleanup(origin.Close)
			originURL := strings.Replace(origin.URL, "127.0.0.1", tc.originHost, 1)

			response, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
				URL: originURL,
				Headers: http.Header{
					"X-Api-Key":           []string{"secret-a"},
					"X-Goog-Api-Key":      []string{"secret-b"},
					"Authorization":       []string{"Bearer secret-c"},
					"Proxy-Authorization": []string{"Basic secret-d"},
					"Cookie":              []string{"session=secret-e"},
					"X-Request-Trace":     []string{"trace-1"},
				},
			})
			if errDo != nil {
				t.Fatalf("host HTTP redirect request error = %v", errDo)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
			}
			for _, header := range []string{"X-Api-Key", "X-Goog-Api-Key", "Authorization", "Proxy-Authorization", "Cookie"} {
				if value := got.Get(header); value != "" {
					t.Errorf("destination received %s = %q, want credential stripped", header, value)
				}
			}
			if value := got.Get("X-Request-Trace"); value != "trace-1" {
				t.Errorf("destination X-Request-Trace = %q, want trace-1", value)
			}
		})
	}
}

func TestHostHTTPClientStripsNonCanonicalCredentialHeaderKeysOnRedirect(t *testing.T) {
	t.Parallel()

	var got http.Header
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)
	destinationURL := strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destinationURL, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	response, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
		URL: origin.URL,
		Headers: http.Header{
			"authorization": []string{"Bearer secret-a"},
			"x-api-key":     []string{"secret-b"},
			"x-request-id":  []string{"trace-1"},
		},
	})
	if errDo != nil {
		t.Fatalf("host HTTP redirect request error = %v", errDo)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	for name, values := range got {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" {
			t.Errorf("destination received %s = %v, want credential stripped", name, values)
		}
	}
	if value := got.Get("X-Request-Id"); value != "trace-1" {
		t.Errorf("destination X-Request-Id = %q, want trace-1", value)
	}
}

func TestHostHTTPClientKeepsCredentialsStrippedAcrossRedirectChain(t *testing.T) {
	t.Parallel()

	t.Run("cross-origin then same-origin hop", func(t *testing.T) {
		var final http.Header
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/hop" {
				http.Redirect(w, r, "/final", http.StatusFound)
				return
			}
			final = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(destination.Close)
		destinationURL := strings.Replace(destination.URL, "127.0.0.1", "localhost", 1) + "/hop"
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destinationURL, http.StatusFound)
		}))
		t.Cleanup(origin.Close)

		response, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
			URL: origin.URL,
			Headers: http.Header{
				"X-Api-Key":     []string{"secret-a"},
				"Authorization": []string{"Bearer secret-b"},
				"X-Trace":       []string{"trace-1"},
			},
		})
		if errDo != nil {
			t.Fatalf("host HTTP redirect request error = %v", errDo)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		for _, header := range []string{"X-Api-Key", "Authorization"} {
			if value := final.Get(header); value != "" {
				t.Errorf("final hop received %s = %q after a cross-origin redirect, want stripped", header, value)
			}
		}
		if value := final.Get("X-Trace"); value != "trace-1" {
			t.Errorf("final hop X-Trace = %q, want trace-1", value)
		}
	})

	t.Run("cross-origin bounce back to initial origin keeps credentials stripped", func(t *testing.T) {
		var final http.Header
		var originURL string
		bounce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, originURL+"/final", http.StatusFound)
		}))
		t.Cleanup(bounce.Close)
		bounceURL := strings.Replace(bounce.URL, "127.0.0.1", "localhost", 1)

		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, bounceURL, http.StatusFound)
				return
			}
			final = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(origin.Close)
		originURL = origin.URL

		response, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
			URL: origin.URL + "/start",
			Headers: http.Header{
				"X-Api-Key":     []string{"secret-a"},
				"Authorization": []string{"Bearer secret-b"},
				"X-Trace":       []string{"trace-1"},
			},
		})
		if errDo != nil {
			t.Fatalf("host HTTP redirect bounce error = %v", errDo)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		for _, header := range []string{"X-Api-Key", "Authorization"} {
			if value := final.Get(header); value != "" {
				t.Errorf("final hop received %s = %q after bouncing through cross-origin hop, want stripped", header, value)
			}
		}
		if value := final.Get("X-Trace"); value != "trace-1" {
			t.Errorf("final hop X-Trace = %q, want trace-1", value)
		}
	})

	t.Run("same-origin redirect preserves credentials", func(t *testing.T) {
		var final http.Header
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/start" {
				http.Redirect(w, r, "/final", http.StatusFound)
				return
			}
			final = r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(origin.Close)

		response, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
			URL: origin.URL + "/start",
			Headers: http.Header{
				"X-Api-Key":     []string{"secret-a"},
				"Authorization": []string{"Bearer secret-b"},
				"X-Trace":       []string{"trace-1"},
			},
		})
		if errDo != nil {
			t.Fatalf("host HTTP same-origin redirect error = %v", errDo)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusOK)
		}
		if value := final.Get("X-Api-Key"); value != "secret-a" {
			t.Errorf("same-origin hop X-Api-Key = %q, want secret-a", value)
		}
		if value := final.Get("Authorization"); value != "Bearer secret-b" {
			t.Errorf("same-origin hop Authorization = %q, want Bearer secret-b", value)
		}
		if value := final.Get("X-Trace"); value != "trace-1" {
			t.Errorf("same-origin hop X-Trace = %q, want trace-1", value)
		}
	})
}

func TestSameRedirectOriginAndRedirectLimit(t *testing.T) {
	t.Parallel()

	mustParse := func(raw string) *url.URL {
		t.Helper()
		parsed, errParse := url.Parse(raw)
		if errParse != nil {
			t.Fatalf("parse URL %q: %v", raw, errParse)
		}
		return parsed
	}

	if !sameRedirectOrigin(mustParse("https://example.com/v1"), mustParse("https://example.com:443/v2")) {
		t.Error("expected https://example.com and https://example.com:443 to be treated as same origin")
	}
	if !sameRedirectOrigin(mustParse("http://example.com/v1"), mustParse("http://example.com:80/v2")) {
		t.Error("expected http://example.com and http://example.com:80 to be treated as same origin")
	}
	if sameRedirectOrigin(mustParse("https://example.com/v1"), mustParse("http://example.com/v2")) {
		t.Error("expected scheme change https -> http to be treated as cross-origin")
	}

	loopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	t.Cleanup(loopServer.Close)

	_, errDo := New().newHTTPClient(nil).Do(context.Background(), pluginapi.HTTPRequest{
		URL: loopServer.URL + "/loop",
	})
	if errDo == nil || !strings.Contains(errDo.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect loop error = %v, want stopped after 10 redirects", errDo)
	}
}
