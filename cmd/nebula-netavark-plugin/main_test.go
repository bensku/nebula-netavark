package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bensku/nebula-netavark/proto"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		code  int
	}{
		{"success", "{\"api_version\":\"1.0.0\",\"version\":\"test\"}\n", 0},
		{"daemon error", "{\"error\":\"could not load config\"}\n", 1},
		{"empty error message", "{\"error\":\"\"}\n", 1},
		{"teardown", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			_ = listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
			args := []string{"plugin", "setup", "/test/netns"}
			payload := []byte(`{"container_name":"test-ipv6"}`)
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var msg proto.PluginMessage
				if err := json.NewDecoder(conn).Decode(&msg); err != nil {
					done <- err
					return
				}
				if !reflect.DeepEqual(msg.Args, args) || !bytes.Equal(msg.Raw, payload) {
					done <- fmt.Errorf("unexpected forwarded message: %+v", msg)
					return
				}
				_, err = conn.Write([]byte(tc.reply))
				done <- err
			}()

			var stdout bytes.Buffer
			if code := run(socket, args, bytes.NewReader(payload), &stdout); code != tc.code {
				t.Errorf("exit code = %d, want %d", code, tc.code)
			}
			if stdout.String() != tc.reply {
				t.Errorf("stdout = %q, want unchanged reply %q", stdout.String(), tc.reply)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunUnavailableDaemon(t *testing.T) {
	var stdout bytes.Buffer
	if code := run(filepath.Join(t.TempDir(), "missing.sock"), []string{"plugin", "info"}, bytes.NewReader(nil), &stdout); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var failure proto.Error
	if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil || failure.Error == "" {
		t.Fatalf("expected useful JSON error, got %q (%v)", stdout.String(), err)
	}
}
