package executor

import "testing"

func TestClaudeFingerprintMatchesJavaScriptUTF16(t *testing.T) {
	// Expected values were computed independently with Node's string indexing and SHA-256.
	for _, tc := range []struct{ text, want string }{
		{"abcdefghij012345678901234", "6e8"},
		{"abcd😀efghijklmnopqrstuvwxyz", "721"},
		{"😀😀😀😀😀😀😀😀😀😀😀", "861"},
		{"abc😀def😀ghi😀jkl😀mnop", "3aa"},
		{"短文本", "d7b"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := computeFingerprint(tc.text, "2.1.280"); got != tc.want {
				t.Fatalf("fingerprint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeFingerprintStopsAtFirstEligibleTextPart(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{`[{"type":"text","text":"<system-reminder>date</system-reminder>"},{"type":"image","source":{}},{"type":"text","text":"first"},{"type":"text","text":"last"}]`, "first"},
		{`[{"type":"text","text":""},{"type":"text","text":"last"}]`, ""},
		{`"string prompt"`, "string prompt"},
	} {
		payload := []byte(`{"messages":[{"role":"user","content":` + tc.content + `},{"role":"user","content":"later turn"}]}`)
		if got := claudeBillingFingerprintMessageText(payload); got != tc.want {
			t.Errorf("fingerprint text = %q, want %q", got, tc.want)
		}
	}
}
