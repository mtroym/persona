#!/usr/bin/env python3
"""Fail when the repository holds this person's private values (CLAUDE.md: no intranet or personal data, ever).

    python3 scripts/privacy_check.py            # tracked files (content and paths)
    python3 scripts/privacy_check.py --staged   # what `git commit` is about to record, plus its message file if given
    python3 scripts/privacy_check.py --staged --commit-msg .git/COMMIT_EDITMSG   # the commit-msg hook (commit_msg_lines)
    python3 scripts/privacy_check.py --range origin/main..HEAD   # what those commits change, and their messages;
                                                     # all of HEAD when the two share no history (an orphan branch)
    python3 scripts/privacy_check.py --range <base sha>..<head> --message <file>   # plus text recorded as it is, every
                                                     # line (merge-pr.sh: the fetched base sha, the squash message)

Every mode reads what Git records: a symlink is scanned as its link text, never the file it points to; --staged and
--range take added, copied, modified, renamed and type-changed files (a file turned into a symlink); the tracked-files
scan reads a tracked file the worktree lacks (sparse checkout, skip-worktree, deleted but not staged) from the index.

What counts as private comes from this machine, never from the repo: the values of ~/.config/persona/config.json
(hosts, hub address, machine names, Apple ids, emails), ~/.config/persona/private-words.txt (one word per line:
internal project names, people, anything else) and CLAUDE.local.md's `code spans`. On top of that, any private IPv4
address, any email outside example.* domains, and the names in BANNED_NAMES (vendors and internal repositories that
only extensions may name). A hit says what kind of value matched and where it comes from (a config key path such as
`machines.id`, private-words.txt, CLAUDE.local.md, the home directory), never the value. Install as a hook:
scripts/hooks/pre-commit (docs/operations/testing.md).

Not private: loopback addresses (127.0.0.0/8, ::1, also with a port or as a URL's host) even when the config or
CLAUDE.local.md holds one, hosts under the example domains (RFC 2606, RFC 6761: example and example.com/.net/.org,
and the .test, .invalid and .localhost TLDs) held there as placeholders, the documentation and IETF protocol ranges
(192.0.0.0/24), an `xcode` in /Applications,
values config.example.json ships as defaults, exact generic provisioning profile labels already published in origin/main's
mapping/fixture (only under app_store.provisioning_profiles), a config value made only of ASCII letters that a `!word` line in
private-words.txt takes back (an ordinary word, a machine id named after a room), the repo's own Go module path (it
names the GitHub owner),
CLAUDE.local.md spans that only name repo files, commands or config keys, a listed word of one case run where the text
spells it only across a camel-case join ("fooBar", only_in_camel_joins), and in site/ the contact address and domain
the App Store and the OAuth consent screen publish anyway.
"""
import ast, io, ipaddress, json, os, plistlib, re, subprocess, sys
from functools import lru_cache
from urllib.parse import unquote, urlsplit
from xml.parsers.expat import ExpatError

ROOT = subprocess.run(["git", "-C", os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                       "rev-parse", "--show-toplevel"], capture_output=True, text=True, check=True).stdout.strip()
HOME = os.path.expanduser("~")
# Environment variables that name a build config, current name first (config_values reads each one set).
CONFIG_ENV = ("PERSONA_CONFIG", "JARVIS_CONFIG")
# Values too generic to search for (a machine called "mac", an OS name).
IGNORE = {"darwin", "linux", "local", "ssh", "true", "false", "server", "localhost"}
# Files that may hold placeholders that look like the real thing.
SKIP_FILES = {"scripts/privacy_check.py"}
# Config keys whose values are published on purpose (App Store contact, the site's address, where personad is published):
# fine in PUBLIC_DIRS.
PUBLIC_KEYS = {"feedback_email", "privacy_url", "site_url"}
# Hosts many people publish on: a URL there is told apart by its path, so the term keeps the path (a privacy policy that
# is a Google Doc must not make every docs.google.com link private).
SHARED_HOSTS = {"docs.google.com", "drive.google.com", "sites.google.com", "github.com", "gist.github.com",
                "raw.githubusercontent.com", "notion.so", "www.notion.so"}


def url_term(url):
    """What of a URL value to look for: its host, or host + path on a shared host."""
    rest = re.sub(r"^\w+://", "", url)
    host = rest.split("/")[0].split(":")[0]
    if host in SHARED_HOSTS:
        path = rest[len(rest.split("/")[0]):].split("?")[0].split("#")[0].rstrip("/")
        return host + path if path else ""
    return host


def strings(o, key=()):
    """(key path, value) for every string in a JSON value, the path a tuple of keys (("hub", "url")); a list item takes
    its list's path (("machines", "id"))."""
    if isinstance(o, dict):
        for k, v in o.items():
            yield from strings(v, key + (k,))
    elif isinstance(o, list):
        for v in o:
            yield from strings(v, key)
    elif isinstance(o, str) and o:
        yield key, o


def example_values():
    try:
        return {v for _, v in strings(json.load(open(os.path.join(ROOT, "config.example.json"))))}
    except (OSError, ValueError):
        return set()


def standard_app(key, value):
    """An app in /Applications (the `xcode` key): every Mac has the same path, it names no person or machine."""
    return key == "xcode" and re.fullmatch(r"/Applications/[\w .-]+\.app/?", value) is not None


def profile_labels(source, function_name):
    """Read public literal profile labels, never execute a config helper or a historical fixture."""
    try:
        tree = ast.parse(source)
    except SyntaxError:
        return set()
    labels = set()
    for function in tree.body:
        if not isinstance(function, ast.FunctionDef) or function.name != function_name:
            continue
        for node in ast.walk(function):
            if not isinstance(node, ast.Assign):
                continue
            names = {target.id for target in node.targets if isinstance(target, ast.Name)}
            if function_name == "provisioning_profiles" and "profiles" in names and isinstance(node.value, ast.Dict):
                labels.update(value.value for value in node.value.values
                              if isinstance(value, ast.Constant) and isinstance(value.value, str) and value.value.strip())
            if function_name != "export_options" or "plist" not in names:
                continue
            value = node.value
            marker = "__privacy_dynamic_field__"
            if isinstance(value, ast.JoinedStr):
                text = "".join(part.value if isinstance(part, ast.Constant) and isinstance(part.value, str)
                               else marker for part in value.values)
            elif isinstance(value, ast.Constant) and isinstance(value.value, str):
                text = value.value
            else:
                continue
            try:
                options = plistlib.loads(text.encode())
                profiles = options.get("provisioningProfiles", {}) if isinstance(options, dict) else {}
            except (ValueError, plistlib.InvalidFileException, ExpatError):
                continue
            if isinstance(profiles, dict):
                labels.update(name for name in profiles.values()
                              if isinstance(name, str) and name.strip() and marker not in name)
    return labels


@lru_cache(maxsize=1)
def public_profile_labels():
    """Exact generic labels already published as defaults or deployment fixture data (#1245)."""
    source_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    sources = (("scripts/persona_config.py", "provisioning_profiles"),
               ("scripts/test/fixtures/deployment/persona_config.py.fixture", "export_options"))
    labels = set()
    for path, function in sources:
        try:
            source = subprocess.run(["git", "-C", source_root, "show", "origin/main:" + path],
                                    capture_output=True, text=True, timeout=10)
        except (OSError, subprocess.TimeoutExpired):
            continue
        if source.returncode == 0:
            labels.update(profile_labels(source.stdout, function))
        # No published definition means no waiver; adding a private label in this PR cannot approve itself.
    return frozenset(labels)


def config_values():
    """The config's values, each with the key paths it is held under, and the subset that is published on purpose
    (PUBLIC_KEYS)."""
    out, public = {}, set()
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(HOME, ".config")
    # Scan both profiles, even when only one is selected for deployment.
    paths = {os.path.join(base, brand, "config.json") for brand in ("persona", "jarvis")}
    paths.update(os.path.expanduser(os.environ[key]) for key in CONFIG_ENV if os.environ.get(key))
    defaults = example_values()
    for path in paths:
        try:
            with open(path) as source:
                cfg = json.load(source)
        except (OSError, ValueError):
            continue
        for key_path, o in strings(cfg):
            key = key_path[-1] if key_path else ""
            if key in ("os", "simulator") or o in defaults or standard_app(key, o):
                continue
            if len(key_path) == 3 and key_path[:2] == ("app_store", "provisioning_profiles") and o in public_profile_labels():
                continue  # Only this config source is generic; another key or an explicit private word still wins.
            if key == "url" or key.endswith("_url"):
                o = url_term(o)
                if not o:
                    continue
            elif key.endswith("path"):
                o = o.replace(HOME, "~")
            out.setdefault(o, set()).add(key_path)
            if key in PUBLIC_KEYS:
                public.add(o)
    return out, public


# A token that looks like somebody's value: a long id mixing letters and digits (key ids, team ids, hostnames like
# n12-345), a run of six or more digits (app ids), or a host under a real TLD. Repo paths, commands and config keys
# (`docs/operations/config.md`, `python3 scripts/x.py`, `google.proxy`) have none.
ID_LIKE = re.compile(r"(?=[\w-]*\d)(?=[\w-]*[A-Za-z])[\w-]{8,}|\d{6,}")


def private_looking(span):
    if ID_LIKE.search(span) or "@" in span or IPV4.search(span):
        return True
    for host in re.findall(r"[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+", span):
        if host.rsplit(".", 1)[-1].lower() in REAL_TLDS and not host.lower().endswith((".md", ".py", ".sh", ".json")):
            return True
    return False


# What a `!word` line in private-words.txt can take back: a config value that is an ordinary word (a machine id named
# after a room) matches every variable, comment and environment variable of that name. Only ASCII letters: a host with
# a domain, an address, an email, a path or an id is never taken back. The waiver is by value, under every key that
# holds it (an ssh alias or URL host spelled like the machine id goes with it).
WAIVABLE = re.compile(r"[A-Za-z]+")
# How a hit names a config key. Schema keys are snake_case words; any other key (provisioning_profiles is keyed by
# bundle ids), or one that is itself a private value, shows as `*`: the report never carries a value.
KEY_NAME = re.compile(r"[a-z][a-z0-9_]*")


def word_list():
    """The listed words, each with the file that lists it, and the config values `!word` lines take back (lowercase)."""
    out, waived = {}, set()
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(HOME, ".config")
    for brand in ("persona", "jarvis"):
        name = os.path.join(brand, "private-words.txt")
        try:
            with open(os.path.join(base, name)) as source:
                for n, line in enumerate(source, 1):
                    line = line.strip()
                    if not line or line.startswith("#"):
                        continue
                    if line.startswith("!"):
                        word = line[1:].strip()
                        if WAIVABLE.fullmatch(word):
                            waived.add(word.lower())
                            continue
                        # The line number only: the value may be private.
                        print("privacy_check: %s:%d: a `!` line only takes back a config value made of ASCII letters; "
                              "searched as written instead" % (name, n), file=sys.stderr)
                    out.setdefault(line, set()).add("private-words.txt")
        except OSError:
            pass
    local = os.path.join(ROOT, "CLAUDE.local.md")
    if os.path.exists(local):
        for span in re.findall(r"`([^`\n]{4,})`", open(local).read()):
            if private_looking(span):  # hosts, addresses, accounts, ids; not prose, repo paths or commands
                out.setdefault(span.strip(), set()).add("CLAUDE.local.md")
    return out, waived


def loopback(value):
    """127.0.0.0/8, ::1 and localhost are every machine's own address, not anybody's private value: bare, as host:port
    or [IPv6]:port, or as a URL's host (http://127.0.0.1:18660/x)."""
    value = value.strip()
    try:
        return ipaddress.ip_address(value.strip("[]")).is_loopback
    except ValueError:
        pass
    try:
        parts = urlsplit(value if "://" in value else "//" + value)
        parts.port  # raises ValueError on a port that is not a number in range
    except ValueError:
        return False
    host = parts.hostname or ""
    if parts.username is not None or not host:
        return False
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


# Example domains (RFC 2606, RFC 6761): every host under example.com, .net or .org, and under the example, test,
# invalid and localhost TLDs.
RESERVED_DOMAINS = ("example.com", "example.net", "example.org")
RESERVED_TLDS = {"example", "test", "invalid", "localhost"}


def reserved(value):
    """A host under an example domain names nobody: a config that holds one as a placeholder must not make every
    me@example.com private. Bare, as host:port or as a URL's host (https://relay.example.com:443/x), not with a user."""
    value = value.strip()
    try:
        parts = urlsplit(value if "://" in value else "//" + value)
        parts.port  # raises ValueError on a port that is not a number in range
    except ValueError:
        return False
    host = parts.hostname or ""
    if parts.username is not None or not host:
        return False
    labels = host.split(".")
    if labels[-1] in RESERVED_TLDS and len(labels) >= 3 and labels[0] in REAL_TLDS:
        # A reverse-DNS id (a bundle id, com.<owner>.persona.test) is not a host: it names the domain it reverses.
        labels.reverse()
        host = ".".join(labels)
    return labels[-1] in RESERVED_TLDS or any(host == d or host.endswith("." + d) for d in RESERVED_DOMAINS)


def term_sources():
    """Words to look for, longest first, each with where it comes from ("config machines.id", "private-words.txt",
    "CLAUDE.local.md", "the home directory"); those of them allowed in PUBLIC_DIRS; and those a `!word` line could
    take back (config values of ASCII letters that no file lists plainly)."""
    config, public = config_values()
    listed, waived = word_list()
    plain = {v.lower() for v in listed}
    private = {v.lower() for v in config} | plain

    def label(path):
        return "config " + ".".join(k if KEY_NAME.fullmatch(k) and k not in private else "*" for k in path)

    found = {v: {label(p) for p in paths} for v, paths in config.items() if v.lower() not in waived}
    waivable = {v for v in found if WAIVABLE.fullmatch(v) and v.lower() not in plain}
    for v, files in listed.items():
        found.setdefault(v, set()).update(files)
    found = {v: where for v, where in found.items()
             if len(v) >= 4 and v.lower() not in IGNORE and not loopback(v) and not reserved(v)}
    user = os.path.basename(HOME)
    if len(user) >= 3:
        for v in ("/Users/" + user, "/home/" + user):
            found.setdefault(v, set()).add("the home directory")
    return {v: sorted(found[v]) for v in sorted(found, key=len, reverse=True)}, public, waivable


def terms():
    """Words to look for, longest first, and those of them allowed in PUBLIC_DIRS."""
    found, public, _ = term_sources()
    return list(found), public


def module_paths():
    """The repo's own Go module paths (github.com/<owner>/...): every import carries them."""
    out = []
    for f in git("ls-files", "-z", "--", "*go.mod").decode().split("\0"):
        if f:
            m = re.search(r"^module\s+(\S+)", open(os.path.join(ROOT, f)).read(), re.M)
            if m:
                out.append(m.group(1).split("/")[0:3])
    return re.compile("|".join(re.escape("/".join(p)) for p in out), re.I) if out else None


# Not part of a longer dotted run of digits (a version, an OID): a digit, or a dot with a digit past it, on either side.
# A dot alone is punctuation, the end of a sentence or an ellipsis, and does not hide the address (#3924).
IPV4 = re.compile(r"(?<!\d)(?<!\d\.)(\d{1,3}(?:\.\d{1,3}){3})(?!\d|\.\d)")
# Documentation ranges (RFC 5737): what redacted fixtures and docs use for addresses. Also the IETF protocol
# assignments block (RFC 6890): Python counts it private, but it is nobody's intranet (bundled SDKs list 192.0.0.192
# as a cloud metadata address).
DOC_NETS = [ipaddress.ip_network(n) for n in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "192.0.0.0/24")]
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,})")
# What every IPV4 and EMAIL match holds. Both patterns try every start position (EMAIL backtracks at each word start);
# problems() runs them only on a line that has the hint, which a quick scan finds.
IPV4_HINT = re.compile(r"\d\.\d")
EMAIL_HINT = re.compile(r"[A-Za-z0-9._%+-]@[A-Za-z0-9-]")
# Placeholders (example domains, public mail providers, noreply) are fine; so are made-up TLDs (the fixture redactor
# scrambles domains): only addresses at a real-looking domain of somebody's are flagged.
OK_EMAIL_DOMAINS = re.compile(r"(^|\.)(example(\.(com|org|net))?|test|invalid|localhost|users\.noreply\.github\.com|anthropic\.com|"
                              r"apple\.com|gmail\.com|googlemail\.com|outlook\.com|hotmail\.com|icloud\.com|yahoo\.com|"
                              r"qq\.com|163\.com|126\.com|foxmail\.com|yeah\.net|188\.com|"
                              r"substack\.com|beehiiv\.com|"  # newsletter platforms (kb tests that their senders are not people)
                              r"b\.com)$")  # a@b.com: the placeholder in the Lark extension's skill (assistantskills/)
REAL_TLDS = {"com", "net", "org", "io", "cn", "dev", "app", "ai", "co", "me", "cc", "info", "so", "xyz", "top", "edu",
             "gov", "uk", "jp", "us", "de", "fr", "hk", "tw", "sg", "in", "ru", "kr", "tech", "cloud", "site", "work", "biz"}
# The public site (site/) shows a contact address on purpose: the OAuth consent screen requires one.
PUBLIC_DIRS = ("site/",)

# Names that are never in the repository, in any spelling (#4967): vendors and internal repositories Persona only
# reaches through extensions, which bring their own names. Case-insensitive, with a hyphen, underscore, space or
# nothing between the parts; checked in contents, paths and commit messages on every machine, CI included, with no
# private configuration needed. This file is the only place they are spelled, as patterns.
BANNED_NAMES = re.compile(r"aster[-_ ]?gate|open[-_ ]?lark|open[-_ ]?wechat|extensions[-_ ]?internal|corp[-_ ]?link",
                          re.IGNORECASE)
# The same pattern without IGNORECASE, run on the lowercased line problems() already has: about twice as fast. It finds
# the same spans when lowering kept every character in place (same length) and the line holds neither of the two
# characters IGNORECASE also takes for a pattern letter but lower() leaves alone (dotless i, long s); otherwise
# problems() falls back to BANNED_NAMES.
BANNED_NAMES_LOWER = re.compile(BANNED_NAMES.pattern)
CASE_FOLD_ONLY = ("\u0131", "\u017f")


CAMEL_JOIN = re.compile(r"[a-z][A-Z]")


def only_in_camel_joins(text, w):
    """Whether every place a word of one case run (no lowercase → uppercase step of its own) shows up in `text` has such
    a step inside it: two ordinary words joined into an identifier ("fooBar"), which happened to spell a listed word and
    flagged a design token (#3819). A word written with the step ("FooBar") is matched as before, camel-cased too."""
    if not w.isalpha() or CAMEL_JOIN.search(w):
        return False
    hits = [m.group(0) for m in re.finditer(re.escape(w), text, re.I)]
    return bool(hits) and all(CAMEL_JOIN.search(h) for h in hits)


def problems(text, words, emails=True):
    found = []
    low = text.lower()
    for w in words:
        # Short values (a name of 4–5 letters) only as a word of their own: inside identifiers ("inboxItem") they
        # matched by accident and flagged whole files.
        if len(w) < 6 and w.isalnum():
            if re.search(r"(?<![a-z0-9])" + re.escape(w.lower()) + r"(?![a-z0-9])", low):
                found.append(w)
        elif w.lower() in low and not only_in_camel_joins(text, w):
            found.append(w)
    if len(low) == len(text) and not any(c in text for c in CASE_FOLD_ONLY):
        found += [text[m.start():m.end()] for m in BANNED_NAMES_LOWER.finditer(low)]
    else:
        found += [m.group(0) for m in BANNED_NAMES.finditer(text)]
    for m in IPV4.finditer(text) if "." in text and IPV4_HINT.search(text) else ():
        try:
            ip = ipaddress.ip_address(m.group(1))
        except ValueError:
            continue
        if ip.is_private and not ip.is_loopback and not ip.is_unspecified and not any(ip in n for n in DOC_NETS):
            found.append(m.group(1))
    for m in EMAIL.finditer(text) if emails and "@" in text and EMAIL_HINT.search(text) else ():
        domain = m.group(1).lower()
        if not OK_EMAIL_DOMAINS.search(domain) and domain.rsplit(".", 1)[-1] in REAL_TLDS:
            found.append(m.group(0))
    return found


def private_kind(value, sources=None):
    """What a hit is, never the value itself, and where the value comes from when it is a searched word (`sources`,
    from term_sources): the operator tells a false positive from a leak by it (#1065)."""
    if BANNED_NAMES.fullmatch(value):
        return "banned name (a vendor or internal repository; use the extension's own wording, #4967)"
    kind = "private IP" if IPV4.fullmatch(value) else "email" if "@" in value else "private value (%d chars)" % len(value)
    where = (sources or {}).get(value)
    return kind + " from " + ", ".join(where) if where else kind


NPM_LOCK_FILES = {"package-lock.json", "npm-shrinkwrap.json"}
NPM_REGISTRY_HOST = "registry.npmjs.org"


def npm_source_allowed(source, link=False):
    if not isinstance(source, str) or not source or any(ord(c) < 32 or ord(c) == 127 or c == "\\" for c in source):
        return False
    try:
        url = urlsplit(source)
        if url.query or url.fragment or url.username is not None or url.password is not None:
            return False
        if url.scheme == "https":
            return url.hostname == NPM_REGISTRY_HOST and url.port in (None, 443)
        if url.scheme != "file" and not (link and not url.scheme):
            return False
        path = unquote(url.path)
        return (not url.netloc and bool(path) and not path.startswith("/")
                and not any(ord(c) < 32 or ord(c) == 127 or c in "\\:%?#" for c in path))
    except ValueError:
        return False


def unique_json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def npm_resolutions(node):
    if isinstance(node, dict):
        for key, value in node.items():
            if key == "resolved":
                yield value, node.get("link") is True
            yield from npm_resolutions(value)
    elif isinstance(node, list):
        for value in node:
            yield from npm_resolutions(value)


def json_field_lines(text, key):
    decoder = json.JSONDecoder()
    cursor = 0
    while True:
        start = text.find('"', cursor)
        if start < 0:
            return
        value, cursor = decoder.raw_decode(text, start)
        colon = cursor
        while colon < len(text) and text[colon].isspace():
            colon += 1
        if value == key and colon < len(text) and text[colon] == ":":
            yield text.count("\n", 0, start) + 1


def invalid_json_constant(value):
    raise ValueError("invalid JSON constant")


def npm_lock_problems(text):
    try:
        lock = json.loads(text, object_pairs_hook=unique_json_object, parse_constant=invalid_json_constant)
    except (ValueError, RecursionError):
        return [(1, "invalid npm lockfile")]
    if not isinstance(lock, dict):
        return [(1, "invalid npm lockfile")]
    try:
        found = []
        for line, (source, link) in zip(json_field_lines(text, "resolved"), npm_resolutions(lock)):
            if not isinstance(source, str):
                found.append((line, "invalid npm resolved field"))
            elif not npm_source_allowed(source, link):
                found.append((line, "unapproved npm source"))
        return found
    except (ValueError, RecursionError):
        return [(1, "invalid npm lockfile")]


def git(*args):
    return subprocess.run(["git", "-C", ROOT, *args], capture_output=True, check=True).stdout


def git_file_data(files, prefix):
    """Read the same index/commit blobs as git show, using one bounded Git process."""
    safe = [name for name in files if "\n" not in name and "\r" not in name]
    data = {}
    fallback = [name for name in files if "\n" in name or "\r" in name]
    if safe:
        requests = b"".join((prefix + name).encode() + b"\0" for name in safe)
        output = subprocess.run(["git", "-C", ROOT, "cat-file", "--batch", "-z"],
                                input=requests, capture_output=True, check=True, timeout=120).stdout
        stream = io.BytesIO(output)
        for name in safe:
            header = stream.readline()
            if header.endswith(b" missing\n"):
                continue
            fields = header.split()
            if len(fields) != 3:
                raise ValueError("invalid Git batch header")
            size = int(fields[2])
            payload = stream.read(size)
            if size < 0 or len(payload) != size or stream.read(1) != b"\n":
                raise ValueError("truncated Git batch object")
            if fields[1] == b"blob":
                data[name] = payload
            else:
                # Preserve git show's rendering of gitlinks/other non-blob entries.
                fallback.append(name)
        if stream.read():
            raise ValueError("unexpected Git batch output")
    for name in fallback:
        try:
            data[name] = git("show", prefix + name)
        except subprocess.CalledProcessError:
            pass
    return data


GIT_OID = re.compile(rb"[0-9a-f]{40}|[0-9a-f]{64}")
# Git's empty tree, by object id length (SHA-1, SHA-256): the base of a range whose two ends share no history.
EMPTY_TREE = {40: "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
              64: "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"}


def commit_messages(base, head):
    """(commit, message) of every commit in base..head (every commit reachable from head when base is the empty tree),
    newest first, from one `git log` instead of one per commit (a long range spent most of its time starting git). Git
    ends %B at a NUL byte, so NULs only separate the fields; no signature check output, which log.showSignature would
    mix into the messages."""
    commits = head if base in EMPTY_TREE.values() else base + ".." + head
    parts = git("log", "-z", "--no-show-signature", "--format=%H%x00%B", commits).split(b"\0")
    if len(parts) % 2 != 1 or parts[-1] or not all(GIT_OID.fullmatch(c) for c in parts[0:-1:2]):
        raise ValueError("unexpected git log output")
    return [(parts[i].decode(), parts[i + 1].decode("utf-8", "replace")) for i in range(0, len(parts) - 1, 2)]


def commit_range(spec):
    """<base>..<head>: the fork point of the two (what a branch adds on top of base), and head. Two ends that share no
    history (an orphan branch, a base left from a rewritten history) give the empty tree: all of head is scanned."""
    base, sep, head = spec.partition("..")
    if not sep or not base or not head or head.startswith("."):
        sys.exit("privacy_check: --range wants <base>..<head>")
    try:
        head = git("rev-parse", "--verify", head + "^{commit}").decode().strip()
        base = git("rev-parse", "--verify", base + "^{commit}").decode().strip()
    except subprocess.CalledProcessError:
        sys.exit("privacy_check: --range %s does not name two commits; scan did not complete" % spec)
    fork = subprocess.run(["git", "-C", ROOT, "merge-base", base, head], capture_output=True)
    if fork.returncode == 1 and not fork.stdout:  # no common ancestor
        return EMPTY_TREE[len(head)], head
    if fork.returncode:
        sys.exit("privacy_check: git merge-base failed; scan did not complete")
    return fork.stdout.decode().strip(), head


def git_config(key):
    """The value Git uses for key ("" when unset)."""
    result = subprocess.run(["git", "-C", ROOT, "config", "--get", key], capture_output=True, text=True)
    if result.returncode == 1:
        return ""
    result.check_returncode()
    return result.stdout.strip()


def comment_string():
    """The string git starts comment lines with, read from its config by git itself (core.commentChar or
    core.commentString, the one set last; `auto` gives `#`)."""
    commented = subprocess.run(["git", "-C", ROOT, "stripspace", "--comment-lines"], input="x\n", capture_output=True,
                               text=True, check=True).stdout
    comment = commented.rpartition(" x\n")[0]
    if not comment:  # an empty prefix would skip every line
        sys.exit("privacy_check: cannot tell git's comment string")
    return comment


CUT_LINE = " ------------------------ >8 ------------------------"


def commit_msg_lines(path):
    """(line number, line) of the file `git commit` hands its commit-msg hook, for the lines the commit records.

    Git cleans the file up after the hook. When it opened an editor (otherwise it sets GIT_EDITOR=: for the hook), the
    default cleanup drops the comment lines (hints, branch, local paths) and everything from the scissors line down
    (the `commit -v` diff); a message from -m or -F keeps its lines that start with the comment string (Markdown
    headings) unless commit.cleanup is strip. Lines end at \n only, as in git. Not visible here: --cleanup and -v given
    to `git commit` (git ends a message at the scissors line only with -v, commit.verbose or cleanup scissors) and the
    character `auto` picks; pre-push's --range scan reads every line of what was recorded.
    """
    edited = os.environ.get("GIT_EDITOR") != ":"
    cleanup = git_config("commit.cleanup") or "default"
    strip = cleanup == "strip" or (cleanup == "default" and edited)
    comment = comment_string()
    text = open(path, encoding="utf-8", errors="replace", newline="").read()  # as --range decodes recorded messages
    for n, line in enumerate(text.split("\n"), 1):
        if edited and line == comment + CUT_LINE:
            break
        if not (strip and line.startswith(comment)):
            yield n, line


def option(name):
    """The argument after name on the command line (None without name); name as the last argument is an error."""
    if name not in sys.argv:
        return None
    if sys.argv.index(name) + 1 == len(sys.argv):
        sys.exit("privacy_check: %s wants a value" % name)
    return sys.argv[sys.argv.index(name) + 1]


def main():
    staged = "--staged" in sys.argv
    message = option("--message")
    commit_msg = option("--commit-msg")
    span = commit_range(option("--range")) if "--range" in sys.argv else None
    sources, public_words, waivable = term_sources()
    words = list(sources)
    modules = module_paths()
    if staged:
        files = git("diff", "--cached", "--name-only", "--diff-filter=ACMRT", "-z").decode().split("\0")
    elif span:
        files = git("diff", "--name-only", "--diff-filter=ACMRT", "-z", span[0], span[1]).decode().split("\0")
    else:
        files = git("ls-files", "-z").decode().split("\0")
    files = list(filter(None, files))
    if staged or span:
        from_git = [name for name in files if name not in SKIP_FILES]
    else:
        # Tracked files the worktree lacks (sparse checkout, skip-worktree, deleted but not staged): the index's copy.
        from_git = [name for name in files if name not in SKIP_FILES and not os.path.lexists(os.path.join(ROOT, name))]
    snapshots = {}
    if from_git:
        try:
            snapshots = git_file_data(from_git, span[1] + ":" if span and not staged else ":")
        except (OSError, ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
            sys.exit("privacy_check: cannot read Git objects; scan did not complete")
    from_git = set(from_git)
    bad, hit = [], set()

    def report(f, where, w):
        hit.add(w)
        bad.append((f, where, private_kind(w, sources)))

    for f in files:
        public = f.startswith(PUBLIC_DIRS)
        allowed = [w for w in words if w not in public_words] if public else words
        for w in problems(f, words):
            report(f, "path", w)
        if f in SKIP_FILES:
            continue
        path = os.path.join(ROOT, f)
        try:
            if f in from_git:
                data = snapshots.get(f)
                if data is None:
                    continue
            elif os.path.islink(path):
                data = os.fsencode(os.readlink(path))  # the link text git records, never the file it points to
            else:
                data = open(path, "rb").read()
        except (OSError, subprocess.CalledProcessError):
            continue
        npm_lock = os.path.basename(f) in NPM_LOCK_FILES
        if b"\0" in data[:4096] and not npm_lock:
            continue  # binary
        text = data.decode("utf-8", "replace")
        if npm_lock:
            try:
                lock_text = data.decode("utf-8")
            except UnicodeDecodeError:
                bad.append((f, 1, "invalid npm lockfile"))
            else:
                bad.extend((f, n, kind) for n, kind in npm_lock_problems(lock_text))
        if modules:
            text = modules.sub("", text)
        for n, line in enumerate(text.splitlines(), 1):
            for w in problems(line, allowed, emails=not public):
                report(f, n, w)
    if span:
        try:
            messages = commit_messages(*span)
        except (ValueError, subprocess.CalledProcessError):
            sys.exit("privacy_check: cannot read commit messages; scan did not complete")
        for c, body in messages:
            for n, line in enumerate(body.splitlines(), 1):
                for w in problems(line, words):
                    report("commit " + c[:9] + " message", n, w)
    lines = []
    for name, path in (("--message", message), ("--commit-msg", commit_msg)):
        if path is not None and not os.path.isfile(path):
            sys.exit("privacy_check: %s %s is not a file; scan did not complete" % (name, path))
    if message is not None:
        lines.extend(enumerate(open(message).read().splitlines(), 1))
    if commit_msg is not None:
        lines.extend(commit_msg_lines(commit_msg))
    for n, line in lines:
        for w in problems(line, words):
            report("commit message", n, w)
    for f, where, kind in bad:
        print("%s:%s: %s" % (f, where, kind))
    if bad:
        print("privacy_check: %d hit(s). Use placeholders (<hub address>, me@example.com); see CLAUDE.md." % len(bad), file=sys.stderr)
        if hit & waivable:
            print("privacy_check: a config value that is an ordinary word and names nobody (a machine id like a room's "
                  "name) can be marked not private with a !<word> line in private-words.txt "
                  "(docs/operations/testing.md, \"Privacy scan\").", file=sys.stderr)
        sys.exit(1)
    print("privacy_check: clean (%d values checked)" % len(words))


if __name__ == "__main__":
    main()
