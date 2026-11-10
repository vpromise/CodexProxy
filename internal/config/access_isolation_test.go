package config

import "testing"

func TestPreservePoolAccountIDsRequiresUniqueRecord(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, scenario := range []string{"same-key", "new-duplicate", "old-duplicates", "changed-key", "explicit-id"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				previous := &Config{}
				next := &Config{}
				old := []CodexKey{{APIKey: "synthetic-key", PoolAccountID: "record-one"}}
				incoming := []CodexKey{{APIKey: "synthetic-key", Prefix: "edited"}}
				want := ""
				switch scenario {
				case "same-key":
					want = "record-one"
				case "new-duplicate":
					incoming = append(incoming, CodexKey{APIKey: "synthetic-key", Prefix: "new"})
				case "old-duplicates":
					old = append(old, CodexKey{APIKey: "synthetic-key", PoolAccountID: "record-two"})
				case "changed-key":
					incoming[0].APIKey = "synthetic-replacement"
				case "explicit-id":
					incoming[0].PoolAccountID, want = "explicit-record", "explicit-record"
				}
				if provider == "codex" {
					previous.CodexKey, next.CodexKey = old, incoming
				} else {
					for _, entry := range old {
						previous.ClaudeKey = append(previous.ClaudeKey, ClaudeKey{APIKey: entry.APIKey, PoolAccountID: entry.PoolAccountID})
					}
					for _, entry := range incoming {
						next.ClaudeKey = append(next.ClaudeKey, ClaudeKey{APIKey: entry.APIKey, PoolAccountID: entry.PoolAccountID})
					}
				}
				next.PreservePoolAccountIDs(previous)
				if provider == "codex" {
					for _, entry := range next.CodexKey {
						if entry.PoolAccountID != want {
							t.Fatalf("pool ID = %q, want %q", entry.PoolAccountID, want)
						}
					}
				} else {
					for _, entry := range next.ClaudeKey {
						if entry.PoolAccountID != want {
							t.Fatalf("pool ID = %q, want %q", entry.PoolAccountID, want)
						}
					}
				}
			})
		}
	}
}
