package nodecmd

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	return home
}

// A node that takes the connection and never answers: launch gives up after LaunchNodeWait and says which socket
// did not answer and what to do, instead of waiting forever (#4548).
func TestLaunchGivesUpOnANodeThatDoesNotAnswer(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", "")
	sock, requests := silentNode(t, home)
	defer func(w time.Duration) { LaunchNodeWait = w }(LaunchNodeWait)
	LaunchNodeWait = 200 * time.Millisecond

	stopped := false
	start := time.Now()
	err := runLaunch(func() { stopped = true }, "foo", nil)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("launch took %s with a %s limit", took, LaunchNodeWait)
	}
	if !stopped {
		t.Error("launch kept main's signal handling")
	}
	if err == nil {
		t.Fatal("launch succeeded against a node that never answered")
	}
	for _, want := range []string{"did not answer within 200ms", sock, "node doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	select {
	case line := <-requests:
		if !strings.Contains(line, `"launcher.env"`) || !strings.Contains(line, `"foo"`) {
			t.Errorf("node got %q", line)
		}
	default:
		t.Error("the node never got the request")
	}
}

// No node at all is not a timeout: the error stays the node's own (nothing listens), not the "did not answer" one.
func TestLaunchWithoutANodeIsNotATimeout(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", "")
	err := runLaunch(func() {}, "foo", nil)
	if err == nil || strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err = %v", err)
	}
}
