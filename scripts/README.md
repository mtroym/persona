# Scripts

Scripts are grouped by purpose and resolve their own repository root. Build/deploy configuration is read only from `PERSONA_CONFIG` or `~/.config/persona/config.json`; runtime state uses `PERSONA_HOME` or `~/.persona`. No previous-brand aliases, fallback configuration reads or dual environment exports are supported. See [configuration](../docs/operations/config.md), [the naming contract](../docs/operations/rename.md) and [the approval-gated cutover](../docs/operations/persona-cutover.md).

| Directory | Purpose | Common entry points |
| --- | --- | --- |
| `ci/` | Portable hosted-runner checks and path/result aggregation | `ci/server.sh`, `ci/web.sh`, `ci/android.sh`, `ci/apple.sh`, `ci/scripts.sh`, `ci/runtimes.sh`, `ci/hub-image.sh` |
| `app/` | Apple / Android builds, simulator control and screenshots | `app/sim-boot.sh`, `app/sim.sh`, `app/android.sh`, `app/personactl.py`, `app/mac-link.sh` |
| `deploy/` | Hub / node installation and service definitions | `deploy/deploy.sh`, `deploy/install.sh`, `deploy/link-systemd.sh` |
| `release/` | App / binary releases, signing and artifact checks | `release/release.sh`, `release/publish.sh`, `release/asc_testflight.py`, `release/entitlements.py` |
| `migrate/` | One-shot, explicitly approved offline maintenance | `migrate/persona-cutover.py` (plan only by default; never starts a service) |
| `mock/` | Synthetic hub and screenshot/demo fixtures | `mock/mock_hub.py`, `mock/mock_scenarios.py`, `mock/make_demo_data.py` |
| `assets/` | Icons and avatar packages | `assets/make_icon.py`, `assets/make_alt_icons.py`, `assets/make_persona.py` |
| `diag/` | Push replay, client traffic and redacted transcript diagnostics | `diag/hub_traffic_report.py`, `diag/agentsync_replay.py`, `diag/pane_probe.py`, `diag/redact_transcript.py` |
| `test/` | Server, protocol, source and script regression checks | `test/check-fast.sh`, `test/test-server.sh`, `test/test_doc_paths.py` |
| `hooks/` | Privacy hooks and PR merging | `hooks/merge-pr.sh` |
| `gen/` | Generated extension wire types and SDK bundles | `gen/extension_schema.py`, `gen/sdk_bundle.py`, `gen/sync-sdk.sh` |
| `i18n/` | Localization checks and catalog synchronization | `i18n/apple_l10n.py`, `i18n/apple_l10n.py check` |

## Shared tools

- `persona_config.py` reads private build/deploy configuration and generates Xcode settings without changing registered app IDs.
- `persona_identity.py --service hub|node|link` resolves only a matching canonical service's root. It honors unit resets, ordered drop-ins, XDG paths, HOME-only environment files and unset rules; it never executes configuration or exposes other credentials. `--prepare` creates only the selected directory, with no directory aliases. `--prefix` resolves remote paths using the remote HOME.
- `privacy_check.py` scans repository/staged content. Git hooks, web builds and CI use it.
- Existing non-brand shell shortcuts (`sim.sh`, `android.sh`, `deploy.sh`, `release.sh` and others) only forward arguments/environment/stdin/exit status to their grouped implementation. Repository examples use grouped paths. Python tools have only their canonical entry points.

## Verification

Use synthetic fixtures and an isolated outer HOME for script tests. Server tests need a real **test** Postgres database under `PERSONA_TEST_DATABASE_URL`; never let a failed environment override select live configuration. Test modules may import neighboring helpers, so run each in its own directory rather than assuming one top-level discovery covers every suite.

```sh
n=0 failed=""
for f in $(find scripts -name 'test_*.py' -not -path '*/node_modules/*' | sort); do
  grep -q unittest "$f" || continue  # a verification script, not a suite: CI skips it too (scripts/ci/python-tests.py)
  n=$((n + 1))
  (cd "$(dirname "$f")" && python3 -B -m unittest -q "$(basename "$f" .py)") || failed="$failed $f"
done
printf '%s suites, failed:%s\n' "$n" "${failed:- none}"
[ -z "$failed" ]
node scripts/test/pr_needs_issue_test.mjs
python3 scripts/privacy_check.py
python3 scripts/gen/extension_schema.py --check
```

The doc/source checks verify current paths, top-level docs registration, shell syntax, Python 3.9 syntax/annotations and real mock startup. Do not raise token baselines or remove checks to pass a rename. macOS's Python 3.9 needs postponed annotations for `X | None`. Actual deployments/releases require separate approval; a passing synthetic test is not permission to operate live services.
