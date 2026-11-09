"""scripts/hooks/merge-pr.sh merges exactly the head, title and body it scanned, read from one snapshot of the PR.

Each test builds a throwaway clone (isolated HOME and Git config) whose origin is a local bare repository with
refs/heads/main and refs/pull/7/head, and puts a fake gh first on PATH. The fake serves `pr view` from a state file,
answers `repo view` and `api`, and logs every call. Like GitHub with squash messages set to PR_TITLE / PR_BODY, it
records `<title> (#7)` and the body as they are at merge time (`at_merge`: an edit saved after the last view) unless
the merge passes --subject / --body-file. The real gh never runs.
"""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent.parent
WORD = "quill" + "wren"  # a made-up private word, split so the repository scan of this file does not see it
# The variables that name a build config (privacy_check.CONFIG_ENV): left set, they would point the scan at this
# machine's real config.
_spec = importlib.util.spec_from_file_location("privacy_check", SCRIPTS / "privacy_check.py")
_privacy_check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_privacy_check)
CONFIG_ENV = set(_privacy_check.CONFIG_ENV)

FAKE_GH = r'''
import json, os, sys
args = sys.argv[1:]
state_path, log_path = os.environ["FAKE_GH_STATE"], os.environ["FAKE_GH_LOG"]
state = json.load(open(state_path))
entry = {"argv": args, "cwd": os.getcwd()}
out = ""
if args[:2] == ["pr", "view"]:
    out = json.dumps({k: state["pr"][k] for k in args[args.index("--json") + 1].split(",")})
elif args[:2] == ["repo", "view"]:
    out = "example/persona"
elif args[:2] == ["pr", "merge"]:
    now = state.get("at_merge") or state["pr"]
    entry["subject"] = args[args.index("--subject") + 1] if "--subject" in args else now["title"] + " (#7)"
    body_file = args[args.index("--body-file") + 1] if "--body-file" in args else None
    entry["body"] = open(body_file, newline="").read() if body_file else now["body"]
with open(log_path, "a") as log:
    log.write(json.dumps(entry) + "\n")
print(out)
'''


class MergePrTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="merge-pr-test-")
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.repo, self.home, self.origin, bin_dir = root / "repo", root / "home", root / "origin.git", root / "bin"
        for d in (self.repo, self.home, bin_dir):
            d.mkdir()
        (bin_dir / "gh").write_text("#!" + sys.executable + "\n" + FAKE_GH)
        (bin_dir / "gh").chmod(0o755)
        self.state, self.log = root / "state.json", root / "gh.log"
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("GIT_", "GH_", "PYTHON")) and k not in CONFIG_ENV}
        self.env.update({"HOME": str(self.home), "XDG_CONFIG_HOME": str(self.home / ".config"),
                         "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                         "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                         "GIT_AUTHOR_NAME": "Test User", "GIT_AUTHOR_EMAIL": "test@example.com",
                         "GIT_COMMITTER_NAME": "Test User", "GIT_COMMITTER_EMAIL": "test@example.com",
                         "FAKE_GH_STATE": str(self.state), "FAKE_GH_LOG": str(self.log)})
        words = self.home / ".config/persona/private-words.txt"
        words.parent.mkdir(parents=True)
        words.write_text(WORD + "\n")
        self.git("init", "-q", "--bare", str(self.origin))
        self.git("init", "-q", "-b", "main")
        self.git("config", "commit.gpgSign", "false")
        self.git("remote", "add", "origin", str(self.origin))
        (self.repo / "scripts").mkdir()
        shutil.copy2(SCRIPTS / "privacy_check.py", self.repo / "scripts/privacy_check.py")
        shutil.copytree(SCRIPTS / "hooks", self.repo / "scripts/hooks")
        (self.repo / "config.example.json").write_text("{}\n")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "Initial")
        self.git("push", "-q", "origin", "main")

    def git(self, *args):
        result = subprocess.run(("git",) + args, cwd=self.repo, env=self.env, capture_output=True, text=True,
                                timeout=30)
        if result.returncode:
            self.fail(result.stdout + result.stderr)
        return result.stdout.strip()

    def open_pr(self, message="Add notes", content="Public notes.\n"):
        """A commit on top of main pushed as refs/pull/7/head; returns its sha."""
        self.git("checkout", "-q", "-B", "feature", "main")
        (self.repo / "notes.md").write_text(content)
        self.git("add", "notes.md")
        self.git("commit", "-q", "-m", message)
        self.git("push", "-q", "origin", "+HEAD:refs/pull/7/head")
        return self.git("rev-parse", "HEAD")

    def view(self, head, **fields):
        return dict({"headRefOid": head, "headRefName": "fix/7-notes", "isCrossRepository": False,
                     "baseRefName": "main", "title": "fix: add notes", "body": "Adds notes.\n\nFixes #6"}, **fields)

    def merge_pr(self, pr, at_merge=None, arg="7"):
        self.state.write_text(json.dumps({"pr": pr, "at_merge": at_merge}))
        result = subprocess.run(["bash", str(self.repo / "scripts/hooks/merge-pr.sh"), arg], cwd=self.repo,
                                env=self.env, capture_output=True, text=True, timeout=60)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        return result, calls

    def merges(self, calls):
        return [c for c in calls if c["argv"][:2] == ["pr", "merge"]]

    def test_merges_the_scanned_head_title_and_body_once(self):
        head = self.open_pr()
        result, calls = self.merge_pr(self.view(head))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        [merge] = self.merges(calls)
        argv = merge["argv"]
        self.assertIn("--squash", argv)
        self.assertEqual(argv[argv.index("--match-head-commit") + 1], head)
        self.assertEqual(argv[argv.index("--repo") + 1], "example/persona")
        self.assertEqual(merge["subject"], "fix: add notes (#7)")
        self.assertEqual(merge["body"], "Adds notes.\n\nFixes #6")
        self.assertEqual(merge["cwd"], "/")
        self.assertEqual(len([c for c in calls if c["argv"][:2] == ["pr", "view"]]), 1)  # one snapshot
        self.assertIn(["api", "-X", "DELETE", "repos/example/persona/git/refs/heads/fix/7-notes"],
                      [c["argv"] for c in calls])

    def test_an_edit_after_the_scan_does_not_reach_the_merge(self):
        head = self.open_pr()
        edited = self.view(head, title="fix: " + WORD, body="Now names " + WORD + ".")
        result, calls = self.merge_pr(self.view(head), at_merge=edited)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        [merge] = self.merges(calls)
        self.assertEqual((merge["subject"], merge["body"]), ("fix: add notes (#7)", "Adds notes.\n\nFixes #6"))

    def test_a_private_word_blocks_the_merge(self):
        cases = {"title": ({}, {"title": "fix: " + WORD}), "body": ({}, {"body": "Tested on " + WORD + "."}),
                 "commit message": ({"message": "Add " + WORD + " notes"}, {}),
                 "file": ({"content": "Runs on " + WORD + ".\n"}, {})}
        for name, (commit, fields) in cases.items():
            with self.subTest(name):
                self.log.unlink(missing_ok=True)
                result, calls = self.merge_pr(self.view(self.open_pr(**commit), **fields))
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("not merged", result.stderr)
                self.assertEqual(self.merges(calls), [])

    def test_a_moved_head_is_refused(self):
        self.open_pr()
        result, calls = self.merge_pr(self.view(self.git("rev-parse", "main")))
        self.assertEqual(result.returncode, 1)
        self.assertIn("moved", result.stderr)
        self.assertEqual(self.merges(calls), [])

    def test_a_cross_repository_branch_is_not_deleted(self):
        result, calls = self.merge_pr(self.view(self.open_pr(), isCrossRepository=True))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)
        self.assertEqual([c for c in calls if c["argv"][0] == "api"], [])

    def test_a_clone_without_remote_tracking_refs_scans_the_fetched_base(self):
        self.git("config", "--unset-all", "remote.origin.fetch")
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        result, calls = self.merge_pr(self.view(self.open_pr()))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.merges(calls)), 1)

    def test_a_non_number_is_refused_before_gh_runs(self):
        result, calls = self.merge_pr(self.view("0" * 40), arg="7a")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
