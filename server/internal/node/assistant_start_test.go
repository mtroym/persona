package node

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
)

// fakeClaudeHome puts a fake claude on PATH, run through the interactive zsh the assistant starts in.
func fakeClaudeHome(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".zshrc"), nil, 0o600)
	// The fake claude is meant to work, so mark its first-run setup finished: the launcher check's onboarding gate
	// (claudeonboarding.go) otherwise stops before it ever runs.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
}

// A launcher that exits before reading the message (its node socket gone: "no persona node") lost it: the send says
// so with what it printed, instead of answering ok and leaving the message at "Delivering" for good.
func TestAssistantSendFailsWhenTheLauncherDoesNotStart(t *testing.T) {
	fakeClaudeHome(t, "printf '\\033[31mERROR\\033[0m: gitstatus failed\\n\\n' >&2\necho 'personad launch opus: no persona node: dial unix node.sock: connect: connection refused' >&2\nexit 1\n")
	n := codexTestNode(t)
	withFakeHub(t, n)
	_, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "")
	if err == nil {
		t.Fatal("a launcher that did not start answered ok")
	}
	if msg := err.Error(); !strings.Contains(msg, "没有启动起来") || !strings.Contains(msg, "no persona node") || strings.Contains(msg, "\x1b") {
		t.Fatalf("error: %q", msg)
	}
	n.mu.Lock()
	left := len(n.procs)
	n.mu.Unlock()
	if left != 0 {
		t.Fatal("the dead process is still kept")
	}
}

// One that starts answers as before, as soon as its first line comes out.
func TestAssistantSendReturnsOnceTheProcessStarted(t *testing.T) {
	fakeClaudeHome(t, "read line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	n := codexTestNode(t)
	withFakeHub(t, n)
	start := time.Now()
	id, err := n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= assistantStartWait {
		t.Fatalf("waited the whole start wait (%v) for a process that started", time.Since(start))
	}
	n.stopAssistant(id)
}

// The node socket replaced by a file nothing answers on is re-created; one a newer node listens on is left alone.
func TestSockGoneTellsADeadReplacementFromALiveOne(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ps") // a unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	if err := os.MkdirAll(filepath.Dir(nodeclient.SocketPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Node{}
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if why := n.sockGone(); why != "" {
		t.Fatalf("its own socket: %q", why)
	}

	// Another process listens on the path and exits without unlinking.
	_ = os.Remove(nodeclient.SocketPath())
	other, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	other.Close()
	if why := n.sockGone(); !strings.Contains(why, "nothing answers") {
		t.Fatalf("a dead replacement: %q", why)
	}
	if _, err := n.listenOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := net.Dial("unix", nodeclient.SocketPath()); err != nil {
		t.Fatalf("not re-created: %v", err)
	} else {
		c.Close()
	}

	// A newer node's live socket stays its own.
	_ = os.Remove(nodeclient.SocketPath())
	newer, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	go func() {
		for {
			c, err := newer.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if why := n.sockGone(); why != "" {
		t.Fatalf("a live newer socket: %q", why)
	}

	_ = os.Remove(nodeclient.SocketPath())
	if why := n.sockGone(); why != "vanished" {
		t.Fatalf("a removed socket: %q", why)
	}
}
