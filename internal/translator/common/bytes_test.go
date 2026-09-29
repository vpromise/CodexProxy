package common

import (
	"bytes"
	"testing"
)

func TestJoinRawArray(t *testing.T) {
	tests := []struct {
		name  string
		items [][]byte
		want  string
	}{
		{name: "empty", want: "[]"},
		{name: "single", items: [][]byte{[]byte(`{"id":1}`)}, want: `[{"id":1}]`},
		{name: "multiple", items: [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`)}, want: `[{"id":1},{"id":2}]`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(JoinRawArray(test.items)); got != test.want {
				t.Fatalf("JoinRawArray() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNewRawArrayItems(t *testing.T) {
	if items := NewRawArrayItems(0); items != nil {
		t.Fatalf("NewRawArrayItems(0) = %#v, want nil", items)
	}
	if items := NewRawArrayItems(3); len(items) != 0 || cap(items) != 3 {
		t.Fatalf("NewRawArrayItems(3) len = %d, cap = %d; want len 0, cap 3", len(items), cap(items))
	}
}

func TestSetRawArrayItems(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		path  string
		items [][]byte
		want  string
	}{
		{name: "empty", data: `{"items":[]}`, path: "items", want: `{"items":[]}`},
		{name: "single nested", data: `{"before":1,"request":{"contents":[]},"after":2}`, path: "request.contents", items: [][]byte{[]byte(`{"id":1}`)}, want: `{"before":1,"request":{"contents":[{"id":1}]},"after":2}`},
		{name: "single fallback", data: `{"items":[{"old":1},{"old":2}]}`, path: "items", items: [][]byte{[]byte(`{"id":1}`)}, want: `{"items":[{"id":1}]}`},
		{name: "multiple", data: `{"items":[]}`, path: "items", items: [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`)}, want: `{"items":[{"id":1},{"id":2}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SetRawArrayItems([]byte(test.data), test.path, test.items)
			if string(got) != test.want {
				t.Fatalf("SetRawArrayItems() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSSEEventData_SelfTerminatingFrame(t *testing.T) {
	event := "response.completed"
	payload := []byte(`{"id":"resp_1"}`)
	got := SSEEventData(event, payload)
	want := "event: response.completed\ndata: {\"id\":\"resp_1\"}\n\n"
	if string(got) != want {
		t.Fatalf("SSEEventData() = %q, want %q", string(got), want)
	}
}

func TestSSEEventData_ConcatenatedFramesSeparable(t *testing.T) {
	frame1 := SSEEventData("event1", []byte(`{"a":1}`))
	frame2 := SSEEventData("event2", []byte(`{"b":2}`))
	concatenated := append(frame1, frame2...)

	frames := bytes.Split(concatenated, []byte("\n\n"))
	if len(frames) != 3 || len(frames[2]) != 0 {
		t.Fatalf("expected 2 non-empty frames followed by trailing empty slice, got %d parts", len(frames))
	}
	if string(frames[0]) != "event: event1\ndata: {\"a\":1}" {
		t.Fatalf("unexpected frame1: %s", string(frames[0]))
	}
	if string(frames[1]) != "event: event2\ndata: {\"b\":2}" {
		t.Fatalf("unexpected frame2: %s", string(frames[1]))
	}
}
