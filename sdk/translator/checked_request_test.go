package translator

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCheckedRegistryAndPipelinePropagateErrors(t *testing.T) {
	r := NewRegistry()
	want := errors.New("unrepresentable user turn")
	calls := 0
	r.RegisterCheckedRequest(FormatOpenAI, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) { calls++; return []byte(`{"messages":[]}`), want }, ResponseTransform{})
	hooks := &fakePluginHooks{}
	r.SetPluginHooks(hooks)
	got, err := NewPipeline(r).TranslateRequest(t.Context(), FormatOpenAI, FormatClaude, RequestEnvelope{Model: "test", Body: []byte(`{}`)})
	if !errors.Is(err, want) || calls != 1 || !bytes.Contains(got.Body, []byte("messages")) || !reflect.DeepEqual(hooks.calls, []string{"normalize-request"}) {
		t.Fatalf("calls=%d hooks=%v error=%v", calls, hooks.calls, err)
	}
	if len(r.TranslateRequest(FormatOpenAI, FormatClaude, "test", []byte(`{}`), false)) == 0 {
		t.Fatal("legacy byte API lost its output")
	}
	r.Register(FormatOpenAI, FormatClaude, func(_ string, body []byte, _ bool) []byte { return body }, ResponseTransform{})
	if _, err = r.TranslateRequestChecked(FormatOpenAI, FormatClaude, "test", []byte(`{}`), false); err != nil {
		t.Fatal("legacy registration did not replace checked converter")
	}
}

func TestCheckedPluginFallbackNormalizesBeforeTranslation(t *testing.T) {
	r := NewRegistry()
	hooks := &fakePluginHooks{requestTranslateOK: true, requestTranslateBody: []byte(`{"messages":[{"role":"user","content":"repaired"}]}`), normalizeRequest: func(body []byte) []byte { return []byte(`{"normalized":true}`) }}
	r.SetPluginHooks(hooks)
	body, err := r.TranslateRequestChecked(Format("custom-plugin-input"), FormatClaude, "test", []byte(`{"attachment":"plugin-owned"}`), false)
	if err != nil || gjson.GetBytes(body, "messages.0.content").String() != "repaired" || !reflect.DeepEqual(hooks.calls, []string{"normalize-request", "translate-request"}) {
		t.Fatalf("body=%s error=%v calls=%v", body, err, hooks.calls)
	}
}
