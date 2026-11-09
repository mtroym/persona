"""check-fast.sh runs the scripts/ unittests a change needs (#3968), and the scripts/test suites that guard docs,
markdown, apple/ and deploy/docker/.

Each test builds a throwaway repository with check-fast.sh and stubs for the repository-wide checks it always picks
for a scripts/ change (the privacy scan, the extension schema test, the PR workflow test), commits a base, makes a
change on top and runs check-fast.sh --base against the base. The default-base tests run it without --base, against
refs/remotes/origin/* refs they write themselves.
"""
import fnmatch
import importlib.util
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
PASSING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        pass\n"
FAILING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_broken(self):\n        self.fail('broken on purpose')\n"
# The scripts/test suites that guard files outside scripts/; stubbed passing unless a test breaks one.
GUARD_SUITES = ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_wire.py", "test_apple_l10n.py",
                "test_docker_entrypoint.py")
# A python3 that records each call ("<cwd>|<args>") instead of running anything.
FAKE_PYTHON = '#!/bin/sh\nprintf "%s|%s\\n" "$PWD" "$*" >> "$FAKE_PYTHON_LOG"\n'


def ci_is_suite():
    """is_suite of scripts/ci/python-tests.py: the rule CI's Scripts tests job picks its suites by."""
    saved = list(sys.path)  # the module puts scripts/ci and scripts/test on sys.path for its own imports
    try:
        spec = importlib.util.spec_from_file_location("ci_python_tests", ROOT / "scripts/ci/python-tests.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    finally:
        sys.path[:] = saved
    return module.is_suite


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastScriptsTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/tools/other.py", "")
        self.write("scripts/tools/test_other.py", FAILING)  # not related to the changes below: must not run
        self.write("scripts/tools/gone.py", "")
        self.write("scripts/tools/test_gone.py", PASSING)
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def break_suite(self, name):
        """Make scripts/test/<name> fail and commit it into the base, so only the change made afterwards selects it."""
        self.commit_on_top(f"scripts/test/{name}", FAILING, "base")

    def check_fast(self, *args, path=None, env=None, base="base"):
        # TMPDIR inside the throwaway repo: a failed run keeps its log directory, and the cleanup removes it.
        # base=None runs without --base, so the script picks its default base.
        env = dict(os.environ, TMPDIR=str(self.repo), **({"PATH": path} if path else {}), **(env or {}))
        args = args or (("--base", base) if base else ())
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", *args], cwd=self.repo,
                             capture_output=True, text=True, timeout=300, env=env)
        return run.returncode, run.stdout + run.stderr

    def commit_on_top(self, path, text, tag):
        """Commit one more file on top of HEAD and point tag at the new commit."""
        self.write(path, text)
        self.git("add", path)
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", tag)
        self.git("tag", "-f", tag)

    def test_without_base_it_diffs_against_origin_head(self):
        # The default branch's tip carries a failing test; a stale origin/master left at the first commit would
        # select it too, and fail the run.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_origin_head_wins_over_origin_main(self):
        # A default branch that is neither main nor master; origin/main left at the first commit would select the
        # failing test on top of it.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/trunk", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/trunk", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_plain_origin_head_still_counts(self):
        # origin/HEAD written as a plain ref to a commit, not as a symbolic ref.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/HEAD", "mid")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/HEAD", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_dangling_origin_head_falls_back_to_origin_main(self):
        # origin/HEAD is set at clone time; it can name a branch that was deleted since (here: master).
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)

    def test_without_base_a_legacy_clone_diffs_against_origin_master(self):
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast(base=None)
        self.assertEqual(1, code, out)
        self.assertIn("merge-base with refs/remotes/origin/master", out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)

    def test_without_base_or_origin_refs_it_stops_and_says_how_to_fix_it(self):
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(2, code, out)
        self.assertIn("no default base", out)
        self.assertIn("--base <ref>", out)
        self.assertNotIn("test_new.py", out)

    def test_an_explicit_base_wins_over_origin_head(self):
        self.commit_on_top("scripts/tools/test_mid.py", PASSING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        code, out = self.check_fast("--base", "base")
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with base", out)
        self.assertIn("PASS     scripts/tools/test_mid.py", out)

    def test_an_empty_base_is_refused_not_read_as_no_base(self):
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        for args in (("--base", ""), ("--base=",)):
            with self.subTest(args=args):
                code, out = self.check_fast(*args)
                self.assertEqual(2, code, out)
                self.assertIn("--base needs a git ref", out)
                self.assertNotIn("test_new.py", out)

    def test_a_changed_test_is_run_and_its_failure_fails(self):
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_changed_script_runs_its_test_from_its_directory(self):
        # sim-boot.sh is tested by test_sim_boot.py: a dash in the name is an underscore in the test's.
        # Run from scripts/tools, as CI does: a relative fixture path resolves only there.
        self.write("scripts/tools/fixture.txt", "x")
        self.write("scripts/tools/test_tool_name.py", PASSING.replace("pass", "open('fixture.txt').close()"))
        self.git("add", "scripts/tools/test_tool_name.py", "scripts/tools/fixture.txt")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/tool-name.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/tools/test_tool_name.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_shell_script_named_with_its_extension_runs_its_test(self):
        self.write("scripts/tools/db.sh", "echo changed\n")
        self.write("scripts/tools/test_db_sh.py", FAILING)
        self.git("add", "scripts/tools/test_db_sh.py")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/db.sh", "echo changed again\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_db_sh.py", out)

    def test_a_deleted_test_is_not_run(self):
        (self.repo / "scripts/tools/test_gone.py").unlink()
        (self.repo / "scripts/tools/gone.py").unlink()
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertNotIn("test_gone.py", out)

    def test_a_doc_change_runs_the_doc_path_suites(self):
        self.break_suite("test_doc_paths.py")
        self.write("docs/x.md", "see `scripts/missing.py`\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_wire.py", out)
        self.assertNotIn("test_apple_l10n.py", out)
        self.assertNotIn("test_docker_entrypoint.py", out)

    def test_a_top_level_markdown_change_runs_the_doc_path_suites(self):
        self.write("CLAUDE.md", "index\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_go_paths.py", out)
        self.assertIn("PASS     scripts/test/test_doc_wire.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_a_change_to_the_wire_lists_runs_the_doc_wire_suite(self):
        # test_doc_wire.py reads these sources: a new message type, flag, point or field breaks it from the code side.
        self.break_suite("test_doc_wire.py")
        for path in ("server/internal/proto/proto.go", "server/internal/hub/extensions_link.go",
                     "server/internal/extension/points.go", "schema/extension/manifest.schema.json"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast(path=self.toolchain_without_node_and_go())
                self.git("clean", "-fdq", "--", path.split("/", 1)[0])
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_doc_wire.py", out)
                self.assertNotIn("test_doc_paths.py", out)

    def test_an_apple_change_runs_the_l10n_suite(self):
        self.break_suite("test_apple_l10n.py")
        self.write("apple/Persona/Views/Settings/Options.swift", 'let s = "中文"\n')
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_apple_l10n.py", out)
        self.assertIn("SKIP  apple/** builds and tests", out)  # the Mac-only checks are still reported
        self.assertNotIn("test_doc_paths.py", out)

    def test_a_docker_change_runs_the_entrypoint_suite(self):
        self.break_suite("test_docker_entrypoint.py")
        self.write("deploy/docker/entrypoint.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)
        self.assertNotIn("test_doc_paths.py", out)

    def test_a_root_dockerfile_change_runs_the_entrypoint_suite(self):
        # test_docker_entrypoint.py asserts the EXPOSE line of the repo-root Dockerfile.
        self.break_suite("test_docker_entrypoint.py")
        self.write("Dockerfile", "EXPOSE 18660\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_docker_entrypoint.py", out)
        self.assertNotIn("no fast check maps to", out)

    def test_the_docker_doc_runs_both_the_doc_and_the_entrypoint_suites(self):
        self.write("docs/operations/docker.md", "changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_docker_entrypoint.py", out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)

    def test_all_runs_every_scripts_suite_in_one_job(self):
        # Mirror CI's Scripts tests job: --all runs every scripts/**/test_*.py, changed or not, except the ones a
        # dedicated job already runs. That stub fails under unittest, so the suite job must not pick it up. The count
        # is 8: test_other, test_gone, test_deep and the five guard suites stubbed in setUp.
        self.write("scripts/test/test_external_service_refs.py", FAILING)
        self.write("scripts/deep/nested/test_deep.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # missing node and go are core failures in --all mode
        self.assertIn("queued: scripts unittest suites (8)", out)
        self.assertIn("FAIL     scripts unittest suites", out)
        self.assertIn("8 suite(s): 1 failed, 0 timed out", out)
        self.assertIn("failed: scripts/tools/test_other.py", out)
        self.assertNotIn("failed: scripts/test/test_external_service_refs.py", out)
        self.assertNotIn("queued: scripts/tools/test_gone.py", out)  # one job, not one per suite

    def test_a_test_file_that_never_mentions_unittest_is_skipped_as_in_ci(self):
        # python -m unittest runs no tests in such a verification script and exits 5 ("NO TESTS RAN") from Python 3.12
        # on, so CI's python-tests.py skips it. The --all count is the seven suites of setUp.
        self.write("scripts/tools/test_verify.py", "print('a verification script that takes arguments')\n")
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # test_other.py fails, and node and go are missing
        self.assertIn("SKIP  scripts/tools/test_verify.py (not a unittest suite)", out)
        self.assertIn("7 suite(s): 1 failed, 0 timed out", out)
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("SKIP  scripts/tools/test_verify.py (not a unittest suite)", out)
        self.assertNotIn("queued: scripts/tools/test_verify.py", out)

    def test_all_runs_the_suites_ci_runs(self):
        # Drift guard: on a copy of every real test_*.py, --all runs python-tests.py's suites minus the ones the
        # dedicated jobs run (script_test_add), and reports each file CI skips. A python3 stub records the runs.
        for real in ROOT.joinpath("scripts").rglob("test_*.py"):
            if "node_modules" not in real.parts:
                self.write(real.relative_to(ROOT), real.read_text(encoding="utf-8", errors="replace"))
        is_suite = ci_is_suite()
        found = {str(p.relative_to(self.repo)): is_suite(p) for p in self.repo.joinpath("scripts").rglob("test_*.py")}
        dedicated = {"scripts/test/test_external_service_refs.py", "scripts/test/test_sdk_bundle.py",
                     "scripts/test/test_extension_schema.py"}
        expected = sorted(p for p, suite in found.items()
                          if suite and p not in dedicated and not p.startswith("scripts/app/test_android_"))
        bin_dir = Path(self.toolchain_without_node_and_go())
        (bin_dir / "python3").unlink()
        (bin_dir / "python3").write_text(FAKE_PYTHON)
        (bin_dir / "python3").chmod(0o755)
        log = bin_dir / "python.log"
        code, out = self.check_fast("--all", path=str(bin_dir), env={"FAKE_PYTHON_LOG": str(log)})
        ran = sorted(f"{Path(cwd).resolve().relative_to(self.repo.resolve())}/{args.split()[-1]}.py"
                     for cwd, args in (line.split("|", 1) for line in log.read_text().splitlines())
                     if args.startswith("-B -m unittest -q "))
        self.assertIn("scripts/test/test_check_fast.py", expected)
        self.assertEqual(expected, ran, out)
        for path in (p for p, suite in found.items() if not suite):
            self.assertIn(f"SKIP  {path} (not a unittest suite)", out)

    def test_a_hook_change_runs_the_privacy_suite(self):
        # test_privacy_check.py copies scripts/hooks into its temp repositories and runs the hooks there; a hook has no
        # .py or .sh name for the name mapping to match.
        self.break_suite("test_privacy_check.py")
        self.write("scripts/hooks/pre-push", "#!/bin/sh\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_privacy_check.py", out)

    def test_a_privacy_check_change_also_runs_the_suites_that_load_it(self):
        # scripts/release/check_bundle_privacy.py imports privacy_check; test_merge_pr_sh.py copies it into its temp
        # repositories, where merge-pr.sh runs it.
        self.commit_on_top("scripts/test/test_privacy_check.py", PASSING, "base")
        self.commit_on_top("scripts/release/test_check_bundle_privacy.py", FAILING, "base")
        self.commit_on_top("scripts/test/test_merge_pr_sh.py", FAILING, "base")
        self.write("scripts/privacy_check.py", "print('clean')\n# changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("PASS     scripts/test/test_privacy_check.py", out)
        self.assertIn("FAIL     scripts/release/test_check_bundle_privacy.py", out)
        self.assertIn("FAIL     scripts/test/test_merge_pr_sh.py", out)

    def test_a_migration_edit_runs_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id int);\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "migrations")
        self.git("tag", "-f", "base")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id text);\n")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertIn("PASS     frozen migrations", out)
        self.assertIn("server go tests (internal/db)", out)  # no Go module here; the freeze check still ran

    def test_an_unrelated_change_does_not_run_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "checker")
        self.git("tag", "-f", "base")
        self.write("docs/guide.md", "changed\n")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertNotIn("frozen migrations", out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)

    def test_renaming_a_migration_out_of_its_directory_runs_the_freeze_check(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        self.write("server/internal/db/migrations/hub/0001_init.sql", "CREATE TABLE t (id int);\n")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "migrations")
        self.git("tag", "-f", "base")
        (self.repo / "notes").mkdir()
        self.git("mv", "server/internal/db/migrations/hub/0001_init.sql", "notes/moved.sql")
        code, out = self.check_fast(path=self.toolchain_without_node_and_go())
        self.assertEqual(0, code, out)
        self.assertIn("PASS     frozen migrations", out)
        self.assertNotIn("server go test", out)  # the destination is outside server/, and --name-only drops the old path

    def test_all_runs_the_freeze_check_when_the_script_exists(self):
        self.write("scripts/ci/migration_freeze.py", "print('freeze-ran')\n")
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # the stubbed scripts/tools/test_other.py fails, and node/go are missing
        self.assertIn("PASS     frozen migrations", out)
        self.assertIn("queued: scripts unittest suites (7)", out)

    def test_all_reports_a_slow_suite_as_timeout_and_runs_the_rest(self):
        self.write("scripts/tools/test_other.py", PASSING)
        self.write("scripts/tools/test_slow.py", PASSING.replace("pass", "__import__('time').sleep(30)"))
        self.write("scripts/tools/test_z_after.py", FAILING)  # sorted after the slow one: it still runs
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertEqual(1, code, out)
        self.assertIn("9 suite(s): 1 failed, 1 timed out", out)
        self.assertIn("timed out: scripts/tools/test_slow.py", out)
        self.assertIn("failed: scripts/tools/test_z_after.py", out)
        self.write("scripts/tools/test_z_after.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertIn("TIMEOUT  scripts unittest suites (9)", out)
        self.assertIn("1 suite(s) timed out: scripts/tools/test_slow.py", out)

    def toolchain_without_node_and_go(self):
        # A PATH with only the tools check-fast.sh itself needs, so node and go are missing on any machine. It lives
        # outside the repository, so a diff-mode run does not count it as a change.
        if getattr(self, "bin_dir", None):
            return str(self.bin_dir)
        self.bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, self.bin_dir)
        bin_dir = self.bin_dir
        for tool in ("bash", "python3", "timeout", "gtimeout", "git", "mktemp", "rm", "dirname", "basename", "find",
                     "sort", "tee", "tail", "grep", "sed", "awk", "tr", "wc", "cut", "cat", "env"):
            found = shutil.which(tool)
            if found:
                (bin_dir / tool).symlink_to(found)
        return str(bin_dir)


FAKE_GO = '#!/bin/sh\nprintf "%s|%s\\n" "${PWD##*/}" "$*" >> "$FAKE_GO_LOG"\n'
# The package lists of check-fast's two repo-wide guard calls: whole packages, and the named guard tests (-run) of two
# packages whose other tests are slow or need Postgres.
GUARD_PACKAGES = "./internal/lazyre ./internal/archguard ./internal/tzguard ./plugins"
GUARD_RUN_PACKAGES = "./internal/hub ./internal/assistant"


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastServerInputsTests(unittest.TestCase):
    """Repository inputs outside server/ that server tests read run go test on the packages that read them."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/test/server_test_env.py", "")
        self.write("scripts/test/test_sdk_bundle.py", "")  # a schema/ change also runs the sdk bundle and sdk/go jobs
        self.write("sdk/go/go.mod", "module example.com/sdk\n")
        self.write("spec/fixtures/x.json", "{}\n")
        self.write("spec/tokens/colors.json", "{}\n")
        self.write("docs/example-guide.md", "guide\n")
        self.write("docs/other.md", "other\n")
        self.write("schema/extension/examples/Manifest.x.json", "{}\n")
        self.write("web/package.json", "{}\n")  # a schema/extension change also runs the web check (stubbed npm)
        self.write("server/go.mod", "module example.com/server\n")
        self.write("server/internal/foo/foo_test.go", 'package foo\n\nconst dir = "../../../spec/fixtures/x"\n')
        self.write("server/internal/joined/joined_test.go",
                   'package joined\n\nvar dir = filepath.Join("..", "..", "..", "spec", "fixtures", "wire")\n')
        self.write("server/internal/noted/noted_test.go", 'package noted\n\n// Same as spec/fixtures/x.json.\n')
        self.write("server/internal/bar/bar_test.go", "package bar\n")
        self.write("server/internal/guide/conformance/guide_test.go",
                   'package conformance\n\nconst guide = "docs/example-guide.md"\n')
        self.write("server/internal/ext/ext_test.go",
                   'package ext\n\nvar d = filepath.Join("..", "schema", "extension", "examples")\n')
        self.write("server/internal/db/dbtest/env_test.go",
                   'package dbtest\n\nconst launcher = "../../../../scripts/test/server_test_env.py"\n')
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        # A fake go outside the repository records each call ("<cwd basename>|<args>") instead of building anything.
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-go-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        (bin_dir / "go").write_text(FAKE_GO)
        (bin_dir / "go").chmod(0o755)
        (bin_dir / "npm").write_text("#!/bin/sh\n")  # passes npm ci, typecheck and vitest without running them
        (bin_dir / "npm").chmod(0o755)
        self.go_log = bin_dir / "calls.log"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_GO_LOG": str(self.go_log)}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def check_fast(self):
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=self.env,
                             capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(0, run.returncode, out)
        calls = self.go_log.read_text().splitlines() if self.go_log.exists() else []
        # The package pattern is the last argument of each recorded "go test ... ./internal/<pkg>/..." call.
        return out, sorted(call.split()[-1] for call in calls if call.startswith("server|test "))

    def go_calls(self):
        """Every recorded go call ("<cwd basename>|<args>") without the -timeout value check-fast derives from its budget."""
        calls = self.go_log.read_text().splitlines() if self.go_log.exists() else []
        return [re.sub(r" -timeout \S+", "", call) for call in calls]

    def guard_calls(self):
        return [call for call in self.go_calls() if call.endswith((" " + GUARD_PACKAGES, " " + GUARD_RUN_PACKAGES))]

    def test_a_spec_fixture_runs_the_packages_that_read_it(self):
        self.write("spec/fixtures/x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertIn("queued: server go test ./internal/foo/...", out)
        # filepath.Join elements name the fixtures too; a // comment that mentions them is not a reader.
        self.assertEqual(["./internal/foo/...", "./internal/joined/..."], packages, out)
        self.assertNotIn("no fast check maps to", out)

    def test_other_spec_files_stay_unmapped(self):
        self.write("spec/tokens/colors.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual([], packages, out)
        self.assertIn("no fast check maps to: spec/tokens/colors.json", out)

    def test_a_doc_runs_only_the_package_that_reads_it(self):
        self.write("docs/example-guide.md", "guide changed\n")
        self.write("docs/other.md", "other changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/guide/conformance/..."], packages, out)

    def test_a_schema_example_runs_its_readers(self):
        self.write("schema/extension/examples/Manifest.x.json", '{"changed": true}\n')
        out, packages = self.check_fast()
        self.assertEqual(["./internal/ext/..."], packages, out)

    def test_the_server_test_launcher_runs_its_reader(self):
        self.write("scripts/test/server_test_env.py", "# changed\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/db/dbtest/..."], packages, out)

    def test_a_server_go_change_also_runs_the_repo_wide_guards(self):
        # The guards fail on code outside their own package (#1936 broke the lazyre guard on main that way), so a change
        # in any package runs them next to that package's own tests.
        self.write("server/internal/foo/x.go", "package foo\n")
        out, packages = self.check_fast()
        self.assertIn("queued: server repo-wide guards", out)
        self.assertIn("./internal/foo/...", packages)
        calls = self.guard_calls()
        self.assertEqual(2, len(calls), out)
        self.assertEqual("server|test -count=1 " + GUARD_PACKAGES, calls[0])
        match = re.fullmatch(r"server\|test -count=1 -run \^\(([\w|]+)\)\$ " + re.escape(GUARD_RUN_PACKAGES), calls[1])
        self.assertIsNotNone(match, calls[1])
        # go test -run passes when a name matches nothing: every named guard must exist, and every package directory.
        sources = "".join(path.read_text() for package in GUARD_RUN_PACKAGES.split()
                          for path in (ROOT / "server" / package).glob("*_test.go"))
        defined = set(re.findall(r"^func (Test\w+)\(t \*testing\.T\)", sources, re.M))
        self.assertEqual(set(), set(match.group(1).split("|")) - defined)
        for package in (GUARD_PACKAGES + " " + GUARD_RUN_PACKAGES).split():
            self.assertTrue((ROOT / "server" / package).is_dir(), package)

    def test_a_plugin_change_runs_its_own_tests_and_the_guards_not_a_build(self):
        self.write("server/plugins/wechat/x.go", "package wechat\n")
        out, packages = self.check_fast()
        self.assertIn("./plugins/wechat/...", packages)
        self.assertEqual(2, len(self.guard_calls()), out)
        self.assertNotIn("server|build ./...", self.go_calls(), out)

    def test_an_sdk_go_change_runs_the_guards(self):
        # The plugins guard reads sdk/go: a plugin uses only the SDK, and the SDK stands alone.
        self.write("sdk/go/x.go", "package sdk\n")
        out, _ = self.check_fast()
        self.assertEqual(2, len(self.guard_calls()), out)

    def test_a_docs_or_web_change_runs_no_guard(self):
        self.write("docs/example-guide.md", "guide changed\n")  # a server test reads it: that package still runs
        self.write("web/src/x.ts", "export {}\n")
        out, packages = self.check_fast()
        self.assertEqual(["./internal/guide/conformance/..."], packages, out)
        self.assertEqual([], self.guard_calls(), out)
        self.assertNotIn("repo-wide guards", out)


# A privacy-scan stub that never finishes on its own: it records its own pid, its parent's (the GNU timeout that runs
# it) and a sleeping child's, then waits.
HANGING_CHECK = """import os, subprocess, sys, time
child = subprocess.Popen(["sleep", "60"])
with open(sys.argv[0] + ".pids.tmp", "w") as f:
    f.write(f"{os.getpid()} {os.getppid()} {child.pid}")
os.replace(sys.argv[0] + ".pids.tmp", sys.argv[0] + ".pids")
time.sleep(60)
"""


@unittest.skipUnless(shutil.which("ps"), "the signal tests list processes with ps")
@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastSignalTests(unittest.TestCase):
    """Ctrl-C, SIGTERM and SIGHUP to check-fast.sh stop the checks it started instead of leaving them orphaned."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-signal-"))
        self.addCleanup(shutil.rmtree, self.repo, True)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        (self.repo / "scripts/privacy_check.py").write_text(HANGING_CHECK)
        (self.repo / "scripts/test/test_extension_schema.py").write_text("")
        (self.repo / "scripts/test/pr_needs_issue_test.mjs").write_text("")
        for args in (["init", "-q"], ["add", "-A"],
                     ["-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"],
                     ["tag", "base"]):
            subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)
        with (self.repo / "scripts/privacy_check.py").open("a") as f:
            f.write("# changed\n")

    def survivors(self, pids):
        """Live processes from this run: any whose command line names the throwaway repo, plus the recorded pids."""
        ps = subprocess.run(["ps", "-A", "-o", "pid=", "-o", "stat=", "-o", "args="], capture_output=True, text=True,
                            check=True).stdout
        left = []
        for line in ps.splitlines():
            pid, stat, args = (line.split(None, 2) + ["", ""])[:3]
            if stat.startswith("Z"):
                continue
            if str(self.repo) in args or int(pid) in pids:
                left.append(line.strip())
        return left

    def interrupt(self, sig, code, group=False):
        pid_file = self.repo / "scripts/privacy_check.py.pids"
        # An absolute script path puts the repo into the job subshells' command lines; TMPDIR puts it into tee's.
        # start_new_session keeps a stray signal off the test runner.
        proc = subprocess.Popen(["bash", str(self.repo / "scripts/test/check-fast.sh"), "--base", "base"],
                                cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo)), stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, start_new_session=True)
        pids = set()
        try:
            deadline = time.monotonic() + 30
            while not pid_file.exists():
                self.assertIsNone(proc.poll(), "check-fast.sh exited before the stub check started")
                self.assertLess(time.monotonic(), deadline, "the stub check never started")
                time.sleep(0.1)
            pids = {int(p) for p in pid_file.read_text().split()}
            if group:
                # A terminal's Ctrl-C or hangup, kill -- -PGID and runners that time out signal the whole process
                # group: the job subshells and tee get the signal too, not just the script.
                os.killpg(proc.pid, sig)
            else:
                proc.send_signal(sig)
            out, _ = proc.communicate(timeout=15)
            self.assertEqual(code, proc.returncode, out)
            self.assertIn("check-fast: interrupted", out)
            self.assertNotIn("Killed", out)  # bash's report of each job subshell it KILLed stays off the output
            left = self.survivors(pids)
            deadline = time.monotonic() + 5
            while left and time.monotonic() < deadline:
                time.sleep(0.2)
                left = self.survivors(pids)
            self.assertEqual([], left, out)
            # The interrupted run keeps its log directory (made under TMPDIR, the throwaway repo).
            self.assertTrue([p for p in self.repo.glob("check-fast.*") if p.is_dir()], out)
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
            for pid in pids:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass

    def test_sigterm_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143)

    def test_sigint_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130)

    def test_sighup_stops_the_running_checks(self):
        self.interrupt(signal.SIGHUP, 129)

    def test_sigint_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130, group=True)

    def test_sigterm_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143, group=True)

    def test_sighup_to_the_process_group_stops_the_running_checks(self):
        # Closing a terminal or a tmux pane sends SIGHUP to the whole foreground process group.
        self.interrupt(signal.SIGHUP, 129, group=True)


def web_lane_inputs():
    """The paths outside web/ that select CI's web lane in scripts/ci/paths.py, minus CI's own runner files.

    scripts/ci/web.sh and the workflow decide how CI runs the web checks, not what typecheck and vitest read, so a change
    to one of them does not need the local web check.
    """
    spec = importlib.util.spec_from_file_location("ci_paths", ROOT / "scripts/ci/paths.py")
    paths = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(paths)
    return [p for p in paths.FILTERS["web"] if not p.startswith(("web/", "scripts/ci/", ".github/"))]


# A fake node that reports major version 22 (check-fast.sh's gate) and passes anything else, and a fake npm that
# records each call ("<cwd basename>|<args>") instead of installing or testing anything.
FAKE_NODE = '#!/bin/sh\nif [ "$1" = -e ]; then echo 22; fi\n'
FAKE_NPM = '#!/bin/sh\nprintf "%s|%s\\n" "${PWD##*/}" "$*" >> "$FAKE_NPM_LOG"\n'


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastWebInputsTests(unittest.TestCase):
    """Repository files outside web/ that web/src and its vitest suite read run the web check when they change."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/test_sdk_bundle.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.write("sdk/go/go.mod", "module example.com/sdk\n")
        self.write("web/package.json", "{}\n")
        self.write("schema/other.json", "{}\n")
        self.write("docs/guide.md", "guide\n")
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")
        # An installed (untracked, ignored) node_modules, so the web check skips npm ci as on a set-up checkout.
        (self.repo / "web/node_modules").mkdir()
        self.write(".git/info/exclude", "web/node_modules/\n")
        bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, bin_dir)
        for name, text in (("node", FAKE_NODE), ("npm", FAKE_NPM), ("go", FAKE_GO)):
            (bin_dir / name).write_text(text)
            (bin_dir / name).chmod(0o755)
        self.npm_log = bin_dir / "npm.log"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_NPM_LOG": str(self.npm_log), "FAKE_GO_LOG": str(bin_dir / "go.log")}

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def check_fast(self):
        self.npm_log.unlink(missing_ok=True)
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", "--base", "base"], cwd=self.repo, env=self.env,
                             capture_output=True, text=True, timeout=300)
        out = run.stdout + run.stderr
        self.assertEqual(0, run.returncode, out)
        return out, self.npm_log.read_text().splitlines() if self.npm_log.exists() else []

    def test_every_web_lane_input_runs_web(self):
        inputs = web_lane_inputs()
        self.assertIn("scripts/mock/inbox_events.json", inputs)  # paths.py still has the shape this test reads
        for pattern in inputs:
            with self.subTest(pattern=pattern):
                sample = pattern.replace("**", "sample.json").replace("*", "sample")
                self.assertTrue(fnmatch.fnmatchcase(sample, pattern), sample)
                self.write(sample, '{"changed": true}\n')
                try:
                    out, npm_calls = self.check_fast()
                finally:
                    (self.repo / sample).unlink()
                self.assertIn("queued: web\n", out)
                self.assertEqual(["web|run typecheck", "web|test"], npm_calls, out)
                self.assertNotIn("no fast check maps to", out)

    def test_web_inputs_keep_their_other_checks(self):
        self.write("schema/extension/examples/x.json", "{}\n")
        out, _ = self.check_fast()
        self.assertIn("queued: web\n", out)
        self.assertIn("queued: extension schema", out)  # the schema/ mapping still applies
        (self.repo / "schema/extension/examples/x.json").unlink()
        self.write("scripts/mock/inbox_events.json", "{}\n")
        out, _ = self.check_fast()
        self.assertIn("queued: web\n", out)
        self.assertIn("queued: pr-needs-issue workflow test", out)  # the scripts/ mapping still applies

    def test_other_schema_files_do_not_run_web(self):
        self.write("schema/other.json", '{"changed": true}\n')
        out, npm_calls = self.check_fast()
        self.assertNotIn("queued: web\n", out)
        self.assertEqual([], npm_calls, out)

    def test_a_doc_change_does_not_run_web(self):
        self.write("docs/guide.md", "guide changed\n")
        out, npm_calls = self.check_fast()
        self.assertNotIn("queued: web\n", out)
        self.assertEqual([], npm_calls, out)


if __name__ == "__main__":
    unittest.main()
