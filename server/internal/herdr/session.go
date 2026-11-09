package herdr

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
)

// One user account has one default herdr server, and what runs in its panes (`personad launch`, the hooks) finds the
// node by the PERSONA_HOME the server was started with. Two nodes with different data directories on one account (a
// test node next to the everyday one) would share that server, and the Agents of one would register with the other
// (#6482). herdr's own answer to several servers on an account is a named session (HERDR_SESSION): a socket, saved
// workspaces and log of its own, under <config dir>/sessions/<name>, with the same config.toml. So a node whose data
// directory is not one of the default ones uses a session named after that directory, and the node with the default
// directory keeps the default server it always had.

// sessionPrefix starts the name of the session a data directory gets.
const sessionPrefix = "persona-"

// Session is the herdr session this node's herdr is: $HERDR_SESSION when it is set (the user's choice), else none when
// $HERDR_SOCKET_PATH names a socket (also the user's), else the one of a custom data directory (PERSONA_HOME, see
// DataDirSession). "" is the default server. Windows keeps the default one: its named-pipe layout is not checked.
func Session() string {
	if s := os.Getenv("HERDR_SESSION"); s != "" {
		if s == defaultSession {
			return ""
		}
		return s
	}
	if os.Getenv("HERDR_SOCKET_PATH") != "" {
		return ""
	}
	return OwnSession()
}

// OwnSession is the session of this process's data directory (DataDirSession of PERSONA_HOME), whatever
// $HERDR_SESSION and $HERDR_SOCKET_PATH say: what Session is when the user chose nothing. "" on Windows.
func OwnSession() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return DataDirSession(home, identity.DataDir(home))
}

// defaultSession is the name herdr gives the default server's session.
const defaultSession = "default"

// DataDirSession is the session of the data directory dir of an account whose home is home: "" for the default ones
// (~/.persona and the older name), else a name that is the same for the same directory, differs between directories,
// and is made of characters a session name and a path can hold.
func DataDirSession(home, dir string) string {
	dir = filepath.Clean(dir)
	if dir == filepath.Join(home, ".persona") || dir == filepath.Join(home, compat.StateDir) {
		return ""
	}
	sum := sha256.Sum256([]byte(dir))
	return sessionPrefix + hex.EncodeToString(sum[:4])
}

// env is the environment a herdr command of this client runs with: the node's own, naming the session when the client
// talks to one (the CLI would otherwise find the default server), and its socket in place of one the node's
// environment may name (a node started in another session's pane has that one's). nil: inherit.
func (c *Client) env() []string {
	if c.Session == "" {
		return nil
	}
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != "HERDR_SESSION" && name != "HERDR_SOCKET_PATH" {
			out = append(out, kv)
		}
	}
	out = append(out, "HERDR_SESSION="+c.Session)
	if c.Socket != "" {
		out = append(out, "HERDR_SOCKET_PATH="+c.Socket)
	}
	return out
}

// command is exec.Command for a herdr command of this client.
func (c *Client) command(args ...string) *exec.Cmd {
	cmd := exec.Command(c.Bin, args...)
	cmd.Env = c.env()
	return cmd
}
