package usage

import (
	"math"
	"testing"
)

func TestTokenBreakdownValidRejectsOverflow(t *testing.T) {
	// Three nonnegative operands can wrap all the way back to a nonnegative
	// total, bypassing checks that only reject negative fields.
	for _, breakdown := range []TokenBreakdown{
		{SchemaVersion: TokenAccountingSchemaVersion, Quality: TokenAccountingQualityComplete,
			Input: TokenInputBreakdown{UncachedTokens: math.MaxInt64, CacheReadTokens: math.MaxInt64, CacheWriteTokens: 2}},
		{SchemaVersion: TokenAccountingSchemaVersion, Quality: TokenAccountingQualityUnclassified, UnclassifiedTokens: 2,
			Input:  TokenInputBreakdown{TotalTokens: math.MaxInt64, UncachedTokens: math.MaxInt64},
			Output: TokenOutputBreakdown{TotalTokens: math.MaxInt64, NonReasoningTokens: math.MaxInt64}},
	} {
		if breakdown.Valid() {
			t.Fatalf("accepted a wrapped token total: %+v", breakdown)
		}
	}
}

func TestTokenBreakdownConstructorsHandleCacheOverflow(t *testing.T) {
	constructors := map[string]func(int64, int64, int64, int64, int64, int64) TokenBreakdown{
		"subset":             NewSubsetTokenBreakdown,
		"separate-reasoning": NewSeparateReasoningTokenBreakdown,
	}
	for name, build := range constructors {
		t.Run(name, func(t *testing.T) {
			bad := build(math.MaxInt64, math.MaxInt64, math.MaxInt64, 0, 0, math.MaxInt64)
			if bad.Quality != TokenAccountingQualityInconsistent || !bad.Valid() || bad.Input.UncachedTokens < 0 {
				t.Fatalf("overflow did not produce safe inconsistent accounting: %+v", bad)
			}
			boundary := build(math.MaxInt64, math.MaxInt64, 0, 0, 0, math.MaxInt64)
			if boundary.Quality != TokenAccountingQualityComplete || !boundary.Valid() || boundary.TotalTokens != math.MaxInt64 {
				t.Fatalf("valid boundary was rejected: %+v", boundary)
			}
			zero := build(0, 0, 0, 0, 0, 0)
			if zero.Quality != TokenAccountingQualityComplete || !zero.Valid() {
				t.Fatalf("valid zero accounting was rejected: %+v", zero)
			}
		})
	}
}
