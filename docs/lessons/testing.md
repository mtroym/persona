# Lessons: testing and validation

- On Linux, replacing a running executable requires a new file and rename; direct writes fail with `ETXTBSY`. Use a ready handshake and reap the fixture process to test the real overlap.

- Run standalone release-worker tests on the oldest supported Python, including receipt verification and verified-artifact retries (#1077). Python 3.9 has no `hashlib.file_digest`; bounded reads into `hashlib.sha256` keep hashing portable without loading an APK into memory. Cover empty files and binary inputs spanning multiple chunks.

Pitfalls, odd behavior, and the tradeoffs made at the time (originally in `CLAUDE.md`, "Lessons"; index in `docs/lessons/README.md`). Add new lessons to the relevant section, or start a section if none fits.

- Fixed `time.Sleep` calls in `_test.go` files under `server/` and `sdk/go/` are ratcheted by `scripts/ci/sleep_ratchet.py` (baseline `scripts/ci/sleep_baseline.json`): a file's count may stay or fall, never rise, a new file starts at zero, and `// sleep-ok: <reason>` on the call's line is the only escape. Prefer `testing/synctest`, a polling helper or a channel.

- `synctest.Wait` is not a general goroutine barrier (#1836): mutex contention and real socket I/O, including an `httptest` server's accept loop, do not count as durably blocked. A test waiting for quiescence can hang even though its target operation is waiting as intended. For these integration tests, use explicit entered/release/completed channels and bounded waits; reserve `synctest` for fixtures whose blocking operations it can track.

- Test authoring realm comparisons as a matrix, not just default-export shape (#1200). `structuredClone`, Response JSON, iterators, streams and WebCrypto can return host objects during initialization; compare Object/Array, containers, regexps, dates and binary families in both native ESM and the probe. Preserve cycles, transfers, real subclass negatives and native null-prototype records. State the supported `instanceof` contract separately from exact cross-realm prototype identity. EventTarget listener methods must be synchronous and return undefined: observe nonconforming ordinary promise/thenable rejections before returning OPTIONS, and keep secondary cleanup errors alongside the primary failure. If caller constructor/species machinery prevents handler installation, report the observation error and attempt owned-worker cleanup, but do not claim that the original rejection was handled or alter caller promises/global rejection policy. Ready must precede `module.evaluate()`, which can block synchronously forever; recorded SIGINT/SIGTERM exit priority survives a late or failed cleanup without inventing reaping evidence.

- Keep the source authoring suite inside its portable budget with bounded parallelism, not a larger timeout or retries (#1200). Run only cases that neither stall the parent event loop on purpose nor count process-wide resources together, give each owned worker or child one slot sized from `availableParallelism()`, and spawn children asynchronously: a `spawnSync` in one case stalls the parent loop and can push another case's library result past its absolute deadline.
- A source authoring verdict must match SDK property lookup, not just own descriptors (#1200). Inspect inherited data members without invoking accessors and allow legitimate class/prototype Programs. Reread the live default export and its members after the observation window. Shared host Web APIs need matching binary-data and WebCrypto constructors for ordinary Node initialization semantics. Recheck an absolute monotonic deadline when admitting every result, including after worker cleanup; an event-loop stall can deliver an over-budget success before the timer callback. Convert worker setup/permission denial into structured library failure without granting the denied authority. Signal tests wait for a real handler-ready IPC handshake, including under startup stalls, never a fixed delay or extra stdout lines.

- Source authoring must distinguish host intrinsic prototypes from user prototypes (#1200). Snapshot both intrinsic Object.prototype descriptor baselines before evaluation; inspect added/changed contributions while excluding unchanged native members and preserving nearer data shadowing. Web APIs return host-realm plain objects, so inspect that realm too and still follow explicit SDK member lookup. Exempt only a prototype's own `constructor` back-reference to its class or function: a hidden inherited `constructor` is a member the SDK dispatches (`h is not a function`). Reject directly recognizable class constructors in handler slots without invoking them or claiming to predict opaque bound/proxy behavior. Use a curated, version-gated Web API list with constructors matching native instances, including Performance and CustomEvent. Direct literal global-loader destructuring is the same policy as a literal property read, not arbitrary alias analysis. Canonicalize the tool entry for npm/symlink invocation without permitting input links. Accept canonical disabled capabilities (`http: false`), not enabled or malformed ones. Validate structural AbortSignals and option getters before creating a worker; listener registration/removal failures must produce a structured failure and never bypass termination. Test real library calls under strict unhandled-rejection handling and compare owned-worker/MessagePort counts to the baseline.

- Offline source authoring is not production admission (#1200). Keep parsing separate from an explicit trusted-code probe; VM/worker heap guards are not OS security or hard RSS bounds. A successful module with unclosed MessageChannel ports or intervals must not hang the caller: emit the verdict only after terminating and waiting for its owned worker. Keep the control port referenced while top-level await is pending so Node cannot abandon evaluation before the external deadline. Observe initialization rejections before success, test real SIGINT/SIGTERM and pipe EOF with a neighboring sentinel, and do not claim verification of arbitrary future handler errors or real confined-Deno acceptance.

- Transport fixtures must not repair the protocol they test (#1227). A registry fixture that silently rewrites a malformed manifest route can validate an incorrect OCI client. Preserve the exact request path and the real registry response. Separate the client's direct-source requests from a mirror's own upstream fetches: only the former prove fallback. Record observed response bytes and hashes, not copies of the expected metadata, and require uncached download evidence. A negative case must reach its intended injected fault and leave no installed extension or contributions; an unrelated preview error is not a pass. A positive case requires explicit enabled, installed-source and running states in both detail and list responses, plus the expected contributions. Fixture self-checks, HTTP 200 and a `starting` state are preparation, not installation acceptance.
- Mock mutation responses must use the same session decoration and manager ownership as HTTP lists and WebSocket deltas (#1124). An incoming reply returned the undecorated ownership envelope while its delta included `manager`; reuse `stamp_row(decorated_session(...))` rather than weakening full-session equality assertions.

- Run release privacy preflight with the actual selected build config (#1245): a scan without `PERSONA_CONFIG` can miss account-specific inputs. Generic provisioning profile labels already published in the default mapping or deployment fixture are not private merely because an account selects them. Classify only those exact literal labels under `app_store.provisioning_profiles`, reading the already published `origin/main` definitions rather than allowing a working-tree or unpublished commit to approve its own new label; keep custom/near-match labels, values under another key and explicit `private-words.txt` entries protected. Parse historical fixture data without executing it, and never work around the failure by dropping signing overrides, renaming registered resources or disabling the source/bundle privacy gates. Compound release commands use `set -euo pipefail` and stop on a rejected preflight before commit, push, archive or upload.

- Separate code-signature verification from simulator entitlement context (#6192). An unsigned run had Keychain and saved-pairing failures; the final ad-hoc signed run passed those unchanged assertions. However, its embedded signature and ordinary `.xcent` were empty while `*-Simulated.xcent` contained three configured Keychain access groups. Check both and verify the actual signature; do not infer effective simulator entitlements solely from `codesign -d --entitlements`, or treat a signing difference as a proven root cause. Keep failed evidence, preserve persistence assertions, and do not extend simulator results to production Keychain access-group continuity or change registered profiles.
- GUI send acceptance needs run-unique messages and appearance-scoped synthetic histories (#6192). Reusing the same old-hub conversation both scrolled the original seed outside the visible tail and left same-name replies that could falsely satisfy a later send assertion. Use fresh synthetic regular/main conversations for each appearance, give sends a unique suffix and retain the exact suffix across the cold relaunch. Keep the no-pairing-reinjection assertion, all history/main checks and previous failure evidence; do not delete old messages or weaken the expected reply.
- Mirrored transport bytes must be compared from the same envelope (#6192). Calling a computed recording receipt dictionary twice performs two JSON encodings whose object-key ordering may differ. Capture one dictionary, then compare its canonical and compatibility values; separately test decoding and invalid-canonical precedence. Do not change production serialization or relax the same-envelope byte equality assertion.
- Compare compatibility fixtures with the actual old wire value, not an engine-specific assumption (#6192). The Echo runtime's session origin flag is false even when opened through the hub; treating every created session as a herdr-origin true caused an otherwise successful old-hub integration run to fail. Assert field presence and exact old/new value equivalence; test true, false and conflicting aliases separately with synthetic unit fixtures.
- An embedded SDK runtime may not keep a standalone fixture process alive before its node is ready (#6192). Its retry timers are unref'd: launching the Echo example before the runtime socket exists can end the process before reconnection. Wait for the socket and keep a referenced lifetime handle in the lab wrapper; retain bounded startup checks and stop only the lab's process/container.
- Independent SwiftPM modules can reuse the single Apple compatibility source via a relative source symlink (#6192). Define `NATIVE_MIGRATION_STORAGE_ONLY` on the Link target to exclude main-app Intent/Activity types. Test the actual package build, and compare both paths after `resolve()` when enforcing the single physical source: on macOS `/tmp` itself resolves to `/private/tmp`.

- An environment-only live probe is not a Gradle test-task input (#6223). `cleanTest` removes the local report but does not stop the build cache restoring a successful report from another disposable endpoint. Disable build/configuration caches and explicitly rerun the selected test task; retain its output privately. Independently assert the fixture's real send counter increases exactly once for two same-ID writes. A cached exit 0 then fails acceptance instead of falsely proving transport compatibility.

- Go `-overlay` paths must match the source paths used by the build (#4889). When a checkout is reached through a symlink, mapping only its resolved path can leave the original file in use. Map both the logical and resolved paths, then confirm the mutation actually makes the regression test fail before treating it as evidence.

- GitHub's GFM Markdown API inserts breaks between separate badge source lines (#6182). Put a badge group on one source line to keep it horizontal. When a private repository is unavailable to the browser, label API-rendered screenshots as previews, use GitHub styles, and resolve `<picture>` sources and badges from the verified branch assets.

- Regenerate README assets before checking them (#6182). A committed Android 11 badge looked correct while the generator had no API 30 mapping and regenerated it as API 30. Compare every emitted badge and icon with its committed SVG, not only localized banners, and check HTML image/link paths as well as Markdown links.

- A restart is a separate bounded test phase (#6168). Do not reuse the initial activation context after RPC checks and a graceful stop: stop can consume its remaining deadline before restart begins. Give restart a fresh context with the same eight-second budget, retain all isolation/cleanup assertions, and verify both ordinary and race runs; increasing the timeout would hide the coupling.

- A web test that times out only in the full suite is usually loading or reading something inside its body (2026-11-06, #4955). Vitest reports the timeout as `STACK_TRACE_ERROR` at about 5000 ms (or the test's own budget). On unchanged master with `--maxWorkers=96`, 10 of 10 runs were red. `catalog.test` re-read every language's catalog files and loaded the admin catalogs in each test, `busy-motion.test` walked the whole source tree in each test, and `highlight-lazy.test` did its first `import("rehype-highlight")` inside the test, while the checks themselves take milliseconds. Load or read once at the top of the file instead (collection is not covered by the test timeout), and don't raise the timeout; #5326 (inlined catalogs under Vitest) and #5729 (Persona's warm-up) fixed other cases of the same kind. Two of the failures were not load. `initI18n` applied whichever overlapping call finished last, so a test that timed out mid-switch changed the next test's language (`a、b、c`); now only the latest call applies. `formatList` follows the system region, which under Node is the machine's locale, so on an en-SG machine `a, b and c` failed every run; tests that format set `navigator.languages` themselves.

- A concurrency test that passes with `-race` can still fail without it (2026-11-06, #5908). `-race` slows the instrumented code, so the goroutines meet in a different order. `TestInstallClaimsOperation` passed 20 of 20 runs with `-race` and failed 13 to 29 of 40 without it. Without `-race` its second install usually ran only after the first had committed and used up the preview, and got a 403 from the market check that the PR's own `-race` runs never reached. Run a new concurrency test with `-count`, both with and without `-race`. For the interleaving that matters, set up the state directly instead of hoping two goroutines meet there (here: claim the operation, drop the preview, then install again).

- Goroutines that outlive a test must not call `t.Logf` or read global switches changed after the test ends (2026-11-06, #4822). `computer.nolock` / `computer.cursor` send in their own goroutines; late logging panics (`Log in goroutine after ... has completed`). A fake Mac node reading global `fakeNodesBuiltin` per request sees changes from the preceding test's cleanup, letting late requests fail an already-finished test. Guard logging with a lock and `over` flag set by `t.Cleanup`; fake nodes capture the switch once at creation.

- The assistant bus writes `m.sessions` and `h.clients`; tests modifying them need the same lock (2026-11-06, #4822). `takeFleetSessions` writes `fleet.sessions` under `m.mu`. Direct test assignment to `n.m.sessions` produces DATA RACE with `-race -count=30` and can be overwritten by an old list still in transit. Use `editSessions` (first `settleAssistant`, then edit under `m.mu`) and `appClient(h)`, not direct assignment.

- When multiple goroutines independently generate and send the same state, generation and sending need one lock (2026-11-06, #4822). On assistant connection, `FleetUp` sends each machine's list, while `Resync` snapshots also resend lists. Previously each called `decorated()` before leaving its lock, then `Tell` afterward; a stale list lacking the snapshot's "Persona" title sometimes reached hub last and stayed, failing `TestTheMainConversationIsAlwaysPersona` about 3% of the time. `publishList` now generates, deduplicates, and sends under `sentMu`. Waiting in tests for "the new list arrived" is insufficient: it may be that stale list.

- Waiting for one effect does not wait for another made on a different goroutine (2026-11-09, #2152). A source page's tree changes first and its frame is sent later by the delivery goroutine (`ui_page_delivery.go`), so `TestPageNativeFiniteLoadStillStartsWithLoading`, which waited for the tree to say `loaded` and then counted delivered frames, failed only on busy CI. Wait for each effect you assert on (`pageAwait` for the frames too). To reproduce such a gap, slow the receiving side in a scratch copy (a fake `Notify` that sleeps 20 ms failed 10 of 10 runs), or run `go test -cpu 1 -count=60` with every core busy (4 of 60 failed); an idle machine passed 40 of 40.

- Fixtures must use real response shapes (2026-11-06, #2938 / #2017). Assistant-feed `view_actions` is an object indexed by card key, empty as `{}` too. When both web and acceptance drivers mistook it for an array, array fixtures kept unit tests green while the real model lost answered state after reload. Use protocol objects covering empty objects, answered snapshots, and pagination; in a real browser also reload and check latest version, answered state, and no repeat sends.

- For autonomous rendering acceptance, specify the user scenario without secretly prescribing tools (2026-11-06, #2017). "Update progress in the same location" lets the model show a selection card in the body, then a progress card after selection; a driver requiring the first card to be live will not select it. State a same-interface-throughout requirement in natural language, without schemas, tool names, or prefab cards. Preserve the original failure; an available tool list does not prove the model called a tool.

- Launch an actual browser once before spending real model calls (2026-11-06, #2017). `python -I` omits user sites, so an installed Playwright may not import. Retain import isolation and explicitly add only trusted installed dependency paths, not working/download directories or `sitecustomize`. Resume after dependency failure only with private failure records, a same-version test hub without a main chat, and a session with no submitted actions; never click already-submitted actions again.

- Progress changes do not prove end-to-end completion (2026-11-06, #2017). After checking the same DOM and View ID, wait for completion, preserve answered state, restore after reload, and read-only verify the unique outbox entry and chat reply in the test database. The model may remove selection buttons during updates; removed or disabled controls must not resubmit, but answered state must still restore.

- Check pinned-source test evidence separately from production downgrade prevention (2026-11-06, #2017). Further undeployed mainline commits neither erase passed evidence for pinned source nor let old evidence cover new source. Controlled forward updates must confirm the candidate belongs to official history and contains the actually installed version as an ancestor, then verify production web provenance, input tree, build output, configuration, and hash; never overwrite web assets of unknown provenance. Preserve backups and CAS rollback. If release windows conflict, continue isolated validation only, without defaulting to production updates again.

- A guard-rejected launch is not successful restricted execution (2026-11-06, #2017). Once verified that rejection returns before native CLI execution, classify it separately as blocked and preserve logs. Still require an actual restricted launch, tool allowlist, and real chat tool-call evidence. No successful launch or any unknown launch means failure; never delete rejection logs to manufacture a pass.


- `-race` reports reads/writes without a happens-before relationship; a test's own lock can hide a race (2026-11-05, #2955). `startAsync` reads the launch record's session after `go func()`; a fast background failure changes it in `finish` under a lock. Existing tests read, then wait through the `startLog` lock; `finish` acquires that lock next, so the detector sees the read before the write. It reports only if the caller is preempted just after `go` (one occasional full-suite `-race` failure). Regression `TestStartAsyncAnswersBeforeAFastFailure` waits only on the record's `done` channel and reports every time before the fix. Race-test result waiting must not share a lock with the code under test.

- When testing fixed interactive-shell scripts, isolate `HOME`, `USERPROFILE`, and `ZDOTDIR` first (2026-11-05, #3120). Replacing only the update command still reads the user's shell startup files, mixing toolchain notices into `CombinedOutput` and failing exact-output assertions. Point all three directories at a test temp directory; retain success, failure, and unknown-action rejection assertions without changing production shell behavior or user configuration.

- Model stubs record only the calls under test (2026-11-05, #2682). Agent naming inherits the same test model through `kb` and can be miscounted as completion judging; GET probes with no body can also overwrite `js.input`, making successful real judgments appear to have empty input. Use a separate synthetic naming stub; judge endpoint accepts POST only. Regressions verify naming and GET before/after judgment do not change counts or input. On interrupt, assert zero judge calls directly, not mixed counts after sleep.

- Model rate-limit fixtures count only model POSTs (2026-11-05, #2763). Counting every HTTP method in sent/inflight/most lets GET probes alter peak concurrency and consume the next fake response, measuring something other than the model concurrency limit. Non-POST returns 404 before counting; regress counts before/after GET and one real model POST. Preserve limit learning, recovery probes, max-two concurrency, and no-failure assertions. Do not alter the production limiter, loosen limits, or add sleep.

- Publish startup evidence only after dependent resources are ready (2026-11-05, #2504). nodeupdate fixtures wrote version/PID/environment evidence before listening on the Unix socket; re-exec tests connected immediately after seeing the new version and sometimes found no socket. Ten passing targeted repeats do not eliminate a confirmed race. Move evidence creation after successful net.Listen; retain connection timeout and actual re-exec assertions rather than hiding ordering errors with retries or longer deadlines.

- Full-script fixture timeouts are hang protection; leave load-dependent headroom (2026-11-05, #2264). Hub deployment fixtures run real `deploy.sh`, spawning about 48 Python processes plus bash for every fake ssh hop. CPU time is about 7 seconds; wall time grows with load (18-core Mac: load 35 ≈10 seconds, 45–50 ≈24–27 seconds, 80 >30 seconds). Timestamped `bash -x` shows time spread across steps, even fake `hostname -s` taking 1.7 seconds, with no single hang. Thus `timeout=30` occasionally raises `TimeoutExpired` on busy machines (40 failures in 50 runs with six concurrent tests). Use a named 120-second constant with rationale, preserving assertions. Diagnose by child-process rusage CPU far below wall time, `uptime` load far above core count, and no single long trace interval.

- First execution of newly written scripts/programs on macOS can wait nearly 10 seconds (2026-11-05, #2307). System/security checks precede execution; the first tens of seconds of `go test ./...` link and launch dozens of new test programs, queueing those checks.
  Executing a newly written two-line sh script every 0.5 seconds measured median 0.26 seconds, max 9.5 seconds, while existing `/bin/sh` and `zsh` stayed below 0.1 seconds. If a test passes alone but fails in `go test ./...`, check whether it waits on a new executable first.
  These are hang-protection deadlines; allow 30 seconds. This is not load-related: a separate `go test` at the same load stayed fast.

- A deadline that starts at a child's exec also times the child's start-up (2026-11-08, #1110). The path-access preflight tests start the node test binary as the check's child and cut it after a 400 ms stall. Under the whole server suite's load the binary took longer than one 400 ms stall to print its first line, which the old timers counted as a read, so the check read nothing and warned, while each read takes microseconds. Start-up now has its own budget (`preflightStart`: one stall in the product, so a run is still five stalls; 30 s in `TestMain`), and read stalls count from the child's first line. The hung-read test is timed from the moment the child runs (`NODE_TEST_PREFLIGHT_STARTED`) and keeps the short stall, so it still proves the hung read is cut. A child made to start late (`NODE_TEST_PREFLIGHT_SLOW`) fails on the old timers: test that way rather than by raising the stall.

- `.Local()` in timezone scanning is not always `time.Time.Local()` (#2239): Orca terminals have a same-named boolean method for local execution. Inspect AST and return types; exclude only calls explicitly returning bool, conservatively inspect unknown types, and add no whole-file exemptions. A test with boolean and time calls on the same line ensures the exclusion does not hide real timezone calls.

- WebKit readiness probes wait for native loading state before JS (2026-11-06, #2025). `loaded` invoked JS with a 5-second watchdog before checking `isLoading`; first WebContent startup exceeding 5 seconds let the helper remove the page before its 8-second loading window expired. If navigation has not begun (nil URL) or loading continues, wait on native state only; inspect DOM afterward, preserving timeout and assertions. Give initial load, page clear, and final snapshot independent stage labels. Watchdog-caused clear/exit logs do not prove early app destruction; slow-start logs are clues, not confirmed root cause.

- `@MainActor` does not serialize Swift Testing (2026-11-06, #2025). Async `@Test`s interleave at await; the old real-WebView suite lacked `.serialized`, and mixed execution with the security suite timed out loading the network-positive-control page. Declare resource suites `@Suite(.serialized)` and validate the two WebKit suites in separate batches. Preserve original page-wait bounds, network positive controls, and zero-connection assertions; no extra time or skipped controls to pass.

- Network-isolation regression listeners can also be probed by Agent port discovery (2026-11-06, #2025). `server/internal/node/ports.go` finds listeners in Agent process trees and sends `GET /`, `User-Agent: persona-ports` to actual bind addresses; another loopback address does not guarantee isolation. Attribute the noise only after an independent listener without WebView receives the same requests. Isolate only this task's test processes, retaining reachable-network controls and zero-new-connection assertions. Do not filter by UA or stop/change real node; preserve original failures.
- JVM loopback mocks have the same port-discovery exposure (#6181): an independent listener received a resident probe while unrelated MockWebServer tests timed out or counted an extra request. Run offline JVM-only suites in a private user/network namespace with its own loopback; keep test servers and clients together, without filtering requests or changing the resident node. Set `GRADLE_USER_HOME` and Java's `user.home` explicitly to the existing development directories: mapping the namespace UID to root otherwise makes the JVM choose an inaccessible root home even when the shell's `HOME` is unchanged. Database-backed native UI fixtures still need their real routed connectivity and are verified separately.
- Security acceptance covers only the source actually tested (2026-11-06, #2025). The normal Mac matrix verified #2778's exact sandbox. A candidate adding only `loaded != nil` duplicate protection in the content-process-exit callback still cannot inherit that matrix as tested evidence for its branch. Record source hash and diff; untested platforms retain fallback. A normal matrix is not infinite-loop or real OS-termination acceptance.

- Queued HTTP mocks need a private network when resident port discovery is active (2026-11-07, #5837). A probe can consume a MockWebServer response before the test's request, causing different tests to wait until their original read timeout. On Linux, run the same tests under `unshare -rn`, bring loopback up, and retain their assertions and deadlines; separate JVMs or lower worker counts do not isolate sockets. A mapped-root namespace also changes Java's default home, so set `-Duser.home` and `ANDROID_USER_HOME` to private temporary directories. Give it a private `GRADLE_USER_HOME` seeded with dependency caches but without lock files or daemon configuration: shared cache-lock coordination sockets are outside its network namespace. Inspect the current Gradle failure stage before reading test XML, which may be left over from a previous run. For offline Robolectric tests, provide cached public runtime jars through `robolectric.offline` and `robolectric.dependency.dir`; Gradle's `--offline` does not configure Robolectric's own resolver. Do not change production timeouts, filter away probe requests, or stop someone else's resident node.
- Security acceptance covers only the source actually tested (2026-11-06, #2025). The normal Mac matrix verified #2778's exact sandbox. A candidate adding only `loaded != nil` duplicate protection in the content-process-exit callback still cannot inherit that matrix as tested evidence for its branch. Record source hash and diff; untested platforms retain fallback. A normal matrix is not infinite-loop or real OS-termination acceptance.

- Detaching test processes from an Agent process tree does not isolate WebKit resources (2026-11-06, #2025). Double-fork prevents local port discovery from probing listeners, but WebKit helpers still use XPC in the shared login session. Public APIs cannot target this WebView's content process for retrieval/termination or set CPU quotas. Do not run literal loops in shared user sessions or kill by global process name. Infinite-loop acceptance needs a disposable Mac VM / test login session destroyable externally. Record normal matrices, public-delegate fault injection, and real OS termination separately; keep fallback until complete.
- External watchdogs must cover the actual test process group (2026-11-06, #2025). If both outer driver and inner test use `start_new_session`, stopping only the driver's PGID misses the real test group. Record owned PID/PGID, TERM then bounded KILL and wait; print terminal state on success, failure, and timeout. Every listener/JS callback needs a deadline too. Fail immediately on load failure, rather than continuing and treating empty results as passes.

- Chat empty state must reflect rendered content, not only transcript rows (2026-11-06, #2024). Live cards come from a separate feed, so a page can have cards when `store.rows` is empty. Using that alone draws "No messages yet" above cards and possibly a loading skeleton. Check composed chat rows, transcript, pending/incoming messages, and streaming body together. Retain a truly empty-chat control test; do not simply remove empty states.

- HTML screenshots wait for page content, not just the native container (2026-11-05, #2024). The shared example's `assistant-view-card` can appear before WebView finishes loading, yielding a white screenshot. Wait for actual shared-HTML buttons, then scroll into the unobstructed area between chat header and input. A button behind the header cannot be clicked and produces blank captures. Sending scrolls to the new message and releases offscreen WebViews; duplicate-selection regressions must first scroll back to the same card before clicking, retaining one-reply and zero-extra-POST assertions.
- Resetting the same mock session does not reset client chat caches (2026-11-06, #2024). A body-decision example cached in `ChatCache` remains when switching to a form example, leaving two cards and ambiguous `assistant-view-card` queries. Launch synthetic fixtures with existing `-openChatsFresh YES`, narrowing card queries by specific form fields. Form restart recovery then depends more strictly on server feed; still verify native confirmation, one reply, and restored submitted values.

- Runtime CLI element filtering uses `id`, not `identifier` (2026-11-06, #2024). The latter is an XCUITest property; passing it to the runtime omits ID filtering and is not card-readiness evidence. Validate real card IDs plus visibility/on-screen bounds; still use XCUITest for nested WebView body buttons. Never fall back to fixed-sleep screenshots when a button cannot be found.

- Do not nest `#require` in Swift Testing macros (2026-11-05, #2024). `#require(JSONSerialization.jsonObject(with: try #require(body)) …)` fails real compilation with recursive expansion. Unwrap body in a separate `#require`, then parse/assert. `#expect(array.allSatisfy(keyPath))` can also be mistaken for a throwing closure during macro expansion; use an explicit nonthrowing closure. Preserve assertions rather than skip tests.
- Another session can claim a build slot between inspection and launch (2026-11-05, #2024). `pgrep` observes, not locks. All `xcodebuild` must use `~/.local/bin/xcode-slot xcodebuild …` for the machine-wide slot, max two concurrent, queue when full. Never bypass the lock or increase `XCODE_SLOTS`. Outer waits have an overall timeout; distinguish not started, build failure, and success. A queue-exit code is neither test failure nor pass; serialize your own builds.

- After resolving rebase conflicts, `git diff --check` covers only uncommitted edits; a clean worktree does not prove commits lack conflict markers (#2195). Before push/merge also run `git diff origin/main...HEAD --check` over the full PR diff.

- When moving a settings entry between levels, update DEBUG deep links too (#2195). After channels leave extensions, `-openChannels` / `-openChannel` must no longer push the extensions page first. iPad / Mac columns retain both settings list and detail; assertions that a detail "has no such row" must scope to the detail list, not the whole app. Scope scrolling to the intended list too, rather than swiping the neighboring detail.

- Plain-text replacement of assistant cards must track every code fence (2026-11-05, #2024). Looking only for `persona-view` starts mistakes examples inside ordinary fences for cards. Splitting by `\n` and stripping only horizontal whitespace leaves `\r` in CRLF language names, leaking JSON to Watch and Siri. Split by actual newline characters while preserving endings; track ordinary-fence opens/closes and replace only cards outside them with fallback. Regress examples in ordinary fences, unclosed ordinary fences, CRLF / CR / LF, and language-name case.

- macOS Unix-socket regressions need short canonical temp directories (2026-11-03, #277). Long system `TMPDIR` exceeds socket address length; `/tmp` can still resolve to `/private/tmp`, making a Codex fixture's original HOME differ from canonical HOME returned after initialization and appearing as handshake timeout. Use independent `/private/tmp/<short-directory>` as test `TMPDIR`, without changing lexical compatibility rules for runtime roots. Preserve failures first, retest pinned source separately; real `lsof` port-scanning defects belong to #541 / #728, not temp-directory issues.
- Go 1.27's `testing.T.TempDir()` prefers `GOTMPDIR` over `TMPDIR` (confirmed in the toolchain's `src/testing/testing.go`, #6223). Changing only `TMPDIR` leaves test fixtures under the old root. For Unix-socket and configuration-ancestor tests, set both variables to the same new, real, short system temporary directory and isolate `HOME` separately. A long root can exceed Unix-socket path limits; a symlink into a personal home can expose real Codex configuration to ancestor scans. Verify available disk space and effective roots, preserve build caches separately, and never weaken the security assertions to compensate for a misconfigured runner.
- The server test wrapper hides macOS `/var` path-spelling mismatches (2026-11-08, #1105). `scripts/test/server_test_env.py`, and so hosted CI, sets `TMPDIR` and `GOTMPDIR` to a resolved directory. A test that compares a path the product resolves with `filepath.EvalSymlinks` (such as `hubBinary`) against one built from `t.TempDir()` passes there and on Linux, but fails under a plain `go test` on a Mac, where `t.TempDir()` is under `/var -> /private/var`. Resolve the expected value at the assertion. Resolving the shared temp home instead would also hide a product that stopped resolving. To check a whole package under `/var` with the wrapper, reset the variables inside its command: `server_test_env.py --database-file <file> -- env -u GOTMPDIR TMPDIR=<macOS temp dir> go test ./internal/hub/`.
- The same `/var -> /private/var` mismatch hid a release bug in the Python scripts (2026-11-09, #1960). `prune_nodes` compared the resolved `bin/nodes` target with unresolved version directories, so under a root with a linked ancestor it deleted the version in use; `NodeRetentionTests` failed only on a Mac, and resolving the test's temporary directory would have made it pass. Fix such tests at the assertion (or in the product), and build the fixture root through an explicit link so Linux CI catches it too. Separately, `scripts/ci/python-tests.py` runs every `scripts/**/test_*.py` under `unittest`: an argument-driven script with that name runs no tests, which exits 0 on CI's Python 3.11 but 5 ("NO TESTS RAN") on 3.12+, so the runner skips files that are not `unittest` suites.

- `xcodegen` and `xcodebuild` must not concurrently read/write the same worktree's project (2026-11-04, #1045). During iOS revalidation, project regeneration left Mac build temporarily without `project.pbxproj`, exiting 74 before compilation. Generate completely once, then build serially; parallel validation requires separate projects/worktrees, not just separate DerivedData. Preserve original exit code; record serial retest after generation separately.

- Dynamic Island pixel-layout tests should not wait for `ImageRenderer.cgImage` before copying it (2026-11-04, #1127). On a dedicated new simulator, providers repeatedly timed out in phase 6, with the first 9 samples empty; the same log showed about 19 seconds of pipeline compilation, but the specific delay was not confirmed.
  For pure SwiftUI symbol/text/geometry layouts, use public `ImageRenderer.render(rasterizationScale:renderer:)` to draw directly into the test's CGContext, explicitly preserving point-to-pixel scale. Retain nonempty, radius, bounds, and four-digit overflow assertions; calibrate with rectangles of known sizes/offsets. No production UI changes or loosened thresholds.
  Parameterized-case xcresult summaries count methods, not expanded arguments. Verify completeness from test-tree Arguments nodes, ensuring every digit range executed. If postprocessing counts are wrong, fix reading only; retain original script exit code separately from actual XCTest results.

- `xcrun simctl privacy <udid> grant` can take about a minute on a just-booted stock simulator (2026-11-08, #1402). On the hosted
  iOS lane it needed 54.5 s on a passing run and passed 60 s on a failing one, after `simctl bootstatus -b` had already returned.
  Give the step room in `scripts/ci/apple.sh` (300 s) rather than retrying it, and read a grant timeout as slowness, not a refused permission.

- Validate display names separately from persisted identity during compatible renaming (2026-11-04, #1189). After `persona-link` / `Persona Link` shipped, compressed-install regressions still asserted old display names, causing full release exit 1. Update only display contracts, while verifying real `persona-link` files, legacy download names, old/new executable commands, and actual installer calls; never blindly rename compatibility identities. Script tests must discover `scripts/test`, `release`, `deploy`, `mock`, and `app` separately; rename-only subsets miss old assertions elsewhere. Preserve original failed freeze/exit code; after merge, rerun with a new build number.

- Do not use computer-use to click simulators for screenshots (Mac lock screen hides windows; unlocked it steals the user's mouse). Use DEBUG launch arguments (`docs/apple/ios.md`) to open pages directly, then capture.
- `xcodebuild test` launches the app without `-pairURL` and requests notification permission; subsequent screenshots are covered by that dialog. Restart the simulator after tests before screenshots.
  Live-card tests also handle dialogs first (#2024). HTTP-loaded cards do not prove the app is foreground or WebSocket-connected. Before pushing updates, wait for the mock's actual upgrade connection, not optional `client_hello`, which compatible clients may omit.
- A fresh install (or reinstall with changed signing) launched first with `-pairURL` shows notification permission. While the dialog is open, the app remains inactive and Inbox does not refresh:
  the new shell avatar's Inbox badge is absent, `ShellAvatarTests` reports "no inbox mark", and screenshots are gray (2026-11-04). `simctl privacy grant` cannot grant notification permission
  (Operation not permitted). UI tests tap Allow in springboard after launch (`ShellAvatarTests.launch`); run such a test once before manual screenshots.
- System permission dialogs left by a previous launch survive app uninstall/reinstall; restart with `simctl shutdown` (manual `simctl launch` without `-pairURL` triggers notification permission).
- `xcrun simctl launch` only foregrounds an already-running app; new launch arguments do not apply. Use `--terminate-running-process` before screenshots.
- `MOCK_EXTENSIONS=1` keeps plugin settings, secrets, and switches in process memory (2026-11-04, #1711). After light-mode `ExtensionSettingsTests`, "Example Notes" is configured;
  an immediate dark run fails at the start of `testSetUpAnExtension` because "Needs setup" is absent, looking like broken code. Restart the mock by port between modes/reruns (like `MOCK_WECHAT`).
- All UI tests launch with `app.launchPinned()` (`UITestStyle`). The app remembers prior test styles; BeautifulUI looks different, so unspecified tests inherit someone else's appearance
  (LongSessionTests failed after BeautifulStyleTests). Default is Liquid Glass; `TEST_RUNNER_PERSONA_UI_STYLE=beautiful` runs the whole suite in BeautifulUI.
  Style-specific tests pass `-ui.style` themselves. It launches through `launchPersona()`, which only performs a clean Demo Mode start and never overrides navigation. `UITestStyleTests` scans source (`launch()` and `launchPersona()` both count as omissions).
- On compact screens, SwiftUI can inherit `assistant-sidebar` onto the drawer footer buttons, replacing their individual identifiers in XCUITest even though they remain visible. Query the actual exported tree: the shared test helpers first try the individual identifier, then the drawer-container identifier plus the exact localized label. Do not weaken the visibility or tap assertions, or use an app-wide Settings label that can match a hidden page.
- Drawer-close swipes start on the visible pushed-aside page, not a fixed screen fraction: conversation rows deliberately own left swipes for Archive, and sidebar sizing can move a row under that fraction. Intersect the close surface with the window before choosing the start point; its full frame can extend offscreen. Keep the real swipe and the closed-drawer assertion.
- Runtime CLI `defaults --set` uses the registration domain, not persisted preferences. Upgrade tests must seed a disposable app's real preferences while its simulator is shut down, verify the baseline reads them, then install in place and inspect persistence after startup cleanup. Resolve the app container before shutdown; `simctl get_app_container` cannot look it up in the Shutdown state. CFPreferences writes asynchronously: terminating the app does not make an immediate disk read authoritative. Resolve its preferences path, shut down the owned simulator to let writes finish, then inspect that file; do not add synchronous writes to production startup just to satisfy the test.
- Tests using `ShellFinger` must launch with `-probes YES`; its page/selection assertions deliberately require the real shell probe. Migrating a test from platform tabs requires adding that probe argument, not weakening the helper to infer state from controls that remain mounted offscreen. An empty Shell composer exposes dictation instead of Send until text is entered; check the initial dictation/interrupt control and still require an enabled Send after typing.
- Model switching coverage must follow the chosen UI's actual transport: the Shell sends `/model <alias>` to the conversation, while platform mode chips can use the modes endpoint. Assert the destination and command receipt in the mock log rather than applying a retired control's disabled-state or endpoint assumptions to the default UI. UI-test runners can have a separate protected pasteboard; verify copy payloads by pasting back into the owning app, not by reading the runner's clipboard.
- Do not run PersonaTests on a simulator paired to a real hub (the one paired by `persona-sim` / `sim.sh`). iOS test hosts do not skip keychain (only Mac does, `Shared/Keychain.swift`),
  so launch uses pairing to connect to hub, registers phone tools as that simulator, and shows notification permission. On 2026-11-02 three consecutive runs reported "test runner hung before establishing connection"
  (not locked, same after simulator restart); a simulator that had never installed the app passed first try. Exact hang cause was not pursued. Pick a simulator never used for Persona from `simctl list devices`.
- UI tests can leave simulator screens locked (Home does not unlock simulators; swipe up), causing the next `xcodebuild test` to report "test runner hung before establishing connection".
  English-language simulators label system-permission buttons "Allow"; UI tests must recognize both languages.
- XCUITest typeText switches simulators to "hardware keyboard" mode, after which the software keyboard no longer appears (keyboard button says shown, but no keyboard is visible), failing all height-dependent tests.
  This machine has no Simulator.app, and `defaults write com.apple.iphonesimulator …ConnectHardwareKeyboard` cannot restore it. Create a new simulator with `scripts/app/sim-boot.sh "<new-name>"`.
- So a test that taps a field and types at once never runs keyboard-dependent layout (`KeyboardState.isUp`, the pending cards' keyboard cap) (2026-11-09, #1580):
  `SecretRunUITests` passed while tapping the password card's field froze the app as soon as the on-screen keyboard came up. Focus tests assert `app.keyboards.count > 0`
  after the tap and that the cards' area then holds one frame (a busy app stays in the foreground), and run on a fresh stock simulator before any test that types
  (`PasswordFieldFocusTests`). A tap followed by "App event loop idle notification not received" means the main thread is busy: `sample <pid>` the simulator's
  app process to see where.
- On the iOS 27.0 simulator `app.screenshot()` and `XCUIScreen.main.screenshot()` leave the keyboard's place blank (2026-11-09, #1580). Take keyboard screenshots with
  `xcrun simctl io <udid> screenshot` while it is up.
- XCUITest misses the key bar during keyboard animation (the bar is moving with it); wait for the keyboard, pause, then tap.
- XCUITest `typeText` inserts text directly, bypassing the input method, so it cannot test pinyin marked text. Tap onscreen keys (`app.keys["y"].tap()`, space selects the first candidate).
  Third-party keyboards (2026-11-03, #909): outside the repository, build a tiny app with only a keyboard extension whose buttons use `textDocumentProxy` `setMarkedText` / `unmarkText` / `insertText` to enter fixed text.
  After `simctl install`, UI tests add it in **Settings › General › Keyboard › Keyboards › Add New Keyboard**, return to the app, and hold **Next keyboard** to select it (a tap toggles only the last two).
  Third-party keyboards are not in `app.keyboards`. Uninstall afterward, or later keyboard-wait tests all report "no keyboard".
- Time-sorted lists need deterministic ties (then sort by ID): two spaces with equal update times can swap on every render, causing UI tests to tap another row.
- XCUITest launch arguments (`-key value`) override UserDefaults / `@AppStorage` reads, hiding app-written values. Use them only for values tests never change
  (last machine for new chats); reset UI-edited values (Agents grouping/filtering) through the UI at test start. `-dev.openChat ""` prevents restoring the last chat.
  Conversely, pair `-dev.openChat <session>` with `-app.tab chats`: cold launch restores chats only if the stored tab is Agents, and `-tab dev` switches too late.
  If the preceding test ended on another tab, the chat does not open (2026-10-31, `LiveUpdateTests` image case on another lane "couldn't find the process card at step one"). Use `-openSession` to open a particular chat only.
- If `-pairURL` points at a different hub from the simulator's prior pairing, the app starts over on the assistant page; `-app.tab`, `-dev.openChat`, and `-tab dev` have no effect (2026-11-01).
  Old tests always used the same mock address and missed it; changing port/hub reveals it. For reliability, launch once with only `-pairURL`, quit, then launch with page arguments (`ScrollUnderPushTests`).
- `-tab` recognizes only values listed in `PersonaApp`; unknown values do nothing, leaving stored `app.tab` from the previous run (2026-11-04, #1664). `-tab feishu` was never recognized,
  so four Lark suites (encrypted messages, multiple accounts, pinned bar, pinned chats) depended on prior state. Earlier helpers forced tab navigation: `-tab settings` tests (`SecretFieldTests`) saved `settings` without the shell resetting it.
  The next shell test opens a settings sheet that covers Messages, looking like "Messages cannot find chats". After one failure, the shell writes `app.tab` back to `assistant` and the rerun passes.
  Add a `-tab` value with every new page. To reproduce "passes alone, fails after other tests", inspect `Library/Preferences/<bundle-id>.plist` in the app container first
  (`simctl spawn … defaults write` does not write into the app container).
- "Push does not interrupt" must push while the finger is still down or momentum is active. Pushing after scrolling stops misses whole-screen jumps: `LiveUpdateTests` "middle row stays still" was always green,
  while real-hub Agents lists jumped over 900 pt during scroll. Measure frame by frame with `PerfScroll` (`docs/operations/testing.md`, "Push during scrolling").
- Mock hubs must answer like real hubs, especially unready states: with Telegram disabled, real hub cannot list its capabilities (the program has never run; capabilities are self-reported),
  but mock always advertised `login`, leaving tests/screenshots green while the real page lacked a switch, said "Configure in hub.json", and could not enable it (2026-11-03, #622).
  Before writing a mock, inspect the real path's response in every state; start tests from what users first see (disabled).
- To measure real data without modifying user state, put a read-only proxy in front of hub: forward GET; return `{}` for other methods without forwarding; forward only subscribe, unsubscribe, pong over WebSocket
  (not `register_phone`, so the simulator receives no `phone_*` calls). Run the same data with pushes enabled and held back (A/B); only the difference is caused by pushes.
  Keep proxy, recorded pushes, and screenshots under `/tmp`, never in the repository (real sessions).
- To find "why did this body run again?", temporarily add `let _ = Self._logChanges()` in body, then
  `xcrun simctl spawn <udid> log show --info --debug --predicate 'eventMessage CONTAINS "AgentsList:"'`.
  It shows which property changed (`\AppModel.allSessions changed`, `_xxx changed` is `@AppStorage`).
  On 2026-11-01 it located missed UserDefaults writes and model reads in `onChange`. Remove afterward.
- Attributing frame-by-frame scroll anomalies to pushes in the prior 0.6 seconds is only temporal coincidence: mock pushes arrive in batches, so nearly every anomaly matches one.
  To identify the cause, disable push categories one by one with temporary mock switches and rerun, then inspect the app timeline (`PerfScroll.mark`).
- Missing screenshots (Read opens PNG with no image, model says "tool had no output") are not a directory issue (2026-10-31: `$TMPDIR`, `/tmp`, and repository images all behaved alike).
  The model gateway drops images from tool results, retaining only text. Send a known image directly to the gateway in both a user message and tool_result, then compare recognition.
  UI changes must be delivered only after actually seeing screenshots; fix this path first if images are invisible. Gateway details/fix live in `CLAUDE.local.md`.
- Manually passed XCUITest environment variables use command-line `TEST_RUNNER_<variable>`, with the test process reading the unprefixed name (2026-11-03, #697). `PERSONA_MOCK_HUB=... xcodebuild test` did not reach the runner, so cases skipped for lack of mock address while still printing `TEST SUCCEEDED`. Verify actual case execution, skip counts, and assertions, not just exit code; after correcting the environment, four legacy-interface configuration regressions genuinely failed.
- Before manual `xcodebuild test` / `build-for-testing`, run `eval "$(python3 scripts/persona_config.py env)"`: only this converts configured `xcode` to `DEVELOPER_DIR`.
  Direct invocation uses system-default Xcode; an older SDK reports "no exact matches in call to initializer" for `toolbarVerticalEdge`, etc. (2026-10-31, mistaken for someone else's broken code, costing an afternoon).
- Measuring UI jumps / flicker needs VNC: record with `simctl io recordVideo`, split frames with ffmpeg, and track a pixel frame by frame. One-frame jumps are visible only this way.
  App position logs can disagree with actual pixels (2026-10-31: logs said the header stayed put while the page shifted 10pt). Trust the image.
- XCUITest `element.tap()` on iOS 27.0 simulators taps about 4pt inside the frame's top-left (27.1 taps within the circle). Circular `contentShape(.circle)` buttons
  (Persona header glass circles "Chat history", "New chat") get taps outside the circle and do nothing, appearing broken (2026-10-31 iPhone 17 Pro `testButtonsAndPickingAConversation`
  also failed on master). Fingers and VoiceOver target centers; real devices are unaffected. Use `tapCenter()` (`PersonaUITests/TapCenter.swift`).
  After tapping, assert the page actually changed, not just content shared by both pages (new-chat and chat both have "Ask Persona…", so missed taps pass and failure gets blamed on the next step).
- Do not measure scroll frame rate with XCUITest drags: each gesture first captures accessibility on the app main thread, itself causing 40–100 ms frames (every 3 seconds, looking like stutter).
  Use in-app constant-speed scrolling (`AutoScroller`, `-showFileAutoScroll`) and frame counting (`FrameMeter`), opened from an idle session (running sessions redraw each second behind it).
- Alerts with input: typing raises the keyboard and moves the alert upward; immediately tapping Save misses (alert remains, nothing happens, looking like failed save; 2026-11-01 `AssistantTitlesTests` nearly prompted an app change).
  Pause after typing, tap, then confirm the alert disappeared. Input caret placement is unpredictable; before clearing, tap the field's right edge to place it at the end.
- Run privacy scans after actual build/deployment configuration is complete: missing configuration means incomplete checks, and `clean` is not a full pass. Rescan after adding configuration.
  Fixture machine identifiers can accidentally match configured values; use neutral placeholders, not exemptions.
- `xcrun simctl push` does not run notification service extensions (2026-11-01, Xcode 27.1 / iOS 27.0). CoreSimulatorBridge adds pushes directly to Notification Center as local notifications,
  never starting the extension; `mutable-content` payloads display unchanged (relay pushes show "New message"). Put extension logic in shared code and test in the app process
  (`PushCrypto.service`, `NotificationServiceTests`); the extension itself needs a real device. Notifications from one app group together;
  expand the group before UI text queries (label contains "Grouped").
- Duo outer screens render four tabs as sidebar buttons with no `TabBar` parent in accessibility. After pairing, assert pairing page disappearance and four root navigation buttons, then inspect the engine saved by hub.
  Missing `app.tabBars` does not prove pairing failed to exit. Outer screen is only about 678pt tall; Form's final row may not be lazily loaded yet. Scroll to **Confirm and continue** before tapping.
- With Agents search collapsed by default, identify the list by a fixed navigation menu or list content, not search-field presence to decide whether still in chat. Quick filters are the first row and cannot identify the page after scrolling away.
  Pull down when input is needed; do not pull down again if already expanded and trigger refresh. Gap regressions check first-row distance from navigation bar and actual selected-pill pixels.

- If the simulator keychain retains a paired hub (such as mock `127.0.0.1:18831`), `-demo.on YES` has no effect: `Keychain.serverConfig` takes priority,
  rendering snapshots and sending requests to the dead address. Taps appear ineffective, with toast "Can't reach the hub; that action did not take effect". Before demo-mode tests,
  run `xcrun simctl keychain <udid> reset`. Same for Mac acceptance copies (keychain keyed by bundle ID): use `-openWelcome YES -demo.on YES` and clear this copy's pairing first.

## Privacy checks and dependency download sources

- 2026-11-04, #1775: even unmodified Mermaid assets can coincidentally match short private terms inside public event names. Download the pinned package from npm first, verify registry SHA-512 integrity, then byte-compare repository assets with the archive. Never log private terms or matched text.
  `check_bundle_privacy.py` checks private terms of up to 4 ASCII letters as whole words only when both filename and pinned SHA-256 match original Mermaid 12.3.0 assets. Complete short words, nonletter values, longer private values, and generic email/private-address checks remain. Hashing and scanning use the same byte snapshot, not a mutable local allowlist.
  Modified, appended, truncated, renamed, or normalization-created fragments cannot receive this classification. Other app files retain strict substring checking; existing public Go mode is unchanged. Dependency upgrades require rechecking public provenance, negative cases, and pinned digests, not merely copying new hashes. Preserve the original failed archive read-only; release fully after merge with a new freeze/build number.

- On 2026-11-02, after configuring `app.push_relay`, the publisher's public relay URL had to enter Info.plist but also matched configured personal-value scans.
  App bundles recognize only the complete configured HTTPS `/relay` URL with no credentials/query. The same domain elsewhere, appended paths/parameters, emails, or credentials still reject;
  public Go binaries get no exception. Do not remove the whole domain from the private-term list; regress adjacent and extended URLs.

- On 2026-11-02, after fixing privacy scanning, public personad still failed to deploy: networking dependencies made raw binaries about 35–37 MiB, exceeding Workers' 25 MiB per-static-asset limit, not expired cf authentication.
  Publish `.gz` downloads (about 12–14 MiB), scan uncompressed binaries first, and verify compressed archives before installer decompression; check every asset size before upload. `NO_DEPLOY=1` must also check sizes; build/scan alone is not deployment validation.
  Do not suggest signing in again for every failure. Report successful TestFlight upload separately from failed server publication, and finish internal/external distribution of uploaded builds.
- On 2026-11-02, public personad privacy false positives included SSH global-request names mistaken for emails, official contact links in Tailscale embedded pages, complete RFC1918 ranges, and cloud-metadata constants mistaken for personal data.
  Go concatenates read-only strings, even one CIDR immediately after another, so printable-string boundaries are unreliable. Trace matches to dependency source using actual compiler inputs first; never log raw matches.
  `check_bundle_privacy.py --whole-short` classifies only exact protocol/HTML/network contexts in dependencies built with the `public` tag, without replace, and with checksums matching `server/go.sum`;
  `publish.sh` first runs `go mod verify`. Configuration/private-term matching still takes precedence on raw text. Missing dependencies, checksum mismatches, or changed context reject; no whole email-domain or private-range exemptions.
  Regressions: `python3 -B -m unittest discover -s scripts/release -p 'test*privacy*.py' && python3 -B -m unittest discover -s scripts/test -p 'test_privacy*.py'`, plus `NO_DEPLOY=1 ./scripts/release/publish.sh` for four platforms.
- 2026-11-03, #963: SSH algorithm and certificate key-type names in Swift archives also contain `@`, causing generic-email false positives. Check complete printable fields against public dependency source first; never log matches.
  `check_bundle_privacy.py` classifies only 7 verified fixed names in `swift-nio-ssh`, requiring complete original-field matches. Names formed after removing bundle/team IDs do not count.
  This is not an email-domain exemption and does not change generic-email TLD scope. Private terms still match normally; surrounding context, URLs, variants, and ordinary same-domain emails do not receive protocol classification.
  Validate field-level and actual-scanner regressions, then inspect failed archives read-only. Preserve original freeze, exit code, deployed/not-uploaded state; later releases use new freezes/build numbers, never reuse failed builds.
- 2026-11-04, #1026: full SSH name-list compatibility must not become removing names from arbitrary word-boundary contexts; that wrongly permits `mailto:` URLs, XML, and names formed only after normalization (#975 regression).
  Classify only fixed names in complete original fields, or comma lists whose every item is in the fixed set with no empty items/whitespace. Unknown items, URLs, context, and fragments formed after bundle/team-ID removal still scan.
  First fail synthetic counterexamples on the old implementation, then verify field-level, actual-scanner, and private-term precedence. Retain positive full-name-list cases; a boundary fix must not break prior compatibility.
- Watch Release binaries can be arm64 + arm64_32 universal Mach-O. Certificate email/personal names in signatures are not business data; privacy scans exclude only embedded signature blobs verified structurally and by bounds in every slice.
  `LC_CODE_SIGNATURE` offsets are slice-relative; never truncate the entire file at the first slice's signature, or remove private terms because certificate false positives occurred in a universal file.
  Scan both architectures before/after signatures, unused signature-placeholder bytes, and file tails normally; malformed signatures and out-of-bounds/overlapping slices are not excluded. Regress thin/universal 32-/64-bit and both endiannesses, plus a real Watch archive.
- `privacy_check: clean` does not prove a complete private-value dictionary. Without local config/private terms, registered matches may include only local paths. npm lockfile download hosts need independent public-source rules, not only private terms; public package names do not prove public `resolved` URLs.
- Staged-content checks must read the Git index, not only the worktree. If fixed in the worktree but still old in the index, commit hooks must reject; conversely, a safe index must not be rejected because subsequent worktree edits are unsafe.
- Check lockfiles as complete JSON before ordinary text scans. NUL/binary detection must not skip them, and duplicate keys/JSON escapes must not bypass checks. Errors report only location/type, never blocked addresses.
- Existing hook files do not mean enabled hooks: `core.hooksPath` is clone-local configuration. After enabling, verify real `git commit` in an isolated temporary repo, never rejection-test the main worktree. Web builds check too, preventing hook-disabled clones from direct publication.
- Recursive-failure tests must explicitly control or simulate recursion limits, not assume a fixed depth fails on every Python version. Run Python regressions separately; `npm test` does not cover them automatically.
- If `npm ci` returns 404 because a mirror lacks a pinned version, inspect the command registry first. Even public lockfile sources can be redirected by local registry configuration. Retry with explicit public registry and locally configured proxy; do not change lockfiles, versions, or test baselines to hide environment failures.
- Async refreshes triggered by config hot switches need a generation, checked under the state lock before committing cache. Comparing only captured booleans cannot prevent out-of-order returns or old requests overwriting after "on → off → on". New-connection handshake refreshes use the same commit check.
- Static-card previews distinguish reply from push/pop/open. Action IDs are opaque, not message bodies when reply is absent. Regress with shared-example detail buttons; navigation only demonstrates locally or accurately explains its target, never calls a real chat.
- Background builds/tests require pinned source snapshots; do not merge branches or edit their worktree while running. On 2026-11-05, targeted Go regression enumerated old files before a merge introduced a new caller, producing undefined-method compilation errors. Mixed snapshots cannot judge commits or count as passes. Freeze a commit in a separate detached worktree and rerun.
- A Go test wrapper embedding a narrow `Store` interface does not gain methods by assigning real Postgres to it. Once card handlers checked optional persistence interfaces first, admin-page tests returned 503; old handlers rejected bad JSON first and hid the fixture gap. Real bus regressions explicitly pass the full DB to the assistant, while hub admin login keeps its wrapper. Retain status assertions, not longer waits or 503 as a pass.

## Dead-code reports depend on the selected build (2026-11-07, #5856)

- `deadcode -test ./...` only analyzes the selected platform and build tags. A default Linux run reports `sysmon.ptr`, the Windows tray notification helpers and `DevelopmentLimits.valid` as unreachable, but macOS, Windows and the `persona_dev_resources` build respectively call them.
- Before deleting a reported function, use repository-wide `git grep` to check callers, tests, scripts, docs and string-based references, including files excluded from the current build. Keep candidates whose reachability is uncertain; a clean build on one platform cannot establish that they are unused everywhere.

## Keep service proxy routing out of server fixtures (2026-11-08, #1175)

- Server test children must not inherit service proxy routing (#1175). An ambient HTTPS proxy redirected a local Git clone and synthetic voice DNS fixtures, and remained visible outside an extension sandbox. A local rejecting proxy reproduced all four failures; isolating only the test child's upper/lowercase HTTP, HTTPS, ALL and NO proxy variables made the unchanged fixtures pass. `server_test_env.clean_environment` removes that routing at launch; explicit fixture proxy setup still works. Preserve the parent's route for toolchain resolution, dependency preparation and publishing, rather than changing production proxy settings or increasing test deadlines.

## When Go on the hub is older than the repository (2026-11-03, #984)

- `test-server.sh` only says "server test environment preparation failed; tests were not started". `server_test_env.py` first runs `go env`;
  if hub Go is older than `server/go.mod` requires, it downloads a toolchain, but `go env -w` previously set `GOSUMDB=off` on that machine, preventing verification of the downloaded toolchain and failing `go env`.
  Not a database-config issue. Download the toolchain first in the repo with `GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn go version`,
  then put `~/go/pkg/mod/golang.org/toolchain@v0.0.1-go<version>.linux-amd64/bin` in PATH and run the same command with `GOTOOLCHAIN=local`, `GOPROXY=https://goproxy.cn,direct`.
- Only hub-cached dependencies build: if `proxy.golang.org` is unreachable, other packages report `[setup failed]`, making it look as though half the tests are broken.
- If hub Postgres is system-installed (no `persona-postgres` container), `db.sh` cannot create `persona_test` (its output is discarded), and role `persona` lacks CREATEDB:
  `internal/db`, `internal/db/dbtest`, `internal/hub` report `database "persona_test" does not exist`, while others pass.
  Run these three packages against temporary local `postgres:18-alpine` (`-p 127.0.0.1:<port>:5432`) with `PERSONA_TEST_DATABASE_URL`, then remove the container.

## Running iOS locally on a Mac with only macOS 15 (2026-11-04, #984)

- **Xcode 26.3 is the newest installable**: 26.4 requires macOS 26.2. `xcodes` downloads return 403 if the Apple ID has not accepted the developer agreement at developer.apple.com;
  xcodes saves Apple's "page not found" HTML as `.xip`, then extraction hits trace trap. Check with `file` first. Download `.xip` in a browser, then `xcodes install --path` is easiest.
- **Compiling iOS also requires the iOS platform** (simulator runtime, 8.4 GB), not only running simulators. `xcodebuild -downloadPlatform iOS` uses system services and only system proxies:
  when proxy software routes Apple CDN slowly, speed is about 20 KB/s; adding `*.apple.com`, `*.cdn-apple.com` to system proxy bypass (`networksetup -setproxybypassdomains`) reached 4 MB/s.
- **Persona scheme embeds the Watch app**: testing it requires watchOS platform first; even after installation, without a paired Watch simulator the runner still "timed out while preparing".
  For iOS unit tests only, temporarily generate a differently named project without `PersonaWatch` dependency (`xcodegen --spec <modified-copy> --project apple`), necessarily under `apple/` (Info.plist/xcconfig paths are project-relative), then delete afterward.
- **Unsigned apps lack the shared keychain group**: `CODE_SIGNING_ALLOWED=NO` builds fail `NotificationServiceTests`; temporary simulator signing with `CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY=- DEVELOPMENT_TEAM=` passes.
- **Two existing Xcode 26.3 issues (also on master)**:
  - `FileViewerTests.pathsAreLinkedOnlyWhenThereAndAskedInOneBatch` triggers a Swift concurrency isolation assertion in `FakeHub` callbacks, crashing/restarting the test host and failing xcodebuild;
    totals stop at the crash (576, actually 766). The test struct is `@MainActor`, making the closure main-thread-only, but `FakeHub` invokes it on a background thread. `{ @Sendable req in` fixes it.
  - The shell assistant drawer (`AssistantDrawerLayout`) continuously recomputed views on the main thread, starving queued work: avatar lists never refreshed and runtime CLI answered ping once only. Reproduce and fix the default tree; changing navigation for screenshots hides the user's failure.
- **Uninstalling does not clear keychain**: simulator pairing survives reinstall, so the app launches paired directly into the new shell and the runner reports "timed out while preparing". First `xcrun simctl keychain <udid> reset`.
- **Mac app deployment target is macOS 26**: macOS 15 can build only, not run the app or `PersonaMacTests`.

## Web grouping and collapse

- When aligning with iOS status groups, do not bucket raw `state` directly: idle/unknown with loops, background work, or running sub-Agents is Waiting. Read mutes from `/mutes`; match project mutes by machine/project name. Agent-specific allows override project mutes, not the Agent's own mute.
- CSS `grid-template-rows: 0fr` and opacity only hide collapsed content, not remove links from keyboard focus/accessibility. Add `inert` to sidebar collapse containers. Status dots in group headings already have equivalent text; use `aria-hidden` to avoid reading "Working" twice.
- Web screenshots wait for menu items to render, not capture on the opening frame (which may show only the shell). Store theme preference in localStorage for light/dark checks; changing only the root class reverts after reload.

## WebSocket handshake tests

- `Connection` is a comma-separated token list, so `Contains(..., "upgrade")` is not a valid upgrade check: it also matches `notupgrade`. Iterate `Header.Values`, split/trim, and `EqualFold` whole tokens. Test case, multiple fields, and substring rejection (#692).
- Go printable rodata can concatenate independent constants, making artifact scans see short terms across boundaries (2026-11-03, #692: protocol-header token adjacent to a VirtualBox NIC prefix). Locate sanitized context and check source first; do not mistake this for actual personal-value insertion, remove sensitive terms, or add exemptions. Store brand prefixes in display case and normalize comparisons; regress unchanged NIC detection, then build/scan actual artifacts for four platforms. Header validation alone does not remove this adjacency.

- Node 22's built-in `WebSocket` may emit only `error`, not `close`, when HTTP 401 / 400 rejects a handshake, leaving `readyState: CONNECTING`.
  Rejection tests must not wait for `close` or treat arbitrary network errors as correct authentication. Make HTTP upgrade requests and assert rejection status directly (wrong/missing token 401,
  wrong/missing subprotocol 400, wrong path 404); unexpected 101 or connection errors fail. Set an overall timeout and reclaim sockets. Still verify successful handshakes with a real WebSocket.

## Script-directory migration

- 2026-11-05, #1596: Robolectric screenshot output directories are generated by test execution, absent in clean checkouts. After adding hub-version and translation screenshots, doc-path checks again treated these outputs as stale source references.
  `test_doc_paths.py` registers only exact screenshot outputs, tied to real Kotlin screenshot-test files and their default `build/…` output declarations. Missing generators or renamed defaults reject even if stale output remains locally. Do not skip entire Android docs, allow arbitrary `build/` paths, or create empty directories to pass. Ordinary repository references still require existence; screenshot generation and visual acceptance are separate.

- Moving scripts into subdirectories requires more than text replacement: check root derivation via `__file__` / `$0`, sibling Python imports, cross-directory subprocesses, Go path construction, and script transfer over SSH.
  Shared config and privacy scanning stay top-level; privacy scanning locates the root from its own repository, not the caller's cwd, which might select another checkout.
- `unittest discover -s scripts` does not discover subdirectories without package markers automatically. Discover `test/`, `release/`, `deploy/`, and `mock/` separately and check counts.
  Compatibility shell entry points only `exec` forward. Test paths with spaces, empty arguments, stdin, environment, and nonzero exits; do not actually publish or restart services to verify forwarding.
- After adding old/new environment-variable aliases, subprocess fixtures clear both before injecting config. Overriding only `PERSONA_CONFIG` inherits higher-priority `PERSONA_CONFIG`,
  accidentally reading release configuration. Cross-directory import regressions cover new variables, legacy fallback, and new-variable precedence on conflict; all paths target synthetic fixtures, never change production precedence for tests.
- Server-test launchers restore the service root only to prepare databases. Before Go, clear all `PERSONA_*` / `PERSONA_*` runtime variables and explicitly inject only both variable sets for the same dedicated test DB,
  with independent temporary `HOME` / `TMPDIR` and cleared Agent / XDG external-config roots. Preserve build caches/toolchains separately, preventing tests without fixtures from reading real defaults.
  `server_test_env.py` parses URLs to confirm `persona` → `persona_test`; custom DB names, identical targets, or parse failures fail closed, never assume isolation from string replacement.
  URL paths are not necessarily the driver's final database: pgx query `dbname` / `database` (including encoded keys) overrides them. The test launcher permits only explicit TLS/connection parameters, rejects unknown/duplicate keys, service/credential-file options, and invalid queries; clears all `PG*` environment; explicitly disables implicit password/TLS files in OS user directories. Anonymous `pgconn.ParseConfig` regressions check actual `Database` exactly, without connecting; do not infer past real configurations were affected.
  Validate the launcher itself with fake resolver / SSH / Go, not just cleanup functions. Production-root inheritance on 2026-11-03 opened the real DB and started sources/knowledge cleanup before the test hub failed to listen,
  deleting at least 14 old single-post JSON files. Healthy status, occupied ports, or eventual test failure do not prove no data changes. Preserve protected original logs, investigate read-only, never automatically restore/delete production data.

## Inferring UI state from pixels

- The new shell's visible page was inferred from the screenshot color of the "All" filter (`ShellPixels`). Moving filters into the list, collapsing search by default, and changing filter rendering in BeautifulUI
  changed those pixels; tests failed, appearing to indicate app bugs, but some detection was wrong (2026-11-01). Read state if possible: add a 1pt DEBUG accessibility element
  (formerly `shell-pager-probe`, now `probe.shell`: visible page + selected tab), and read its value. Both must agree, also catching TestFlight 70's
  "selection says Agents, content shows assistant".
- Before analyzing failed UI tests, identify the failing step and actual app state at that moment (screenshots/tree via `xcresulttool export attachments`, or temporary probes + NSLog,
  timing via `simctl spawn … log show`). Only then decide stale test versus app regression: of six failures here, one was stale (intentional default-collapsed search),
  the other five reflected two real app bugs.

- Do not use `element.tap()` on controls duplicated on both pages, such as segmented switches. Accessibility can return the hidden copy, so the tap does not target where a finger would,
  letting tests pass while user taps do nothing (2026-11-02). Use coordinates (`ShellFinger`: read position once on launch, then tap coordinates), and assert probes afterward.
  Include recent events (`ShellTrace`, `trace` in `probe.shell`, each timestamped) so failures directly show whether tap arrived, routing ran, and time was spent; much faster than screenshots.
- Do not poll probes with queries that count every match: `app.probe(area, …)` first does `matching(identifier:).count`, then reads values, asking the app for a full accessibility tree each time.
  `ShellStatusRowTests` polled `probe.shell` every 0.1 seconds for status-row opening. At the placeholder moment, navigation collapsed the Agents search field (#413 behavior),
  failing two or three of the three status cases. Changing only to `["probe.shell"].firstMatch.value` made all pass (#499, 2026-11-03). Moving the probe or removing `@State` did not help.
  Read unique probes (`probe.shell`) with `firstMatch`; use `probe(_:as:page:)` only for page-duplicated probes requiring `page` selection, and avoid high-frequency polling during animations.
- `.screenProbe(area, value)` evaluates `value` in body even without `-probes`; every state read becomes a redraw dependency (RootView `connection`
  redrew the whole root on every connection change, DEBUG only). For values reading other state, use `ScreenProbe.enabled ? Value(…) : Value()`.
  Reading XCUI element `label` / `frame` can also coincide with disappearance (expired toast, collapsed search) and fail immediately. Use `try? e.snapshot()` for transient elements (2026-11-02).

- A mock-hub pairing (`-pairURL`) left in simulator keychain overrides `-demo.on YES`: `Keychain.serverConfig` takes priority over demo mode, so the app keeps connecting to a stopped
  mock, shows "Connecting…", a gray offline avatar, and a cached list (2026-11-02, appearing like broken demo mode). Before UI tests/screenshots,
  run `xcrun simctl keychain <udid> reset`.
  Within one xcodebuild, an earlier mock-paired test makes later `-demo.on YES` tests use mock data too. If mock is down (2026-11-03, old launch paths broke after moving into `scripts/mock/`),
  they all show "Connecting…", a 32pt status row, and untappable back. More pending cards after #622 fill an Agents screen, hiding Agent rows and pushing chat headers away;
  master shell tests failed too. `launchPersona()` now automatically adds `-openWelcome YES` for `-demo.on YES` without `-pairURL` (clearing pairing and cached list), so demo mode is truly demo mode.
  Test scripts also verify listening ports with `lsof` after starting mock and fail immediately if absent.

## New UI tests

- File-preview mock has two entry points (2026-11-03, #796): `MOCK_TREE` for the tree, `FILES` for full text/pagination. Update both for new Markdown fixtures, or names/sizes show new content but opening still loads the old long text and tests cannot find new links.
- `socket.create_connection(timeout=3)` leaves the timeout on the returned socket too. Forwarding mocks must call `settimeout(None)` after connection, letting WebSocket disconnection end the stream. If the simulator pauses over 3 seconds before WebView creation / HTTP requests, the old mock closes TCP while waiting for responses, leaving a correctly opened drawer white. Do not loosen page-load assertions because of it.

- DEBUG launch actions (send a message, open a page) must not run on every `onAppear` (2026-11-02). With `-openSession`, the chat page appeared, disappeared, and appeared again three times in 0.2 seconds,
  so `-openSendMessage` wrote three messages with new IDs. `ConnectionTests` saw three hub receipts, resembling reconnect-resend bugs. Side-effecting launch triggers run once per launch (`ChatView.debugSent`).
  Detect real duplication via `client_msg_id` in mock `/mock/sent`: retries share an ID and both server/mock deduplicate it.
- New test files require `xcodegen` first; otherwise `-only-testing:` selects nothing but xcodebuild still prints `** TEST SUCCEEDED **` (`Executed 0 tests`).
  Grep `Executed N tests` and require nonzero N (2026-11-01, first "passing" port-forwarding UI run executed none).
- Mock resets between sorting cases also restore `updated_at` and `last_text`, or prior live pushes change later ordering.
  Capture original activity snapshots after all scenario fixtures are customized, preventing reset from overwriting intentional data. Read-and-return cases need fixtures with actual replies, not just list metadata with empty history.
- **Build UI-test apps with simulator signing, without `CODE_SIGNING_ALLOWED=NO`** (2026-11-05, #1929). Unsigned executables lack `__TEXT,__entitlements`
  (`keychain-access-groups`, `application-identifier`), so keychain reads always fail and `HubStore.read()` returns `.unreadable`. `AppModel.init` treats pairing as unknown,
  `isPaired` is false, neither `restoreSnapshot()` nor `restoreOutbox()` runs, and `retryRegistry()` after `protectedDataDidBecomeAvailable` still fails.
  `ConnectionTests.testWaitingMessageAfterRelaunch` then fails consistently (no "Send automatically when connected" after reopen, zero hub receipts), while the `waiting` message remains in `Library/Caches/state/outbox.json`,
  looking like an outbox-restoration bug and costing multiple people half a day. Default simulator signing (`sim.sh`, or `xcodebuild build-for-testing` without signing overrides)
  passed on both iOS 27.2 and 26.0. If pairing, outbox, and chat lists all fail to restore, check build mode first: no output from `otool -l <App>/<executable> | grep __entitlements` means unsigned.
- Always launch with `app.launchPinned()` (pinning style), not `launchPersona()`; `UITestStyleTests` scans source to enforce this.
- In zsh, `for v in "dark shell" …; do set -- $v` does not split on spaces (no default word splitting); `$1` is the whole string and `$2` empty, breaking appearance/layout arguments
  while tests stay green. Run such loops in `bash -c '…'`.
- When new clients move model selection to a separate API, legacy-feature save requests omit model fields to avoid stale drafts overwriting selections. But old-hub PUT may interpret omission as clearing, so first verify unified-config capability and retain original fields for old hubs. FakeHub tests cover both protocols, not only the new endpoint.
- Matching SDK/simulator entries do not prove Xcode can build: platform installation or runtime-version mismatch makes schemes unavailable and actool report no compatible runtime. Independent temporary projects and separately built dependencies can type-check Swift, but bypassing resource compilation is not a normal app build or screenshot acceptance. Never alter official projects to hide environment failures.
- **Check Xcode, SDK, and simulator runtime separately**. Xcode can use compatible older runtimes; runtime device restrictions are not Xcode or real-device restrictions.
  `supportedDeviceTypes` in `xcrun simctl list runtimes -j` gives actual supported devices. Runtime `Contents/Resources/profile.plist`
  `restrictedToDeviceTypes` may restrict devices; check against configured `productClass`, not guessed OS versions. Check support before choosing another combination after creation failure.
- **Same-version runtime installed, but Xcode says OS missing**. If `simctl list runtimes` is available and devices can be created, inspect
  SDK → runtime-build mapping with `xcrun simctl runtime match list`. The SDK's default expected build may not be in installed images.
  Set a same-OS-version runtime with `xcrun simctl runtime match set <SDK canonical name> <installed-runtime-build>`,
  then verify the dedicated device is selectable with `xcodebuild -showdestinations`. Restore automatic selection with the same command's `--default`; do not edit system plists to fake installation.
- **Runtime available, but creation says `runtime path not found`**. Check `simctl runtime list -j` and actual runtime paths before deleting images or editing system plists.
  Export a cached image with `xcodebuild -downloadPlatform iOS -buildVersion <OS-version> -architectureVariant arm64 -exportPath <new-empty-directory>`,
  then register again with `xcodebuild -importPlatform <exported-.exportedBundle>`. One investigation saw cached/export-complete messages but exit 70;
  this is not success. Inspect exports and require import exit 0. Reimport allowed the previously unavailable device to create, boot, and run the app.
  Complete acceptance: create independent device → boot / bootstatus → install app → launch → screenshot. For tests, also confirm Xcode launches the test host.
  Swift Testing can print XCTest `Executed 0 tests` before actually running Swift tests; use final `Test run with N tests ... passed`, not the earlier line as proof of zero execution.
- **`LabeledContent` inside `List` is one accessibility element** (2026-11-02). Label and value combine into one label ("Login, gh login from MacBook"),
  so `app.staticTexts["来自 MacBook 的 gh 登录"]` ("gh login from MacBook") cannot find it, failing despite visible screenshot text. To find by value, use
  `descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", value))`. Standalone `Text` (account/status) on the same page remains staticText.
- **On smaller iPhones, list rows can sit under the floating tab bar** (2026-11-02, #488). `row.swipeLeft()` starts at the row center,
  which can land on Liquid Glass's tab bar and drag its selection to leftmost Persona. Tests only see "can't reveal Close", though the row is still
  `exists` / `hittable`. One test passed on iPhone 18 Pro Max and failed 100% on iPhone 18 Pro, initially blamed on #382 (pre-#382 commits also fail on Pro).
  Before row swipes/taps, use `app.bringAboveTabBar(row)` (`PersonaUITests/AgentsSearch.swift`). Compare simulator models first for lane-only UI failures.
- **With the keyboard up, `app.collectionViews.firstMatch` is not the list** (2026-11-05, #2189). The 44pt candidate/input accessory strip is itself a collection view,
  ordered before the list, outside `app.keyboards` frame (SE keyboard starts at 451, strip is 407–451). `swipeUp` there leaves the list still,
  looking like pinned filters and once described as "keyboard blocks element swipes". Find the list by its elements (`collectionViews.containing(.button, identifier:)`).
  Element swipes start at whole-frame center and can fail next to the keyboard on small screens; coordinate-drag within the visible row segment (`AgentsListTests.dragListUp`).
- Simulators and Mac share loopback: an app listening on simulator `127.0.0.1:<port>` conflicts with Mac processes on that port (port forwarding falls back to a random port, testing that path).
- **Scripts executed by tests really run on the development machine** (2026-11-01). `TestDeployScriptInstallsTheBinary` runs hub deployment, whose final
  `pkill -f '[p]ersonad node --attach'` killed the actual persistent node on the test machine. Every hub-test run on a Mac with persistent node disconnected it
  (launchd restart ≈10 seconds, hub reconnect through a tunnel takes tens of seconds, DSH plugin reconnect afterward: ≈1.5 minutes total, with transcript not found meanwhile).
  Previously nobody ran these tests on machines with persistent nodes. Replace every process/service-affecting script command (`pkill`, `launchctl`, `systemctl`, etc.) in tests
  with PATH-prepended fakes that record and assert arguments (`fakePkill` in `devices/deploy_test.go`). Record real process pid before validation and verify unchanged afterward.
- In the new shell, "swipe right from left edge" is not necessarily Back: on Agents without a chat it switches to assistant; on assistant it opens the drawer. Random-action tests assumed Back,
  ended in the drawer, hid the Agents list and intentionally withheld updates, looking like failed convergence (2026-11-02, `ScreenTruthTests` seed 42). Return via actual controls:
  chat Back, drawer Close, left swipe on assistant (`ScreenTruthTests.backToList`). Do not identify the list only by filters, which scroll away; look for tappable rows.
  Chat once had two Back buttons: visible layer and invisible full-expanded layer tucked into the status-bar strip after collapse (also `isHittable`, but no navigation). First-hittable selection always chose wrong.
  Since 2026-11-02 the invisible layer is absent from the tree, leaving only the onscreen Back (`ChatHeaderTests`). The collapsed strip stays below the status bar in both styles;
  "title bar is in the status bar" was actually the invisible Back frame.
  In BeautifulUI's new shell, use `probe.chat` (page `agents`) to detect open chats, not Back.
- Chat-content UI tests always use `-openChatsFresh YES` (clear disk chat caches). Every mock reset starts chats at the same seq; prior churn-generated steps
  stay cached at this round's `/mock/say` seq, hiding new replies as "already present", leaving previous "Tool 21 · Reply 5" onscreen.
  On 2026-11-02 `ChatFollowTests.testPersonaChatFollows` failed after `ScrollUnderPushTests` / `ScreenTruthTests`, resembling missing Persona replies in the new shell but actually environmental. Real hub seq positions in transcripts increase without reuse.
- Tests selecting rows by "where one swipe stops" change results after row-height/font changes (#245 changed new-shell text sizes) (2026-11-02, #339).
  `testScrolledListHoldsStillThroughUpdates` previously happened to stop on a space whose rank did not change, missing replacement of the reader's screen. Bisect landed on a font-only commit, but the real issue was failure to preserve the reader's row.
  A bisected commit shows where the issue became visible, not necessarily its cause; check whether its edits explain the symptom. Find rows by title, not whole accessibility labels containing "3 minutes ago", which change across minutes.
- Body-count tests (`RenderBudgetTests`) wait for the page to settle before measurement (2026-11-03, #475). Launch still loads/layouts history, and the first measured `rev` push
  gets blamed for those bubbles, looking like "unchanged-screen push redraws 6 bubbles"; order changes or a second measurement remove it. Now wait for unchanged counts for 1 second (`settled()`).

## Disconnection does not mean refusal logging finished (2026-11-05, #2243)

`TestProxyProtocolEndToEnd` checked refusal logs immediately after the client saw TLS disconnection, occasionally reporting `refusal not logged`. `proxyproto.Conn` closes before calling `Report`; when the client sees disconnect, the server callback may not have logged yet. The buffer mutex makes reads/writes safe, not callback completion guaranteed.

Tests check target text and subscribe to write notifications under the same lock, waiting for actual logs with an overall deadline. Unrelated logs cannot reset the deadline; missing logs still fail. Pure waiting regressions use `testing/synctest` to control timing, covering existing text, delayed/segmented writes, multiple waiters, unrelated writes, and timeout. Real end-to-end validation still checks rejection of headerless connections, then refusal logs. Do not change production close ordering, remove logging assertions, or hide races with fixed sleeps.

## Register new platforms in static compatibility checks (#1997)

After `PersonaVision` was added to `apple/project.yml`, `test_apple_rename.py` still had the original fixed target set, misclassifying legitimate visionOS targets as rename regressions. Register both target and module identity when adding a platform, and verify canonical source directories and schemes. Adding only an allowlist without module checks misses identity regressions.

Retain strict checks for the complete target set, existing modules, artifact paths, and legacy schemes, not subset containment or skipping new platforms. visionOS target is `PersonaVision`, source `apple/Vision/`, shared module still `Persona`.

## Go tests requiring macOS built-in commands (2026-11-02)

- The Messages connector reads `chat.db` with macOS `/usr/bin/sqlite3` (personad has no cgo). The Linux hub lacks `sqlite3`, so these tests skip there.
  If the development Mac has no Go, cross-compile on hub: `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c -o x.test ./internal/connector/imessage`,
  copy to Mac, and run **in the package directory** (`testdata` is relative). Use the same approach for macOS-dependent `node` tests.
- `sqlite3 -json` prints nothing for no rows, not `[]`; check empty before parsing.

## Go tests dependent on time of day (2026-11-03, #656, #664)

- Tests deriving "an hour ago" / "now" from `time.Now()` and asserting by day/hour inevitably fail for a few minutes daily: messages cross local midnight into the next daily file,
  or hardcoded daily 09:00 tasks really become due just after 09:00 and run again. Both failed full hub tests at 01:00 UTC (09:00 Shanghai), blocking deployment.
  Fix test times within a day ("yesterday at noon") or far from trigger times (12 hours ahead).
- Reproduce without waiting: write a TZif file with one offset making its "now" the desired time. `TZ=/path/to/file` controls `time.Local`,
  `ZONEINFO=<directory>` containing `Asia/Shanghai` controls `time.LoadLocation`. Round offsets to minutes; second-level offsets mismatch times written back at minute precision,
  failing unrelated tests such as `TestBackfill` and appearing like regressions.

## Hub end-to-end waits must exceed managers' fixed delays (2026-11-05, #2960)

- Managers have fixed delays: `reconnectGrace` keeps disconnected nodes online for 5 seconds (`devices/warm.go`); repeated route failures back off 2, 4, 8 seconds, etc. in `pathEnded` (`devices/paths_choose.go`).
  `eventually` windows only a second or two longer time out under load (`TestCenterServiceConnector` waits 10 seconds for an 8-second backoff). Leave ample margin based on the delay, with comments explaining the wait.
- Running 8–16 copies of the same test binary on two cores via `taskset -c 0,1` exposes these more readily than serial `-count=50`.
  It also exposed a real `TestCenterServiceConnector` race (late probe reply overwrote 4002's "Pair again"), so timeouts are not always tight windows; inspect whether logged state is stuck first.

## Mac acceptance in real windows (2026-11-02, #403)

- On unsigned simulator builds (`CODE_SIGNING_ALLOWED=NO`), PersonaTests `NotificationServiceTests.opensWithTheKeyInTheSharedGroup` always fails: no signature means no keychain group,
  so `PushCrypto.ensureKey()` returns nil. Same on master; do not blame your changes.
- To verify **View › Refresh** (⌘R), foreground the app with the main window as key window. Without one, only section-level hub data refreshes (`MacViewCommands.refreshTarget`).
  Background cua `invoke_menu` makes Inbox/Knowledge look unrefreshed (fake-hub logs show only sessions/machines), but the method is wrong.
- cua cannot accessibility-click toolbar menu buttons or sheet windows in this app while backgrounded (AXPress -25202, sheet tree unreadable). Use `bring_to_front`, then desktop coordinates.
  `delivery_mode: "foreground"` returns to the original foreground app after clicking, closing newly opened menus/overlays. Close sheets with foreground Esc; message action bars require real right-click (`rightMouseDown`), not AXShowMenu.
  cua cannot simulate trackpad horizontal swipeActions; it sends wheel events without gesture phases.
- cua scales coordinates according to the most recent full-screen screenshot. If a session captured a reduced image (`max_image_dimension` 1280 for a 1920-wide display),
  subsequent original coordinates scale by 1.5 and hit wallpaper. Clicking wallpaper triggers macOS Show Desktop, pushing all windows to edges; click it again to restore.
  Always capture full screens at native resolution (`max_image_dimension: 0`). Before coordinate clicks, confirm the point lies in your window or menu.
- For "broken only with mock", compare demo mode, then inspect the real hub path (#511). Two fake hubs can both differ from real hub, one better and one worse.
  New Persona chats: mock initially neither wrote the first message nor pushed session lists ("No messages yet", new chat unselected); DemoHub wrote immediately (normal).
  Real hub first pushes the list; node returns empty items before transcript exists, then pushes once claude writes the first line (watch every 0.5 seconds). Real hub also flashes "No messages yet";
  this is the app bug to fix (retain the first message onscreen as sent). The original "demo reproduces too" report actually used a copy retaining mock pairing, overriding `-demo.on YES` (see the keychain lesson above).
- cua `element_token` is valid only in one session. `cua-driver call` without `session` creates a session each time, so a token read on one call becomes `stale_element_token` on the next.
  Pass the same `session` every time (such as `"session": "qa"`) for reuse; it expires after 5 idle minutes (0.38.0, tested 2026-11-04, #1182). For work fitting one call, use `run_steps` / `run_script`.
- cua `tree_format: "elements"` omits static text with a value but no label (optimistic new-chat bubbles, "Persona is preparing…"), so it falsely indicates absence.
  Check text with `get_window_state` and `query` (tree shows `AXStaticText = "…"`), or screenshots.
- cua `type_text` defaults to background accessibility insertion (`AXSelectedText`). Main-toolbar search filters, but sheet `.searchable` fields (**New chat › Launcher**)
  display text without filtering, appearing buggy. Same text through `delivery_mode: "foreground"` (real keys) filters (2026-11-03). Always foreground-deliver sheet typing,
  and state this in acceptance instructions, or the executor reports failure.
- cua cannot read a sheet's own window_id tree (`ax_window_unresolved`) or target it directly. Read the main window_id (sheet appears as `AXSheet`),
  get the input token, then foreground `type_text`. cua restores the previous foreground app afterward; do not send later keys to `scope: desktop`, which targets that other app.

## Pixel-by-pixel comparison ("unchanged with only one group", docs/operations/testing.md, "Multiple hubs")

- Two captures of the same build are not identical. Liquid Glass button/input/card edge highlights randomize each launch (stable within one launch), and the system spinner before "Working" keeps spinning;
  Reduce Motion cannot stop them. Capture the pre-change build three or four times to establish noise, exclude pixels in that noise, then magnify and inspect each remaining difference (2026-11-02, #256; procedure in testing.md, "Multiple hubs").
- For stable comparison: pin status-bar time, freeze mock clock, uninstall/reinstall the app and restart mock each time (matching relative times), wait for visual settling before capture (not fixed sleeps; 7 seconds can still yield white under high load).

- `scripts/test/test-server.sh` synced only `server/`, but #630's `spec/fixtures` tests (`TestWireSamples`, resumption, resolution) read `../../../spec` and Swift source in `apple/`,
  failing full hub tests with `open ../../../spec/fixtures/wire: no such file` and looking like your regression. The script now syncs `spec/` and `apple/**/*.swift` too (#642).
  Add any future test dependency outside `server/` to the script likewise (2026-11-03).
- Local Go-module `replace` is part of remote test snapshots too (#2028). After `server/go.mod` added `../sdk/go`, copying only `server/` failed setup with `replacement directory ../sdk/go does not exist`.
  Sync complete `sdk/go/` (including embedded schema) to `sdk/go/` in the same checkout's independent cache, retaining relative paths and `--delete` for stale SDK files. Do not share source directories with other test/deployment lanes.
  Extension conformance tests also read source-root `schema/extension/examples` (valid samples and `invalid/` cases); SDK embedded schema is not a substitute. Sync `schema/extension/` with the same relative-path/stale-file rules.
- Docs count too: `connector/conformance` previously compared an integration guide with fixtures (#2725); omitted `docs/` broke full predeployment tests. `docs/**/*.md` now syncs too (#2769).
  Synthetic rsync must do more than create empty directories: fake Go reads current SDK source, embedded files, and valid/invalid examples. Test cold start, updated source, and stale-file removal, preventing false passes with missing sync inputs.

## Sandbox tests use independent synthetic protected directories (#2052)

Isolated test launchers place HOME and TMPDIR under temp directories, which can also contain packages/data/runtime directories. bubblewrap's private `/tmp` creates parents for these mount points; ReadDir or writes against launcher HOME may touch newly created empty sandbox directories, not host-home content. Use an independent unbound synthetic directory with preset synthetic files. Inside the sandbox, verify files unreadable and directory unwritable; on the host, verify no write marker and original files unchanged. Never use a real personal home or treat directory existence as host-content leakage.

Retain allow/deny assertions for data directories, private tmp, and proxy. Negative controls deliberately bind only this synthetic directory: read-only binding must expose reads, writable binding reads and writes, causing the original tests to fail. Do not change production sandbox policy, remove read-only root mounts, or skip real tests.

## Attachment-draft restart regressions (2026-11-03)

- `typeText` returning does not mean the field processed all text. Before terminating the app, predicate-assert the complete `value`, retain pre/post-restart screenshots, then check real disk restoration. One dark-mode regression restored only the first two characters after immediate termination; without the pretermination value, input versus save failure was unclear. This precondition identifies the failing stage. Do not hide issues with fixed sleep or reinjecting drafts on restart.

- Three-page-shell right-swipe regressions must end at the same height as the message row, not drag from a top row to window center. Header changes can make that path exceed 45°, testing vertical scrolling instead. Use a horizontal in-row path, then verify the target page; such failures do not prove missing paging capability.

- New `proto.Features` capabilities also require updating shared hello/manifest wire samples (`go test ./internal/hub -run '^TestWireSamples$' -update`). Check the diff contains only intended flags; capability-handler-only tests miss protocol-snapshot failures.

- SwiftUI Form Toggle accessibility frames can span the row. Taps at row fractions 0.88 or 0.95 can still miss on different devices (no POST). Select internal `toggle.switches.firstMatch`, wait for enabled/hittable, tap center, and assert values after service response. After adding system-message fixtures, locate quoted messages by ID, not the last array item, so mock 500s are not blamed on live sync.
- Collapsed shell status rows retain the previous toast for transition; XCUITest nodes can still `exists` while video shows nothing. To wait until later long presses are unobstructed, assert not `hittable`, not destroyed.

## Starting sshd for tests (2026-11-03, #891)

- macOS `/usr/sbin/sshd` can run without root: `sshd -D -e -f <config>`, with `UsePAM no`, `StrictModes no`, and owned `HostKey` / `PidFile` / `AuthorizedKeysFile`; only the current user can log in. OpenSSH 10 needs `PerSourcePenalties no`, or repeated connections from one source incur delays.
- Other processes connect as soon as ports open (local port probes send `GET / HTTP/1.1`); many `invalid format [preauth]` logs are normal. Filter them before inspection.
- Privilege-separated sessions have two processes (`sshd-session: user [priv]` and its child), with sockets in the child. Kill listener descendants too when simulating disconnection; killing only `[priv]` leaves connections alive. Stopping sshd without descendants leaves orphan sessions parented to 1 indefinitely.
- `swift test` output arrives only after completion. Find hung cases with file traces or `sample <pid>`; Swift Testing `.timeLimit` cannot stop uncancellable waits.
- System `.alert` buttons cannot be clicked through accessibility activation; `personactl.py tap` falls back to idb (not installed locally). Use XCUITest for alert-click flows (`app.alerts[...].buttons[...]`).

## Verify nonempty udid before deleting simulators (2026-11-03)

Reading udid from a file, then `xcrun simctl delete $SIM`: if the file is missing, `$SIM` is empty. This time it only printed usage, but `delete "$SIM" all` could delete everybody's simulators.
After reading udid, use `[ -n "$SIM" ] || exit 1` before deletion/uninstall actions.

## Do not run unit tests on screenshot simulators (2026-11-03, #930)

After `xcodebuild test` on screenshot simulators (test-host app without `-pairURL` requests notifications), every reinstall prompts for notification permission,
which uninstall cannot clear. The dialog makes the app inactive, prevents WebSocket connection, leaves screenshot tasks "Starting" and machine names as IDs. `simctl privacy` does not support notifications.
Use separate simulators for unit tests and screenshots; delete/recreate contaminated ones.

## Selecting library videos in UI tests (2026-11-03, #981)

- Generate synthetic test video (no ffmpeg locally: AVAssetWriter produces gradient/vertical-bar H.264 MOV), add with `xcrun simctl addmedia <udid> <file>`.
  Built-in landscape photos also exist; find cells by `label CONTAINS "Video"` ("视频" in Chinese, meaning "Video") to select your added clip.
- PHPicker runs in another process, but elements appear in `app`'s tree. The simulator picker is English: confirm using top-right ✓, **identifier `Add`, label `Done`**.
  Searching the localized Add label first matches the app's own "+" behind the picker (also labeled Add), selects nothing, and only reports "attachment missing".
  The "Private Access to Photos" banner has another `Close`; do not confuse it with player Close.
- System AVPlayerViewController's top-left Close has label `Close`; its appearance proves the player opened.
- Classic-layout + Liquid Glass chat input uses `layoutIsland`, which once omitted "+" / Send from accessibility, forcing coordinate taps (#995, fixed; see `lessons/ios.md`).
  Find by label now; do not add island identifiers for coordinate taps again.

## UI launch helpers must exercise default navigation (#1653)

Pin the visual style, not an alternate navigation tree. A shared launch helper that forces tab navigation can keep hundreds of tests green while compact iPhone users run an untested shell. Keep only the clean Demo Mode reset in `launchPersona()`; `launchPinned()`, runtime CLI cases, performance runners and README captures must use the app's default navigation. Test compact shell and regular-width sidebar by choosing the appropriate device / width, not by setting a navigation preference. Preserve page-specific waits and interaction assertions when moving tests to the default tree; do not route around a shell failure just to capture screenshots.

## Lazy timelines: unscrolled rows do not exist, and identical text may occur elsewhere (2026-11-04, #1055)

- GitHub thread timelines use `LazyVStack`; the bottom comment is absent from accessibility until scrolled into view, so `waitForExistence` alone waits in vain.
- Retained navigation pages may keep Inbox in the tree, whose comment preview has the same text. Text waits accidentally passed in Liquid Glass but failed in BeautifulUI's different previews.
- Wait for page-specific elements (header), then search row identifiers (`github.entry.<id>`) while swiping up. Before screenshots, move the target into the upper half;
  otherwise "one row barely visible" counts as found and several screenshots capture the same screen.
- Do not reuse keys other tests look for when recording `/mock/sent` (this quoted reply used `"item": "github:review12"`, picked up by Inbox tests).

## Narrow-screen headless Chrome screenshots (2026-11-04, #1208)

- `Google Chrome --headless=new --window-size=390,844 --screenshot=…` does not lay out at 390px: minimum window width is about 500px, so a 500px page gets cropped to 390,
  looking like horizontal overflow (admin devices/plugins pages) but not a page bug. Use `--window-size=500,…` for narrow captures; true 390px needs DevTools device emulation.
- Admin screenshots need no hub: `cd web && npx vite --port <free-port>`, then `/admin/<page>?mock=app&theme=dark` (`?dialog=…` opens dialogs directly; `web/src/admin/mock.ts`).
  Point `--user-data-dir` at your own directory under `/tmp`, never the user's Chrome profile.

## Cannot click buttons in list rows during UI tests (2026-11-04, #1328)

- **Container `accessibilityIdentifier` overrides its children's identifiers**: identifying a row `VStack` made its `Menu` `market-menu-…` unfindable. Identify only clickable controls,
  or first apply `.accessibilityElement(children: .contain)` to the container.
- **A `lineLimit`-cut `Text` still reads in full to XCUITest**: its label is the whole string, so "the last line is not there" never proves folding (#1666, `NewChatLayoutTests`). Assert the fold's control (Expand) and look at the screenshot.
- **First taps just after sheet dismissal can be swallowed** (keyboard focus is still settling). Immediately tapping a list button after form Add does nothing, with no confirmation dialog.
  Wait for sheet-field `waitForNonExistence`, tap a harmless button (Refresh) first, then the target button.
- Icon-sized `Menu` (20 pt) often does not expand under XCUITest. Put row actions in a row of `.borderless` text buttons, clearer on both phone and Mac.
- **Fake extensions installed through `dir:` in e2e are sandboxed by default** (bwrap, writes only to own data/runtime directories). A fake channel plugin writing received messages to a test directory throws and exits;
  hub restarts it, replayed input is deduplicated, and it looks like lost messages. Store files in `extension.data_dir` from `initialize` (tests read `~/.persona/extensions/data/<id>/`), retaining sandboxing.

## Local port discovery probes fake model servers in tests (2026-11-04)

`TestInboxTriageWaitsOutAnExhaustedQuota` occasionally reports "20 items used 21 requests". The extra request is `GET /`, `User-Agent: persona-ports`,
from the local development node finding Agent listeners (`docs/operations/ports.md`) and probing the `httptest` port. Count only your own API
(`POST /v1/messages`); return 404 without counting other paths (`assistant/inbox_limit_test.go`). Use this pattern for similar fakes or they become flaky on node-running machines.
`TestOpenMacLeavesAgentAlertsToTheApp` also occasionally got an extra "push" with token `/` for the same reason; fake APNs counts only `POST /3/device/…` (`hub/liveactivity_test.go`).
`TestGateProxyReportCutsTheText` counted the same probe as a third request (2026-11-06, #6057). Its fake API (`fakeAPI`, `node/gateproxy_test.go`) has to accept any path the gate forwards, so it can't answer 404 to other paths. Instead it leaves out exactly the probe's shape: `GET /` with a User-Agent from `portProbeAgents`. That list holds today's `portProbeAgent` and the original installed-node User-Agent, which older nodes keep sending until they are replaced. When #6079 renamed the User-Agent, nodes already on the machines kept the old one, and the flake came back (#6225). Use such a filter only for a fake that must accept any path. Counting your own paths and answering 404 to the rest stays the default. Neither replaces real isolation in network-isolation or zero-connection tests: see the #2025 bullet on network-isolation listeners near the top of this file.
`TestAIElsewhereAsksNothingForOrdinaryMessages` counted it as a call to Lark (`calls: GET /`, #6274). The bot tests' fake open platform (`fakeOpenAPI`, `extmgr/bots_test.go`) now records only `/open-apis/` and `/callback/` and answers 404 to anything else: the default, which also holds for a node of any version.
A sweep then fixed every other plain-HTTP fake in `server/` that recorded, counted or failed the test on any request, so the next one doesn't have to flake first (#6282). Each now answers 404 to the probe's request before anything else: `GET /`, or for a proxy fake any request that isn't a proxy request (`r.URL.Host == ""`). The APNs fakes keep to `POST /3/device/…`. A new fake that records requests starts with the same guard. TLS fakes never see the probe, which speaks plain HTTP. `TestProbePanesKeepsEveryPanesProbes` (`node/ports_test.go`) counts the probe itself, so it can't filter it out and is left as it is.

## Fake model servers also receive the hub's own model calls (2026-11-04)

On its first run, `TestModelCallStopsWhenTheManagerStopsWaiting` never reached its fake server: after assistant connection, hub features (Agent-stage judging, etc.)
sent calls with the same model configuration, which the fake server blocked. Same-address/key requests share one gate (`ai/limit.go`), so the test request timed out waiting before being sent;
the stuck request also blocked `srv.Close()`, exhausting the package's 10-minute timeout. Identify the test's own request by a unique phrase in its body,
return a valid answer immediately for all others, and bound blocked requests too (10 seconds here) so cleanup cannot hang (formerly hub inbox_test.go; removed after #2856 moved model configuration to managers).

## UI cases see state left by other cases when run together (2026-11-05, #1961)

- App state persists across simulator cases (Lark outbox, drafts, chat caches). A prior case leaves failed/unknown-result bubbles in the same chat; later cases using the same identifiers
  tap those instead, or find multiple elements. Each passes alone but fails in the suite.
- Start cases with corresponding `-open…Fresh YES` / `-show…Fresh YES` to clear leftovers (Lark uses `-showLarkSendFresh` in both `LarkSendTests` and `LarkAttachmentFlowTests`).
  **Remove the flag** on the relaunch that tests persistence, or it clears the very data under test.
- After adding UI test classes, run the whole suite sequentially, not only the new case.
- `./scripts/test/test-server.sh … -run 'A|B'` became `bash: B: command not found` on hub, running nothing (2026-11-05, #1830): remote shell reparses ssh arguments and treats `|` as a pipe.
  Add another quote layer: `-run "'A|B'"`. Exit codes alone can look like test failure; inspect the final log lines first.


## Tests must provide data available only under particular MOCK_* flags (2026-11-05, #2334)

`PhotoSaveTests` needed a chat with a screenshot that existed only when mock launched with `MOCK_PHOTO=1`, documented only in a test comment. Full-suite and other-case mocks omitted it,
so the app opened a nonexistent empty chat ("No messages yet") and both cases always failed after "no button within 20 seconds", looking like broken Photos/save. Tests now add it themselves in setUp with `GET /mock/photo?on=1`
and restore the prior state in tearDown (`MOCK_PHOTO=1` still works). New UI tests prepare data through `/mock/…`, not mock-launch environment dependencies. If an environment variable is unavoidable, check in setUp and `XCTSkip` with an explanation when absent.

## For intermittent unique violations, find the two transactions touching the same row first (2026-11-05, #2704)

`TestTodoBotmuxChatMajority/store` failed about one in three runs with unique constraint `todo_agents_active` (23505). Initially blamed on shared test DBs, but 30 isolated repetitions reproduced 11 failures:
node-reported sessions automatically linked b2 to big in the background, immediately followed by the user manually assigning it to small. Transactions locked different to-do rows, never waited on each other, and neither saw the other's uncommitted link.
The later insert waited for the first commit, then collided with the unique index. Link/remove operations now first take an Agent-keyed advisory lock, then lock related to-do rows by ID (#2742 `lockTodoAgentMembership`), without affecting other Agents.
Assistant automatic links use `AutoLinkUnlinkedTodoAgent`: if already linked elsewhere at DB write time, do nothing instead of overriding manual choices (direct `AutoLinkTodoAgent` still moves it). Also remove unpersisted automatic links from memory, preserving user-removed ones; rollback recognizes only its own link (same LinkedAt), clearing "current to-do" only when that link is removed, never dropping a user's coincidental manual relink to the same to-do.

For intermittent constraint violations, reproduce with `-count=30`, enumerate transactions writing the same row, then check whether they lock the same thing. Do not pass through reruns, longer timeouts, or skipped store checks.

## Node tests time out in terminals: interactive zsh stopped in a background process group (2026-11-05, #3265)

In real terminals (hc / herdr panes, Terminal), `go test ./internal/node` leaves `TestLauncherRepair` stuck until the 10-minute timeout. CI, `test-server.sh` (ssh without `-t`), and Claude Code's Bash tool lack terminals and do not hang.
node launcher commands use interactive login shells (`zsh -ic`), previously placed in their own group with `Setpgid`. With a controlling terminal, interactive zsh tries to claim it but its group is not foreground,
so SIGTTOU / SIGTTIN stops it forever (goroutine stack at `launcherRepair` → `Cmd.Wait`). Foreground `personad node` in production terminals likewise hangs launcher probes, health checks, and repair until timeout.
`killGroupOnCancel` now uses `osproc.SessionGroupAttr()` (`Setsid`): child in a new session, no controlling terminal, with whole-group killing unchanged.

On test hangs, send `kill -QUIT` to the test process first, identify the test and blocked syscall, then judge environment issues. Interactive-shell children need either a new session or no separate process group.
## Testing extensions with a test hub on a Linux development machine (#3136, 2026-11-06)

- Without `machines` in hub.json, hub treats startup as first run and auto-connects local node (probing local herdr/claude). Test hubs set `"machines": []` and launch with `PERSONA_LOCAL_NODE=0`.
- Extensions are sandboxed by default, outbound hosts limited to the manifest through hub's proxy, so they cannot reach a local `127.0.0.1:<port>` proxy. If GitHub extension Proxy points at a local proxy, run `personad extension sandbox github off` on the test hub.
- Extension package.sh outputs packages to `dist/`; a private extension repository's `packages/` contains published packages, so do not install those by mistake (same-version packages do not reinstall either).
- Empty SDK `ext.Page.Action` results mean "redraw entire page". UI version 5 `lines` / `items` already count as a response; when nothing needs drawing (marked Seen), return non-nil empty `Items`.

## Built-in executable extensions run the test binary itself in tests (2026-11-06, #3500)

When a built-in extension's `runtime.command` is `personad`, the manager launches `os.Executable()` with manifest `args`; inside `go test`, that is the test binary (`extmgr.test voice`).
The test binary does not recognize `voice`, reruns the whole package recursively, and times out. `extmgr` `TestMain` therefore checks `os.Args[1]`: if `voice`, run as `personad voice`
(`voice.Run`). Future built-in executable extensions (not declarations only) need the same `TestMain` dispatch, so tests actually run them through hub forwarding.

## Web screenshots on Linux unsupported by Playwright (2026-11-06, #3140)

- Symptom: `npx playwright install chromium` on Debian 11 says "does not support chromium on debian11-x64"; ubuntu20.04 packages also fail. Playwright MCP looks for `/opt/google/chrome/chrome` and cannot launch either.
- Download Chrome for Testing `chrome-headless-shell-linux64.zip` (version from `last-known-good-versions.json`), inspect missing libraries with `ldd`. Without sudo, `apt-get download` their debs, `dpkg-deb -x` into your own directory, and set `LD_LIBRARY_PATH` when launching.
  Extract Chinese fonts (such as Noto Sans CJK) likewise, add the font directory in `fonts.conf`, and set `FONTCONFIG_FILE`, or Chinese renders as boxes. Use installed `playwright-core`: `chromium.launch({ executablePath, env })`. Put each download in a new directory, outside the repository.
- Waiting: pages maintain WebSockets, so `waitUntil: "networkidle"` may never settle. Use `load`, then wait for specific page text. After fake-hub login with `/?token=mock`, wait for the sidebar before navigating to the screenshot target, or capture the login page instead.

## Local iOS unit tests: xcodebuild waits ten more minutes after failures (2026-11-06, #3507)

- On case failures, `xcodebuild test` collects simulator diagnostics after tests finish, waiting 600 seconds before exit (`Failure collecting diagnostics from simulator: Timed out after 600.0 seconds`), despite subsecond cases. Add `-collect-test-diagnostics never` for repeated local runs.
- New test files need `xcodegen generate` before running. If absent from the project, `-only-testing:PersonaTests/<suite>` reports no error and runs none ("Test run with 6 tests" contains only other suites), easy to misread as success.
- If every model callback captures `[weak self]`, test `let (_, socket) = …` drops/deallocates the model immediately, swallowing all later frames and mysteriously failing assertions. Tests must retain the model (the app retains it through `VoiceCalls.current`).

## Making fake Claude call Persona tools in isolated-hub e2e tests (2026-11-06, #5073)

- Inbox handling chats submit results only through the MCP tool (`inbox_suggest`); a fake Claude returning plain text cannot test this. Read `mcpServers.persona` command / args / env from fake Claude's own `--mcp-config`
  (the JSON node passes to headless assistant chats), launch `personad mcp`, and send `initialize`,
  `notifications/initialized`, and `tools/call`. The tool identifies the chat by `PERSONA_SESSION_ID`, following the real node → hub → manager path. A test-written plan file decides which tool to call by matching a phrase in the message.
- `prompts.TestEveryTemplateIsUsed` searches Go and templates for quoted full template names (`"inbox/handling/sent"`); names built by concatenation (`"inbox/handling/" + name`) look unused. Write full names in constants.
- The old Inbox e2e cases in `run.py` for "hub keeps reading sources after the manager stops" no longer apply after sources moved into managers on 2026-11-05. The same six cases failed on master at the time too; when comparing, run them on `main` first.

## Checking Android and Swift pure logic on a Linux-only machine (2026-11-05, #2840)

On a Linux development machine without the Android SDK or Xcode, pure-logic client changes can still be checked first:

- **Android**: `:core:contracts` and `:core:domain` are pure Kotlin modules; `JAVA_HOME=<JDK 21> ./gradlew --offline :core:contracts:test :core:domain:test` runs without the SDK.
  It needs JDK 21 (the daemon is pinned to 21, see `docs/android/android.md`); JDK 11 does not start. Pages and screenshots still need a machine with the SDK.
- **Swift**: Foundation-only files (such as `AgentRepoTree.swift`) build with the swift.org Linux toolchain (the Debian 12 package also works on related distributions) inside a temporary SwiftPM package running Swift Testing.
  Copy the needed types verbatim from `Models.swift` and write minimal stand-ins for the rest; match the App's settings: Swift 6, `defaultIsolation(MainActor.self)`, plus the `NonisolatedNonsendingByDefault` and `InferIsolatedConformances` upcoming features (`SWIFT_APPROACHABLE_CONCURRENCY`).
  This catches syntax, type and isolation errors and really runs the tests, but it is not an Xcode build: say "not compiled with Xcode" in the PR; Xcode 26.0 is not verified either.

## 后台测量停滞时，先区分未完成测量与残留管道（2026-11-06，#5766）

性能批次停在某一轮，不等于 App 仍在测量。本次 App 的 `frames-page-idle.json`、`frames-page-glide.json` 已生成，但前台测量 shell 已退出；后台 mock 的残留 shell 仍持有输出管道，父批次的 `grep` 收不到 EOF，一直等待。

- 先核对进程命令、PPID、工作目录、日志时间及 App 文档目录中的 JSON。已有完整读数另存并记录来源，不重跑、不覆盖。
- mock 直接在当前 shell 后台启动，保存 `$!`，重定向 stdin/stdout/stderr；用退出 trap 处理这个已确认的子进程。不要用后台子 shell 启动常驻服务后丢失 PID。
- 恢复前确认后续脚本有超时、失败结论和断点跳过。需要清理残留进程时，重新核实 PID；优先 TERM 本任务的 mock，让父进程自然解阻。不要按宽泛进程名或端口清理。
- 模拟器操作整段持有 `/tmp/playbox-sim.lock`；锁等待和单次测量都设超时，成功、失败、中断均有明确结果。近零或不一致的前后差异不能写成显著性能收益。

## UIKit 焦点探测不能当成开始搜索的用户意图（2026-11-07，#5766）

分组持久化测试通过，但截图中的键盘遮住了页面。加入「开菜单不弹键盘」断言后发现：键盘在菜单打开时出现，不是选择回填后出现；改按钮样式或隔离 List 单元都不能解决。

原因是静态搜索框的 `textFieldShouldBeginEditing` 返回 false 前还调用了开始搜索。UIKit 打开菜单时也会探测输入焦点，这个副作用把探测升级成了真正搜索。iPhone 的静态输入现在只由明确点击或 `accessibilityActivate` 开始搜索；焦点探测只返回编辑是否允许。不可见的折叠输入不参与点击。

回归同时覆盖：菜单不弹键盘、明确点击可搜索并找到结果、关闭后再开菜单不恢复键盘、无障碍激活仍能开始搜索。只看状态断言不够，交付前仍需实际查看截图。

## macOS bash 3.2 can report an executable failure as exit 1 under `set -e` (#4467)

The Mac-only `test_hub_restart_mac.py` fixture used an empty, non-executable `personad` file. After blue-green switching was added, `hub-systemd.sh` executed it before restarting launchd. With `set -e`, macOS bash 3.2 returned 1 for `"$f" … || rc=$?`, which the script interpreted as a failed switch, rather than 126 for an executable failure. Bash 5 returned 126.

Use an executable fixture that logs calls and returns the documented result: 0 for a successful switch, 3 for no edge to switch. Assert that success avoids launchd and that the no-edge case restarts only the hub agent, skipping node agents. Run this Mac-only suite on macOS; Linux CI skips it: `cd scripts/test && python3 -B -m unittest -v test_hub_restart_mac`.

## Import guards read source files, not `go list` (2026-11-06, #5814)

`go list -deps` resolves imports for the host's GOOS / GOARCH and no build tags, so a `server/internal` import in a `_darwin.go` file, a `//go:build public` file (release builds use `-tags public`) or a `//go:build ignore` generator passed the plugin guard on Linux. A guard that must hold for every build parses each `.go` file's imports with `go/parser` (`parser.ImportsOnly`), whatever its constraints, and keeps `go list` as a second view of indirect dependencies, run for every GOOS / GOARCH pair the server ships and every set of build tags it is built with (with `CGO_ENABLED=0`, as the builds are made): an `_arm64.go` file is as invisible to an amd64 host as a `_darwin.go` file is to Linux, and a `// +build !public` line still counts.

Walking files has pitfalls of its own. Reviews of `server/plugins/guard_test.go` found each of these: the first six let plugin or internal code into a binary unseen, the last two failed on code that is fine.

- The go tool leaves `testdata`, `_x` and `.x` directories out of `./...` patterns, but a package there can still be imported by its explicit path: scan them too.
- `filepath.WalkDir` does not follow a linked directory: a link to a plugin from `internal/` builds the plugin into the hub unseen. Check where every link to code leads, including into a nested module the walk skips (the go tool builds it as the server's through the link).
- A module prefix written with a trailing slash (`…/server/`) never matches the module's root package.
- A package in the directory that holds the plugins (`server/plugins` itself) is a way around "only programs import a plugin".
- Go's `internal` rule limits only who imports the internal package itself: a package next to it can re-export it to anyone. Hold a program's whole tree to itself instead: no package outside `cmd/<x>` imports one of `cmd/<x>`'s, and no link from outside leads into it.
- A package the guard lets through by its path (the SDK) must be read the same way: a link or a tagged file in it reaches past the guard. Likewise a local `replace` or a `go.work` module is this repository's code under another path: judge `go list` results by `.Dir` as well as by import path, for local code only (the main module and directory replacements: a module cache may sit inside a checkout). A `replace` by a published version of this repository's own module has no local directory to judge: refuse it in go.mod and go.work. And since a published module could import a plugin by its path, the programs that run no plugin are held to `go list -deps` too.
- Decide what a directory is (a plugin's program or not) from its non-test files: a test importing a plugin is not linked into a program.
- Skip files whose names start with `.` or `_` (the go tool never builds them; editors leave lock files so named), but not directories so named.

## Apple 改名验收要区分测试语义、注册身份与升级版本（2026-11-07，#6152）

- 批量改名可能把负向 URL fixture 变成合法链接。先按协议确认解析器契约，再用中性的其他 scheme 保留拒绝断言，并补上 canonical 链接的正向用例；不能为了旧断言限制合法的 opaque item ID。
- 同一 Bundle ID 覆盖安装只是第一步。旧、新测试构建使用递增的 `CFBundleVersion`，区分「文件保留」「普通偏好保留」「改名键或类型转换」；不使用清配对或重新注入配对的参数证明升级首启。
- Shortcut 先由旧 App 在系统 UI 中创建，再检查持久化 action 的 `AppIntentDescriptor.AppIntentIdentifier`。不能搜索整个记录中的旧命名空间来认定旧类型：必须保留的 `BundleIdentifier` 也可能匹配。测试标题只能用于定位，不能替代 action 身份检查。
- 带一次性配对码的 mock 不能在独立 suite 或深浅色轮次之间复用。每组配对 UI 测试启动自己的新 mock；保存原始失败，再记录修正隔离后的重跑结果。
- Debug 的主程序可能只是加载 `.debug.dylib` 的壳；检查 Swift 类型时同时看实际动态库。模拟器的授权信息可以放在 Mach-O 的 `__entitlements` 中，`codesign` 输出为空不等于缺失；正式签名和 production APNs 仍需单独检查。

## Scenarios through AppModel: make the model first, then hook up the test hub (2026-11-08, #1112)

`AppModel()`'s init calls `DemoHub.install()`, which points `APIClient.local` and `LiveConnection.makeLocal` at Demo Mode's hub. A test that hooks up its own fake hub first and then makes the model sends every request to DemoHub (`DemoHub miss: …` in the log), and some assertions still pass, which hides it. Make the model, then hook up the test's hub, and again for the new model a `relaunch` makes (`SyncDevice.connect`).

Two more from the same harness:

- The scenarios' times are fixed (`2026-01-02T…`), and the app ties a sent bubble to its transcript line partly by time. Lines the hub writes during a case get the time they were written (`SyncHub.writtenNow`).
- A failed message is placed before later ones by the whole second it was written (`ChatLines.placeFailed`). Two sends in a row in a test land in the same second, so the harness spaces sends more than a second apart, as typing does.

## Simulator screenshots for an agent's live reply (2026-11-09, #1309)

- `mock_hub.py` is one long module: `REVIEW` is the PR #42 review agent's session id near the top, and the `MOCK_REVIEW` block much later rebound it to a bool, so `/mock/agent_live` (MOCK_AGENT_LIVE) and the later fixtures that use the session id looked up `False` (the step answered with a 500 and a `StopIteration` in the mock's log). The flag is `GIT_REVIEW` now; give a mock flag a name of its own, and when a mock step fails, read the mock's log before the app's.
- `xcrun simctl status_bar … --time` on Xcode 27.1 takes only an ISO date with fractional seconds: `"$(date +%H:%M)"` and `…T10:24:00+08:00` fail with "Invalid, non-ISO date/time string"; `"$(date -u +%Y-%m-%dT%H:%M:%S.000Z)"` works and shows the local time.
- A notification permission dialog left by a launch without `-pairURL` stays over every later launch, also after the app is uninstalled. Restarting SpringBoard clears it without shutting the simulator down: `xcrun simctl spawn <udid> launchctl stop com.apple.SpringBoard`, wait a few seconds, then launch with `-pairURL` (or `personactl.py launch --hub`).

## A model made in a unit test starts with what the simulator last saved (2026-11-09, #1638, #1637)

`AppModel()` restores the last Dev snapshot (machines, agents, cards; `Library/Caches/state/dev.json`) and the outbox when the simulator's keychain holds a pairing, which any mock-hub UI test run leaves behind. A unit test that reads everything the model holds (the watch summary, all agents) then counts those too: it passes on a brand-new simulator and on hosted CI and fails on a used one. In the sync scenarios, a snapshot of 145 sessions pushed the case's chat out of the 40 newest that `AgentSync` plans, so its stall check never ran. The model also writes its own snapshot and outbox back over the app's.

- A test that only needs the model empty calls `forgetRestoredForTests()` after making it.
- A test that saves and reads back (a relaunch, the outbox) makes its models with `AppModel(stateFolder:)` on a folder of its own, and again on the same folder for the relaunch (`SyncDevice`, `OutboxRestoreTests`). Nothing it writes reaches the app's files.

To check that a failure is this, move `dev.json` aside in the app's data container and run the test again.

## Playwright's "visible" counts a drawer parked off the screen (2026-11-09, #1643)

- At phone width the web sidebar is a drawer moved off the screen with a transform, not hidden: `getByRole(…).filter({ visible: true })` and `isVisible()` still count its buttons, and a click on one waits out its timeout. Reach a control at that width from the page that shows it (the Agents page's New agent, `web/e2e/launcher-health.mjs`), or scope the locator to `main`.
- `?token=mock` pairs asynchronously: wait for something only a paired page has (`main textarea`) before setting `localStorage` and reloading, or the reload lands on the sign-in page.

- Real WebSocket nudge tests need observable ordering (#2297): wait until all input audio frames arrive and the answer reaches the app before sending a late caption. Answer text/result frames on receipt rather than after an arbitrary delay. A negative nudge assertion still needs its full observation window; watch incoming frames and fail immediately on an unexpected count, then check the count at the deadline. Do not replace a sleep with an unconditional timer wait or use `synctest.Wait` around real sockets.
