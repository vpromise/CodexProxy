package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRegeneratedClientKeyBindingCanBeRemoved(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			const oldKey = "synthetic-old-client"
			const newKey = "synthetic-new-client"
			const otherKey = "synthetic-other-client"
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte("api-keys: ["+oldKey+", "+otherKey+"]\nrequest-retry: 9\n"), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			cfg, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			cfg.AccessIsolation = config.AccessIsolation{
				Enabled: enabled,
				Pools: []config.AccountPool{
					{ID: "one", Name: "Codex pool", Provider: "codex"},
				},
				Keys: []config.ClientKeyBinding{
					{Key: oldKey, Name: "Old client", Note: "Old metadata", Pool: "one"},
					{Key: otherKey, Name: "Other client", Note: "Keep metadata", Pool: "one"},
				},
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			call := func(handler gin.HandlerFunc, method string, body []byte) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(method, "/", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				handler(c)
				return rec
			}
			// The visual configuration editor replaces the client secret in the YAML,
			// while retaining the isolation section for subsequent policy editing.
			cfg.APIKeys = []string{newKey, otherKey}
			raw, errMarshal := yaml.Marshal(cfg)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if rec := call(h.PutConfigYAML, http.MethodPut, raw); rec.Code != http.StatusOK {
				t.Fatalf("regenerate: status %d: %s", rec.Code, rec.Body.String())
			}
			var view struct {
				Revision string                 `json:"revision"`
				Settings config.AccessIsolation `json:"settings"`
				APIKeys  []string               `json:"api-keys"`
			}
			get := func() {
				t.Helper()
				rec := call(h.GetAccessIsolation, http.MethodGet, nil)
				if rec.Code != http.StatusOK {
					t.Fatalf("read isolation: status %d", rec.Code)
				}
				if errDecode := json.Unmarshal(rec.Body.Bytes(), &view); errDecode != nil {
					t.Fatal(errDecode)
				}
			}
			get()
			if !reflect.DeepEqual(view.APIKeys, []string{newKey, otherKey}) || len(view.Settings.Keys) != 2 || view.Settings.Keys[0].Key != oldKey {
				t.Fatal("fixture did not reproduce a revoked binding after key replacement")
			}
			view.Settings.Keys = view.Settings.Keys[1:]
			body, errEncode := json.Marshal(map[string]any{"revision": view.Revision, "settings": view.Settings})
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			if rec := call(h.PutAccessIsolation, http.MethodPut, body); rec.Code != http.StatusOK {
				t.Fatalf("remove revoked binding: status %d: %s", rec.Code, rec.Body.String())
			}
			if rec := call(h.PutAccessIsolation, http.MethodPut, body); rec.Code != http.StatusConflict {
				t.Fatalf("stale cleanup: status %d, want 409", rec.Code)
			}
			reloaded, errReload := config.LoadConfig(path)
			if errReload != nil {
				t.Fatal(errReload)
			}
			if !reflect.DeepEqual(reloaded.APIKeys, []string{newKey, otherKey}) || !reflect.DeepEqual(reloaded.AccessIsolation, view.Settings) || reloaded.RequestRetry != 9 {
				t.Fatal("cleanup changed active keys, pools, other bindings, or unrelated configuration")
			}
			h.cfg = reloaded
			get()
			if len(view.Settings.Keys) != 1 || view.Settings.Keys[0].Key != otherKey {
				t.Fatal("revoked record returned after reload")
			}
		})
	}
}
