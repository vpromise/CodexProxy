package diff

import "testing"

func expectContains(t *testing.T, list []string, target string) {
	t.Helper()
	for _, entry := range list {
		if entry == target {
			return
		}
	}
	t.Fatalf("expected list to contain %q, got %#v", target, list)
}
