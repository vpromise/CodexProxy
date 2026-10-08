package pluginhost

import (
	"errors"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestPluginExecutorPreparationRejectsAttachmentLoss(t *testing.T) {
	adapter := &executorAdapter{inputFormats: []sdktranslator.Format{sdktranslator.FormatClaude}, outputFormats: []sdktranslator.Format{sdktranslator.FormatClaude}}
	for _, stream := range []bool{false, true} {
		_, err := adapter.prepareExecutorCall(coreexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"opaque"}}]}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream})
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != 400 {
			t.Fatalf("error=%v, want 400 before plugin dispatch", err)
		}
	}
}
