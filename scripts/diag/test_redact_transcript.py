"""redact_transcript.py runs on macOS's own Python 3.9 (#579): its datetime.fromisoformat takes only 3 or 6 fraction
digits, while Go writes RFC 3339 times with 0 to 9 (trailing zeros dropped)."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("redact_transcript", Path(__file__).with_name("redact_transcript.py"))
redact = importlib.util.module_from_spec(spec)
spec.loader.exec_module(redact)


class TimestampTests(unittest.TestCase):
    def test_any_number_of_fraction_digits(self):
        r = redact.Redactor()
        self.assertEqual(r.ts("2026-03-01T10:00:01Z"), "2026-01-01T00:00:00.000Z")  # the first time moves to 2026-01-01
        for stamp, shifted in [("2026-03-01T10:00:01.5Z", "2026-01-01T00:00:00.500Z"),
                               ("2026-03-01T10:00:01.12Z", "2026-01-01T00:00:00.120Z"),
                               ("2026-03-01T10:00:01.1234Z", "2026-01-01T00:00:00.123Z"),
                               ("2026-03-01T10:00:01.123456789Z", "2026-01-01T00:00:00.123Z")]:
            with self.subTest(stamp=stamp):
                self.assertEqual(r.ts(stamp), shifted)


class KeptAsIsTests(unittest.TestCase):
    """`--words` is the review step before a fixture is committed, so it lists every string the output keeps as is."""

    def test_every_verbatim_string_is_listed_with_its_reason(self):
        r = redact.Redactor()
        out = r.obj({"type": "user", "version": "2.1.0", "model": "claude-placeholder", "name": "mcp__srv__tool",
                     "filePath": "/home/someone/notes.cfgx",
                     "message": {"content": '<example_note kind="x">hello</example_note>'},
                     "toolUseResult": {"example_key": {"ok": True}}})
        self.assertEqual(out["toolUseResult"], {"example_key": {"ok": True}})
        for why, kept in [("key", "example_key"), ("key", "toolUseResult"), ("enum", "user"), ("version", "2.1.0"),
                          ("model", "claude-placeholder"), ("tool", "mcp"), ("tag", "example_note"), ("tag", "kind"),
                          ("ext", ".cfgx")]:
            with self.subTest(kept=kept):
                self.assertIn(kept, r.kept.get(why, set()))
        report = r.kept_report()
        self.assertTrue(report.startswith("kept as is: "), report)
        self.assertIn("kept as is (key): ", report)
        self.assertIn("example_note", report)

    def test_an_enum_value_naming_a_host_or_url_is_redacted(self):
        r = redact.Redactor()
        for key, value in [("source", "git.example.invalid:team/repo"), ("channel", "https://example.invalid/x"),
                           ("action", "host.example.invalid/path")]:
            with self.subTest(value=value):
                out = r.value(key, value)
                self.assertNotEqual(out, value)
                self.assertNotIn("invalid", out)
                self.assertNotIn(value, r.kept.get("enum", set()))
        for key, value in [("media_type", "image/png"), ("type", "user"), ("type", "response.completed")]:
            with self.subTest(value=value):
                self.assertEqual(r.value(key, value), value)
                self.assertIn(value, r.kept["enum"])

    def test_an_id_prefix_word_followed_by_letters_is_not_kept(self):
        # Ids keep only their known prefix (msg_, toolu_, ws, ...): a word that merely starts with one gets a new body.
        r = redact.Redactor()
        self.assertEqual(r.new_id("toolu_01AbCdEfGh")[:6], "toolu_")
        for word in ["requirements", "wsplaceholder"]:
            with self.subTest(word=word):
                self.assertNotIn(word, r.text(f"see {word} here"))


if __name__ == "__main__":
    unittest.main()
