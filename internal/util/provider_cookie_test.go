package util

import (
	"strings"
	"testing"
)

func TestMaskSensitiveHeaderValueMasksCookies(t *testing.T) {
	for _, name := range []string{"Cookie", "cookie", "SET-COOKIE", " Set-Cookie ", "X-Session-Cookie"} {
		t.Run(name, func(t *testing.T) {
			value := "session=synthetic-cookie-secret-value; Path=/; HttpOnly"
			masked := MaskSensitiveHeaderValue(name, value)
			if strings.Contains(masked, "synthetic-cookie-secret-value") {
				t.Fatalf("cookie credential remains visible: %q", masked)
			}
			if got := MaskSensitiveHeaderValue(name, "a=b"); got == "a=b" {
				t.Fatalf("short cookie credential remains visible: %q", got)
			}
		})
	}
	if got := MaskSensitiveHeaderValue("X-Request-Id", "synthetic-trace-value"); got != "synthetic-trace-value" {
		t.Fatalf("ordinary diagnostic header changed: %q", got)
	}
}
