import shlex
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parent
HELPER = SCRIPTS / "default-branch.sh"
GIT = shutil.which("git")


class DefaultBranchShTestCase(unittest.TestCase):
    """default-branch.sh against real throwaway git repositories, so the assertions exercise genuine remote/clone
    ref resolution rather than a mocked git (#1064: a main-only remote must work without a master branch)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-default-branch-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.env = {"GIT_CONFIG_NOSYSTEM": "1", "PATH": "/usr/bin:/bin", "HOME": str(self.base / "home")}
        Path(self.env["HOME"]).mkdir()
        self.remote = self.base / "origin.git"
        self.clone = self.base / "clone"
        self.git(self.base, "init", "-q", "--bare", "--initial-branch=main", str(self.remote))
        self.git(self.base, "init", "-q", "--initial-branch=main", str(self.clone))
        self.git(self.clone, "config", "user.name", "Fixture")
        self.git(self.clone, "config", "user.email", "fixture@example.com")
        self.commit("first")
        self.git(self.clone, "remote", "add", "origin", str(self.remote))
        self.git(self.clone, "push", "-q", "-u", "origin", "main")
        # A real clone points origin/HEAD at the remote's default branch; a hand-made init + remote add does not, so
        # create the symbolic ref the way `git clone` would (the fallback tests delete or repoint it deliberately).
        self.git(self.clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

    def git(self, cwd, *args):
        result = subprocess.run([GIT, *args], cwd=cwd, env=self.env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def commit(self, message):
        self.git(self.clone, "commit", "-q", "--allow-empty", "-m", message)
        return self.git(self.clone, "rev-parse", "HEAD")

    def run_helper(self, snippet):
        script = "source %s\n%s\n" % (shlex.quote(str(HELPER)), snippet)
        return subprocess.run(["bash", "-c", script], cwd=self.clone, env=self.env,
                              capture_output=True, text=True, timeout=60)

    def assert_no_master_ref(self):
        result = subprocess.run([GIT, "show-ref", "refs/heads/master", "refs/remotes/origin/master"],
                                cwd=self.clone, env=self.env, capture_output=True, text=True, timeout=30)
        self.assertNotEqual(result.returncode, 0, "a main-only fixture must not gain a master ref")

    def test_resolves_main_on_a_main_only_remote(self):
        # The #1064 symptom: nothing named master exists, yet the default branch must resolve to main.
        self.assert_no_master_ref()
        result = self.run_helper('persona_default_branch')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "main")

    def test_detached_worktree_builds_on_main_without_master(self):
        wt = self.base / "build-wt"
        result = self.run_helper('persona_detached_worktree %s' % shlex.quote(str(wt)))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "main")
        self.assertTrue((wt / ".git").exists())
        # The worktree is detached at the remote main tip, not on a branch and not relying on master.
        tip = self.git(self.clone, "rev-parse", "refs/remotes/origin/main")
        self.assertEqual(self.git(wt, "rev-parse", "HEAD"), tip)
        symbolic = subprocess.run([GIT, "symbolic-ref", "-q", "HEAD"], cwd=wt, env=self.env,
                                  capture_output=True, text=True, timeout=30)
        self.assertNotEqual(symbolic.returncode, 0, "the build worktree must be detached")
        self.assert_no_master_ref()

    def test_detached_worktree_reuses_and_advances_an_existing_worktree(self):
        wt = self.base / "build-wt"
        first = self.run_helper('persona_detached_worktree %s' % shlex.quote(str(wt)))
        self.assertEqual(first.returncode, 0, first.stderr)
        old_tip = self.git(wt, "rev-parse", "HEAD")

        later = self.commit("later")
        self.git(self.clone, "push", "-q", "origin", "main")
        second = self.run_helper('persona_detached_worktree %s' % shlex.quote(str(wt)))
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.git(wt, "rev-parse", "HEAD"), later)
        self.assertNotEqual(old_tip, later)

    def test_falls_back_to_origin_head_then_master_when_the_remote_is_unreachable(self):
        self.git(self.clone, "remote", "set-url", "origin", str(self.base / "missing.git"))
        # ls-remote fails; the clone's origin/HEAD (set by the clone/push) still names main.
        result = self.run_helper('persona_default_branch')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "main")

        # With no origin/HEAD either, master remains the documented last resort.
        self.git(self.clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
        result = self.run_helper('persona_default_branch')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "master")

    def test_fetch_default_branch_fails_on_an_unreachable_remote(self):
        self.git(self.clone, "remote", "set-url", "origin", str(self.base / "missing.git"))
        result = self.run_helper('persona_fetch_default_branch main')
        self.assertNotEqual(result.returncode, 0, "a missing remote must make the fetch fail, not silently pass")


if __name__ == "__main__":
    unittest.main()
