package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// launchKeyHome points HOME at a short new directory (sun_path) with its data directory, for the socket and the key.
func launchKeyHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "lk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("JARVIS_HOME", "")
	if err := os.MkdirAll(filepath.Dir(nodeclient.SocketPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLaunchKeyMadeOnceAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.key")
	if _, err := readLaunchKey(path); err == nil {
		t.Fatal("a missing file holds no key")
	}
	k, err := ensureLaunchKey(path)
	if err != nil || len(k) != launchKeyLen {
		t.Fatalf("made %q, %v", k, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode: %v %v", fi, err)
		}
	}
	if again, err := ensureLaunchKey(path); err != nil || again != k {
		t.Fatalf("a second node replaced the key: %q, %v", again, err)
	}
	if got, err := readLaunchKey(path); err != nil || got != k {
		t.Fatalf("read %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLaunchKey(path); err == nil {
		t.Fatal("a malformed file passed for a key")
	}
	if fresh, err := ensureLaunchKey(path); err != nil || len(fresh) != launchKeyLen || fresh == k {
		t.Fatalf("a malformed file was not replaced with a new key: %q, %v", fresh, err)
	}
}

// personad launch sends the key it can read, and nothing when it cannot.
func TestLaunchRequestCarriesTheKey(t *testing.T) {
	launchKeyHome(t)
	if r := launcherEnvRequest("gw"); r.Kind != "launcher.env" || r.Name != "gw" || r.LaunchKey != "" {
		t.Fatalf("no key file: %+v", r)
	}
	k, err := ensureLaunchKey(nodeclient.LaunchKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if r := launcherEnvRequest("gw"); r.LaunchKey != k {
		t.Fatalf("sent %q, want the key in the file", r.LaunchKey)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // no unreadable file for the owner or root
	}
	// A process that may not read the file, as an agent in a sandbox that hides the data directory.
	if err := os.Chmod(nodeclient.LaunchKeyPath(), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(nodeclient.LaunchKeyPath(), 0o600)
	if r := launcherEnvRequest("gw"); r.LaunchKey != "" {
		t.Fatalf("sent %q from an unreadable file", r.LaunchKey)
	}
	// Nor does the node replace a key it failed to read: a launch that has just read it must still get its answer.
	if _, err := ensureLaunchKey(nodeclient.LaunchKeyPath()); err == nil {
		t.Fatal("an unreadable key file passed")
	}
	_ = os.Chmod(nodeclient.LaunchKeyPath(), 0o600)
	if got, err := readLaunchKey(nodeclient.LaunchKeyPath()); err != nil || got != k {
		t.Fatalf("the key was replaced: %q, %v", got, err)
	}
}

// The node's file APIs (file.read, fs.list, downloads) refuse the key like the data folder's other keys.
func TestLaunchKeyIsOffLimitsToFileAPIs(t *testing.T) {
	home := launchKeyHome(t)
	n := &Node{home: home}
	if err := n.offLimits(nodeclient.LaunchKeyPath()); !errors.Is(err, errOffLimits) {
		t.Fatalf("%s through the file APIs: %v", nodeclient.LaunchKeyPath(), err)
	}
}

const launchKeyTestSecret = "sk-test-launch-key-provider-secret"

func launchKeyTestNode() *Node {
	n := &Node{}
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", BaseURL: "http://gw.example.com", Model: "m1", APIKey: launchKeyTestSecret}})
	return n
}

// refusedWithoutSettings fails unless resp is an error that carries none of the launcher's settings nor the key.
func refusedWithoutSettings(t *testing.T, what string, resp map[string]any, key string) {
	t.Helper()
	b, _ := json.Marshal(resp)
	s := string(b)
	if resp["error"] == nil || resp["env"] != nil || resp["args"] != nil || resp["model"] != nil {
		t.Errorf("%s: not refused: %s", what, s)
	}
	for _, leak := range []string{launchKeyTestSecret, "gw.example.com", "ANTHROPIC_", key} {
		if strings.Contains(s, leak) {
			t.Errorf("%s: the refusal says %q: %s", what, leak, s)
		}
	}
}

func TestLauncherEnvNeedsTheLaunchKey(t *testing.T) {
	launchKeyHome(t)
	n := launchKeyTestNode()
	key, err := ensureLaunchKey(nodeclient.LaunchKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(name, k string) map[string]any {
		return n.launcherEnvFor(sockRequest{Kind: "launcher.env", Name: name, LaunchKey: k})
	}
	// The key is the machine's, not one start's: herdr's resume command and a terminal use it again and again.
	for i := 0; i < 2; i++ {
		if env, _ := ask("gw", key)["env"].([]string); !slices.Contains(env, "ANTHROPIC_API_KEY="+launchKeyTestSecret) {
			t.Fatalf("with the key (#%d): %v", i+1, env)
		}
	}
	other := strings.Repeat("0", launchKeyLen)
	for what, k := range map[string]string{"no key": "", "another key": other, "the key cut short": key[:launchKeyLen-1],
		"the key and more": key + "0"} {
		refusedWithoutSettings(t, what, ask("gw", k), key)
	}
	// Refused alike whether the launcher exists or not: no way to learn the names without the key.
	if a, b := ask("gw", "")["error"], ask("nope", "")["error"]; a != b {
		t.Errorf("refusals differ by name: %q / %q", a, b)
	}
	if resp := ask("nope", key); resp["error"] == nil || resp["env"] != nil {
		t.Errorf("an unknown launcher with the key: %v", resp)
	}
}

// Through the real socket, with what personad launch sends: the key the node wrote when it began listening.
func TestLauncherEnvOverTheSocket(t *testing.T) {
	launchKeyHome(t)
	n := launchKeyTestNode()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := n.listenOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ask := func(req sockRequest) map[string]any {
		t.Helper()
		line, err := nodeclient.Dial(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var resp map[string]any
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return resp
	}
	resp := ask(launcherEnvRequest("gw"))
	env, _ := resp["env"].([]any)
	if !slices.Contains(env, any("ANTHROPIC_API_KEY="+launchKeyTestSecret)) {
		t.Fatalf("personad launch's request: %v", resp)
	}
	key, err := readLaunchKey(nodeclient.LaunchKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	refusedWithoutSettings(t, "a caller without the key", ask(sockRequest{Kind: "launcher.env", Name: "gw"}), key)
	refusedWithoutSettings(t, "a caller with another key",
		ask(sockRequest{Kind: "launcher.env", Name: "gw", LaunchKey: strings.Repeat("f", launchKeyLen)}), key)
}
