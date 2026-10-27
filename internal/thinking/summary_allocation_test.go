package thinking

import (
	"bytes"
	"testing"
)

func TestDisabledSummaryMissingFieldsAvoidCopies(t *testing.T) {
	body := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	body = append(body, []byte(`"}`)...)
	var result []byte
	allocations := testing.AllocsPerRun(5, func() {
		result = ApplySummaryConfig(body, "codex", SummaryConfig{Mode: SummaryDisabled})
	})
	if !bytes.Equal(result, body) {
		t.Fatal("summary removal changed an unrelated body")
	}
	if allocations > 1 {
		t.Fatalf("missing summary paths allocate %.0f times; want at most 1", allocations)
	}
}
