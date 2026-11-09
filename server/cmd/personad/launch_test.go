package main

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodecmd"
)

// silentNode listens on the node socket under home like a node that is stuck: it takes connections and reads the
// request but never answers. Each request line read is sent on the returned channel.
func silentNode(t *testing.T, home string) (sock string, requests <-chan string) {
	t.Helper()
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock = filepath.Join(dir, "node.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
			go func() {
				if line, err := bufio.NewReader(c).ReadString('\n'); err == nil {
					got <- line
				}
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-done
		for _, c := range conns {
			c.Close()
		}
	})
	return sock, got
}

// shortHome is a temp home short enough for a unix socket path (macOS caps it near 104 bytes; t.TempDir is long).
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

// launchEnv is the environment for a child personad with home as its home and no Persona / Persona overrides.
func launchEnv(home string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PERSONA_") && !strings.HasPrefix(kv, "JARVIS_") && !strings.HasPrefix(kv, "HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home)
}

// launchWait is nodecmd's bound on launch's wait for the node.
var launchWait = nodecmd.LaunchNodeWait

// Ctrl-C (SIGINT) and kill (SIGTERM) end `personad launch` while it waits for a node that does not answer, well
// before the wait runs out (#4548). The child is this test binary running main().
func TestLaunchStopsOnASignalWhileWaiting(t *testing.T) {
	if os.Getenv("PERSONAD_TEST_LAUNCH") == "1" {
		os.Args = []string{"personad", "launch", "foo"}
		main()
		os.Exit(0)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			// A child inherits an ignored signal (a test run in the background ignores SIGINT); launch then
			// rightly keeps ignoring it, so there is nothing to test.
			if signal.Ignored(sig) {
				t.Skipf("%s is ignored in this process, and so in the child", sig)
			}
			home := shortHome(t)
			_, requests := silentNode(t, home)
			cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchStopsOnASignalWhileWaiting$")
			cmd.Env = append(launchEnv(home), "PERSONAD_TEST_LAUNCH=1")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			// Signal only once the child is waiting on the node: before that the signal would hit it starting up,
			// which proves nothing about the wait.
			select {
			case <-requests:
			case err := <-exited:
				t.Fatalf("child exited before asking the node: %v", err)
			case <-time.After(launchWait):
				cmd.Process.Kill()
				t.Fatal("child never asked the node")
			}
			sent := time.Now()
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Fatalf("child: %v", err)
				}
				if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != sig {
					t.Errorf("child ended with %v, want killed by %s", err, sig)
				}
			case <-time.After(launchWait / 2):
				cmd.Process.Kill()
				t.Fatalf("child still running %s after %s", time.Since(sent).Round(time.Millisecond), sig)
			}
		})
	}
}
