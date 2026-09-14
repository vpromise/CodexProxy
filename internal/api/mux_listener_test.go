package api

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
)

func TestMuxListener_PutAfterClose(t *testing.T) {
	l := newMuxListener(nil, 1)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		conn, peer := net.Pipe()
		err := l.Put(conn)
		_ = conn.Close()
		_ = peer.Close()
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Put after Close = %v, want net.ErrClosed", err)
		}
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
}

func TestMuxListener_CloseDrainsQueuedConnections(t *testing.T) {
	l := newMuxListener(nil, 2)
	t.Cleanup(func() { _ = l.Close() })
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	if err := l.Put(conn); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if len(l.connCh) != 0 {
		t.Fatal("Close left connections queued")
	}
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("queued peer did not observe closure: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("repeated Close = %v", err)
	}
}

func TestMuxListener_ConcurrentPutAndClose(t *testing.T) {
	const count = 32
	l := newMuxListener(nil, 2)
	for i := 0; i < cap(l.connCh); i++ {
		conn, peer := net.Pipe()
		t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
		if err := l.Put(conn); err != nil {
			t.Fatal(err)
		}
	}
	var ready, done sync.WaitGroup
	ready.Add(count)
	done.Add(count)
	start := make(chan struct{})
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			defer done.Done()
			conn, peer := net.Pipe()
			defer func() { _ = conn.Close(); _ = peer.Close() }()
			ready.Done()
			<-start
			results <- l.Put(conn)
		}()
	}
	ready.Wait()
	close(start)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	done.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("blocked Put during Close = %v", err)
		}
	}
	if len(l.connCh) != 0 {
		t.Fatal("an in-flight Put queued a connection after draining")
	}
}

func TestMuxListener_ClosePreservesAcceptedConnection(t *testing.T) {
	l := newMuxListener(nil, 1)
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	if err := l.Put(conn); err != nil {
		t.Fatal(err)
	}
	accepted, err := l.Accept()
	if err != nil || accepted != conn {
		t.Fatalf("Accept returned %v, %v", accepted, err)
	}
	if errClose := l.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	written := make(chan error, 1)
	go func() { _, errWrite := peer.Write([]byte("x")); written <- errWrite }()
	buf := make([]byte, 1)
	if _, errRead := io.ReadFull(accepted, buf); errRead != nil || string(buf) != "x" {
		t.Fatalf("Close interrupted an accepted connection: %q, %v", buf, errRead)
	}
	if errWrite := <-written; errWrite != nil {
		t.Fatal(errWrite)
	}
}
