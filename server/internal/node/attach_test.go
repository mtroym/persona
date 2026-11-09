package node

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
)

// The relay pipes both ways to whatever listens on the attach socket, and says "not relayed" when nothing does.
func TestRelay(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ok, _ := Relay(ctx, strings.NewReader(""), io.Discard); ok {
		t.Fatal("relayed with no resident node listening")
	}

	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // a resident node that answers each line with "re: <line>"
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			_, _ = c.Write([]byte("re: " + sc.Text() + "\n"))
		}
	}()

	var out strings.Builder
	ok, err := Relay(ctx, strings.NewReader("one\ntwo\n"), &out)
	if !ok {
		t.Fatal("not relayed with a resident node listening")
	}
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if got := out.String(); got != "re: one\nre: two\n" {
		t.Fatalf("got %q", got)
	}
}

// A second resident node refuses to start while one is listening: it would steal relays from the first.
func TestAttachIsSingle(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = Attach(ctx, func() (*Node, error) { t.Fatal("served a connection"); return nil, nil }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("want 'already listening', got %v", err)
	}
}

// While another resident node holds the run lock, a second one refuses to start even when nobody answers on the
// socket (the first has not started accepting yet), and leaves the first one's socket in place. Once the lock is
// given back, the socket is stale and a new resident node takes over.
func TestAttachRefusesWhileLocked(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := link.LockFile(AttachSocketPath()+".run", false)
	if err != nil {
		t.Fatal(err)
	}
	// The first node's socket, refusing connections: a listener closed without removing its path.
	ln, err := net.Listen("unix", AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	before, err := os.Stat(AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = Attach(ctx, func() (*Node, error) { t.Error("served a connection"); return nil, io.EOF }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("want 'already running', got %v", err)
	}
	if after, err := os.Stat(AttachSocketPath()); err != nil || !os.SameFile(before, after) {
		t.Fatalf("the first node's socket was removed or replaced (%v)", err)
	}

	release()
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Attach(ctx2, func() (*Node, error) { return nil, io.EOF }, nil, nil) }()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("unix", AttachSocketPath()); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel2()
			t.Fatal("no resident node started once the lock was free")
		}
	}
	cancel2()
	if err := <-done; err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

// A stale socket file (the last resident node died without cleaning up) doesn't stop a new one.
func TestAttachReplacesStaleSocket(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AttachSocketPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Attach(ctx, func() (*Node, error) { return nil, io.EOF }, nil, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.Dial("unix", AttachSocketPath()); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resident node did not start listening")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

// A resident node set up for tailcat serves the hub's connections from there too, alongside relays over ssh.
func TestAttachServesExtraConnections(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{}, 4)
	extra := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- Attach(ctx, func() (*Node, error) { served <- struct{}{}; return nil, io.EOF }, extra, nil)
	}()
	wait := func(what string) {
		t.Helper()
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s was not served", what)
		}
	}
	a, b := net.Pipe()
	defer a.Close()
	extra <- b
	wait("a tailcat connection")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.Dial("unix", AttachSocketPath()); err == nil {
			defer c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resident node did not start listening")
		}
		time.Sleep(50 * time.Millisecond)
	}
	wait("a relayed connection")
	close(extra) // tailcat gone: relays go on
	c, err := net.Dial("unix", AttachSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	wait("a relay after tailcat stopped")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

// shortHome sets HOME to a short temp dir: a unix socket path is limited to 104 bytes on macOS, and t.TempDir() under
// /var/folders makes ~/.persona/node-attach.sock longer than that. Windows has no /tmp; its temp directory is short.
func shortHome(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	home, err := os.MkdirTemp(base, "jn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	return home
}

func TestAttachSocketPathIsolateFromPersonaHome(t *testing.T) {
	for _, envVar := range []string{"PERSONA_HOME", "JARVIS_HOME"} {
		t.Run(envVar, func(t *testing.T) {
			t.Setenv(envVar, "/some/real/persona/dir")
			home := shortHome(t)
			sock := AttachSocketPath()
			if !strings.HasPrefix(sock, home) {
				t.Fatalf("%s set: AttachSocketPath() = %q, want path under %q", envVar, sock, home)
			}
		})
	}
}
