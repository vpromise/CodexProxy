package logging

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

var _ io.WriterTo = (*FileBodySource)(nil)

type boundedLogWriter struct {
	data      bytes.Buffer
	remaining int
	err       error
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n > w.remaining {
		n = w.remaining
	}
	_, _ = w.data.Write(p[:n])
	w.remaining -= n
	if n < len(p) {
		return n, w.err
	}
	return n, nil
}

func TestFileBodySourceWriteToCountsBytesAndPreservesErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		limit   int
		failure error
		want    string
	}{
		{"all", 100, nil, "one\ntwo"},
		{"partial-part", 5, io.ErrClosedPipe, "one\nt"},
		{"separator-error", 3, io.ErrClosedPipe, "one"},
		{"short-separator", 3, io.ErrShortWrite, "one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := NewFileBodySourceInDir(t.TempDir(), "count")
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"one", "two"} {
				file, errCreate := source.CreatePart("raw")
				if errCreate != nil {
					t.Fatal(errCreate)
				}
				_, errWrite := io.WriteString(file, part)
				errClose := file.Close()
				if errWrite != nil || errClose != nil {
					t.Fatalf("write part: %v; close: %v", errWrite, errClose)
				}
			}
			failure := test.failure
			if test.name == "short-separator" {
				failure = nil
			}
			writer := &boundedLogWriter{remaining: test.limit, err: failure}
			n, errWrite := source.WriteTo(writer)
			if n != int64(len(test.want)) || writer.data.String() != test.want || !errors.Is(errWrite, test.failure) {
				t.Fatalf("WriteTo=(%d,%v), payload=%q; want (%d,%v), %q", n, errWrite, writer.data.String(), len(test.want), test.failure, test.want)
			}
		})
	}
}

func TestFileBodySourceWriterToInterfaceSkipsMissingParts(t *testing.T) {
	source, err := NewFileBodySourceInDir(t.TempDir(), "copy")
	if err != nil {
		t.Fatal(err)
	}
	if errAppend := source.AppendPart([]byte("missing")); errAppend != nil {
		t.Fatal(errAppend)
	}
	if errAppend := source.AppendPart([]byte("retained")); errAppend != nil {
		t.Fatal(errAppend)
	}
	if errRemove := os.Remove(source.Paths()[0]); errRemove != nil {
		t.Fatal(errRemove)
	}
	var output bytes.Buffer
	var writerTo io.WriterTo = source
	n, errCopy := writerTo.WriteTo(&output)
	if errCopy != nil || n != 9 || output.String() != "retained\n" {
		t.Fatalf("WriterTo=(%d,%v), payload=%q", n, errCopy, output.String())
	}
}
