package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Named sessions (#2196). Besides the default server, an account can run other herdr servers as named sessions
// (`herdr --session <name>`), each with its own socket under <config dir>/sessions/<name>, and its own ids: "w4:p1"
// can be a pane of two of them at once. A node keeps the ids of its own server as herdr gives them and writes those of
// another session as "@<session>:<id>" (QualifyID): no host kind starts with "@", so every reader takes that for a
// herdr pane as before (host.ParsePane, the apps' host kind), and none of it holds a "/" that a path could split on.

// SessionInfo is one session of `herdr session list --json`.
type SessionInfo struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	Running bool   `json:"running"`
	Socket  string `json:"socket_path"`
}

// listTimeout bounds `herdr session list`: it only reads the sessions' directories and knocks on their sockets.
const listTimeout = 5 * time.Second

// ListSessions is `herdr session list --json`: every session of the account, running or stopped.
func (c *Client) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Bin, "session", "list", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr session list: %w", err)
	}
	return parseSessions(out)
}

func parseSessions(out []byte) ([]SessionInfo, error) {
	var res struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("herdr session list: %w", err)
	}
	return res.Sessions, nil
}

// ValidSessionName: name can stand in a qualified id and in a path: letters, digits, '-', '_' and '.', not "." or
// "..". A session named otherwise is left alone.
func ValidSessionName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// QualifyID is a herdr id (pane, tab or workspace) of session as the node writes it: as it is when session is "" (the
// node's own server), "@<session>:<id>" otherwise. An empty id stays empty.
func QualifyID(session, id string) string {
	if session == "" || id == "" {
		return id
	}
	return "@" + session + ":" + id
}

// SplitID undoes QualifyID: the session an id names ("" for one of the node's own server) and herdr's id in it.
func SplitID(id string) (session, bare string) {
	if rest, ok := strings.CutPrefix(id, "@"); ok {
		if name, bare, ok := strings.Cut(rest, ":"); ok && bare != "" && ValidSessionName(name) {
			return name, bare
		}
	}
	return "", id
}

// IsOwnSession: name is a session of another node's data directory (DataDirSession), never one of the user's.
func IsOwnSession(name string) bool { return strings.HasPrefix(name, sessionPrefix) }

// EnvSession is the session a process in a herdr pane runs in, from what herdr puts in its environment:
// HERDR_SESSION, else the session directory HERDR_SOCKET_PATH is in (<config dir>/sessions/<name>/herdr.sock).
// "" for the default server.
func EnvSession(getenv func(string) string) string {
	if s := getenv("HERDR_SESSION"); s != "" {
		if s == defaultSession {
			return ""
		}
		return s
	}
	return SocketSession(getenv("HERDR_SOCKET_PATH"))
}

// SocketSession is the named session whose socket sock is by herdr's layout (<dir>/sessions/<name>/herdr.sock),
// "" for any other path (the default server's among them).
func SocketSession(sock string) string {
	if sock == "" || filepath.Base(sock) != "herdr.sock" {
		return ""
	}
	dir := filepath.Dir(sock)
	if filepath.Base(filepath.Dir(dir)) != "sessions" {
		return ""
	}
	if name := filepath.Base(dir); ValidSessionName(name) && name != defaultSession {
		return name
	}
	return ""
}

// PaneOf is the pane a process says it runs in (HERDR_PANE_ID, in session: EnvSession) as a node whose own server is
// session own writes it: as it is in own, qualified in any other session — the default server's too ("@default:…"),
// for a node on a session of its own.
func PaneOf(pane, session, own string) string {
	if pane == "" || session == own {
		return pane
	}
	if session == "" {
		session = defaultSession
	}
	return QualifyID(session, pane)
}

// OwnName is the session c's server is: Session, else the one its socket's path names ("" the default server).
func (c *Client) OwnName() string {
	if c.Session != "" {
		return c.Session
	}
	return SocketSession(c.Socket)
}

// OnDefaultServer: c talks to the default server, at its usual socket (not one HERDR_SOCKET_PATH moved elsewhere).
func (c *Client) OnDefaultServer() bool {
	if c.Session != "" || c.Socket == "" {
		return false
	}
	return filepath.Clean(c.Socket) == filepath.Clean(sessionSocket(""))
}

// sessionSocket is where herdr's session name listens ("" the default server), whatever this process's environment
// says: herdr.sock in its directory under the config directory.
func sessionSocket(name string) string {
	d := configDir()
	if d == "" {
		return ""
	}
	if name != "" {
		d = filepath.Join(d, "sessions", name)
	}
	return filepath.Join(d, "herdr.sock")
}

// ForSession is a client of the same herdr for another session of the account: sock is where it listens (from
// `herdr session list`; "" works it out).
func (c *Client) ForSession(name, sock string) *Client {
	if sock == "" {
		sock = sessionSocket(name)
	}
	return &Client{Bin: c.Bin, Socket: sock, Session: name}
}
