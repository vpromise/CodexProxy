package registry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRemoteCatalogFiltersRetiredClaudeSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{
			"claude": [
				{"id": "claude-sonnet-4-5-20250929"},
				{"id": "claude-haiku-4-5-20251001"},
				{"id": "claude-fable-5-2"},
				{"id": "claude-future-20270101"}
			],
			"codex-free": [{"id": "gpt-test-20260101"}]
		}`)
	}))
	defer server.Close()

	previousURLs := modelsURLs
	modelsURLs = []string{server.URL}
	t.Cleanup(func() { modelsURLs = previousURLs })

	catalog, source := fetchModelsFromRemote(context.Background())
	if catalog == nil || source != server.URL {
		t.Fatalf("fetchModelsFromRemote() did not load test catalog, source = %q", source)
	}

	var ids []string
	for _, model := range catalog.Claude {
		ids = append(ids, model.ID)
	}
	want := []string{"claude-haiku-4-5-20251001", "claude-fable-5-2", "claude-future-20270101"}
	if !slices.Equal(ids, want) {
		t.Fatalf("Claude IDs = %v, want %v", ids, want)
	}
	if len(catalog.CodexFree) != 1 || catalog.CodexFree[0].ID != "gpt-test-20260101" {
		t.Fatalf("Codex models changed: %+v", catalog.CodexFree)
	}
}
