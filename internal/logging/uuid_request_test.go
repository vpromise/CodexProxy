package logging

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

func TestRequestIDMiddlewareUsesFullUUIDv7(t *testing.T) {
	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.GET("/v1/models", func(c *gin.Context) {
		id := GetGinRequestID(c)
		parsed, err := uuid.Parse(id)
		if err != nil || len(id) != 36 || parsed.Version() != 7 {
			t.Errorf("request ID = %q, parse error=%v; want full UUIDv7", id, err)
		}
		if GetRequestID(c.Request.Context()) != id {
			t.Error("request context lost full ID")
		}
		c.Status(http.StatusOK)
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
}

func TestCreateUniqueLogFilePreservesCompleteRequestID(t *testing.T) {
	for _, id := range []string{"019994a8-7623-7b51-9a29-0123456789ab", "01234567", "legacy-request-id"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			prefix := "v1-responses-2026-09-29T120000"
			filename := prefix + "-" + id + ".log"
			for i := 0; i < 2; i++ {
				file, path, err := createUniqueLogFile(dir, filename)
				if err != nil {
					t.Fatal(err)
				}
				if _, errWrite := file.WriteString("fixture"); errWrite != nil {
					t.Fatal(errWrite)
				}
				if errClose := file.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				want := filename
				if i > 0 {
					want = prefix + "_1-" + id + ".log"
				}
				if filepath.Base(path) != want {
					t.Errorf("filename=%q want %q", filepath.Base(path), want)
				}
				if !strings.HasSuffix(path, "-"+id+".log") {
					t.Error("complete ID no longer matches")
				}
				info, errStat := os.Stat(path)
				if errStat != nil {
					t.Fatal(errStat)
				}
				if info.Mode().Perm() != 0600 {
					t.Errorf("log permissions=%o", info.Mode().Perm())
				}
			}
		})
	}
}

func TestRequestIDEntropyFailureDoesNotInventIdentity(t *testing.T) {
	id, err := GenerateRequestIDFromReader(bytes.NewReader(nil))
	if err == nil || id != "" {
		t.Fatalf("id=%q error=%v; want empty ID with error", id, err)
	}
	called := false
	engine := gin.New()
	engine.Use(ginLogrusLogger(func() (string, error) { return "", errors.New("fixture entropy failure") }))
	engine.GET("/v1/models", func(c *gin.Context) {
		called = true
		if GetGinRequestID(c) != "" || GetRequestID(c.Request.Context()) != "" {
			t.Error("entropy failure created a request identity")
		}
		c.Status(http.StatusOK)
	})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if !called || recorder.Code != http.StatusOK {
		t.Error("logging failure prevented normal request handling")
	}
}

func TestFullUUIDSurvivesLogFormattingAndTrace(t *testing.T) {
	id, err := GenerateRequestID()
	if err != nil {
		t.Fatal(err)
	}
	entry := log.NewEntry(log.New()).WithField("request_id", id)
	entry.Time = time.Now()
	entry.Message = "fixture"
	data, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatal(errFormat)
	}
	if !strings.Contains(string(data), "["+id+"]") {
		t.Fatalf("formatted log lost UUID: %s", data)
	}
	ctx := WithRequestID(context.Background(), id)
	if !strings.HasSuffix(FormatCPATraceID(time.Now(), "fixture-auth", GetRequestID(ctx)), "-"+id) {
		t.Error("trace lost complete UUID")
	}
}

func TestFullUUIDNonStreamingAndErrorLogs(t *testing.T) {
	const id = "019994a8-7623-7b51-9a29-0123456789ab"
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "forced_error"}[force], func(t *testing.T) {
			dir := t.TempDir()
			logger := NewFileRequestLogger(!force, dir, "", 0)
			errLog := logger.LogRequestWithOptions("/v1/responses", "POST", nil, []byte("request-body"), 500, nil, []byte("response-body"), nil, nil, nil, nil, nil, force, id, time.Now(), time.Now())
			if errLog != nil {
				t.Fatal(errLog)
			}
			files, errList := os.ReadDir(dir)
			if errList != nil {
				t.Fatal(errList)
			}
			if len(files) != 1 {
				t.Fatalf("files=%d, want one final log", len(files))
			}
			name := files[0].Name()
			if !strings.HasSuffix(name, "-"+id+".log") {
				t.Fatalf("filename lost UUID: %s", name)
			}
			if force && !strings.HasPrefix(name, "error-") {
				t.Fatalf("missing error prefix: %s", name)
			}
			data, errRead := os.ReadFile(filepath.Join(dir, name))
			if errRead != nil {
				t.Fatal(errRead)
			}
			if !bytes.Contains(data, []byte("request-body")) || !bytes.Contains(data, []byte("response-body")) {
				t.Fatal("log body missing")
			}
		})
	}
}
