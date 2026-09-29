package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestGetRequestLogByIDFullUUIDAndLegacy(t *testing.T) {
	const first = "019994a8-7623-7b51-9a29-0123456789ab"
	const second = "019994a8-7623-7b52-8b30-0123456789ab"
	dir := t.TempDir()
	h := newLogsTestHandler(dir, true)
	timestamp := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, file := range []struct{ name, body string }{
		{"v1-responses-2026-09-29T120000_9-" + first + ".log", "sequence9"},
		{"v1-responses-2026-09-29T120000_10-" + first + ".log", "sequence10"},
		{"v1-responses-2026-09-29T120000-" + second + ".log", "other request"},
		{"v1-responses-2026-09-29T120000-abcdef12.log", "legacy"},
	} {
		path := filepath.Join(dir, file.name)
		if errWrite := os.WriteFile(path, []byte(file.body), 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		if errTime := os.Chtimes(path, timestamp, timestamp); errTime != nil {
			t.Fatal(errTime)
		}
	}
	for _, tc := range []struct {
		id, want string
		status   int
	}{
		{first, "sequence10", 200}, {second, "other request", 200}, {"abcdef12", "legacy", 200},
		{"456789ab", "", 404}, {"0123456789ab", "", 404},
	} {
		t.Run(tc.id, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}
			c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-log/"+tc.id, nil)
			h.GetRequestLogByID(c)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want %d", rec.Code, tc.status)
			}
			if tc.status == 200 && rec.Body.String() != tc.want {
				t.Fatalf("body=%q want %q", rec.Body.String(), tc.want)
			}
		})
	}
}
func TestLogMetadataFullUUID(t *testing.T) {
	meta := parseLogMetadata("error-v1-responses-2026-09-29T120000_10-019994a8-7623-7b51-9a29-0123456789ab.log")
	if !meta.hasTime || meta.seq != 10 || meta.prefix != "error-v1-responses" {
		t.Fatalf("metadata=%+v", meta)
	}
}
