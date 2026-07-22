// Command fakehardcore is a lightweight stand-in for the hardcore server,
// built and driven only by internal/e2e's tests (never shipped as part of
// the product). It speaks just enough of the MOD⇔Manager protocol
// (docs/protocol-mod-manager.md) to exercise Manager's /start·/load
// sequence without needing a real Minecraft server:
//
//  1. ensure world/level.dat exists — but only create it if missing, so a
//     world/ restored by /load (architecture-manager.md 4節) isn't
//     clobbered; this is what lets the e2e test verify a /load actually
//     restored the archived content rather than a fresh one
//  2. connect to FAKEHARDCORE_SIGNAL_ADDR and send `ready`
//  3. send one `archive-request` (name omitted, so Manager auto-generates
//     it) and wait for `archive-complete`, so the e2e test has an archive
//     to /load
//  4. wait for SIGTERM
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := os.Getenv("FAKEHARDCORE_SIGNAL_ADDR")
	if addr == "" {
		log.Fatal("fakehardcore: FAKEHARDCORE_SIGNAL_ADDR is required")
	}

	if err := ensureWorldMarker(); err != nil {
		log.Fatalf("fakehardcore: world marker: %v", err)
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("fakehardcore: dial %s: %v", addr, err)
	}
	defer conn.Close()

	if err := sendLine(conn, map[string]any{"type": "ready", "running": true}); err != nil {
		log.Fatalf("fakehardcore: send ready: %v", err)
	}
	log.Println("fakehardcore: sent ready")

	if err := sendLine(conn, map[string]any{"type": "archive-request", "elapsedTime": 100}); err != nil {
		log.Fatalf("fakehardcore: send archive-request: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Fatalf("fakehardcore: read archive-complete: %v", err)
	}
	log.Printf("fakehardcore: received %s", line)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	log.Println("fakehardcore: got SIGTERM, exiting")
}

func sendLine(conn net.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// ensureWorldMarker simulates "world generation": it creates world/level.dat
// with unique content (so the e2e test can tell restored content apart from
// freshly-generated content) only if the file isn't already there.
func ensureWorldMarker() error {
	path := "world/level.dat"
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(time.Now().Format(time.RFC3339Nano)), 0o644)
}
