package api

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRESPPreservesLocalBounds(t *testing.T) {
	if maxRESPArrayElements != 128 || maxRESPBulkStringBytes != 1<<20 || maxRESPInlineLineBytes != 64<<10 {
		t.Fatal("local RESP limits changed")
	}
	for _, raw := range []string{"9223372036854775806\r\n", "2147483647\r\n", fmt.Sprintf("%d\r\n", maxRESPBulkStringBytes+1)} {
		if _, err := readRESPBulkString(bufio.NewReader(strings.NewReader(raw))); err == nil {
			t.Errorf("oversized bulk accepted: %q", raw)
		}
	}
	payload := strings.Repeat("x", maxRESPBulkStringBytes)
	got, err := readRESPBulkString(bufio.NewReader(strings.NewReader(fmt.Sprintf("%d\r\n%s\r\n", len(payload), payload))))
	if err != nil || got != payload {
		t.Fatalf("existing maximum bulk rejected: %v", err)
	}
	if _, err := readRESPArray(bufio.NewReader(strings.NewReader("*129\r\n"))); err == nil {
		t.Fatal("oversized array accepted")
	}
	if _, err := readRESPLine(bufio.NewReader(strings.NewReader(strings.Repeat("x", maxRESPInlineLineBytes+1) + "\r\n"))); err == nil {
		t.Fatal("oversized line accepted")
	}
}

func TestRESPRejectsInvalidNegativeBulkLength(t *testing.T) {
	for _, raw := range []string{"-2\r\n", "-9223372036854775808\r\n"} {
		if _, err := readRESPBulkString(bufio.NewReader(strings.NewReader(raw))); err == nil {
			t.Errorf("accepted invalid length %q", raw)
		}
	}
	for _, raw := range []string{"-1\r\n", "0\r\n\r\n"} {
		if got, err := readRESPBulkString(bufio.NewReader(strings.NewReader(raw))); err != nil || got != "" {
			t.Errorf("valid empty bulk %q: %q, %v", raw, got, err)
		}
	}
}

type handoffDeadlineConn struct {
	net.Conn
	reader   *strings.Reader
	clearing chan struct{}
	resume   chan struct{}
}

func (c *handoffDeadlineConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *handoffDeadlineConn) Close() error               { return nil }
func (c *handoffDeadlineConn) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		close(c.clearing)
		<-c.resume
	}
	return nil
}

func TestMuxClearsSniffDeadlineBeforeHTTPHandoff(t *testing.T) {
	c := &handoffDeadlineConn{reader: strings.NewReader("GET / HTTP/1.1\r\n\r\n"), clearing: make(chan struct{}), resume: make(chan struct{})}
	l := newMuxListener(nil, 1)
	done := make(chan struct{})
	go func() { (&Server{}).routeMuxConnection(c, l); close(done) }()
	select {
	case <-c.clearing:
	case <-time.After(time.Second):
		t.Fatal("deadline clear not reached")
	}
	select {
	case <-l.connCh:
		t.Error("HTTP connection became visible before sniff deadline was cleared")
	default:
	}
	close(c.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("routing did not finish")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
