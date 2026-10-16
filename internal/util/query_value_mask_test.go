package util

import (
	"net/url"
	"strings"
	"testing"
)

func TestMaskSensitiveQueryManagementValue(t *testing.T) {
	const secret = "sk-fixture-secret-for-deletion"
	for _, key := range []string{"value", "VALUE", "value[]", "%76alue", "%20value%20"} {
		raw := key + "=" + secret + "&value=" + secret + "&model=claude-haiku-5-5&label=value&token=" + secret
		got := MaskSensitiveQuery(raw)
		if strings.Contains(got, secret) {
			t.Fatalf("unmasked secret for %s: %s", key, got)
		}
		values, err := url.ParseQuery(got)
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("model") != "claude-haiku-5-5" || values.Get("label") != "value" {
			t.Fatal("ordinary query changed")
		}
	}
	for _, raw := range []string{"model=m&myvalue=visible", "value=", "value", "value=%ZZ&model=m"} {
		got := MaskSensitiveQuery(raw)
		if strings.Contains(raw, "myvalue") && got != raw {
			t.Fatalf("unrelated key changed: %q", got)
		}
		if strings.Contains(raw, "%ZZ") && strings.Contains(got, "%ZZ") {
			t.Fatalf("malformed secret retained: %q", got)
		}
	}
}
