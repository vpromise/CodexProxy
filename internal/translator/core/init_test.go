package core

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestScopedTranslatorRegistrations(t *testing.T) {
	want := [][2]sdktranslator.Format{
		{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude},
		{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude},
		{sdktranslator.FormatClaude, sdktranslator.FormatCodex},
		{sdktranslator.FormatOpenAI, sdktranslator.FormatCodex},
		{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex},
		{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI},
	}
	for _, pair := range want {
		if !sdktranslator.HasRequestTransformer(pair[0], pair[1]) {
			t.Fatalf("missing scoped request translator %s -> %s", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]sdktranslator.Format{
		{sdktranslator.Format("unsupported"), sdktranslator.FormatCodex},
		{sdktranslator.FormatOpenAI, sdktranslator.Format("unsupported")},
	} {
		if sdktranslator.HasRequestTransformer(pair[0], pair[1]) {
			t.Fatalf("unexpected out-of-scope request translator %s -> %s", pair[0], pair[1])
		}
	}
}
