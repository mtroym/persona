package node

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/lazyre"
)

type codexProcess struct {
	pid            int
	home, endpoint string
	pane           string
	// herdrSession is the herdr session pane is in ("" the default server; herdr.EnvSession): the registry writes the
	// pane as the herdr host does (node herdrPaneIn).
	herdrSession string
}

var codexHomePS = lazyre.New(`(?:^|\s)CODEX_HOME=(.*?)(?:\s+[A-Za-z_][A-Za-z_0-9]*=|$)`)

func codexProcessArgs(pid int, args []string, env string) (codexProcess, bool) {
	at := -1
	for i, a := range args {
		if a == "app-server" {
			at = i
			break
		}
	}
	if len(args) == 0 {
		return codexProcess{}, false
	}
	if at < 1 && progName(args[0]) != "codex" { // progName: codex.exe on Windows
		return codexProcess{}, false
	}
	p := codexProcess{pid: pid}
	if at >= 1 {
		p.endpoint = "stdio://"
	}
	herdrEnv := map[string]string{}
	if strings.ContainsRune(env, '\x00') {
		for _, v := range strings.Split(env, "\x00") {
			if strings.HasPrefix(v, "HERDR_PANE_ID=") {
				p.pane = strings.TrimPrefix(v, "HERDR_PANE_ID=")
			}
			for _, name := range []string{herdrSessionEnv, herdrSocketEnv} {
				if value, ok := strings.CutPrefix(v, name+"="); ok {
					herdrEnv[name] = value
				}
			}
			if strings.HasPrefix(v, "CODEX_HOME=") {
				p.home = strings.TrimPrefix(v, "CODEX_HOME=")
			}
		}
	} else if m := codexHomePS.FindStringSubmatch(env); m != nil {
		p.home = m[1]
	}
	if p.pane == "" {
		if m := envVarRe("HERDR_PANE_ID").FindStringSubmatch(env); m != nil {
			p.pane = m[1]
		}
		for _, name := range []string{herdrSessionEnv, herdrSocketEnv} {
			if m := envVarRe(name).FindStringSubmatch(env); m != nil {
				herdrEnv[name] = m[1]
			}
		}
	}
	if p.pane != "" {
		p.herdrSession = herdr.EnvSession(func(name string) string { return herdrEnv[name] })
	}
	for i := at + 1; i < len(args); i++ {
		if args[i] == "--listen" && i+1 < len(args) {
			p.endpoint = args[i+1]
			break
		}
		if strings.HasPrefix(args[i], "--listen=") {
			p.endpoint = strings.TrimPrefix(args[i], "--listen=")
			break
		}
	}
	return p, true
}

func scanCodexProcesses() []codexProcess {
	out, _ := scanCodexProcessesChecked()
	return out
}

// scanCodexProcessesChecked is scanCodexProcesses, and whether the processes could be listed and read: a resume
// (codex_resume.go) must not take "could not tell" for "no Codex holds it". Windows reads its process table
// (scanCodexProcessesOS).
func scanCodexProcessesChecked() ([]codexProcess, bool) {
	if runtime.GOOS == "windows" {
		return scanCodexProcessesOS()
	}
	var out []codexProcess
	ok := true
	if runtime.GOOS == "linux" {
		// The shared walk of this user's /proc entries (userProcesses), as macOS's ps -U below.
		procs := userProcesses()
		for _, pid := range slices.Sorted(maps.Keys(procs)) {
			args := procs[pid]
			// Read no environments of unrelated processes.
			if _, ok := codexProcessArgs(pid, args, ""); !ok {
				continue
			}
			env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
			if err != nil {
				if !os.IsNotExist(err) { // exited since the walk is fine; unreadable is not
					ok = false
				}
				continue
			}
			if p, ok := codexProcessArgs(pid, args, string(env)); ok {
				out = append(out, p)
			}
		}
		return out, ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "pid=,command=", "-U", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if p, ok := codexProcessArgs(pid, fields[1:], line); ok {
			out = append(out, p)
		}
	}
	return out, ok
}
