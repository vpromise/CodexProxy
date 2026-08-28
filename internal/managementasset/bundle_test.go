package managementasset

import (
	"bytes"
	"testing"
)

func TestHTMLReturnsScopedBundledPanel(t *testing.T) {
	html := HTML("v1.2.3")
	if len(html) < 100_000 {
		t.Fatalf("bundled panel size = %d, want a production single-file build", len(html))
	}
	if !bytes.Contains(html, []byte("CLI Proxy API Management Center")) {
		t.Fatal("bundled panel is missing the management application title")
	}
	if !bytes.Contains(html, []byte("v1.2.3")) {
		t.Fatal("bundled panel is missing the injected server version")
	}
	if bytes.Contains(html, []byte(managementVersionPlaceholder)) {
		t.Fatal("bundled panel still contains the version placeholder")
	}
	for _, removed := range [][]byte{
		[]byte("Plugin Store"),
		[]byte("Plugin Management"),
		[]byte("Gemini"),
		[]byte("Antigravity"),
		[]byte("Grok"),
		[]byte("Kimi"),
		[]byte("Vertex"),
	} {
		if bytes.Contains(html, removed) {
			t.Fatalf("bundled panel contains removed marker %q", removed)
		}
	}
}

func TestHTMLSanitizesInjectedVersion(t *testing.T) {
	html := HTML(`v1.2.3</script>`)
	if bytes.Contains(html, []byte(`v1.2.3</script>`)) {
		t.Fatal("unsafe version text was injected into the panel")
	}
	if !bytes.Contains(html, []byte("v1.2.3--script-")) {
		t.Fatal("sanitized version was not injected")
	}
}
