package util

import (
	"encoding/json"
	"reflect"
	"testing"
)

func compareJSON(t *testing.T, expectedJSON, actualJSON string) {
	t.Helper()
	var expected map[string]any
	var actual map[string]any
	if err := json.Unmarshal([]byte(expectedJSON), &expected); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(actualJSON), &actual); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("JSON mismatch: expected %#v, got %#v", expected, actual)
	}
}
