package cache

import (
	"bytes"
	"context"
	"testing"
)

func useFakeClaudeThinkingReplayKVClient(t *testing.T, client *fakeClaudeThinkingReplayKVClient) {
	t.Helper()
	previous := currentClaudeThinkingReplayKVClient
	currentClaudeThinkingReplayKVClient = func() (claudeThinkingReplayKVClient, bool, error) {
		return client, true, nil
	}
	t.Cleanup(func() {
		currentClaudeThinkingReplayKVClient = previous
	})
}

func TestClaudeThinkingReplayAppendsAssistantTurns(t *testing.T) {
	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client)

	const modelFamily = "claude:auth:model"
	const sessionKey = "execution:multi-turn"
	first := []byte(`[{"type":"thinking","thinking":"first","signature":"sig-1"},{"type":"tool_use","id":"toolu-1","name":"Read","input":{"path":"one"}}]`)
	second := []byte(`[{"type":"thinking","thinking":"second","signature":"sig-2"},{"type":"tool_use","id":"toolu-2","name":"Read","input":{"path":"two"}}]`)

	if !CacheClaudeThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, first) {
		t.Fatal("failed to seed first Claude replay turn")
	}
	_, snapshot, found, errGet := GetClaudeThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("initial Claude replay read = found %v, error %v", found, errGet)
	}
	replaced, errReplace := ReplaceClaudeThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot, second)
	if errReplace != nil || !replaced {
		t.Fatalf("append Claude replay turn = replaced %v, error %v", replaced, errReplace)
	}

	contents, found, errGet := GetClaudeThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || len(contents) != 2 {
		t.Fatalf("Claude replay contents = %d, found %v, error %v; want two turns", len(contents), found, errGet)
	}
	if !bytes.Equal(contents[0], first) || !bytes.Equal(contents[1], second) {
		t.Fatalf("Claude replay contents lost ordering: got %s / %s", contents[0], contents[1])
	}
}
