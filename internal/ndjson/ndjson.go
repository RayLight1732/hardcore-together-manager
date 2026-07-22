// Package ndjson implements the newline-delimited-JSON framing shared by
// MOD⇔Manager (docs/protocol-mod-manager.md) and Gate⇔Manager
// (docs/protocol-gate-manager.md): one message per line, `type` field
// discriminates the message kind. Both protocol docs specify identical
// framing, so modserver and gateserver share this one implementation
// rather than duplicating the read/write loop.
package ndjson

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// maxLine bounds a single NDJSON message. savedata-response can list every
// event across every challenge ever played, so this is generous (8MiB)
// rather than bufio.Scanner's 64KiB default.
const maxLine = 8 * 1024 * 1024

// Conn wraps a net.Conn with line-based JSON framing. Send is safe to call
// concurrently with itself and with Receive; Receive is not safe to call
// concurrently with itself (there is only ever one reader per connection in
// this codebase).
type Conn struct {
	conn    net.Conn
	scanner *bufio.Scanner
	writeMu sync.Mutex
}

// NewConn wraps conn. It does not take ownership of conn's lifecycle beyond
// what Close does.
func NewConn(conn net.Conn) *Conn {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	return &Conn{conn: conn, scanner: scanner}
}

// Send marshals v to JSON and writes it as one line.
func (c *Conn) Send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ndjson: marshal: %w", err)
	}
	data = append(data, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("ndjson: write: %w", err)
	}
	return nil
}

// Receive blocks until the next line arrives and returns it unparsed, for
// the caller to inspect via Type and then unmarshal into a concrete message
// struct. It returns io.EOF once the peer closes the connection (or the
// scanner's own error, e.g. a line over maxLine).
func (c *Conn) Receive() (json.RawMessage, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return nil, fmt.Errorf("ndjson: read: %w", err)
		}
		return nil, io.EOF
	}
	line := c.scanner.Bytes()
	out := make(json.RawMessage, len(line))
	copy(out, line)
	return out, nil
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.conn.Close()
}

// envelope extracts just the `type` discriminator that every message in
// both protocols carries.
type envelope struct {
	Type string `json:"type"`
}

// Type extracts the `type` field from a raw message so the caller can
// switch on it before deciding which concrete struct to unmarshal into.
func Type(raw json.RawMessage) (string, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("ndjson: read type: %w", err)
	}
	if env.Type == "" {
		return "", fmt.Errorf("ndjson: message has no type field: %s", raw)
	}
	return env.Type, nil
}
