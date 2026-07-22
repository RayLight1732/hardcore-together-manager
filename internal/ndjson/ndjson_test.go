package ndjson

import (
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func pipe(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})
	return NewConn(a), NewConn(b)
}

type readyMsg struct {
	Type    string `json:"type"`
	Running bool   `json:"running"`
}

func TestSendReceive_RoundTrip(t *testing.T) {
	client, server := pipe(t)

	done := make(chan error, 1)
	go func() {
		done <- client.Send(readyMsg{Type: "ready", Running: true})
	}()

	raw, err := server.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}

	typ, err := Type(raw)
	if err != nil {
		t.Fatalf("Type: %v", err)
	}
	if typ != "ready" {
		t.Fatalf("Type = %q, want ready", typ)
	}

	var msg readyMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !msg.Running {
		t.Error("Running = false, want true")
	}
}

func TestReceive_MultipleMessagesInOrder(t *testing.T) {
	client, server := pipe(t)

	go func() {
		if err := client.Send(readyMsg{Type: "ready", Running: true}); err != nil {
			t.Error("Send:", err)
		}
		if err := client.Send(readyMsg{Type: "running-changed", Running: false}); err != nil {
			t.Error("Send:", err)
		}
	}()

	for _, want := range []string{"ready", "running-changed"} {
		raw, err := server.Receive()
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		got, err := Type(raw)
		if err != nil {
			t.Fatalf("Type: %v", err)
		}
		if got != want {
			t.Fatalf("Type = %q, want %q", got, want)
		}
	}
}

func TestReceive_EOFOnClose(t *testing.T) {
	client, server := pipe(t)
	client.Close()

	if _, err := server.Receive(); err != io.EOF {
		t.Fatalf("Receive error = %v, want io.EOF", err)
	}
}

func TestType_MissingFieldErrors(t *testing.T) {
	if _, err := Type(json.RawMessage(`{"foo":"bar"}`)); err == nil {
		t.Fatal("expected error for message with no type field")
	}
}

func TestSend_LargeMessage(t *testing.T) {
	client, server := pipe(t)

	type big struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	payload := strings.Repeat("x", 200*1024) // well past bufio.Scanner's 64KiB default

	go func() {
		if err := client.Send(big{Type: "savedata-response", Data: payload}); err != nil {
			t.Error("Send:", err)
		}
	}()

	raw, err := server.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	var got big
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Data != payload {
		t.Fatalf("payload length = %d, want %d", len(got.Data), len(payload))
	}
}

func TestSend_ConcurrentSendsDoNotInterleave(t *testing.T) {
	client, server := pipe(t)

	const n = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := client.Send(readyMsg{Type: "ready", Running: i%2 == 0}); err != nil {
					t.Error("Send:", err)
				}
			}()
		}
		wg.Wait()
	}()

	seen := 0
	for seen < n {
		raw, err := server.Receive()
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if _, err := Type(raw); err != nil {
			t.Fatalf("Type on message %d: %v (raw=%s)", seen, err, raw)
		}
		seen++
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sender goroutines never finished")
	}
}
