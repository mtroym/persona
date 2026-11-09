package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Exercise the same JSON contract used by the hub, not a parallel selection helper.
func stashRequest(t *testing.T, n *Node, cwd, op string, selection map[string]any) (map[string]any, error) {
	t.Helper()
	p := map[string]any{"cwd": cwd, "op": op}
	for k, v := range selection {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	res, err := n.gitWrite(context.Background(), "git.stash", raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	return body, nil
}

func selectedStash(t *testing.T, n *Node, cwd string, index int) map[string]any {
	t.Helper()
	body, err := stashRequest(t, n, cwd, "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := body["stashes"].([]any)
	if len(entries) <= index {
		t.Fatalf("missing stash %d: %v", index, body)
	}
	entry := entries[index].(map[string]any)
	oid, _ := entry["oid"].(string)
	if oid != runPullGit(t, cwd, "rev-parse", "stash@{"+strconv.Itoa(index)+"}") {
		t.Fatalf("list must carry the full selected object ID: %v", entry)
	}
	return map[string]any{"index": index, "oid": oid}
}

func TestStashIdentityContract(t *testing.T) {
	for _, op := range []string{"apply", "pop", "drop"} {
		for _, change := range []string{"push", "drop", "pop"} {
			t.Run(op+"/external-"+change, func(t *testing.T) {
				f := newPullFixture(t)
				for _, content := range []string{"selected\n", "other\n"} {
					putFile(t, f.local, "app.txt", content)
					runPullGit(t, f.local, "stash", "push", "-qm", strings.TrimSpace(content))
				}
				selected := selectedStash(t, f.node, f.local, 1)
				if change == "push" {
					putFile(t, f.local, "app.txt", "new\n")
					runPullGit(t, f.local, "stash", "push", "-qm", "new")
				} else {
					runPullGit(t, f.local, "stash", change, "-q", "stash@{0}")
				}
				before := runPullGit(t, f.local, "stash", "list", "--format=%H")
				treeBefore := runPullGit(t, f.local, "diff")
				_, err := stashRequest(t, f.node, f.local, op, selected)
				if err == nil {
					t.Fatal("stale selection succeeded")
				}
				if code, _ := gitCodeOf(t, err); code != "git_stash_stale" {
					t.Fatalf("code %s: %v", code, err)
				}
				if after := runPullGit(t, f.local, "stash", "list", "--format=%H"); after != before {
					t.Fatalf("changed another stash: %s -> %s", before, after)
				}
				if runPullGit(t, f.local, "diff") != treeBefore {
					t.Fatal("applied stale content")
				}
				// A fresh, explicit selection remains usable after recovery.
				if change == "pop" {
					runPullGit(t, f.local, "checkout", "--", "app.txt")
				}
				fresh := selectedStash(t, f.node, f.local, 0)
				if _, err := stashRequest(t, f.node, f.local, op, fresh); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestStashRejectsMissingOrWrongIdentity(t *testing.T) {
	for _, op := range []string{"apply", "pop", "drop"} {
		for _, bad := range []map[string]any{{"index": 0}, {"oid": strings.Repeat("a", 40)}, {"index": -1, "oid": strings.Repeat("a", 40)}, {"index": 9, "oid": strings.Repeat("a", 40)}, {"index": 0, "oid": "--all"}, {"index": 0, "oid": strings.Repeat("b", 40)}, {"index": 0, "oid": strings.Repeat("b", 64)}} {
			t.Run(op+"/"+stringMustJSON(bad), func(t *testing.T) {
				f := newPullFixture(t)
				putFile(t, f.local, "app.txt", "selected\n")
				runPullGit(t, f.local, "stash", "push", "-qm", "selected")
				before := runPullGit(t, f.local, "stash", "list", "--format=%H")
				_, err := stashRequest(t, f.node, f.local, op, bad)
				if err == nil {
					t.Fatal("accepted missing or invalid identity")
				}
				if code, _ := gitCodeOf(t, err); code != "git_stash_stale" {
					t.Fatalf("%s: %v", code, err)
				}
				if runPullGit(t, f.local, "stash", "list", "--format=%H") != before || runPullGit(t, f.local, "diff") != "" {
					t.Fatal("invalid selection mutated repository")
				}
			})
		}
	}
}
func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// A Git shim runs a genuine independent writer at the last Git read before removal,
// or immediately before applying content. It cannot cooperate with the node mutex.
func stashExternalWriter(t *testing.T, cwd, at, oid string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-controlled external Git boundary runs on Unix")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	receipt := filepath.Join(dir, "receipt")
	t.Setenv("STASH_TEST_GIT", real)
	t.Setenv("STASH_TEST_CWD", cwd)
	t.Setenv("STASH_TEST_OID", oid)
	t.Setenv("STASH_TEST_AT", at)
	t.Setenv("STASH_TEST_RECEIPT", receipt)
	script := `#!/bin/sh
case "$*" in
 *"$STASH_TEST_AT"*)
  if [ ! -f "$STASH_TEST_RECEIPT" ]; then
   "$STASH_TEST_GIT" -C "$STASH_TEST_CWD" stash store -m external "$STASH_TEST_OID" >"$STASH_TEST_RECEIPT" 2>&1
   echo $? >>"$STASH_TEST_RECEIPT"
  fi
 ;;
esac
exec "$STASH_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return receipt
}

func TestStashExternalMutationBoundary(t *testing.T) {
	for _, op := range []string{"apply", "pop", "drop"} {
		t.Run(op, func(t *testing.T) {
			f := newPullFixture(t)
			putFile(t, f.local, "app.txt", "selected\n")
			runPullGit(t, f.local, "stash", "push", "-qm", "selected")
			selected := selectedStash(t, f.node, f.local, 0)
			oid := selected["oid"].(string)
			boundary := "rev-parse --verify refs/stash"
			if op == "apply" {
				boundary = "stash apply"
			}
			putFile(t, f.local, "app.txt", "external\n")
			externalOID := runPullGit(t, f.local, "stash", "create", "external")
			runPullGit(t, f.local, "checkout", "--", "app.txt")
			writerCwd := filepath.Join(filepath.Dir(f.local), "writer")
			runPullGit(t, f.local, "worktree", "add", "-qb", "writer", writerCwd)
			receipt := stashExternalWriter(t, writerCwd, boundary, externalOID)
			if _, err := stashRequest(t, f.node, f.local, op, selected); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal("external writer was not exercised: ", err)
			}
			if op == "apply" {
				if !strings.HasSuffix(string(b), "0\n") {
					t.Fatalf("external push failed: %s", b)
				}
				if runPullGit(t, f.local, "stash", "list", "--format=%H") != externalOID+"\n"+oid {
					t.Fatalf("apply changed stack: %q, expected %q", runPullGit(t, f.local, "stash", "list", "--format=%H"), externalOID+"\n"+oid)
				}
			} else {
				if strings.HasSuffix(string(b), "0\n") {
					t.Fatalf("external writer crossed removal lock: %s", b)
				}
				if runPullGit(t, f.local, "stash", "list") != "" {
					t.Fatal("selected stash was not removed")
				}
			}
			if op != "drop" {
				b, _ := os.ReadFile(filepath.Join(f.local, "app.txt"))
				if string(b) != "selected\n" {
					t.Fatal("wrong content applied")
				}
			}
			assertStashLocksReleased(t, f.local)
		})
	}
}

func assertStashLocksReleased(t *testing.T, cwd string) {
	t.Helper()
	common := runPullGit(t, cwd, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	for _, p := range []string{"refs/stash.lock", "logs/refs/stash.lock", "packed-refs.lock"} {
		if _, err := os.Stat(filepath.Join(common, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Fatalf("lock retained: %s (%v)", p, err)
		}
	}
}

func TestStashRemovalStorageAndConflicts(t *testing.T) {
	for _, mode := range []string{"packed", "linked-worktree", "conflict", "busy", "symbolic"} {
		t.Run(mode, func(t *testing.T) {
			f := newPullFixture(t)
			putFile(t, f.local, "app.txt", "selected\n")
			runPullGit(t, f.local, "stash", "push", "-qm", "selected")
			cwd := f.local
			if mode == "packed" {
				runPullGit(t, cwd, "pack-refs", "--all")
			}
			if mode == "linked-worktree" {
				cwd = filepath.Join(t.TempDir(), "linked")
				runPullGit(t, f.local, "worktree", "add", "-qb", "linked", cwd)
			}
			selected := selectedStash(t, f.node, cwd, 0)
			if mode == "conflict" {
				putFile(t, cwd, "app.txt", "conflicting\n")
			}
			if mode == "busy" {
				putFile(t, filepath.Join(f.local, ".git"), "packed-refs.lock", "external lock\n")
			}
			if mode == "symbolic" {
				runPullGit(t, cwd, "update-ref", "refs/saved-stash", selected["oid"].(string))
				runPullGit(t, cwd, "symbolic-ref", "refs/stash", "refs/saved-stash")
			}
			_, err := stashRequest(t, f.node, cwd, "pop", selected)
			switch mode {
			case "conflict", "busy", "symbolic":
				if err == nil {
					t.Fatal("unsafe pop succeeded")
				}
				code, _ := gitCodeOf(t, err)
				want := map[string]string{"conflict": "git_conflict", "busy": "git_busy", "symbolic": "git_stash_unsupported"}[mode]
				if code != want {
					t.Fatalf("%s: %v", code, err)
				}
				if runPullGit(t, cwd, "rev-parse", "stash@{0}") != selected["oid"] {
					t.Fatal("failed pop lost stash")
				}
				if mode == "busy" {
					b, _ := os.ReadFile(filepath.Join(f.local, ".git", "packed-refs.lock"))
					if string(b) != "external lock\n" {
						t.Fatal("foreign lock changed")
					}
					if err := os.Remove(filepath.Join(f.local, ".git", "packed-refs.lock")); err != nil {
						t.Fatal(err)
					}
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if runPullGit(t, cwd, "stash", "list") != "" {
					t.Fatal("pop retained stash")
				}
				// No hidden packed reference may resurrect after deleting the final stash.
				if out, err := git(context.Background(), cwd, "rev-parse", "--verify", "refs/stash"); err == nil {
					t.Fatalf("stash ref retained: %s", out)
				}
			}
			assertStashLocksReleased(t, cwd)
		})
	}
}

func TestStashRemovesOnlySelectedEntry(t *testing.T) {
	for _, op := range []string{"pop", "drop"} {
		for _, index := range []int{0, 1, 2} {
			for _, packed := range []bool{false, true} {
				t.Run(op+"/"+strconv.Itoa(index)+"/packed="+strconv.FormatBool(packed), func(t *testing.T) {
					f := newPullFixture(t)
					for _, content := range []string{"oldest\n", "middle\n", "newest\n"} {
						putFile(t, f.local, "app.txt", content)
						runPullGit(t, f.local, "stash", "push", "-qm", strings.TrimSpace(content))
					}
					if packed {
						runPullGit(t, f.local, "pack-refs", "--all")
					}
					before := strings.Fields(runPullGit(t, f.local, "stash", "list", "--format=%H"))
					selected := selectedStash(t, f.node, f.local, index)
					if _, err := stashRequest(t, f.node, f.local, op, selected); err != nil {
						t.Fatal(err)
					}
					want := append(append([]string{}, before[:index]...), before[index+1:]...)
					if got := runPullGit(t, f.local, "stash", "list", "--format=%H"); got != strings.Join(want, "\n") {
						t.Fatalf("removed wrong entry: %s; want %v", got, want)
					}
					if runPullGit(t, f.local, "rev-parse", "refs/stash") != want[0] {
						t.Fatal("ref and reflog disagree")
					}
					if op == "pop" {
						b, _ := os.ReadFile(filepath.Join(f.local, "app.txt"))
						if string(b) != []string{"newest\n", "middle\n", "oldest\n"}[index] {
							t.Fatal("wrong content applied")
						}
					}
					// A normal terminal push still works after our locks and rewrite are released.
					putFile(t, f.local, "app.txt", "after\n")
					runPullGit(t, f.local, "stash", "push", "-qm", "after")
					if len(strings.Fields(runPullGit(t, f.local, "stash", "list", "--format=%H"))) != 3 {
						t.Fatal("stack did not remain writable")
					}
					assertStashLocksReleased(t, f.local)
				})
			}
		}
	}
}

func TestStashRaceBeforeLockRefusesChangedSelection(t *testing.T) {
	for _, op := range []string{"pop", "drop"} {
		t.Run(op, func(t *testing.T) {
			f := newPullFixture(t)
			putFile(t, f.local, "app.txt", "selected\n")
			runPullGit(t, f.local, "stash", "push", "-qm", "selected")
			selected := selectedStash(t, f.node, f.local, 0)
			putFile(t, f.local, "app.txt", "external\n")
			external := runPullGit(t, f.local, "stash", "create", "external")
			runPullGit(t, f.local, "checkout", "--", "app.txt")
			receipt := stashExternalWriter(t, f.local, "rev-parse --show-ref-format", external)
			_, err := stashRequest(t, f.node, f.local, op, selected)
			if err == nil {
				t.Fatal("changed selection accepted")
			}
			if code, _ := gitCodeOf(t, err); code != "git_stash_stale" {
				t.Fatal(err)
			}
			b, err := os.ReadFile(receipt)
			if err != nil || !strings.HasSuffix(string(b), "0\n") {
				t.Fatalf("external writer not successful: %s %v", b, err)
			}
			if runPullGit(t, f.local, "stash", "list", "--format=%H") != external+"\n"+selected["oid"].(string) || runPullGit(t, f.local, "diff") != "" {
				t.Fatal("rejected request mutated repository")
			}
			assertStashLocksReleased(t, f.local)
		})
	}
}

// stashApplyAsksRefHook reports whether plain `git stash apply` is stopped by a refusing reference-transaction hook in
// hooks. That depends on the Git version: Git 2.50 updates a ref through a transaction during the apply, Git 2.43 updates
// none and never asks the hook. The node's apply is that same command, so it is refused exactly when Git refuses.
func stashApplyAsksRefHook(t *testing.T, hooks string) bool {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "probe")
	runPullGit(t, filepath.Dir(dir), "init", "--initial-branch=main", dir)
	putFile(t, dir, "probe.txt", "first\n")
	runPullGit(t, dir, "add", "--", "probe.txt")
	runPullGit(t, dir, "commit", "-m", "Probe")
	putFile(t, dir, "probe.txt", "second\n")
	runPullGit(t, dir, "stash", "push", "-q")
	runPullGit(t, dir, "config", "core.hooksPath", hooks)
	_, err := git(context.Background(), dir, "stash", "apply")
	return err != nil
}

func TestStashUnsupportedBackendAndHook(t *testing.T) {
	for _, mode := range []string{"reftable", "reference-hook"} {
		t.Run(mode, func(t *testing.T) {
			f := newPullFixture(t)
			cwd := f.local
			if mode == "reftable" {
				// Git before 2.45 has no reftable backend and echoes the option back (stashRefBackend).
				if runPullGit(t, f.local, "rev-parse", "--show-ref-format") == "--show-ref-format" {
					t.Skip("this Git has no reftable backend (Git 2.45+)")
				}
				cwd = filepath.Join(filepath.Dir(f.local), "table")
				runPullGit(t, filepath.Dir(f.local), "clone", "--ref-format=reftable", f.seed, cwd)
			}
			putFile(t, cwd, "app.txt", "selected\n")
			runPullGit(t, cwd, "stash", "push", "-qm", "selected")
			selected := selectedStash(t, f.node, cwd, 0)
			hookRefusesApply := false
			if mode == "reference-hook" {
				hooks := filepath.Join(t.TempDir(), "hooks")
				if err := os.Mkdir(hooks, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				runPullGit(t, cwd, "config", "core.hooksPath", hooks)
				hookRefusesApply = stashApplyAsksRefHook(t, hooks)
			}
			for _, op := range []string{"pop", "drop"} {
				_, err := stashRequest(t, f.node, cwd, op, selected)
				if err == nil {
					t.Fatal("unsupported removal succeeded")
				}
				if code, _ := gitCodeOf(t, err); code != "git_stash_unsupported" {
					t.Fatal(err)
				}
				if runPullGit(t, cwd, "stash", "list", "--format=%H") != selected["oid"] || runPullGit(t, cwd, "diff") != "" {
					t.Fatal("unsupported operation changed repository")
				}
			}
			_, err := stashRequest(t, f.node, cwd, "apply", selected)
			if hookRefusesApply {
				if err == nil {
					t.Fatal("apply bypassed the reference hook")
				}
				if code, _ := gitCodeOf(t, err); code != "git_hook_failed" {
					t.Fatal(err)
				}
				if runPullGit(t, cwd, "stash", "list", "--format=%H") != selected["oid"] {
					t.Fatal("hook rejection lost the stash")
				}
			} else if err != nil {
				t.Fatal("OID-only apply should remain supported: ", err)
			}
		})
	}
}

func TestStashRepositoryOwnership(t *testing.T) {
	f := newPullFixture(t)
	for _, cwd := range []string{f.local, f.seed} {
		putFile(t, cwd, "app.txt", filepath.Base(cwd)+"\n")
		runPullGit(t, cwd, "stash", "push", "-qm", "local selection")
	}
	selected := selectedStash(t, f.node, f.local, 0)
	before := runPullGit(t, f.seed, "stash", "list", "--format=%H")
	for _, op := range []string{"apply", "pop", "drop"} {
		if _, err := stashRequest(t, f.node, f.seed, op, selected); err == nil {
			t.Fatal("another repository accepted selection")
		}
		if runPullGit(t, f.seed, "stash", "list", "--format=%H") != before || runPullGit(t, f.seed, "diff") != "" {
			t.Fatal("other repository mutated")
		}
	}
	assertStashLocksReleased(t, f.local)
	assertStashLocksReleased(t, f.seed)
}

func TestStashSHA256AndMergeConflict(t *testing.T) {
	for _, mode := range []string{"sha256", "merge-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newPullFixture(t)
			cwd := f.local
			if mode == "sha256" {
				cwd = filepath.Join(filepath.Dir(f.local), "sha256")
				runPullGit(t, filepath.Dir(f.local), "init", "--object-format=sha256", "--initial-branch=main", cwd)
				f.commit(t, cwd, "app.txt", "first\n")
			}
			putFile(t, cwd, "app.txt", "selected\n")
			if _, err := git(context.Background(), cwd, "stash", "push", "-qm", "selected"); err != nil {
				if mode == "sha256" {
					// Git 2.43 cannot stash in a SHA-256 repository at all ("Cannot save the current status").
					t.Skipf("this Git cannot stash in a SHA-256 repository: %v", err)
				}
				t.Fatal(err)
			}
			selected := selectedStash(t, f.node, cwd, 0)
			if mode == "sha256" && len(selected["oid"].(string)) != 64 {
				t.Fatal("not a SHA-256 selection")
			}
			if mode == "merge-conflict" {
				f.commit(t, cwd, "app.txt", "conflicting commit\n")
			}
			_, err := stashRequest(t, f.node, cwd, "pop", selected)
			if mode == "merge-conflict" {
				if err == nil {
					t.Fatal("expected merge conflict")
				}
				if code, files := gitCodeOf(t, err); code != "git_conflict" || !slices.Contains(files, "app.txt") {
					t.Fatalf("conflict must report app.txt: %s %v (%v)", code, files, err)
				}
				if runPullGit(t, cwd, "rev-parse", "refs/stash") != selected["oid"] || runPullGit(t, cwd, "ls-files", "--unmerged") == "" {
					t.Fatal("conflict did not retain stash and unmerged files")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			assertStashLocksReleased(t, cwd)
		})
	}
}

// Umask is process-wide. Run the actual node dispatcher in isolated child test
// processes, so the permission proof cannot change another test's creation mode.
func TestStashStoragePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX group permission bits and umask are verified on Unix")
	}
	if os.Getenv("PERSONA_TEST_STASH_UMASK") == "" {
		for _, mask := range []string{"022", "077"} {
			t.Run("umask="+mask, func(t *testing.T) {
				cmd := exec.Command("sh", "-c", `umask "$1"; shift; exec "$@"`, "stash-permissions", mask, os.Args[0], "-test.run=^TestStashStoragePermissions$", "-test.timeout=90s")
				cmd.Env = append(os.Environ(), "PERSONA_TEST_STASH_UMASK="+mask)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated permission acceptance failed: %v\n%s", err, out)
				}
			})
		}
		return
	}
	for _, mode := range []os.FileMode{0664, 0660} {
		t.Run("preserve-existing-"+strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			f := newPullFixture(t)
			for _, content := range []string{"oldest\n", "selected\n"} {
				putFile(t, f.local, "app.txt", content)
				runPullGit(t, f.local, "stash", "push", "-qm", strings.TrimSpace(content))
			}
			runPullGit(t, f.local, "pack-refs", "--all")
			gitDir := filepath.Join(f.local, ".git")
			// Materialize a loose copy so both replaced loose refs and packed refs are covered.
			top := runPullGit(t, f.local, "rev-parse", "refs/stash")
			putFile(t, gitDir, "refs/stash", top+"\n")
			for _, path := range []string{"refs/stash", "logs/refs/stash", "packed-refs"} {
				if err := os.Chmod(filepath.Join(gitDir, filepath.FromSlash(path)), mode); err != nil {
					t.Fatal(err)
				}
			}
			for remaining := 1; remaining >= 0; remaining-- {
				selection := selectedStash(t, f.node, f.local, 0)
				if _, err := stashRequest(t, f.node, f.local, "drop", selection); err != nil {
					t.Fatal(err)
				}
				paths := []string{"logs/refs/stash", "packed-refs"}
				if remaining > 0 {
					paths = append(paths, "refs/stash")
				}
				for _, path := range paths {
					fi, err := os.Stat(filepath.Join(gitDir, filepath.FromSlash(path)))
					if err != nil {
						t.Fatal(err)
					}
					if fi.Mode().Perm() != mode {
						t.Fatalf("%s lost existing mode: got %04o, want %04o", path, fi.Mode().Perm(), mode)
					}
				}
			}
			assertStashLocksReleased(t, f.local)
		})
	}
	for _, sharing := range []string{"umask", "false", "group", "true", "1", "all", "world", "everybody", "2", "0660", "0640", "bare", ""} {
		t.Run("new-loose-ref/shared="+sharing, func(t *testing.T) {
			f := newPullFixture(t)
			for _, content := range []string{"oldest\n", "selected\n"} {
				putFile(t, f.local, "app.txt", content)
				runPullGit(t, f.local, "stash", "push", "-qm", strings.TrimSpace(content))
			}
			runPullGit(t, f.local, "pack-refs", "--all")
			if sharing == "bare" {
				config, err := os.OpenFile(filepath.Join(f.local, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = config.WriteString("\n[core]\n\tsharedRepository\n")
				closeErr := config.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			} else {
				runPullGit(t, f.local, "config", "core.sharedRepository", sharing)
			}
			selection := selectedStash(t, f.node, f.local, 0)
			// Git itself is the permissions oracle for a newly created ref under this setting.
			runPullGit(t, f.local, "update-ref", "refs/permission-control", selection["oid"].(string))
			control, err := os.Stat(filepath.Join(f.local, ".git", "refs", "permission-control"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(f.local, ".git", "refs", "stash")); !os.IsNotExist(err) {
				t.Fatalf("fixture must start with packed-only stash ref: %v", err)
			}
			if _, err := stashRequest(t, f.node, f.local, "drop", selection); err != nil {
				t.Fatal(err)
			}
			ref, err := os.Stat(filepath.Join(f.local, ".git", "refs", "stash"))
			if err != nil {
				t.Fatal(err)
			}
			if ref.Mode().Perm() != control.Mode().Perm() {
				t.Fatalf("new stash ref does not honor Git sharing: got %04o, Git created %04o", ref.Mode().Perm(), control.Mode().Perm())
			}
			assertStashLocksReleased(t, f.local)
		})
	}
}

// Git before 2.45 echoes the unknown --show-ref-format option; pop and drop must still work there.
func TestStashRefBackendOlderGit(t *testing.T) {
	for out, want := range map[string]string{"files\n": "files", "reftable\n": "reftable", "--show-ref-format\n": "files"} {
		if got := stashRefBackend(out); got != want {
			t.Errorf("stashRefBackend(%q) = %q, want %q", out, got, want)
		}
	}
}
