# Lessons: iOS

Pitfalls, odd behavior and the decisions made at the time (formerly "Lessons" in `CLAUDE.md`; index in `docs/lessons/README.md`). Add new lessons to the relevant section, or create one if none fits.

## A relay push keeps the fallback's sound unless the service extension sets it (#1862)

- The relay writes the outer `aps` itself, `"sound":"default"` included, so the "New message" fallback rings. The notification service extension starts from `request.content.mutableCopy()`, which keeps that sound: replacing the texts, category and interruption level from the sealed payload is not enough. `PushCrypto.apply` sets the sound from the sealed `aps` and clears it when there is none; a silent notification that rings through the relay but not with direct APNs points here.
- `apple/Shared` compiles for watchOS 10 too, and some UserNotifications APIs don't exist there: `UNNotificationSound(named:)` builds for iOS and the Mac and fails only in the watch build (hosted "iOS and Watch" job). A Mac unit test of shared code does not catch it.

## Idle-energy profiling and caller attribution (#1634)

- Use an isolated mock hub and keep the build, simulator profile, data and visible-page history identical. Frame-count probes and frozen animations change the workload. A runtime-CLI root rebuild can pin `scenePhase`; use a cold launch and real tab actions instead. Home is asynchronous: verify the background transition before starting its window.
- Simulator instrument support is not implied by the template list. Preserve unsupported/error logs and label absent counters as unavailable, not zero. Host Instruments can attribute Activity Monitor counters to the simulator app PID, but this is not phone battery or radio measurement. Long all-process recordings on Xcode 27.2 may hang while saving with overlapping-image diagnostics; a timeout is a failed trace, even when independent process counters cover the whole window. Concurrent recordings can contend for the global `kperf` lock.
- macOS `proc_pid_rusage` CPU times are Mach ticks. Convert with `mach_timebase_info` before calculating seconds or percent; do not relabel raw ticks as nanoseconds. Process idle/interrupt wakeups and package-idle wakeups are different counters, not total hardware CPU wakeups.
- A shared credential does not identify one device or process. Separate App, widget, intent and capability-probe callers; a short hello-only WebSocket is not the main App reconnecting. Pre-gzip body counters, decoded `Data.count`, encoded HTTP bodies and TLS/radio bytes are different quantities. Verify actual negotiation with a local gzip fixture and retain attribution caveats.
- Tab visibility and scene activity are different scopes. Polling and animation should stop when the scene is inactive, but blindly adding scene-driven `disappear` to every tab callback can clear voice-call continuation state. Audit consumers before changing a shared lifecycle helper.
- Hosted SwiftUI lifecycle fixtures need a window attached to a real `UIWindowScene`. A frame-only `UIWindow` can execute a timeline closure without mounting its `UIViewRepresentable`, so a missing native view is not proof of animation teardown. Serialize scene-owning fixtures, retain geometry assertions for partial visibility, and test actual native animation removal rather than weakening a missing-view assertion.
- Profiling and offline tests on the same mock-only simulator share paired snapshots. Constructing `AppModel()` restores that inventory before a fixture installs its registry; replacing links alone is not isolation. Explicitly replace the fixture's in-memory inventory, assert it starts empty, and retain persistence needed by relaunch scenarios. Unexpected summary counts or a chat displaced from a bounded prefetch window must not be hidden by wider limits, longer waits or skipped assertions.
- UI setup must establish an active scene, not merely find a retained element. `openAgentsFresh` resets list preferences, not paired snapshots; a clean mock launch keeps `pairURL` to suppress automation-time push registration and handles any pending system alert. A long native menu can expose children whose frames are empty: inspect its actual layout and drag inside the visible menu bounds before asserting offscreen choices or their stability. Churn fixtures use fresh interaction IDs after resolution, since the app correctly refuses to revive a resolved identity.
- A fixed window may end after a GET starts but before its body is recorded. Keep CPU/transport endpoints fixed, audit completion separately, and label drained response totals accordingly. A pending receipt's initial zero lengths or default encoding are not a bandwidth saving or evidence that gzip negotiation failed.
- Replaying only transcript items is not a matching workload. Clock-generated task feeds can move later than the last user message and change the visible cards; pin the source fixture clock before initialization, restore the side feeds, and keep assistant rows out of the regular-session inventory. Confirm both the warmed page history and actual screenshots, retaining rejected trial readings rather than silently treating them as comparable.

## Hosted minimum-Xcode initialization and the stock simulator boot (#1218)

Pin both `DEVELOPER_DIR` and the disposable hosted runner's system Xcode selection, then run that Xcode's first-launch initialization before downloading platforms or creating a simulator: an environment variable selects foreground tools but not the system selection background platform services use. Keep system-wide selection/initialization in the hosted workflow, not the locally runnable Apple script. This did not fix the stock iOS 26.0 boot either: with it, hosted runs booted in 103 s, timed out at the 900-second `simctl bootstatus` deadline (device `Booted`, 71 GiB free), then booted in 76 s; main without it timed out twice. The hang is intermittent and its cause is still unknown. It is not the dyld shared cache: the hosted image ships only newer iOS runtimes (26.2, 26.4.1, 26.5) and downloads 26.0.1 on every run, but an explicit `xcrun simctl runtime dyld_shared_cache update com.apple.CoreSimulator.SimRuntime.iOS-26-0` right after the download returned in 0.8 s with `dyld shared cache is usable` (the identifier form is accepted), so the cache already existed before the boot. Keep the 900-second boot deadline, the stock profile, the pinned 26.0.x runtime and no automatic retries unless the maintainer decides otherwise; treat a single green boot as no proof either way and record the boot time of each hosted run.

## System extension credentials and compiler-free acceptance (#1147)

A widget's App Group snapshot must not reuse Fleet's appearance store: it contains credentials, needs first-unlock protection and must clear removed groups. Serialize cross-process atomic writes and fence late render replies by pairing generation plus invalidation revision. Namespace caches and entity/deep-link identities by hub group; identical contribution ids on different hubs are not interchangeable. Export watch-only configs into a separate wrist App Group, never fall back to phone credentials, and preserve empty WidgetCenter reports as full replacement.

`IntentParameterDependency` exposes an `IntentProjection`: its dynamic-member subscript returns `Value.UnwrappedType`. For an optional entity parameter, use `intent?.action.id`, not `intent?.action?.id`. The SDK interface can establish this type signature without building; it does not establish that the full target compiles. Runtime manifests cannot generate new App Intent parameters. Keep optional static fields and a validated JSON escape hatch, with the Swift 6.4 snippet boundary intact. New complication identities and watch App Group signing need explicit account setup and paired-device acceptance. While builds are paused, report source/static checks separately from unexecuted Swift tests, metadata, screenshots and device/APNs acceptance.

## Hosted simulator boot diagnostics (#1124)

A successful minimum-Xcode app/Watch build does not prove the test simulator became ready. A stock iOS 26.0 boot failed its existing 900-second deadline before tests started; the shared helper had discarded `simctl bootstatus` output, and the workflow retained no diagnostic artifact. Keep boot command/phase errors on stderr (stdout remains the sole UDID), capture them separately, and collect `simctl list` before shutting down the owned simulator. Upload only those logs, runtime metadata and existing xcresult bundles after failure; never the source snapshot, configuration, keys or links outside result bundles. Keep the original boot/step timeouts and do not add automatic retries. A maintainer-approved manual rerun is distinct from a retry policy, but uses the original run's commit: diagnostic changes need a new-head run first.

## Newest geometry after programmatic scrolling (#1211)

A frozen transcript can reveal its withheld rows during Jump to Latest and move UIKit to the end without another SwiftUI scroll-geometry report. The real failure trace had offset -10 pt and adjusted top inset 10 pt (gap 0), while the follow state kept gap 1480 and `near=false`. A visible reply and following mode do not prove that the position state is current. Reconcile from the actual scroll container when the animation ends, keep the existing tolerance and reading intent, and skip a completion without this view's live subscription owner or without a measured container. Query the owner at completion: a captured visibility environment value can be stale, and another visible owner's subscription must not keep a hidden view eligible. Never fix the regression by forcing `near=true` or relaxing the UI assertion. For repeated UI failures, stop at the first failure and resolve the app container after testing: reinstall can change its data container, and a later successful launch overwrites the trace.

## Shell regression fixtures and simulated entitlements (#1117)

- A machine's token summary and its navigation card can repeat the same text. Scope the tap to the actual card button, not an application-wide text query. Choose a known multi-agent demo fixture before testing sibling selection, and expand a folded chat title before looking for its tabs. The home needs-you list intentionally shows at most three rows: verify the overflow route when Inbox actions are not among them, rather than assuming every kind is visible. Tests expecting the Agents list must clear `dev.openChat` at launch: opening a new assistant conversation does not discard the Agent chat a previous test correctly restored.
- Simulator entitlement emulation lives in the Mach-O `__TEXT,__entitlements` section generated from `*-Simulated.xcent`. `codesign`'s host-signature output alone can report no groups even when the simulator's effective groups are present. An unsigned test build can omit the simulated section and make the pairing registry unreadable; build the test app with ad-hoc signing and check the generated section instead of blaming the tab lifecycle. Device exports still need the normal signature entitlement check.

## Retained chat visibility (#739)

A shell can keep a chat mounted after switching tabs: `onDisappear` alone does not release its transcript subscription or visible/read owner. Use paired `onTabVisibility` edges on iPhone as well as Mac, keep the store and scroll state, and resume with `since_seq` on return. This pauses client display traffic, not the Agent's work on the hub. Test hidden initial mounts, duplicate visibility notifications, shared subscription owners and returning after missed items. Linux source checks cannot establish SwiftUI frame times; compile, profile both directions and inspect light/dark simulator screenshots on a Mac before publishing the Apple build.

Releasing the socket subscription is not enough if a background prefetch then applies HTTP items into the same hidden store (#1159). Keep disk prefetch for unseen recent chats, but defer retained stores to their next visible subscription. Recheck queued work and fence responses that outlive their last visible owner or store identity. Validate the actual HTTP journal and covered-sequence replay as well as zero delta deliveries: an empty replay after a hidden HTTP update does not prove that visibility owns transcript synchronization.

## Cached Agent-list projections (#1231)

Caching the Agents list's derived data needs a small key, not a fingerprint of thousands of sessions: the feed's structural revision plus the settings that change the result. Work moved off the main actor must take value snapshots only (no `AppModel`, bindings or model closures), and its completion must recheck the live state rather than captured flags: an immediate change or replaced hub link or token that superseded it, a newer published revision, a changed setting, or a list that became held while it ran. Do not cancel the running derivation for every newer revision: pushes faster than one derivation would then never publish. Run one at a time, keep only the newest waiting request, and let an older completed result land if nothing newer did. Publish through `ReaderAnchor.willChange` like any other change, and let a hold landing resume a result that finished while held. Pure helpers that the projection calls must be `nonisolated` explicitly (`Session.isQuiet`, `slotLabel`, `displayTitle`): the app target's default MainActor isolation applies to `apple/Shared` extensions too.

## Retired drawings and downloadable avatars (#1)

- Retiring local renderers must not turn the hub package catalog into an ID allowlist. Apply manifest validation and catalog/manifest ID equality to both cached and live lists, reserve retired drawing IDs, and keep an unavailable package's selected ID while rendering Mochi. The next valid listing restores the package without another selection. Live Activity normalization must keep custom package faces too; Milk Frog's watch pet remains specific to its own package.

## Release-mode demo coverage (#6227)

- A mock can support a feature while the bundled demo still hides it: omitted collections become empty defaults, and DEBUG screenshot helpers never run in TestFlight. Include the same synthetic todo samples in the bundle, verify every linked Agent has bundled content, and exercise knowledge search through the normal demo route. Keep the response compatible with both grouped results and flat hits; leave real network and phone-data actions refused.

- Helper extensions referenced by shared views must compile into every target too: after replacing `Color.mix` with `blended` for watchOS 10 compatibility, leaving the helper in `Theme.swift` causes Release archives to fail for Dynamic Island widgets that only include `PersonaFace`. Compile the helper with `PersonaFace.swift` into every target; do not drag the entire app theme into extensions or declare it twice.

## 稳定系统身份与可逆升级（#6152）

系统身份不等于显示品牌。公开助手操作保留稳定的 `persistentIdentifier`，Swift 类型和标题使用新名；隐藏适配器接纳中间版本的身份，让新版创建的操作在回装旧版后仍可识别。历史字面量与真实兼容类型声明只放在 `apple/Shared/NativeStateMigration.swift`。

Xcode 27.1 的 metadata extractor 会忽略间接常量引用：App 编译成功，却把新的 Swift 类型名当成系统身份。兼容文件内具体类型 extension 的字面量 static 属性可以正确提取。必须读回 `Metadata.appintents/extract.actionsdata`，再用相同 Bundle ID 原位覆盖，执行旧版实际保存的操作。不能重写 action blob 来通过测试。

2026-11-07 的兼容约定取代删除旧数据的一次性迁移：读取优先使用有效的新数据，再回退旧数据；启动只补缺失的新值，显式保存镜像旧键。兼容逻辑不删除旧键，不递归改写目录、图片、草稿、发件箱和录音。推送密钥双向补缺，保留字节和保护属性，不覆盖冲突身份，不删除来源。锁定或复制失败不能成为生成替代身份的理由。

ActivityKit 必须保留真实 nominal attributes 类型，不能用 Swift alias 冒充系统迁移。旧稳定类型和中间版本的新类型共用绘制配方。原位升级必须核对旧活动的实际 ID；编译、metadata 与运行验收是三种不同证据。

本次 iOS 27.0 模拟器的对照中，重复安装完全相同的旧构建也会丢弃原活动 ID；系统日志在安装阶段记录活动被视为 uninstalled。因此保留真实类型不能证明模拟器覆盖安装无损，新建的 ID 不能代替旧 ID 通过断言。这不证明真机 App Store 升级的行为，仍需另行验收。

原位安装后 App 可能已经被系统拉起，普通 `simctl launch` 不保证重新读取传入参数；探针启动用 `--terminate-running-process`，不注入新配对。图标变更和通知许可弹窗会令 App 保持 inactive，使活动截图样例不创建；用有超时的 XCUITest 处理这些系统弹窗，不把它们误判成迁移死锁。

通知扩展成功解密后会移除 sealed 标记。此时必须用经过认证的 payload 重建 `userInfo`，不能保留外层路由字段，否则 App 将扩展结果视为普通明文推送，外层 wake 或撤回字段便绕过了密文校验。默认组也从成功的密钥得出，并显式覆盖新旧 group 字段。

全量单测也不能读取或切换真实系统图标。两条独立运行都卡在 `LarkFeaturesTests` 构造 `AppModel` 的路径：`AppIconModel.live` 读取 `UIApplication.alternateIconName`，同步等待已有图标切换占用的 LaunchServices 队列；采样中没有兼容迁移或 Keychain 的阻塞帧。DEBUG 且注入了 `XCTestConfigurationFilePath` 的单测 host 使用无系统后端的图标模型；业务断言继续注入 FakeSystem，UI 测试目标 App 未注入 XCTest host，仍检查真实系统行为。Release 不包含此分支，不能把测试挂起写成通过。

## 产品改名不等于签名资源改名（#6150）

2026-11-07，源码中的默认描述文件名称随产品名更新，Apple 账号中原有的有效描述文件没有改名，导致发版在归档前停止。不要把字符串替换当成账号迁移，也不要直接重建所有描述文件来消除名称差异。

已有名称通过 `app_store.provisioning_profiles` 配置，签名预检与 ExportOptions 共用同一映射。先只读核对，再以新冻结重跑发布；保留第一次失败的冻结和日志。不恢复旧品牌别名，不修改证书、capability 或注册身份。

## 事件胶囊的外层对齐（#5962）

SwiftUI 的 `.frame(maxWidth: .infinity)` 默认把内容居中；父层 `VStack(alignment: .leading)` 只对齐这个满宽 frame，不能让里面的短胶囊靠左。事件标签应在 `.background(..., in: .capsule)` 之后加 `.frame(maxWidth: .infinity, alignment: .leading)`，让外层占行宽、背景仍跟内容走。不要把满宽 frame 放到背景之前，否则短标签也会撑满。回归测试同时检查短标签的左边缘和背景右侧留白，再用真实完成弹窗的深浅色截图确认与正文对齐。

## Compiler-free English catalog keys (#5090)

On hosted images, successful SDK lookup does not prove Xcode can resolve a generic device destination (#1124). Prepare the matching iOS platform/runtime before the first device build, not only before simulator tests; retain the minimum compiler and explicit runtime pin. The failed device build reported that iOS 26.0 was not installed even though the SDK checks passed. The pinned Xcode downloads a compatible patch runtime (26.0.1); forcing `-buildVersion 26.0` returned "not available for download". Let that Xcode select its platform, then require an available stable 26.0.x runtime and pass its exact identifier to the simulator helper. Reject newer minor versions and beta runtimes. An embedded watch app also needs the compiler-compatible watchOS runtime before a generic iOS device build; preparing iOS alone then fails with "watchOS 26.0 must be installed". Prepare and validate both platforms without removing the watch target.

A SwiftUI body can compile on a newer toolchain but exceed the minimum compiler's type-checking budget (#1124). Split List construction, style modifiers and observation/lifecycle modifiers into opaque helper functions. Preserve row content and modifier order exactly; do not add type erasure or container views just to shorten the expression. Verify the minimum compiler on hosted CI, not only a newer local build.

The minimum compiler can reject Boolean autoclosures that capture a main-actor probe dictionary containing `Any` (#1124). Read Bool/Double fields on that actor first, then combine the Sendable scalar values. Preserve the settling criteria and assertions; do not disable concurrency checks or skip the test target.

A Sendable fake transport does not make its stored callbacks actor-safe (#1124). MainActor model-test handlers must retain that function-type contract and be invoked with an explicit actor hop. A detached request against the old FakeHub produced an xcresult host crash and SIGTRAP stack through `swift_task_checkIsolated` and the route callback. Keep the route-table lock outside callback execution so handlers can register responses and inspect requests without deadlocking; verify the minimum-toolchain full run separately from a newer local subset.

Swift Testing method selectors include `()` (#1124). An incomplete pre-existing exclusion leaves an opt-in integration selected, so it appears as `Skipped` in xcresult even when xcodebuild exits zero. Match the declared selection precisely and keep the strict result guard; do not add a skip allowlist. The Reminders round-trip can run on a disposable stock simulator by granting its app Reminders access and setting `TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1`; keep its real store assertions and cleanup intact.

Changing the catalog's development language also changes the identity the Swift compiler extracts. Preserve each colliding English translation in its own contextual key and keep explicit English values; do not merge different Chinese or Japanese meanings. Reorder interpolation expressions together with translated argument numbers, and audit reordered expressions for side effects. Named App Intent parameters follow their names, not their old position.

Keep `LocalizedStringResource` and `LocalizedStringKey` arguments as localization values: wrapping a literal in `String(localized:)` is correct for a `String`, but breaks a helper requiring either localization type. Read each helper's signature rather than guessing from its name. Migrate nested literals from the inside out. Without a Swift compiler, reject unresolved raw literals and ambiguous argument types instead of silently guessing.

Source percent signs are literal text, while catalog `%%` is an escaped percent. Parsing `% left` as a printf specifier (`% le`) falsely rejects a valid key. Source-key checks must not run printf normalization twice. Compiler-free checks complement, but do not replace, Apple builds, Swift tests and three-language screenshots.

A catalog can be valid JSON and still fail every Apple consumer (#1123): inserting a complete string entry inside another entry's `localizations` makes the new key look like a locale with no `stringUnit`. The required en / zh-Hans / ja entries may remain valid, so checking only those languages misses it. Reject nested string entries in every locale object, keep new entries as siblings under `strings`, and verify that moving them preserves all translations.


## Asynchronous HTML sandbox loading and teardown (#2025)

Rule-list compilation is asynchronous; calling only `stopLoading()` in `dismantleUIView` cannot stop an unfinished compilation from reloading the page later. Cancel the previous task and advance the generation on every load; after compilation, check both cancellation and generation. At teardown, remove scripts, message handlers and delegates, then clear the page. A pausable rule-list loader tests both late completion of an older compilation and completion after teardown, without depending on accidental compilation speed.

In shells that retain pages, `onDisappear` does not mean a card has scrolled offscreen. Execution checks scroll visibility, `tabIsActive` and `scenePhase` together; remove the WebView if any condition fails. Replace the bridge instance marker on each load so queued messages from an old page cannot act after an update; this is not a hard CPU / memory quota for scripts.

## Use `EmptyView` for content-free platform stand-ins (#2001)

visionOS lacks iOS drawer gestures; `DrawerPan.NoRowSwipe` (a row mark, gone since #1244 took the swipes off Agents' rows) only needed a content-free background. `Color.clear.frame(width: 0, height: 0)` adds unnecessary fixed dimensions; iOS token checks scan shared source and do not ignore visionOS branches. Return `EmptyView()` directly: no drawing, no layout space. Do not add zero-size tokens for the stand-in, raise the baseline or add exemptions.

## Preserve platform defaults when simplifying navigation (#1653)

Compact iPhone width uses only the shell. Regular-width iPad keeps its sidebar and Inbox / Settings routes; making compact navigation unconditional across platforms would remove that split-screen behavior. Shared root tab components still serve Mac and Vision: removing a phone-only branch is not permission to delete those components or their badges, foreground scopes and destinations. Follow every platform caller before deleting a shared view, and protect the compact shell, regular sidebar and desktop / Vision tabs with compiler-free source checks.

## Keyboard-dismiss button and model display in the composer (#2200)

- 2026-11-05: removing the keyboard-dismiss icon also requires removing any conditional row created solely for the button; no transparent placeholder. Main and Agent chats share `ShellComposerBar`. `composerTyping` still controls header collapse in the sidebar layout; `Keyboard.dismiss` and `Theme.Icon.dismissKeyboard` remain used by gestures and terminal key bars and must not be deleted globally.
- GPT `name` formatting is display-only; preserve versions and suffixes. Model picker entries expose friendly names through computed properties; keep `AgentModel.id`, `name`, configuration values and request encoding unchanged. A distinct custom `name` takes precedence. Do not change generic `Fmt.model` or opportunistically shorten non-GPT labels that previously only lost the provider prefix. First confirm UI regression tests fail on the old implementation, then verify both layouts, main chat / Agents, keyboard shown / gesture-dismissed, and button sizes with long labels.

## A refresh task must not change its own key before the request completes (#1860)

After extension-page `invalidate`, using `stale` as `.task(id:)` and clearing it at refresh start makes SwiftUI cancel the network request that task is awaiting. The mock receives the redraw request but the app keeps the old page; all four appearances fail, while unit tests calling `refresh()` directly miss it.

Use an invalidation revision as the task key and handle the flag after completion. Cancelled requests do not consume invalidations; new events received during a request must not be cleared by an older request. Defer while an operation is busy and retry afterwards. Test cancellation and concurrent events with controlled async responses, then verify the whole WebSocket → redraw path in a real-app UI test. Multi-hub invalidations must also carry the group; matching only extension id is insufficient.

## Extension management confirmations and drafts (#1861)

- A `TimelineView` directly in a `List` may generate a blank row even when its content is temporarily empty. Preview expiry does not need per-second redraws: a page task waits for the earliest expiry among the main package and members, then updates confirmation state. No updates after cancellation; still check actual time before submission. Use a UI test that really waits for expiry and verifies the button is disabled.
- If an initially empty view in a `List` only returns `EmptyView`, its attached `.task` may never start. A template menu that only appears after a request should be loaded by the nonempty page and receive templates as input. Real UI tests expose "API readable, unit tests pass, entry never appears".
- `confirmationDialog` may set `isPresented` back to `false` before running the button action. Buttons must not reread a cleared optional selection to decide whether to execute: template actions capture the selected template; `cascade`-disable actions receive `cascade` explicitly from the confirmation button. Cancellation produces no second request.
- Appearance changes rebuild views through `RootView.id(look)`; keeping settings navigation in `AppModel` preserves the page, not its `@State` form. Store unsaved drafts in memory scoped by hub / extension, retain them when rereading, clear after successful save. Unparsed numeric input is also a draft; comparing configuration JSON alone is insufficient.
- UIKit providers for dynamic `Color(light:dark:)` have different identities each time; equal numeric colors do not imply SwiftUI `Color ==`. When changing static role colors into computed properties, keep default colors statically cached and create extension role colors only once per `StyleSpec` composition. Do not hide repeated color-object creation by changing existing tests to RGB comparisons.
- A collection's main preview being unexpired does not guarantee valid members: check every member's expiry, unresolved dependencies and permission digest. When uninstall returns `removed_members`, report only members actually removed successfully, not all members inferred from main-package success.

## Video export

- 2026-11-04, #1045: generated gray video triggered `EXC_BREAKPOINT / SIGTRAP` in a real compression test, with the stack in Swift AVFoundation's `AVAssetExportSession.ProgressStates.Iterator.StateInternal.init`.
  Do not mistake a `progress`-observer crash for encoding validation failure or skip H.264 / duration assertions. Read the session's scalar `progress` cancellably instead; on success or failure, cancel and await the observer before reporting completion or throwing. Already-cancelled operations never start export.
  Verify real MP 4 / H.264, duration, final `progress` on success, no file on cancellation, and no callbacks after failure. `progress` is deprecated in newer SDKs but still available; retain Xcode 26.0 compatibility. The actual crash environment was Xcode 27.1.
  Polling must preserve session actor ownership: pass only Sendable file URLs to an internal `ExportJob` actor, which creates and exclusively owns the asset / session. The observer accesses `self.session` in the same isolation as async export. Do not force the entire non-Sendable SDK object into `unchecked Sendable`.

## Chat lists and scrolling

- New-shell sidebar menus, chat names and body text use the same Dynamic Type level; do not apply `.shellScaled()` to the entire sidebar, since it enlarges another step (17 pt → 19 pt at standard settings),
  making two `Theme.Font.body` labels different. The Persona title keeps `display`, still following system and small / standard / large settings.

- After `List` reorder, insertion above or row-height changes, it preserves scroll offset, not the reader's row (2026-11-02, #339): a push reordered spaces above the middle of Agents, replacing the space onscreen entirely and removing the tested row. Preserve it yourself: rows report window positions with `onGeometryChange`; before changing data, remember the middle row,
  then adjust `contentOffset` by the difference after sorting (`ReaderAnchor`); skip at the top, during touch or scrolling. Two pitfalls: `List` later relays out upper rows using real heights, so correct on every position report for a short interval;
  if the row moves offscreen, recycling stops `onGeometryChange` and only `onDisappear` arrives; first bring it back with `ScrollViewReader.scrollTo`, then correct.
- `ScrollView` + `defaultScrollAnchor(.bottom)` + `LazyVStack`: jumping to distant ids drifts because real heights of upper lazy views differ from estimates; use `-fromSeq` to verify historical rendering.
- Do not attach an inverted chat list directly to the safe area: add a 1 pt empty view above and put the composer in the lower `VStack`. `ignoresSafeArea` plus inset padding can blank the screen and misalign the top when the keyboard appears.
- Invert the chat list (`scaleEffect(x:1,y:-1)` on both `ScrollView` and rows): newest messages are at offset 0, so jumping to latest is always exact. Handle safe areas yourself
  (`ignoresSafeArea(.container, edges: .vertical)` + `contentMargins` using actual insets), and disable iOS 26 `scrollEdgeEffect` (otherwise the whole page becomes hazy).
  Jump only to offset 0 (latest); programmatic distant jumps or `defaultScrollAnchor(..., for: .sizeChanges)` still blank when lazy estimates are wrong.
- `.scrollDismissesKeyboard(.interactively)` does not work on inverted chat lists (2026-11-05, #1823): dragging the chat with the keyboard open never dismisses it.
  Call `Keyboard.dismiss()` on `.interacting` in `onScrollPhaseChange` (actual dragging), not `.tracking` (could still be a tap, dismissing "`Other` answer…" inputs),
  or `.animating` from streaming content. Upright lists (assistant home, new-shell home, GitHub chats) are unaffected and need no change.
- Intermittent blank chat lists (`LazyVStack` + bottom anchoring): jumping by estimated height lands in an unrendered region. Jump to a sentinel id after layout and retry until it is truly visible;
  do not default `atBottom` to true (a blank screen then has no arrow to tap). Verify with scripted repeated cold launches and chat-region pixel standard deviation.
- Restoring saved reading position after cold launch (2026-11-03, #597): initially only the latest two turns are available (`ChatCache.tail`, hub `turns=2`, subscription backlog),
  excluding the previously read row; the old code gave up at bottom (returning during the same run worked because pages remained in memory). Now if absent with older history available, page backwards to find it (10 turns per page, node's maximum; at most 10 pages, giving up beyond ~100 turns at latest).
  Leaving during lookup does not delete the saved position (the screen still shows cached bottom); stop if a page fails or adds no items.
  Wait for rows before deciding absence: `ChatStore.loadedOnce` becomes true on merge, but background row rebuilding finishes at `backlogBuilt`; after paging, wait for `isRebuilding` false.
  Default mock returns all history and misses this: `/mock/paging?on=1` emulates hub paging (enabled in `ChatFollowTests` cold-launch cases, with `/mock/delay` to show cache first).
- After `scrollTo(id)` far from latest, the bottom sentinel may never report invisible through `onScrollVisibilityChange` (lazy loading removes it entirely), leaving `atBottom` true:
  restoration resumes following and the next reply drags the reader to latest (#597). On landing more than one screen from latest, set `atBottom` false (within one screen the sentinel remains alive and callbacks are reliable).
- Do not wrap potentially many rows into one child view (a `VStack` inside `LazyVStack`): hundreds lay out at once, blocking the main thread and blanking the page; make independent lazy rows.
- Lists whose rows expand themselves (topic-card **Expand**) must not use `LazyVStack` + `scrollPosition` (2026-11-04, #1087): after expanding the app never idles
  (no UI snapshot for 60 seconds on iPhone simulator; iPad or shorter cards happen to settle), only after the chat was previously opened (cached, all topics shown immediately).
  Eliminating animation, bottom anchoring, following, `.alignment` and position writes changed nothing; removing the `scrollPosition` binding or using `VStack` fixed it.
  Likely repeated estimated positioning and real-height measurement of offscreen lazy rows (SwiftUI internals were not inspected). Topic groups use `VStack` (`LarkChatStack`),
  with `onScrollVisibilityChange` for on-screen-only work. Upstream row-based position holding (`scrollTargetLayout`, `LarkHistoryHold`) later made this test pass alone,
  but still hang after others in the suite (11/04 evening), so it is not sufficient. This does not contradict the preceding warning: topic pages start at 40 and add 40; hundreds are needed to hit it.
- SwiftUI `onScrollPhaseChange` enters `.animating` for incoming content and programmatic offsets too; to respond only to users, check `.tracking` / `.interacting`.
- Finger-following collapse / expansion (Safari-style bars) is not "toggle state + animation": put 0…1 `progress` in a separate `@Observable` read only by that small view,
  write it per scroll `frame`, and overlay the moving piece rather than changing content frames (which relayout the entire page and jump messages). Snap on release.
- Chat-row bodies reevaluate during scrolling and streaming: cache text-derived Markdown, paths / links and tag-stripped text by message id (`RowMemo`, `TextMemo`),
  warming in the background after loading. Do not run regex / NSDataDetector in body (measured largest main-thread hotspot).
  - 收件箱过滤、分层和来源 chip 在同一完整快照中单遍准备，已处理排序只在展开时做（2026-11-07，#5730）。按 Store 的完整变更序号及筛选/搜索/本地化键复用，乐观更新、回滚和同 id/version 的新载荷都必须同步失效。
  - 缓存长期持有 Store 原数组会让单项修改触发全数组写时复制；有完整变更序号的路径只保留派生数组，不持有源数组，也不在无关重绘时逐项比较。没有序号的独立调用者仍用完整值比较，值相等但存储不同的命中须更新源引用，避免每次都线性比较。
- Do not write page `@State` in row `onAppear` / `onDisappear`, even invisible bookkeeping (2026-11-03, #674):
  Lark chat rows updated "seen" and report timers on entry, receipt bookkeeping on exit, repeatedly reevaluating the entire page during scrolling and doubling dropped frames.
  Keep invisible bookkeeping in a class held by `@State` (`LarkChatBook`), whose writes do not invalidate views; visible counts / receipts remain state, written only when actually changed.
- Do not construct WKWebView on the main thread while scrolling: preload links only for recent replies (within 15 minutes); preloading every old reply caused 1–2 second freezes.
- Scroll bookkeeping (visible rows, touch state) belongs in unobserved objects (`ChatScratch`); writing `@State` reevaluates the page. Cache derived rows by
  `ChatStore.version` and similar keys. Decode WebSocket messages on callback threads, not after returning to main.
- `ScrollPosition.scrollTo(y:)` also zeros horizontal offset: with `contentMargins(.horizontal)`, the page sticks to screen edges.
  Vertical jumps use `scrollTo(point:)` with current x (`ChatView.scrollList`). `scrollTo(id:anchor:)` lands by estimates for unrendered rows;
  for precision, jump then correct once or twice using measurements. XCUITest frames for inverted rows are wrong (offscreen); use in-app DEBUG probes.
- Upright chats with composers in `safeAreaInset(edge: .bottom)` (Lark chats, topic overlays, 2026-11-03, #672): the composer inset is accounted for; these hide the last message:
  - Opening at a message uses `defaultScrollAnchor(.top)`, leaving offset unchanged when keyboard or quote bar consumes bottom space, putting the last screen under the composer.
  - Post-send `scrollTo(id: new row, anchor: .bottom)` aligns a ~7 pt box at the row top, not the full row: 56 pt and 78 pt rows stop at the same offset.
  - `ScrollPosition.scrollTo(y:)` starts at content top, while `ScrollGeometry.contentOffset` starts at inset top (top is −top inset), differing by that inset.
  - Animated `scrollTo` during keyboard inset changes is swallowed: phase becomes `.animating`, offset stays fixed.
  - `LazyVStack` content height alternates between estimates (measured 39 pt apart), making computed offsets wrong.
  - Solution (`LarkFollow.swift`): while following, use `defaultScrollAnchor(.bottom, for: .sizeChanges)`; the scroll view knows real row heights. When not at bottom, add nonanimated `scrollTo(edge: .bottom)`. Split original anchors by role (`.initialOffset`, `.alignment` unchanged).
- Prepending a page to upright chat (`LazyVStack` + `scrollTargetLayout` + `ScrollPosition`, 2026-11-04, #1033, measured in simulator):
  id-based position holding preserves the first target, the top History header, not a message; the read message moves down exactly one page (pixel-verified page height), immediately hitting the top again and requesting another page.
  `defaultScrollAnchor(.bottom, for: .sizeChanges)` fails too (anchors switched during insertion only affect later changes; switching earlier also fails); moving the header outside `LazyVStack` into a wrapping `VStack` blanks the screen.
  Solution (`LarkHistoryHold`): on page arrival, choose the middle visible row from `onScrollTargetVisibilityChange`; attach `onGeometryChange` only there, reporting its content-coordinate position,
  and directly adjust `UIScrollView.contentOffset` by each delta (like `ChatPin`). Content coordinates do not change with scrolling; touch and inertia work normally; messages below do not affect it. Do not compensate by total content-height change: movement of that row's own date separator adds a 58 pt error.
- `onScrollGeometryChange` only calls back on changes, not its initial value: a short chat starts "near top", and a Bool-only value never triggers automatic loading (#1033). Include content height so initial layout reports.
  Conversely, during first landing, account confirmation or foreground return, the list has not reached its opening position: reopening with pending messages briefly stops at top (offset equals top inset), immediately requesting history (measured logs). Wait for settling at these times, then inspect actual geometry (`requestHistoryWhenSettled`).
- Bottom **Load updates** spinner paging via `onAppear` (2026-11-05, #1034): after one page it remains visible, so `onAppear` never repeats and loading stalls;
  `.task(id: cursor)` goes too far: lazy lists construct nearby offscreen spinners and run their tasks, loading from first unread all the way to latest.
  Solution: track true screen visibility through `onScrollVisibilityChange` in unobserved `LarkChatBook`; request only when visible, then wait for page layout and request again only if still visible.
- After replacing the whole window (returning to latest local tail), `scrollTo(edge: .bottom)` ending recomputes `larkFollowsEnd` from geometry before the new window is laid out, falsely marking not-bottom.
  To detect the user leaving bottom, track touch (`onScrollPhaseChange` entering `.tracking` or `.interacting`; some drags skip `.tracking`), not that moment's geometry.
- Freezing inverted history must not cut by row id: ids change as turns `progress` (answers absorbed into work rows, multi-message turns merged into `wt-`),
  so lost ids invalidate the freeze and push visible content (319 pt measured before fix). Rebuild rows from the item snapshot taken on leaving latest (`ChatView.Frozen`); verify with `-showFreezeProbe`.
- Before putting BeautifulUI components in chat lists, inspect appearance work. Lazy rows are repeatedly rebuilt; components starting `.task` and entrance animation (`buiEntrance`) on every appearance
  drop frames in long work cards: unmodified ThinkingState in a 650-step card increased Debug simulator hitch duration from ~11 to ~21 ms/s; removing both gave ~13.7.
  Fix these in the BeautifulUI repo, then copy the whole package into `apple/Packages/BeautifulUI` (`VENDORED.md`); do not modify library code in Persona.
- Expanding / collapsing inverted chat content must pin the header (`ChatPin`, 2026-10-31): new content pushes the header and everything above upward (thinking 40 pt, work card ~440 pt, second tap hits another step).
  Report screen position + content coordinates from the same layout; offset = offset at tap + header movement in content. Do not calculate from reported offset (one-`frame` lag overshoots and oscillates).
  `ScrollPosition.scrollTo(point:)` initially lands correctly, later differs by a content margin (10 pt), then returns; stored `ScrollPosition` (`edge: .top`) reapplies on content changes,
  jumping to latest for a `frame` when hub steps arrive. While pinned, set `UIScrollView.contentOffset` directly (`ChatPin.Finder`), clearing stored position at start.
  `scrollTo(id:anchor:)` anchors the entire row, so an expanded card is one row and cannot pin its header. Only chats taller than the screen can be fully pinned (`MOCK_LONG_HISTORY=1`).
- The hub continuously pushes lists, machines and steps; every push reevaluates consumers. Do not attach active controls to them (`docs/archive/apple/live-updates.md`):
  toolbars in data-driven views are resubmitted to UIKit each push, resetting open overflow-menu scroll position even with unchanged content. `ChatsView` handles navigation / toolbar, `AgentsList` handles the list
  (after splitting, `ChatsView.body` must read `searching`, otherwise chats opened from search retain the system navigation bar, `SearchOpenTests`);
  closure environment values in body (`.environment(\.openFile) { … }`) are new each time, reevaluating every reading row (all visible work steps per push); use once-created `ChatActions`;
  observe `model.session(key)` per session (`SessionCell`), not all-machine lists. Keep fullscreen viewers and row expansion state on the page, not lazy rows.
  Measure with `LiveUpdateTests` (`/mock/churn` continuous pushes, `-perfCounts YES` reporting body counts and main-thread time per round).
- Badges (pending count, Inbox Needs you) change almost every push; their consumers must not contain pages or menus (2026-11-01, TestFlight 70 "it's back", `docs/archive/apple/live-updates.md`):
  changing `Tab.badge` rebuilds its page even without parameters, resetting open overflow menus and reevaluating entire chats.
  Wrap every `Tab` page in `TabPage { … }.equatable()`. Changing `Menu.label` relays out and resets open menus; put counts / red dots in small self-observing views beside it
  (`WaitingBadge`), with menu hosts not reading per-push data. Mock must push `inbox` and `interaction`
  (`/mock/churn?inbox=1&waiting=1`); run default compact shell and regular-width sidebar navigation in both styles.
- Row parameters containing closures or structures with strings / arrays (`Item`) make SwiftUI treat them as changed every time: each push reevaluates the page and all visible plus lazy-buffer rows
  (~45 in a long Persona main chat, 2026-11-01). Chat bubbles, replies, work rows and composer all use `Equatable` + `.equatable()`, comparing only messages;
  permanently mounted but hidden content (closed sidebar) must not read changing `allSessions`; use its previous value while hidden.
  Streaming full text arrives every 80 ms: split and cache completed blocks (`StreamChunks`), redraw only the last. Measurement: `docs/operations/testing.md` ("Persona page (long main chat)").
- Do not sort lists by last update or let pushes touch them while scrolling (2026-11-01, TestFlight 77, `docs/archive/apple/live-updates.md` ("Forced refresh while scrolling")).
  Real hub pushes a machine's sessions every 2 seconds with new `updated_at`; state + last-update sorting reordered spaces on 36% of pushes and jumped the screen.
  - Sort by a time independent of each output: working sessions by work start (`Session.sortDate`, `SpaceGroup.sortDate`).
  - Hold pushes during touch, inertia, covered pages or another tab; land together 0.5 seconds after stopping, at most 5 seconds even with continuous scrolling;
    never hold needs-you, questions, offline or errors (`docs/apple/ios-state.md` ("Timely (bounded)")). The list reads only an intermediary (`AgentsFeed`,
    `withObservationTracking` following the model), with an unobserved hold switch. A list `@State` snapshot would redraw at scroll start.
  - Holding must actually block pushes. These leaks still reevaluate each push:
    - Reading the model in `onChange` / `onScrollPhaseChange` counts as body observation.
    - UserDefaults writes invalidate every `@AppStorage` view. `AgentSync` wrote on every chat fetch, every few seconds on a busy hub:
      changing state belongs in `StateCache`, not UserDefaults.
    - Each `List` row remembers its own dependencies: after body switches to snapshots, unredrawn rows still observe old `model.machine` / `model.session`
      and redraw on pushes. Rows must not read the model directly: populate an unobserved lookup table (`AgentsList.Lookup`) in body and read it from rows;
      pass child values from the list (`InteractionPreviewCard(machine:agentLabel:)`, `model.isMuted(session)` rather than `isMuted(key)` via `model.session(key)`).
  - Even identical data updated in `List` triggers estimated remeasurement (70 pt difference per row), changing offscreen heights and jumping hundreds of points.
    It also occurs when landing between drags, only jumping on the next touch. This intended landing is `landed` in `PerfScroll`, not interruption;
    do not delay landing to hide it, leaving onscreen data stale too long.
  - `onScrollPhaseChange` works on `List` (iOS 27 simulator measured). Callback-only scroll state belongs in an unobserved object; `@State` reevaluates on every phase.
- A resizing TextField in the same tree as a long list causes root-to-page relayout and visible lazy-row remeasurement (~10 ms per typed character).
  Put it in its own hosting controller (`LayoutIsland`), measuring internally and reporting height; the page relays out only when height changes.
  Do not use `sizingOptions = .intrinsicContentSize`: tested, the composer failed to grow while typing (2026-11-01, `ComposerGrowTests`).
  `ShellComposerBar` expands from a line to a card and does not use isolation: long-text snapshots repeatedly time out even on a new simulator; retain its tree. Isolation belongs only on compatible desktop composers, not the iOS shell or regular-width card.
  Content drawing beyond its bounds cannot use an island: the hosting view only accepts in-bounds taps, so visible overflow taps hit the underlying page.
  BeautifulUI `PromptBar` places `/`, `@`, + and model menus above the composer, making all untappable inside it (2026-11-02, `BeautifulStyleTests.testCommandMenuTakesTaps`); this style also skips islands.
  Do not transfer environment by replacing `.environment(\.self, context.environment)` wholesale (2026-11-04, #995, #1006): it includes the outer hosting controller's own values
  (`RenderingRootViewKey`, hosting controller, toolbar context), overwriting the island's. Under a chat navigation hosting controller,
  the island disappears from accessibility: composer, + and send become one childless `Other`, unreachable to VoiceOver / UI tests on every launch (`ComposerAccessibilityTests`).
  Transfer values explicitly (`IslandEnvironment`: AppModel, shell, shellChat, buiTheme, text size, colors, language, direction, tint); add further page values when needed.
  `DrawerSidebarHost` also copies the whole environment but is at root and has a normal tree. Diagnose by writing `String(describing: env)` on both sides and comparing keys / values (only host-related ones differ);
  failed attempts: container controller, removing `EquatableView`, height-measuring `fixedSize` / `frame`, root `.accessibilityElement(children: .contain)`.
- Composer "only shows the last line": first measure card height in screenshots (2026-11-03, #909). On device the card already had two-line height, one line of text and an empty line;
  SwiftUI `VerticalTextView` (`UITextView`) remained scrolled down one line, rather than failing to grow.
  System simulator keyboard, Pinyin marked text and a custom keyboard extension could not reproduce it. After height changes, `ShellComposerBar` pulls focused text-view offsets
  back from beyond the content end (`TextViewScroll`), doing nothing to in-range offsets. Do not determine height with hidden `Text` line measurement:
  `Text` does not compress fullwidth punctuation while the composer does, differing by one or two characters at wraps.
- Before sending, commit ongoing input-method composition rather than reading only the TextField binding (2026-11-05, #2203): with WeChat Input in main chat, send transmitted only the first half,
  unlike other keyboards. Third-party keyboards often retain unconfirmed marked text (already Chinese, underlined, appearing complete); SwiftUI bindings need not follow every extension step,
  and clearing can cause the keyboard to reinsert unconfirmed text. All three composers (`ShellComposerBar`, `ComposerRow`, `PromptComposer`) now use
  `TypedText.commitFocused`: if focused, call `unmarkText()` (as tapping elsewhere does), update the binding from the text view, then send.
  The simulator cannot reproduce WeChat Input itself: a keyboard-extension-only app following `testing.md` exposed `setMarkedText` / `unmarkText` / `insertText` buttons;
  stepwise bindings kept up (including marked text), while dozens of `setMarkedText` calls in one button corrupted both SwiftUI and plain `UITextView`,
  implicating the keyboard-extension channel, not a valid reproduction. `TypedTextTests` directly test `UITextView` marked text and lagging bindings.
  Set keyboard lists without Settings through `simctl spawn <udid> defaults write -g AppleKeyboards -array <extension bundle id> …` and `com.apple.Preferences KeyboardsCurrentAndNext`; it is active at launch.
  System Pinyin is `zh_Hans-Pinyin@sw=Pinyin-Simplified;hw=Automatic`; during candidate selection the space key says `选定` ("Select").
- Do not wrap `List` rows in custom views merely for `Equatable` then put `.swipeActions` / `.contextMenu` inside body: tested 2026-11-01, both pushes and scrolling slowed
  (UIKit reconfigures cell swipe actions every time). Reduce Agents push cost by batching (`AppModel.applyPendingSessions`).

## 长消息输入框的动态阈值（#5685）

- 可用高度取输入框底边在窗口中的位置，扣除顶部安全区，不取输入框自身高度。自身高度随着输入变化，用它计算行数上限会形成反馈循环；窗口坐标还能避免独立 hosting controller 的局部「global」坐标影响结果。
- 展开按钮出现后，文字右边距增加。用于判断是否显示按钮的测量始终保留未缩进的宽度，否则新增边距会让文字再次换行，删字到边界时容易闪烁。实际高度仍由原生 `TextField.lineLimit` 控制，不使用隐藏 `Text` 强行设置高度；隐藏测量只判断阈值，标点的原生压缩行为可能与它相差少量字符。
- 不用 `TextField(axis: .vertical).fixedSize(horizontal: false, vertical: true)` 强撑高度：实测多行原生文本视图的实际 / 无障碍范围会伸进工具栏，测试点在文字末尾却被工具栏接走，键盘不弹。保留原生约束和 `lineLimit`，通过文字区的 `layoutPriority` 和包含按钮偏移的最小高度分配空间。
- 按钮的可见图标很小，但点击区域会随动态字体放大。引用栏压缩文字区时，只有原有最小高度还不够，按钮顶部偏移会让点击区域伸进底部工具栏；最小高度要包含这个偏移，右侧预留宽度也要使用实际测得的点击区域宽度。`assertCorner` 同时检查图标属于文字区且点击区域没有进入工具栏。
- 全屏页的底部发送区用编辑器的兄弟布局。改为 `safeAreaInset` 时，实测 `TextEditor` 的无障碍范围仍延伸到键盘，与发送按钮重叠；引用按钮也不能可靠地点击。改成明确的竖向布局，并给页内引用区独立的标识后，两项主对话 / Agent 引用回归通过。测试同时核对编辑区与发送区不重叠，不把「元素存在」当成「可以点击」。
- 到达阈值时，按钮缩进改变换行，原生文本视图有时仍保留旧的可见范围，刚输入的末行会被裁掉几像素。iOS 输入框在高度变化后的布局完成时先修正越界偏移，再 `scrollRangeToVisible(selectedRange)` 保持光标可见；不修改文本和选区，也不在用户仅手动滚动时触发。截图复查确认三行都能完整显示，光标回传测试继续通过。
- UI 测试不再假设「超过两行就展开」：小屏、大屏和键盘收起后的上限不同。先逐行输入找到当前阈值，再测试停止长高、右上角位置、展开 / 收起和删除。键盘收起后，上限增加，未达到新上限的草稿可以不再显示按钮。

## Navigation

- Markdown in a file-preview `.sheet` inherits the chat page's `openURL` (2026-11-03, #796): tapping only changes the underlying chat's link state; the file drawer hides the link drawer until closed. The current presentation layer must own link state and presenter and override `openURL` / `openLink`; do not close the file first or retry the underlying presenter after a delay. Pass the original chat key; closing the link retains the file and reading `position`. Tap-test both file entry points, normal web pages and port forwarding.

- "Main page over the sidebar" and "two adjacent pages sliding together" describe different spatial relationships (2026-11-02, #341): the underlying sidebar stays horizontally fixed with only a subtle upward lift; centered scaling moves text sideways, while strong fading makes it seem newly created. Neither substitutes for the lift. Check intermediate drag frames; fully open screenshots look identical. Unit tests require x displacement 0, scale 1, opacity 1. Keep the sidebar's separate hosting controller; progress changes only transform, not per-frame list layout.
  Check extracted frame collages too: `ffmpeg` `-t` plus `tile` can exit 0 without a PNG. Use `-ss`, limit collages with `-frames:v 1`, confirm the file `exists` and actually view it; command success is not screenshot acceptance.

- NavigationStack root `.task` / `onAppear` reruns after pushing and returning: do not unconditionally restore saved defaults, overwriting choices just made on a child page (new-chat launcher reverting to the previous selection).
- Put environment values for pushed pages (e.g. `feishuOpen`) on NavigationStack itself; pushed pages do not inherit values attached only to root content.
- Hiding the navigation bar (`.toolbarVisibility(.hidden, for: .navigationBar)`) also disables edge-swipe Back; enable `interactivePopGestureRecognizer` yourself.
- If root hides the navigation bar and a pushed page has active search, swiping Back while searching leaves UIKit's bar on root (root title, ~106 pt high),
  while SwiftUI thinks it remains hidden (2026-11-01 new shell: Knowledge search → assistant edge swipe pushed the shell bar down ~106 pt; old switcher `position` hit an empty bar).
  Closing search on the pushed page when `isPresented` becomes false is too late. On root return, call UIKit `setNavigationBarHidden(true)` again (`AssistantView.RootBarHidden`).
  Tests must inspect bar frames, not just `exists` (`ShellSwitchTests`).
- Horizontal paging (`ShellPager`) lets one page cover part of the other's in-page header because they are ZStack siblings; counter-offsetting the header does not help
  (2026-11-01 screenshot: at 70% drag Agents' menu icon disappeared). During drag, hide both page headers and draw a display-only header above them (`ShellMovingBar`); restore native headers at rest.
  Check a paused intermediate drag: `-showShellPagerAt 0.3`.
- For content replaced after animation, use `withAnimation(…, completionCriteria: .removed)` to await true spring settling, not default `.logicallyComplete` (2026-11-02 new-shell top-tab switch jitter):
  the default fires when the spring looks nearly done, with ~5 pt remaining in `ShellPager`'s 0.32-second spring. Setting `moving` false restores page headers on the still-`moving` offset,
  jumping 3–5 pt before easing back in both styles. XCUITest misses it: frames are layout values without offsets, and one screenshot is slower than the 170 ms event;
  record and measure frame by frame (`simctl io recordVideo`, `ffmpeg` header extraction). Unit tests use a window attached to the host `app`'s scene and `drawHierarchy` pixels (`ShellPagerSettleTests`; windows without a scene draw nothing).
- `.removed` (and `.logicallyComplete`) waits for **all** participating views; an animated view outside the window (root covered by a pushed page, including before spring completion)
  does not animate or finish, so callbacks wait until it returns (2026-11-03: opening an Agent from Inbox Needs you left "assistant turned into Inbox, can't swipe or tap", with a switcher stuck above chat).
  The bar remains `moving`, both pages reject taps, and `ShellMovingBar` covers chat until Back brings root into the window (3.7-second callback delay in trace).
  Page headers participate by appearing / disappearing with `pager.page` derived from `position`; the switcher pill reads `position`. Fix: disable animations around header presence (`ShellBarAtRest` wraps
  `.transaction { $0.animation = nil }` around the `if`, not inside it). Only the temporary `moving` header's pill follows `position`; native headers stay on `shell.page` (transparent during drag).
  Disabling only when covered or only wrapping the pill is insufficient: children directly observing `position` bypass ancestor transaction changes. `ShellPager.settle` also has a 1.2-second fallback (trace `settle lost`).
  The switcher itself read `pager.page` (selected segment as Menu or button), participating too: high simulator load (another running in parallel) still hit `settle lost`; fallback changing `moving` redrew it, and callback followed 20 ms later.
  Now the switcher reads only `moving` (set outside animation), never `position`-derived values. Rule: native-header content reads only `moving` / `lingering` / `shell.page`, not `position` / `pager.page`.
  Failed attempt: a zero-size `Animatable` view reporting per-frame `position` (offscreen drawing does not evaluate body per frame). Diagnose long `slide` → `settled` delays or `settle lost` (forbidden by `ShellInboxAgentTests`).
- Topmost nonblocking content (toast) uses its own scene `UIWindow`: `windowLevel = .normal + 1`, `hitTest` returns nil, letting taps reach the underlying window;
  shell sheets, drawers and pushed pages are below it (2026-11-04, #1053, `ToastWindow`). SwiftUI overlays are covered by sheets, and `allowsHitTesting(false)` does not stop UIKit interception.
  Supply dark / light appearance (copy shell window `overrideUserInterfaceStyle`; later `ColorSchemeChoice` updates all scene windows), `app` text size
  (`textSize.applied(to:)`; a new window otherwise follows only system), `AppModel` and BeautifulUI theme. VoiceOver will not move to a non-touch window; announce through `AccessibilityNotification.Announcement`.
  XCUITest still finds it in the `app` tree. Place it below the shell header, not at the status bar where it covers segments (`ShellNavigationClearance.needed(at:)`).
- The regular-width iOS tabs kept the toast in `RootView`'s overlay after the shell moved to `ToastWindow`, so every toast raised from a
  `fullScreenCover` or sheet was drawn under it: Saved to Photos from the image viewer and the share card never showed (#1492). `PhotoSaveTests`
  passed all along, because XCUITest finds a covered toast's words. A test that a toast is seen compares the pixels where it is with and without it
  (`PhotoSaveTests.expectDrawn`). Now the tabs get a toast window too (`ToastWindowHook`); the overlay is left only where no such window exists.
- Both new-shell pages remain mounted, the hidden one only opacity-hidden; UIKit glass buttons and lists stay in accessibility, with identical switcher ids.
  `exists` / `isHittable` / `firstMatch` cannot identify the drawn page; inspect pixels (`ShellSwitchTests.agentsDrawn`) and select the tappable switcher.
- Pushing from active `.searchable` forces the navigation bar visible (double chat titles); closing search after pushing does not help.
  Close search first (`searchable(isPresented:)` false), then push next run loop (`ChatsView.pushing`), retaining `query` / results. Closing clears system field text while `query` stays unchanged;
  remember the term before pushing and reassign on return (`searchedBeforePush`). `SearchOpenTests` includes field text.
  Do not wait a fixed delay: closing only begins dismissal; pushing during it loses the page while path already contains chat, leaving list pixels and navigation state inconsistent.
  `isSearching` becomes false within milliseconds, not proof of completed dismissal. When redraw takes ~440 ms (measured 2026-10-31), #128's 350 ms was insufficient and search-result chats failed to open.
  Inspect `UISearchController`: inactive, not dismissing, no transition, then push (`SearchCloseWatcher`, at most 2 seconds). Expand folded matching spaces / groups (`isFolded`) so searchable Agents are actually tappable.
- iOS 27 simulator edge-swipe XCUITest needs finger-like speed (hold 0.1 seconds, `.slow`, pause 0.1 at end); a 0.05-second flick is not recognized,
  falsely suggesting broken Back while manual use works (`SearchOpenTests.back`). On smaller phones list rows / headers may be offscreen and unbuilt:
  scroll first (`AgentsListTests.reach`), and test 6.3-inch iPhone 17 Pro as well as Pro Max.
- Within one NavigationStack, pushing view-based `NavigationLink { destination }` then value-based `NavigationLink(value:)` inserts the new page **underneath**:
  taps look ineffective until Back (Settings › Machines › card, machine detail › session; Knowledge search entries also reveal a yellow warning on return, 2026-10-31). Use an entirely value-based chain (`MachineListRoute`,
  `MachineDetailRoute` + `navigationDestination(for:)`) or entirely view-based. `navigationDestination(isPresented:)` is view-based too.
- If `x` in `NavigationPath([x])` is `AnyHashable`, the recorded type is `AnyHashable` and a concrete `navigationDestination(for:)` cannot match,
  producing a yellow warning. Append concrete types (`SettingsView.openDebugPage`).
- `NavigationSplitView` detail pages use destinations registered in the sidebar and are built in its environment. Put detail environment values
  on the whole split view, not the detail NavigationStack (`SplitNavigation`).
- Selecting another sidebar item replaces parameters of the same detail type without rebuilding it (2026-11-03, #885): `@State`, `.task` and subscriptions remain from the old item,
  while directly parameter-driven titles / placeholders change. Switching Lark chats changed title but kept old messages and draft.
  Apply item `.id` to sidebar-opened pages (`InboxDetailView(…).id(item.id)`, `ChatView(key:).id(key)`; Mac Inbox already did);
  `.task(id:)` alone do not reset drafts, quotes or read positions. Covered by `InboxSplitTests` on iPad and open iPhone Duo.
- iPhone Duo (`docs/apple/ios.md` ("iPhone Duo")): choose layout by width / size class, not model. Simulator folding only switches in Device Hub;
  screenshots require `--display=1/3`, otherwise the inactive screen may be entirely black.
- Do not hide tab bars on pushed pages through `.toolbar(.hidden, for: .tabBar)` or `.toolbarVisibility`: it removes the bar in the first push frame,
  simultaneously removing the underlying list navigation / search bar and flashing on chat entry (2026-10-31). Use UIKit `setTabBarHidden(_:animated:)` with appearance / disappearance
  (`hidesTabBarWhenShown`); SwiftUI sets `hidesBottomBarWhenPushed` too late. XCUITest misses the flash:
  record `xcrun simctl io <udid> recordVideo`, `tile` the first 0.3 seconds after tapping with `ffmpeg`.
- Entering Lark chat from a hidden-bar Messages list, iOS 27 automatic scroll-edge blur only appears after animation, allowing cached images to show clearly behind the title (#952).
  `.toolbarBackground(.visible, for: .navigationBar)` alone is insufficient; also specify `.toolbarBackground(.bar, for: .navigationBar)` to retain system frost.
  Record intermediate frames with cached colorful images under the bar; blank-loading screenshots miss it. Keep `hidesTabBarWhenShown` and regress both cancelled and completed edge-swipe returns to the original entry.

## `Layout` and components

- Under iPhone Duo's vertical bars the system draws the navigation bar's title as a row over the content, not in the side column with the buttons: a page that draws its own title shows the name twice. Remove the system's with `.toolbar(removing: .title)` while yours is on screen (`ChatView`, 2026-11-08).
- A `GeometryReader` that ignores the safe area on an edge reports no inset of its own there (`safeAreaInsets.trailing` was 0 on iPhone Duo's outer display, where the system's bars take the right-hand column): a rail meant for that column came out 59 pt wide against the screen edge, half off it. Measure the column as a reader inside the safe area less one that ignores it (`ShellSideRail`, 2026-11-07).
- Every page on the shell's strip draws its own bar, so each one has to switch to `ShellSideRail` on `\.shellRailEdge`; the moving bar only stands in while the strip slides. The optional Messages page was missed and put its bar back across the top whenever it was in front (#2318); `ShellSideRailTests` checks the pages' rails on an iPhone Duo.
- When capping chips in horizontal scrolling, do not put `frame(maxWidth:)` / `scaledFrame(maxWidth:)` outside the background (2026-11-05, #3010): `ScrollView(.horizontal)` proposes no width,
  and flexible frames pass that unlimited proposal through. The chip and background measure the entire title while only the frame's occupied space is capped;
  background and space diverge: shorter chips leave larger gaps, large text does not truncate and capsules overlap neighbors.
  Use `scaledWidthCap(_:)` outside the background (`WidthCap` proposes the cap to its child and reports the child's size).
  `DockPillLayoutTests` render with `ImageRenderer` into transparent side slots and check background pixels stay out, across text sizes.

- A `List`'s rows come and go with the transaction that changed them (2026-11-07, #37): `.animation(_:value:)` on a `ForEach` or section inside the
  list animates what the rows draw, but the list still inserts and deletes rows at once (a recording showed a keyed row replaced in one frame).
  Commit the change with `withAnimation` where the data changes (`ExtensionMotion.redraw`), or put the modifier on the `List` / `Form` itself; a `VStack` honors the inner one.
- Unequal brand marks (2026-11-03, #1003): identical `Image(…).resizable().scaledToFit()` square frames still look different.
  Assets cause it: Lark's 88×88 SVG bird occupies only 70% × 56% and is right-shifted, while Telegram / Microsoft fill the canvas. `scaledToFit` aligns canvases, not drawings.
  Crop whitespace through viewBox, render through `BrandMark`, then optically shrink full-square marks. Measure opaque bounds after `rsvg-convert`, and screenshot @3x frame / centers, not by eye.
- Do not test `items is EmptyView` to decide whether an overflow menu has entries (2026-11-03, #778): false ViewBuilder `if { Button }` produces nil `Optional<…>`,
  not `EmptyView`, causing empty overflow menus (Persona sidebar Pinned with only main chat pinned).
  `HeadingMenu.isEmpty` recognizes only `EmptyView` and a single false `if` (nil `Optional`); multiple ifs, `if`/`else`, `ForEach` or `Group` count as entries even `if` nothing draws,
  so pass explicit availability for those. Hidden-menu rows retain menu height (`HeadingMenu.minHeight`, both styles) to avoid content jumps.
- `converter.localLocation` is relative to the gesture view; placing content in a parent container (action bar on row, gesture on bubble) misses the bubble's row offset,
  moving own-message actions to the left (Mac and iOS, #501, 2026-11-03). Name the container coordinate space and use `converter.location(in: .named(…))`.
- When rewriting a page for a new shell / style, inventory every old section and explicitly decide what carries over, not just the new mockup (2026-11-02):
  new-shell main chat had composer and Needs you but omitted welcome greeting, accessible data and suggested questions, leaving an empty page without to-dos until the user noticed a day later.
  Extract shared content (`AssistantWelcome`) and UI-test onscreen, unobscured elements, not just compilation.
  A `safeAreaInset` without background lets long content show behind Needs you; give it the page background.
- Bottom fixed content (Needs you + composer in `safeAreaInset`) sized by normal iPhone row counts consumes half a short screen (Duo outer ~678 pt, SE),
  cutting scrolling content in half (2026-11-02, #283). Measure the bottom of the row that must remain uncovered (`WelcomeFold`, in content coordinates,
  unaffected by scroll) and page height excluding keyboard; allocate remaining height to bottom content. Use resting composer height to avoid focus / typing row-count changes. On iPhone the keyboard shrinks the entire page frame,
  not safe area; background `ignoresSafeArea(.keyboard)` cannot recover original height. Keep the largest measured height at the same width (rotation / Duo folding changes width).
  Test typing invariance on a model initially showing multiple rows (17 Pro); Duo already shows one and cannot drop further, falsely passing. Fade the background's top edge rather than hard-cutting it.
- `ViewThatFits` needs a height budget, not an outer `.frame(maxHeight:)` that it fills. Use a `Layout` proposing at most that height while reporting child size
  (`AtMostHeight`), without taking the minimum with parent's proposal: keyboard shrinks `safeAreaInset`'s VStack proposal and focus drops rows. If none fit, `ViewThatFits` takes its last child at the smaller proposed height, flattening BeautifulUI rows;
  give each child `.fixedSize(horizontal: false, vertical: true)`.
- Duo outer screen (and landscape inner) has a sideways status bar and horizontal safe insets. A background sized only by `g.size.width` (header fog) stops beside it,
  creating a hard dark-mode edge. Add `safeAreaInsets.leading + trailing` to width and shift left by leading (`BarFog`).

- Container `accessibilityIdentifier` can overwrite child buttons in XCUITest (`assistant-sidebar` hides the bottom New Chat pill's `sidebar-new`):
  find by label, then use identifiers to exclude same-name buttons elsewhere.

- Request state (asking a page to push or open an overlay) needs a sequence and remembered handled id (2026-11-01, `ShellState.request` / `AssistantView.handledRequest`):
  `onChange(…, initial: true)` sometimes handled it again after returning, pushing Inbox back (1 in 3; caught by `-test-iterations 3`).

- Do not use `.toolbar` to align system-bar content with custom floating headers (2026-11-01 Assistant | Agents): asymmetric side buttons push `.principal` off-center,
  and shared navigation glass wraps the pill again, changing selected color. `.sharedBackgroundVisibility(.hidden)` removes glass but not displacement.
  Agents keeps the bar only for `.searchable` (a transparent `.principal` dot hides title) and overlays `AgentsTopBar` using the assistant's components.
  Active search must clear that overlay, as below.
- Custom headers above system navigation (`AgentsTopBar`, system bar retained for `navigationBarDrawer` search) conflict when active search moves into the header row,
  overlapping both layers' controls (TestFlight 70). Do not draw the custom header while `searching`; system search and Cancel occupy the row.
  Compare intersecting frames in `ShellSearchTests`; `exists` alone misses overlap.
- Inactive pull-down search still has only standard-button clearance, so enlarged shell buttons and avatar Inbox badges overlap (2026-11-02: avatar bottom 125 pt, search top 117 pt).
  `safeAreaPadding` does not move UIKit search. Set a space in Agents root `navigationItem.prompt` (`ShellNavigationClearance`),
  adding one 34 pt row independent of text size, moving search and list down; remove during active search; this clearance belongs only to compact shell headers. Cap header controls at `Theme.Shell.topBarLargestTypeSize`
  (xxxLarge, 1.35×, `shellTopBarScaled()`), preventing accessibility-size avatar overlap. `subtitleView` and two-line prompt attempts did not grow further.
  `ShellSearchTests` cover small / standard / large expansion, input, cancel, return from results, with light / dark screenshots.
- **Do not toggle per-page clearance on navigation containers** (#340, 2026-11-02): old outer padding by `path.isEmpty` disappeared on chat push and returned on Back.
  UINavigationController does not reposition its bar during transitions: return starts at chat coordinates (container 88 + old safe area 62 = y 150, expected 98),
  list inset is wrong, drawer search expands as `if` at top (+54), then 0.1–0.6 seconds after transition relayout jumps 52 pt and leaves a different final position with search open.
  Switching without animation at root `viewWillAppear` and forcing `setNeedsLayout` / `layoutIfNeeded` also fails: safe area is already correct but bar frame does not move.
  Keep navigation-container frame / safe area invariant during push and Back (shell status row changes root `additionalSafeAreaInsets`, moving everything together);
  page-specific space belongs to its navigation item for native transitions. `AgentsReturnTests` measure `probe.agents-scroll` per frame (both return gestures × search open / closed × both layouts).
- To inspect one-frame shifts: DEBUG `-showPopFrames YES` logs `POPF` whenever window positions / insets of navigation bar, search bar or large (>300 pt) collection / scroll views change,
  combined with `simctl io recordVideo` and `log stream --predicate 'eventMessage CONTAINS "POPF"'`. UIKit transitions change presentation layers; logs show model end values,
  while SwiftUI frame animations update model values each frame.

- Never wrap system button styles with `Button(configuration).buttonStyle(.glass)` in PrimitiveButtonStyle: glass recurses to stack overflow,
  crashing chat opening with runtime type-resolution frames and no business code. Select styles directly through View extensions (`.themedButton()`).
- Two-axis monospaced scrolling: SwiftUI `ScrollView(.horizontal)` + LazyVStack cannot determine width and only scrolls vertically; self-scrolling UITextView
  wraps in TextKit 2 regardless of container width. Use UIScrollView around a non-scrolling UITextView measured by longest line (`MonoTextView`).
- `ScrollViewReader.scrollTo` also scrolls outer containers: initial scrollTo in a top horizontal strip pushed the whole region into the navigation bar.
- `List` `.simultaneousGesture(TapGesture())` (blank-area dismiss) treats long-press release as a tap unless held >~2 seconds:
  newly opened message actions close on release. Suppress closing during that press and 0.3 seconds after release (`MessageMenu.pressChanged`).
- `List` `Section` headers are not cells; `.contextMenu` rarely responds there. Give header actions visible buttons / `Menu`.
- A `.contextMenu(menuItems:preview:)` preview is drawn in the context menu's own host, which the app's environment objects do not reach (2026-11-08, #1490): a preview showing a view with `@Environment(AppModel.self)` crashed the app on the long press, in SwiftUI's environment assertion with no app frame on the stack. Hand the preview what it reads (`.environment(model)`, the scopes it uses).
- Put MarkdownUI `.markdownTextStyle` inside `.markdownTheme`, called first, or theme text styles overwrite it.
- Link endings have one rule (`Links`): stop at whitespace or any non-ASCII (Chinese / fullwidth punctuation), strip trailing `.,;:!?` and unmatched `)` / `]`.
  MarkdownUI cmark-gfm autolinks stop only at whitespace, swallowing Chinese paragraphs after URLs (2026-10-30). Run all MarkdownUI text through
  `Links.markdown` (bare links become `<url>`): replies, plans, files, Lark messages, knowledge. `FilePaths` also stops at non-ASCII.
- Reply tables must not grow by content (MarkdownUI Grid uses widest cells, sending long Chinese columns offscreen): `FittedTable` allocates available widths,
  preserves short columns and wraps long ones, scrolling horizontally only `if` minimum widths do not fit; right-edge fade hints at scrolling (`TablePlan`).
  - 表格尺寸已有共享缓存，不代表布局准备没有重活（2026-11-06，#5730）：先调用 `renderMarkdown()` 再查尺寸缓存，仍会每次序列化整张表。按完整 `MarkdownContent` 保留当前表的源文本和尺寸；字号变化只重测，内容变化同步替换，不能只按宽度或行数判断。
- 代码高亮缓存超过上限时不能全表清空（2026-11-06，#5730）：用有界 LRU 保留仍在使用的块，键包含代码、语言和字号比例。300 项及 2 MiB 源文本 UTF-8 预算不是整个 `Text` 的内存上限；不把本来就跳过词法分析的 60,000 字节及以上纯文本块放进缓存，避免挤走昂贵的高亮热点。验证字号、内容、语言失效及冷项淘汰，不用帧率中位数代替持续达标。
- Custom `Layout` `proposal.width` can be `.infinity`; check `isFinite` before `Int(...)` conversion or crash (Lark card columns).
- `List` `Section` headers further dim hierarchical `.primary` / `.secondary`, graying card names and nearly hiding descriptions.
  Use explicit `Color.primary` / `Color.secondary` for normal colors (`MachineCard`).
- Empty Sections (collapsed to title) retain header padding and section spacing, fitting only five or six per screen. Make collapsed spaces ordinary rows
  with `listRowBackground(.clear)` and `listSectionSpacing(.custom(0))` (`ChatsView.spaceSection`).
- Do not place self-drawn card / pill rows in `insetGrouped`: group inset, row insets and header margins create four left edges for search, chips, headings and pills;
  the first untitled section adds ~35 pt blank space, and `DisclosureGroup` titles sit on group cards with children as separate inset pills (device feedback 2026-10-31).
  BeautifulUI Agents uses plain `List`, every row aligned to `Theme.Space.page`, headers as rows, idle spaces combined into a card through `TaskRow`'s `.list`.
- `BUIListRow` in native `List` doubles whitespace: its 8 pt vertical padding / minimum height plus `List` insets measured 77.7 pt,
  versus 52.3 pt connector and Liquid Glass rows (device feedback 2026-11-01). BeautifulUI `List` rows use
  `bareListRow()` (`SettingsView.swift`), `listRowInsets(.vertical, 0)`. Apply to row `Group`, not `Section` (which removes header spacing),
  or `NavigationLink` label (does not reach `List`). All groups use the same `SettingsRow`, not one remaining system group.
- Do not combine `.safeAreaBar(edge: .top)` quick filters with scroll-collapsing `.searchable` (`.automatic` drawer):
  on device TestFlight 35, collapsed search left filters at expanded coordinates, exposing ~120 pt hazy list ("huge space at top").
  Initially search used `.navigationBarDrawer(displayMode: .always)`; requested 2026-11-01 less crowding restored automatic, with phone filters as first scrolling `List` row
  (`ChatsView.filterBar`), unchanged above lists in wide split layouts.
  Collapse initially like Mail, as requested: no SwiftUI switch, so first appearance scrolls by search height (`SearchCloseWatcher.foldOnce`,
  directly setting UIKit contentOffset preserving x). Once only, not on Back; short lists need bottom contentInset sufficient to scroll that height.
  After programmatic collapse UIKit shortens top inset and may add the same delta to offset (iOS 27:168→116, requested−116 becomes−64),
  hiding the first row behind navigation controls. Wait for complete inset change, align to new top only when offset equals requested + inset delta; stop for user scrolling / search activation.
  No compensating padding; `AgentsListTests` inspect frames and pixels for gaps / overlap.
  Another active-search gap is `insetGrouped`'s first content y changing 0→35 with offset still−topInset, native first-section spacing.
  Phone filters explicitly use `Theme.Space.xs` top content margin; regular-width split stays system default, without relaxing test gap limits.
  The 42 pt search field sits at the top of its 52 pt bar, only a few points from custom header. `searchFieldBackgroundPositionAdjustment` and transforms on bar / field failed
  (device measured values set but system repositions on expansion, 2026-11-01). Only custom search can change it.
  Retest on device with repeated scroll / pull-down for blank top space. Another source is `ScrollView(.horizontal)` below.
- Top `ScrollView(.horizontal)` filters (safeAreaBar or VStack) may be mistaken for the page's main scroll view, shifting content down by
  status + navigation + search (~170 pt) and exposing hazy list. Always occurs in `NavigationSplitView` sidebar (Duo open / half-folded), and on ordinary iPhone
  when cold-restoring a chat with first list layout underneath it; Back shows TestFlight 35/38's huge top gap. **XCUITest frames remain unshifted**:
  UIKit moves content without SwiftUI layout / accessibility knowing. Earlier "cannot reproduce" checked only frames.
  Measure pixels (`AgentsListTests.drawn`, whether selected All actually draws at its frame). **No `ScrollView(.horizontal)` at page top under navigation**; narrow then wrap filters
  (`QuickFilterBar`, `ViewThatFits`). Covered by `testFilterBarHugsTheTop` / `testFilterBarHugsTheTopBehindARestoredChat`.
- `contentMargins` affects all descendant scroll views, including `safeAreaInset`: Persona header clearance applied after composer inset
  added ~66 pt to horizontal permission / thinking controls, blocking suggestion cards in all styles (2026-10-31, #114). Apply margins to the intended `ScrollView` before inset.
- UI-test Back must not fall back to `navigationBars.buttons.firstMatch`: once back at list, its first button is New Session, opening a sheet
  and making later searches fail as "missing row", seemingly stale demo data (2026-10-31 `DemoModeTests`). Wait for target page tappability, exclude page-owned toolbar buttons.
- `TextField(text:selection:)` reports selection while unfocused; restored drafts may select their middle. Its
  `String.Index` may belong to another string and out-of-range `utf16Offset(in:)` crashes. Insert text / file paths using selection only when focused, clamp to `endIndex` before conversion (`PathInsert`).
  UI-test drafts persist across runs; clear before checking composer text.
- Swift Charts lines need separate `series:` values per gap ("metric#segment"), since same-series points always connect.
  `AxisValueLabel` dates inherit app tint unless explicitly `.foregroundStyle(Color(.secondaryLabel))`.
  Points outside `chartXScale(domain:)` domain still draw past axes / cards; use `.chartPlotStyle { $0.clipped() }`.
- Avoid Swift Charts for a dozen mini-charts (2026-11-04, #390): per-chart scale / plot layout is expensive, adding ~130 ms for 12 machine sparklines on Mac;
  vectorized `LinePlot` / `AreaPlot` save nothing (cost per chart, not per point). Draw axis-free, noninteractive mini-charts in `Canvas` (`Sparkline`).
  - 2026-11-06，#5730：单系列的填充和描边不要各算一次单调插值路径；复用开放路径，关闭副本用于填充，仍保持所有填充先于所有描边。不同 gap 不合并，多系列没有填充时不建立额外缓存数组。
  - 同一优化模拟器构建、28 台机器、5 轮正式 glide：路径复用前后 custom hitch 中位数 8.3 → 4.2 ms/s，cadence 59.5 → 59.7。它不是 GPU 呈现帧率或全页面 60 FPS 验收；主机负载和离群长帧仍须注明。
- 机器详情的历史曲线不要在每次 `body` 重算时生成点 ID、gap 分段和最大值（2026-11-06，#5730）：响应解码和完整曲线准备放到 `Task.detached`，在 MainActor 一次发布。图表时间域随指标刷新推进，不随无关的会话推送或选点变化推进；保留完整历史、稳定点 ID 和 gap，不靠删点隐藏长帧。
  - `Equatable` 图表必须比较不可变的选点快照，不能只比较两个指向同一状态的 `Binding` 当前值。范围切换和并发刷新用请求序号及范围共同校验，防止旧响应覆盖新范围。
  - 预计算的 peak 为 `nil` 可以表示整个系列没有有效点，不是缓存缺失；不要用 `??` 再扫描原数组。
  - 解码及曲线准备搬走后，5 轮正式测量仍有一轮 459 ms 长帧（2026-11-07，#5730），不能用中位数 60 宣称已解决。详情仍按每个样本创建 `LineMark` / `AreaMark`；稠密历史改用批量 plot 时保留所有样本、原 `series` gap 标签、单调插值、填充和选点，旧系统保留逐点入口。它针对逐点成本，不推翻上面迷你图的每图布局成本结论；收益仍须实测。
  - SDK 27 的 plot 同时参与 View 和 ChartContent 重载时，`ForEach` 可能选 ViewBuilder，随后报条件内容的 ChartContent conformance 仅 iOS 27 可用。用显式 `@ChartContentBuilder`、返回 `some ChartContent` 的辅助函数约束两层循环，不为绕过重载问题抬高系统版本。
  - 批量 plot 复测：同口径 5 轮最差长帧 459.0 → 390.1 ms、最差 custom hitch 81.8 → 69.6 ms/s，页面高度及位移相同；仍有一轮明显停顿，另有一轮 33.3 ms 帧。主线及主机负载并未完全隔离，不能写成已经稳定 60 FPS。
- `Menu` label overlays / glass surfaces become part of the menu button: glass covers avatar + name, and overlay text is absent from XCUITest (button reports initial label only).
  Put temporary button hints in a sibling ZStack (`AssistantTopBar.face`).
- Floating-header fog must cover status bar: use GeometryReader `safeAreaInsets.top` and shift background upward; direct `ignoresSafeArea` computes insufficient height.
  Whole-page sidebar drag uses UIKit pan + `gestureRecognizerShouldBegin` to check direction and yield to horizontal children; SwiftUI DragGesture cannot yield this way.
- `Chip` `fixedSize` does not shrink: long labels in narrow two-column space cards widen cards beyond columns and push `LazyVGrid` offscreen / misalign rows (2026-10-31).
  Use `ViewThatFits` to remove secondary chips progressively, finally truncating the main label (`SlotCard.header`); verify `MOCK_LONG_SLOTS=1`.
- Do not put tens of thousands of lines into one UITextView (`MonoTextView` lays out all text, linear cost): `CodeLinesView` reuses labels only for visible rows,
  with fixed left line numbers;20k Go lines open ~0.3 s, scroll 60 fps. Split long MarkdownUI documents into lazy `MarkdownChunks`,
  without `.textSelection(.enabled)`: measured ~40% slower opening, entering-chunk frames ~40→60 ms (`docs/apple/ios.md` ("File viewer")).
  - 代码行只画可见部分，不代表全文准备已在后台（2026-11-07，#5730）：扩展代码预览曾在 `body` 的备用源和初次 `onChange` 各构造一次 `CodeDocument`，两次都复制全文和索引行。按文本和实际解析语言在后台准备，行号/高亮/Load More 属性只替换展示包装并复用文件及高亮器。
  - 取消展示任务不会中止 detached task 中的同步构造；保留最多两个最近输入的准备任务/结果，取消只阻止旧结果发布，快速返回同一输入不再重做。它是有界近期复用，不保证所有历史输入只算一次；响应发布仍检查取消、代次和最新输入，测试必须验证取消后的准备复用，不能只验证没显示旧内容。
- Video detection is not complete transfer (2026-11-03): old binary API capped 2 MiB, while MP 4 index may be at end, so a prefix remains unplayable. Bounded full download first restored playback, later authenticated Range + `AVAssetResourceLoader` reads tail index / target keyframes and caches sparse chunks. Sparse logical file length is not downloaded bytes. Pin versions across requests with `If-Match`, not `If-Range` (which falls back to full 200 after changes). Test long video for first playback before full download, actual `seek` ranges, continued playback and close cleanup; `seek` completion / instant clock jump do not prove frames decoded—check ranges after playback resumes.
- Message paths become tappable files only `if` actually present on that machine (`FilePresence` batches `GET …/stat`, uncertainty means no link). Previously other-machine paths (hub
  `~/.persona/…`) were tappable and only produced "Cannot open this file".

- Direction-selecting `UIPanGestureRecognizer`: when `gestureRecognizerShouldBegin` runs, `translation` is still 0 and `velocity` estimates only the last two samples
  (a 30° diagonal read as(1, 68), almost straight down), incorrectly yielding diagonals to the list. Record touch-down in a recognizer subclass and judge actual displacement
  (`DrawerPan.Recognizer`, 2026-10-31 "swiping does nothing"). Outer pan races the `ScrollView`: at 40° its vertical component crosses the scroll threshold first,
  so the outer recognizer is never asked (no logs, intermittent tests); horizontal code blocks also steal edge drags for bounce. Require scroll pans to await the outer decision
  (`gestureRecognizer(_:shouldBeRequiredToFailBy:)`); the outer decides within the same few points, without delaying scrolling.
- Do not toggle `.toolbarVisibility(_, for: .tabBar)` during finger-following drag: safe-area changes relayout the whole page (navigation, composer, UITextFields) on the first frame
  (Persona sidebar 20–40 ms). Fade instead: SwiftUI refresh makes UIKit reset `tabBar.alpha` to 1,
  so fade the wrapper containing only the tab bar (`AssistantDrawer.applyToUIKit`).
- SwiftUI `.accessibilityHidden(true)` and `opacity(0)` do not hide UIKit TextFields, ScrollViews or rows from XCUITest / VoiceOver.
  Keep large retained hidden UI (Persona sidebar) in a separate `UIHostingController` and hide through UIKit `isHidden`.
  SwiftUI resets the representable view's frame / transform / alpha / `isHidden` every update; modify an inner layer instead
  (`DrawerSidebarHost.Container`), transferring environment via `.environment(\.self, context.environment)`.
  But do not trust that copy for appearance (2026-11-06, #4496): it is only as fresh as the last `updateUIViewController`, and SwiftUI propagates the same stale value
  as controller traits. iOS switches window appearance in background for app-switcher snapshots (env dark→light→dark logs);
  the final update can be missed, leaving the sidebar white `Color.primary` text on light background while glass / red badges remain normal.
  Use the containing window's `traitCollection.userInterfaceStyle` (set by `ColorSchemeChoice`), observe window trait changes and rerender,
  overriding copied `colorScheme` and host `overrideUserInterfaceStyle` consistently. `DrawerSidebarLookTests` guard it; `-showLookBlink <seconds>` simulates snapshot switching.
  `ChromeHeader` had the same trap (2026-11-02): expanded / collapsed layers in ZStack used opacity / `allowsHitTesting`,
  leaving both Back buttons in accessibility. Expanded content moved into clipping after collapse; its Back was over the status bar, deemed hittable but tapping did not return.
  `accessibilityHidden` on layers or plain buttons still failed. Remove invisible layers from the tree (do not build collapsed strip before halfway;
  expanded layer uses `.hidden()` after fading, retaining layout measurement). Identity changes dismiss sheets / menus, so avoid automatic collapse while anything is presented
  (`ChatView.somethingPresented`, `ChatHeaderPresentationTests`).
- Do not measure per-frame drag / animation main-thread cost under XCUITest: accessibility adds substantial SwiftUI work each frame. Script display-frame-driven execution of the same path in-app,
  launched with `simctl launch` (`DrawerAutoDrag`, `docs/operations/testing.md` ("Sidebar dragging")).
- System `.swipeActions` require `List`, not `ScrollView` + `LazyVStack` (2026-11-01 sidebar Archive). Preserve appearance when switching:
  `listStyle(.plain)`, `listRowInsets`, hidden separators, `listRowBackground(.clear)`, `scrollContentBackground(.hidden)`,
  defaultMinListRowHeight 0. **A standalone `Divider()` row draws vertically**; wrap in `VStack`.
- Left-drag closes an open drawer, but row swipes must win. `DrawerPan` makes `UICollectionView` pan wait through `shouldBeRequiredToFailBy`,
  then `gestureRecognizerShouldBegin` yields if the touched cell has `SwipeRowMark` (empty UIView marker).
- A group of icon / text / count `HStack` children with only outer `.accessibilityLabel` gives every child that label, repeating it
  (idle row read "Expand N idle agents" three times on iOS / Mac, #405, 2026-11-02). First combine with `.accessibilityElement(children: .ignore)`,
  then label; wrapping in `Button` remains tappable.

- Hidden `ShellPageSlide` pages cannot leave accessibility through `accessibilityHidden` alone: Menu switchers, glass buttons
  and `UICollectionView` lists are UIKit. After assistant push / edge Back, the whole hidden Agents page reenters the tree above assistant,
  invisible but "hittable"; VoiceOver selects it and UI tests find its first `shell-agents`, whose center hits no segment (2026-11-01,
  four `ShellSwitchTests`). Hidden native header now leaves the tree (`ShellBarAtRest(page:)`: omitted while the bar rests on another page, returned transparent at slide start);
  lists still leak. Identify shown page using `probe.shell`, not control exists / isHittable.
- Moving filters into `List`'s first row (56b 2ead, to collapse search) turns them into UICollectionViewCell. Gesture rules yielding to row drags
  (`DrawerPan.onRow`, right-swipes pin / interrupt / mark seen) then swallow filter-bar swipes back to assistant; the fix was to mark rows lacking their own
  right-swipe (`DrawerPan.NoRowSwipe`). Since #1244 Agents' rows have no swipe at all (a ⋯ holds their actions), and both are gone: a drag along any row pages.

- Do not retain two sources and synchronize with `.onChange`. New shell initially duplicated current page in `AppModel.selectedTab` and bar position,
  sliding from `onChange(of: selectedTab)`. SwiftUI sometimes never reevaluated body (trace showed tab change with no body / onChange),
  leaving position unchanged and sources diverged, breaking later taps (2026-11-02, `docs/apple/shell.md`). React at the write site
  (`didSet` calls `tabRouter`), or retain one shared source.
- A selected segment that turns into a Menu on a second tap becomes a different control if state disagrees with pixels (empty menu), appearing unresponsive.
  Its deciding state must be exactly what is displayed (new shell: only the page where the bar rests is a menu).

- Do not insert recurring strips through `if … { view.transition(.opacity) }` while UIKit animates root `additionalSafeAreaInsets`:
  from the second insertion, the view can remain transparent despite body evaluation and reserved layout, invisible to VoiceOver too (2026-11-02 shell status row).
  Keep it mounted, change only opacity, using previous content while collapsed.
- Removing conditionally inserted tappable views (`if moving { replacement header }`) during a held touch loses that touch:
  no `Button` action, and the identical underlying button never receives it because touch began on the removed view. Shell settling mid-tap does this,
  losing nearly every other rapid tap in slower BeautifulUI animations (2026-11-02,`testQuickTapsEndOnTheLast`). Wait for finger release before removing (`ShellPager.lingering`).
- Both shell pages remain built; hidden-page `onAppear` still runs. Launching on Agents "appeared" assistant chat, subscribing, marking seen,
  and running DEBUG switches despite no user view (2026-11-02, a test lane logged repeated `appeared()` and duplicate sends). Side-effectful appearance is not visibility:
  do not build unselected pages (`ShellState.assistantBuilt`) or use `.onTabVisibility`. Do not run one-shot DEBUG actions on `onAppear` either.

- SwiftUI owns `UINavigationController` beneath `NavigationStack` and `UICollectionView` beneath `List`: UIKit changes to
  `additionalSafeAreaInsets`, `view.transform`, `contentInsetAdjustmentBehavior` / `contentInset` are reset on next layout, or stored positions restore offsets
  (150–180 pt jumps). `ignoresSafeArea` / negative `safeAreaPadding` instead change container frames, oscillating between 0 and 90+ during transitions (2026-11-02, #413,
  search flicker while status row collapses; six approaches' per-frame data in issue). Measure which layer moves with probes such as `probe.agents-scroll` before choosing a fix.

## Swift and compilation

- Closure properties must not share the name of the method invoking them: `func close(_ s: Session) { close(s) }` resolves to recursive method invocation and stack-overflows on **Close Agent**; `rename` has the same trap.
  Use `closeHandler` / `renameHandler` fields, retain public methods, and inspect `function call causes an infinite recursion` warnings.
- Cancelled requests / tasks (page exit, switching) throw URLError.cancelled or CancellationError; filter before showing errors (`Error.shown`).
- With class-valued dictionaries (`CGMutablePath`), `dict[k, default: X].method()` only reads and never inserts the default; modifications are lost (entire terminal border lines disappeared).
- Trailing closures match the first closure parameter positionally. With two optional closures (`SpaceMenuItems.rename`, `open`),
  inserting a new one first silently reroutes existing `SpaceMenuItems(group:) { rename }` calls. Add optional closures last and label call sites.
- In Chinese locale, SwiftUI `.dateTime.hour(.twoDigits(amPM: .omitted))` uses 12-hour time, showing 17:00 as 05:00; for 24-hour times use fixed `HH:mm` (`AssistantScheduleText.hm`).
- Types in `apple/Shared` default to MainActor isolation; new pure-data enums used by `nonisolated` structs need `nonisolated`, otherwise isolated-conformance errors result.
  PersonaTests has no default isolation: mark test structs using app static methods, properties or key paths `@MainActor` (like `ChatStoreTests`),
  rather than removing app isolation for tests. Run all PersonaTests before submitting; one noncompiling file blocks the target (2026-10-31 `KnowledgeTests`).
- The app defaults to MainActor isolation. System-owned callbacks on system queues (`PHPhotoLibrary.performChanges`, `SFSpeechRecognizer.requestAuthorization`,
  `fetchReminders`, `PHImageManager`, `WCSession.sendMessage` reply / error…) without SDK `Sendable` annotation compile without warnings but inherit main isolation,
  crashing when runtime isolation checks fail (`dispatch_assert_queue_fail`, every image save on 2026-10-31). Always write `{ @Sendable … in`; `BackgroundCallbackTests` checks source.
  SDK `NS_SWIFT_SENDABLE` callbacks (HealthKit, URLSession) and main-thread delegates need no change. Actual system-feature UI tests (`PhotoSaveTests`) expose it.
- For custom concurrency helpers delivering results to main, mark the function `@MainActor` and use `withTaskGroup(…, isolation: MainActor.shared)`.
  An `isolation: isolated (any Actor)? = #isolation` parameter alone did not reliably keep task groups on caller actor: `HubFanOut.run`
  callbacks ran in background, and reading `AppModel` triggered isolation checks, crashing every launch (`dispatch_assert_queue_fail`,
  2026-11-06, introduced #4725, fixed #4777). Unit callbacks only modified locals and all passed. Test such helpers from
  `Task.detached`, asserting `Thread.isMainThread` or `MainActor.assertIsolated()` in callback (`HubFanOutTests`), then really launch on simulator.
- In `[String: Any]` literals, `arr.last ?? 0` on Swift 6.2 / Xcode 26.0 can resolve `arr.last` as `last(where:)`, storing a closure;
  `JSONSerialization` throws `Invalid type in JSON write (__SwiftValue)` and crashes. `FrameStats.stop` crashed every 26.0 build,
  preventing any `-showPagePerf` round (2026-11-03, #674), unlike #343's 27.x measurements. First assign `Any`-bound values to typed constants
  (`let busyMax: Double = busy.last ?? 0`); `FrameStatsTests` validates output JSON.
- With `SWIFT_APPROACHABLE_CONCURRENCY`, `APIClient` is `nonisolated` but async methods run on caller actor,
  so view `await api.get(…)` decodes JSON on main. Lark pushes refetch 40 card-bearing messages; main decoding occupied 43 samples
  (2026-11-03, #674 P-19). Use `getDecodedOffMain` for large responses, or fetch `Data` then decode in `Task.detached` as `items` does; results must be `Sendable`.
  - 单机详情直接从页面请求指标，不能因为概览的 fan-out 已在后台就认定单机详情也在后台（2026-11-06，#5730）；分别核对实际调用者。Xcode 27.1 的 `Thread.isMainThread` 带 `noasync`，测试后台准备时在同步 `nonisolated` 探针里读取，再从 detached task 调用，不在异步闭包直接读取。
### Minimum compatible compiler

- **Minimum compiler remains Xcode 26.0 (Swift 6.2)** (2026-11-02): this and many development machines still use 26.0; `main` must compile there for iOS, Mac, watch and extensions. Release machine uses Xcode 27.1 / Swift 6.4.0, which proves nothing about 26.0.
  Three failures: #216 `isDynamicIslandLimitedInWidth` (iOS 27 SDK, fixed #217), `toolbarVerticalEdge` (iOS 27.1 SDK), #164 `ShowsSnippetView` / `view:` (fixed #430).
- **Xcode 27.1 crashes on a nonisolated async Objective-C method that takes a bridged Foundation struct** (2026-11-08, #1378): under the project's `NonisolatedNonsendingByDefault` (`SWIFT_APPROACHABLE_CONCURRENCY`), Swift 6.4's SILGen crashes emitting the Objective-C thunk when a parameter is `URLRequest`, `URL`, `Date`, `UUID`, `IndexPath`… (an async `urlSession(_:task:willPerformHTTPRedirection:newRequest:)` or `willBeginDelayedRequest`). `String` / `Data` parameters, `@MainActor` and `@concurrent` methods compile, and 26.0 compiles all of it: CI stayed green while 27.1 could not build main, twice (`PushRelay`, then `LocalProxies`). Use the completion-handler form; `scripts/test/test_apple_compiler_traps.py` fails on the crashing ones.
- **Members of an extension of a nonisolated type in a main-actor-default file** (#1378): Swift 6.2 makes them nonisolated, 6.4 the main actor's, so a call from nonisolated code (a test's `#expect`) compiles in CI and fails on 27.1. Mark them `nonisolated`.
- Guard new-SDK symbols with `#if compiler(>=6.4)` (27.1 is 6.4.0;26.4–26.6 has Swift 6.3 but still iOS 26 SDK, so 6.3 guards fail there, #453), with old behavior under `#else`, not empty functionality. `#available` / `@available` checks only govern runtime and cannot resolve missing compile-time symbols.
- `#if compiler(>=6.4)` identifies only the iOS 27 SDK. **It cannot guard symbols introduced in 26.x minor SDK releases such as 26.1–26.4**: doing so disables the feature in every Xcode 26 build (CarPlay voice UI `CPVoiceControlState.actionButtons` comes from the 26.4 SDK; this guard once left Xcode 26 builds without buttons in the car).
  Access Objective-C properties / methods at runtime instead: check `responds(to: NSSelectorFromString("setXxx:"))`, then use `setValue(_:forKey:)`; add a source-scanning regression test (`CarPlayVoiceButtonsTests`).
- For new APIs, build with 26.0 before merge when available (`eval "$(python3 scripts/persona_config.py env)"` for `DEVELOPER_DIR`). **Without that environment, no mandatory build or merge / release blockage** (explicitly requested 2026-11-03): build / test with available tools, `state` actual version and "26.0 not verified" in PR without repeated exemptions; retain compatibility targets and compile guards.
- Raising minimum is a product decision: ask the user and update `CLAUDE.md`'s rule and this section.

- Release machine Swift 6.3.3 differs from other machines and may report "unable to type-check this expression in reasonable time":
  multi-statement closures in long modifier chains (`.task { … }`) are inferred with the entire chain, with shifting error lines. Extract closure methods and computed ids (`ChatView.prewarmRows`).
- Xcode 26.3 / Swift 6.2.4 also times out on long array-concatenated fixtures (`RFBClientTests.sessionFollowsDesktopSize`).
  Build `[UInt8]` arrays by protocol header, rectangle fields and encoding segments, preserving bytes / assertions; do not exclude tests to bypass original scheme builds.
- Swift evaluates arguments left-to-right: in `emit(s, i, word(…, &i, …))`, `emit` receives i before `word` changes it (file-viewer `println!` lost its !).
  Store results first when one argument changes a variable used by another.
- Build-product Info.plist script stages (Settings › About › Build commit, `project.yml` `Stamp the commit`) must declare `$(TARGET_BUILD_DIR)/$(INFOPLIST_PATH)`
  as input, or Xcode may run the script then rewrite the plist, losing values (bundle-id changes trigger this, 2026-10-31). Reading `.git` requires disabling `ENABLE_USER_SCRIPT_SANDBOXING` for this target.

## Recording and WatchConnectivity

- Receiving `WCSessionFile` must move audio synchronously before delegate return; system deletes the temporary file afterwards. Transport success does not mean index persistence: phone sends application acknowledgement after atomic index write, and watch deletes original only after acknowledgement.
- Segment-queue callbacks reaching main are delayed. At stop, do not count only main-registered segments: stop `engine`, synchronously collect all finalized segments, then write final total; stopping exactly at a boundary still needs a separate end message.
- watchOS Picker in compact VStack can flatten to a thin line, missed by compile / accessibility-existence checks. Use single-line toggle buttons for a few recording languages and inspect watch screenshots.
- Recording and composer dictation share microphone ownership; failed / late stop callbacks must not close another user's audio session. Recording deletion retains tombstones against late watch transfers resurrecting items.
- The watch app keeps running in the background while it records (`UIBackgroundModes: audio`, 2026-11-02), and the phone's summaries still arrive over WatchConnectivity then, so a page keyed on `model.lastUpdate` or a session's `rev` reloads in the background too. watchOS kills a background app over its CPU budget (`CAROUSEL 0xc51bad01`, #6256): gate page reloads on `scenePhase == .active`, and keep only what the page draws (`WatchMessage`, the newest few) instead of the hub's whole `Item` page, whose synthesized `Hashable` hashes nested tool input on every assignment.

## System capabilities and page conventions

- Tasks on views that may draw nothing never run (2026-11-04, #1517): Group with empty ForEach or conditional empty content leaves no child
  for modifiers, so loading never starts and list remains empty. Attach loading to an always-present page view (or page-owned model triggered by page task);
  capability-dependent tasks include `features` in `task(id:)`, since direct launch routes can appear before hello.

- HealthKit entitlement requires `NSHealthUpdateUsageDescription` even for read-only access, or upload fails 90683.
- WatchConnectivity updateApplicationContext before activation completion is discarded; push pairing after activationDidComplete.
- Avoid async `UNUserNotificationCenterDelegate` didReceive / willPresent: completion returns on background executor and UIKit crashes on notification entry. Use completion-handler variants and finish inside `DispatchQueue.main.async`.
- App startup setNotificationCategories(static set) must not overwrite dynamic categories. Register before NSE delivery and foreground presentation, merging static / registered extension categories by identifier.
  Copy notification content into read-only snapshots before async registration; obtain SDK center inside the registration actor, create static categories with a Sendable factory there, never transfer non-Sendable center / category sets from caller.
  NSE normal completion and expiry claim the same completion through a lock, preventing duplicate delivery (#1862).
- Notification actions execute before rendering: render may return new `state` and overwrite original notification `state`. AppModel system entry stores a claim flag, executes once with original `state`, then renders result or loads read-only. Dedup distinguishes hub, notification id and nullable button id; default tap is not a button named `open`.
  Root temporarily shows the full extension page, avoiding settings-sheet presentation competition. `simctl push` does not run NSE; local tests do not prove first real-device APNs delivery (#1862).
- Local-notification fixtures register dynamic categories in foreground and install background listeners before signaling readiness to a dedicated mock; tests wait for ready before Home. Do not guess asynchronous completion or depend on long initialization after backgrounding.
  SpringBoard accessibility labels may combine title / body; match titles with `CONTAINS`, not whole-label equality.
  New system reply editors are `TextView`, as is notification body; wait for Send then choose the lowest editor, not a fixed `TextField` query (#1862).
- Root swapping to a full extension page dismantles `ShellWindowHooks` and closes its toast window; enable `ToastOverlay` in RootView during system extension pages. Writing `AppModel.toast` alone does not prove visibility; foreground-action regression asserts the toast actually appears (#1862).
- DEBUG argument names must not match Swift identifiers: enum cases enter Release reflection `strings` and trigger `release.sh` false positives
  (`Tab.workbench` collided with `-workbench`, renamed `-openWorkbench`). Use `open…` / `show…` names unlike type members and register in `release.sh`.
  Stored properties also collide (2026-10-31, `AgentsListMenu.Actions`.openMonitor vs `-openMonitor` stopped TestFlight 50). For new properties,
  first build locally `xcodebuild build -configuration Release -destination generic/platform=iOS CODE_SIGNING_ALLOWED=NO` and run `release.sh` `strings` checks on `Release-iphoneos/Persona.app/Persona`.
  It happened again with `WorkspaceSidebarCard.openAgent` vs `-openAgent` (2026-11-09, #1675 stopped TestFlight 214, renamed `selectAgent` in #2179):
  name closure properties `select…` / `on…`, not `open…` / `show…`. `@State` / `@Binding` / `@Environment` properties are stored as `_name` and never collide.
  A quick check against the source: the list in `release.sh` into a file, then
  `git grep -hoE '(var|let|case)[[:space:]]+[A-Za-z_][A-Za-z0-9_]*' -- 'apple/*.swift' ':!apple/PersonaTests' ':!apple/PersonaUITests' ':!apple/Persona/Debug' | awk '{print $2}' | sort -u | grep -xF -f <list>`
  (each hit that is not a property wrapper or a Mac-only case is a collision).
- Launch-argument defaults are `strings` (`-dev.compact YES` is "YES"). Read booleans / numbers with `bool(forKey:)` / `integer(forKey:)` / `double(forKey:)` like AppStorage,
  not `object(forKey:) as? Bool`, which silently falls back (#1473 `StoredValue` broke `-dev.compact YES`; `CompactListShots` showed cards instead of compact rows, #2147).
- Live Activities (`docs/apple/live-activity.md`): ActivityKit uses default JSONDecoder for `content-state`; `Date` values start 2001; pass Unix-second numbers.
  Lock / expanded height ~160 pt; overflow silently clipped. `simctl io screenshot` captures omit Dynamic Island / lock activities (use `LiveActivityShots`);
  `simctl push` does not support activity pushes, but simulator receives real APNs sandbox pushes (`TestManualLiveActivityPush`).
  Repeating symbol rotate / pulse animations do not move (pixel verified); do not design rotating gears.
  Island numbers (TPM, tokens, load) consume push budget: hub sends only displayed fields, two significant figures, at most once per minute per device.
  Sharing with another app on 18 Pro gives two width-limited 28 pt compact activities (second clipped);17 Pro and others use minimal.
  Only iOS 27 `isDynamicIslandLimitedInWidth` reports width limitation; app cannot know presentation or other apps. Compact / minimal provide no width, making `ViewThatFits` useless.
  Screenshot both models (`scripts/app/island_peer.sh` + `LiveActivityShots`, `docs/apple/live-activity.md` ("Sharing the Dynamic Island with other apps")).
- Missing shared keychain groups (push-relay `.push` private key; avatar `.look` also used builds 92–103) silently fall back, while simulator always works:
  build 92 extension signatures had no groups (unsigned archive + ad-hoc signing without entitlements, empty `$(AppIdentifierPrefix)`), always default avatar / system blue.
  Use `$(DEVELOPMENT_TEAM).` prefix in plist; after entitlement / signing changes run `scripts/release/entitlements.py check` on exported ipa (`docs/operations/release.md` ("entitlements")).
- Simulator Live Activity rendering is insufficient (2026-11-02, build 98 signing fixed but device still default, even built-in avatar; `docs/apple/live-activity.md` ("Why build 98 was still wrong")):
  - Put rendered data in ContentState and oversized images in App Group files. **Live Activity extensions cannot read / write keychain on device**:
    build 103 app shared-group write / read succeeded, extension never wrote one diagnostic (simulator both work; Apple only documents its separate sandbox).
    Unchanged-content updates may not redraw; content must really change.
  - Register App Group on developer website and add to App ID; API keys cannot do it (no endpoint, `-allowProvisioningUpdates` export does not create groups).
    Adding unregistered entitlements fails export (`docs/operations/release.md` ("App Group")).
  - Simulator ignores activity image limits. Images cannot exceed presentation size: `UIImage(data:)` decodes 1×, 78 px becomes 78 pt. Supply display scale (`FleetFaces`). Some third-party libraries enforce 4 KB per image (not documented by Apple); keychain storage followed it and blurred images. App Group files now store each presentation at 3×, high-quality resizing from original (#349).
  - Fixtures need real complexity: solid placeholders are hundreds of bytes and hit no limits. Test real avatar packages locally, not in repo.
  - Only extension reports reveal device reads: `category:island` logs and diagnostics opened by long-pressing Settings › Notifications › Dynamic Island preview (`docs/apple/live-activity.md` ("Diagnostics")).
- Machine counts / lists exclude assistant headless sessions from Chat tab (`MachineDetailView.agents`, consistent with Agents): they run on hub machine,
  and old Server card Running counted them too.
- Demo mode (`docs/apple/ios.md` ("Demo mode")) lives beneath `APIClient` / `LiveConnection` (`DemoHub`), never page `if demo` branches. Missing new APIs return 404 "No such data in demo mode";
  add `DemoHub` responses with APIs and run `DemoModeTests` for `DemoHub miss` logs.
  Regenerate `scripts/mock/make_demo_data.py` after `scripts/mock/mock_hub.py` changes. Computer / Lark / email operations never execute for real (`DemoText.notSent`).
- Engine options come from hub `engines`, preserving names / `note`. Clients must not filter Orca / future `engines` by fixed enums or discard detection for unknown current `engine`.
  Known `engines` allow offline configuration; unknown ones require hub `available: true`. Regenerated demo data must retain only task-specific changes, not unrelated Inbox / scheduled tasks.
- Slow / offline networking shows no banner; title becomes spinner + Waiting for network / Connecting… / Updating… (`docs/apple/ios.md` ("Connection status")). `.toolbar(removing: .title)`
  also removes `.principal`. Untitled pages needing `state` use an ordinarily empty `.principal` instead; large-title pages also leave it empty normally to avoid duplicate titles.
  Slow-link detection counts only timely pongs (late pongs are slow), or title flashes each ping.
- Avatars (`docs/apple/persona.md`) may be third-party characters: art / generated files stay in hub `~/.persona/personas`, outside repo / app bundle.
  Avatar, theme-color and style changes rebuild the entire tree by `AppModel.look`. `@State` navigation paths disappear; settings paths belong to `AppModel.settingsPath`,
  keeping Settings › Appearance › Avatar / Theme color / Interface style / Layout `open` after selection (2026-11-01, `PersonaPickerTests` / `LookPickerTests`).
  New-shell settings sheet uses `AppModel.shellSettings`; RootView.id(look) does not rebuild already-presented sheets, leaving input-free samples stale, so content also uses `.id(model.look)` (`LookPickerTests.testShellSheetStaysOnAppearance` compares bubble pixels). Apply elsewhere for look-changing sheets / pages. DEBUG once-`open` flags (`-openAppearance` etc.) also cannot be `@State`, whose reset reroutes users back to startup page; `SettingsView.debugOpened` is static, once per launch.
- Three appearance choices are independent (requested 2026-10-31): avatar does not change colors, theme color does not change components, style does not change primary color. Previously avatar colors overlaid styles (`StyleSpec.tinted`),
  replacing Clay purple on avatar selection. Classify additions first: drawing belongs to style, color to theme (`docs/apple/ios.md` ("Design language")).

- Successful manual pairing replaces RootView's root hosting the pairing sheet; the old sheet must not dismiss, or the new engine page can be visible but untappable (XCUITest hit point−1).
  Explicit dismissal is needed only if previously paired, no engine step entered, and appearance root identity unchanged, so the original host remains.
- **Global caches keyed only by item id cross sessions** (2026-11-02, #372): `RowMemo` (user / assistant parsing) and `ToolTextMemo.input` are shared across chats,
  previously relying on unchanged text per `item.id`. Claude UUID line ids are globally unique, but DSH / pi `<seq>#<block>`
  and Codex fallback `line<seq>#n` are session-local. Two DSH branches split at the same seq / id: after iPad opened one,
  the other showed its message (web has no such cache, server pushes / pages were correct). Keys now include machine/session + id (`MemoScope`),
  passed by `rowMemoScope` environment at outer body (including sheets), with identical prewarm keys. Apply this to every new id cache.
- Alternate icons cannot be validated on Xcode 26.0 + iOS 26.0 simulator (measured 2026-11-03, master and changed branch): `supportsAlternateIcons` false,
  no App icon row in Appearance, direct `-openAppearance icon` taps do nothing, `AppIconTests` fails line 44,
  despite `CFBundleAlternateIcons` in plist and icon sets in Assets.car. #313 used another environment.26.0 can test grid / previews only; switching needs newer runtime or device.
  Xcode 26.3 + iOS 26.3.1 simulator also fails (2026-11-06, #3203): all four alternate icons exist in plist but `supportsAlternateIcons` false;
  `AppIconTests` skips when Appearance › Avatar subtitle is `unavailable`. Home-screen switching requires device; simulator can verify in-app marks,
  `AppIconChoiceTests` avatar-to-icon mapping and migrations with fake `AppIconSetting`.


- Optional home tabs must not own their settings sheet (#392): disabling the tab inside that sheet removes both. Reuse shell-owned settings, set destination path then present; disabling changes only home selection and leaves settings open. Regress entering settings from that tab, disabling itself and persistence across restart.
- Expanding home from two to three pages: release velocity only completes / cancels the current-direction drag. Halfway left from middle then flicking right must return to middle, not jump past origin to left. Page count, selected pill, edge resistance and drawer gestures share one count.
- After adding pages, recheck assumptions about absent neighbors (#2822): Agents Close left-swipe worked only because it was last and no left-drag page existed; enabling Messages stole every swipe for paging. Now left-drag yields only for rows with `SwipeRowMark`, not accidental end-page position.

## `APIClient.get` paths cannot contain `?` (2026-11-03, connector migration M 6)

`get("/api/v1/connectors?all=1")` sends `/api/v1/connectors%3Fall=1`: whole-path encoding escapes ?, hub returns 404, `try?` swallows it, leaving an empty list (Settings › Connectors missing GitHub / Messages, unseen by compile / unit tests).
Queries always use `get(_:query:)`. Found only by mock logs (`MISSING GET …%3F…`) during screenshot comparison.

## Multiple hubs (docs/protocol/multi-hub.md)

- `Session` requests always use `api(key.group)`, not selected-`group` `api` (#950): `sendPending` outbox send / retry once sent another `group`'s message to the selected hub. `api` is only for hub-level Persona / Inbox / settings pages.
- Client fields added to wire `Session` / `Machine` / `Interaction` / `SessionKey`: synthesized `Decodable` requires JSON keys even for nonoptional properties with defaults,
  breaking old data / hub JSON. Store optional `hubGroup: String?` with nonoptional computed `group`; `Encodable` omits nil,
  keeping default-`group` snapshots, drafts and pending-message JSON byte-identical.
- `Shared` data extensions default to MainActor too; nonisolated `APIClient` / decoding gets main-actor errors. Use `nonisolated extension`.
- All `apple/Shared` compiles into `PersonaWatch`; do not use app-only types there (`IntentFailure` in `Persona/Intents`).
  Build watch with `Shared` changes (`Persona` scheme includes it). Mac includes most `Persona` files; UIKit-only `UIScrollView.isTracking` needs `Mac/Compat`.
  #243 broke Mac until discovered #256; build `PersonaMac` after shared changes.
- Group names must not follow current socket route, which can choose another member each time and alternate "Company" / "Company 2". Prefer user name → first member `hub_name` → connected member.
- "One `group` stays pixel-identical": conditionally create entire `ToolbarItem` inside `.toolbar` (`if model.isMultiHub { ToolbarItem … }`),
  not an empty-content item that may reserve space / glass. `Machine.groupLabel` is nil for one `group` (`title == name`), avoiding per-page checks.
- Fake hubs compute relative times from their own startup, changing identical-interaction order. Filter Agents by hub before locating another `group`'s cards, not list positions (`MultiHubTests`).
- Keychain unreadable is not absent: `SecItemCopyMatching` returns `errSecInteractionNotAllowed` before first unlock (background wake / NSE),
  formerly treated like `errSecItemNotFound` by `Keychain.load`. Pairing data must distinguish absence from read failure; never write back or delete on failure.
  Migrate by writing new, reading back, then deleting old (`HubStore`, fake-keychain `HubStoreTests` for write / read failures).

## Temporary-code pairing gets stuck "Connecting" shortly afterwards (2026-11-05, #2766)

- Device-token replacement after pairing previously required entire socket `currentConfig` equality with pairing config and finding the link in the group captured before exchange.
  Candidate addresses, another mesh member, SSH tunnels, or hello regrouping during exchange left the socket using the temporary token; hub disconnects it after 2 minutes,
  then every reconnect 401 loops Connecting. Now replace by **token**: `adoptDeviceToken` records temporary→device token; all sockets still using the old token
  `renewCredential` at their existing address. `LiveConnection.credential` substitutes before every handshake, preventing old routed configs restoring the temporary token.
- Processing one pairing link twice (scan + system-camera link, repeated scan callbacks) caused second `pair()` to write the exchanged temporary token back and fail exchange 401.
  Already-exchanged links retain device token (`DeviceCredentialTrade.keepsDevice`).
- Handshake 401 (`URLSessionWebSocketTask.response` status) is not unreachable: `LiveConnection.rejected` shows Pairing expired and Pair again, slowing retries to 60 seconds (public auth failures trigger address blocking).
- Reproduce with `MOCK_PAIR_TOKEN=jp_…` fake hub, `-openWelcome YES -openPairLink <link> -openPairLinkTimes 2`; alternate-address case in `docs/operations/testing.md` ("Temporary-code pairing").

## Session cells are for display, not sending (2026-11-03, #624)

`model.session(key)` cells deliberately ignore `rev`, `updated_at` and last text to reduce redraws (`docs/apple/ios-state.md` ("Step 3")):
display is fine because those pages do not show them, but **sending** transmits stale values. Actions needing current sessions (handoff to Persona with last text, transcript fetch by `rev`)
use `model.latest(key)` or `allSessions`, never the cell.

## Task cards stuck on text from minutes earlier (2026-11-04, #1108)

`TasksStore.sessions` previously read `model.session(key)`, which ignores last text, so card text only changed with status. When Agents only run tools,
`last_text` itself stays fixed, looking stuck. **Views showing current speech / work use `model.sessionWithLine(key)`** (ignoring only `rev` / `updated_at`),
including `activity` here (`TaskNow`, `docs/assistant/unified-entry.md` ("Task cards")). `session(_:)` and `sameForItsPage` still ignore `activity` so chat pages do not redraw every step.

## Lark drafts and outbox (2026-11-03, #688)

- Swift 6.2 times out on long modifier chains; split chat content and navigation / lifecycle into independent `@ViewBuilder` properties. New-compiler success does not replace 26.0 validation.
  Recurred 2026-11-05: `ChatView.chatScroll()` timed out on 26.3 / Swift 6.2.4, also master (fixed incidentally #2420). Extract `chatRows` and split `let list = ScrollView {…}…` from `return list.…`; busyness worsens it, but it reproduced consistently when idle too.
- Do not name drafts by URL / token: group routing or device-token changes lose access. Use group and actual Lark user id, reread account before sending; migrate old account-less records after user ownership confirmation.
- Do not wrap "restore input" in inaccessible, endlessly accumulating backups. Let users explicitly clear local drafts; disconnecting a group invalidates memory queues before deleting files, and late request completions must not recreate directories.
- Unlock the composer immediately after local commit, not in defer awaiting networking; another send may already have started, and old defer incorrectly unlocks that transaction.
- DEBUG attachment injection follows async draft restore or restored snapshots overwrite injected attachments. Chat reading can run parallel to account confirmation, avoiding first-screen account waits.

## Chat list blanks after collapsing work, fixed only by killing the app (2026-11-03, #686, #754)

A new Persona chat fits one screen collapsed. While a turn runs, expand work, inspect tool output, then collapse by its header: content blanks, cannot drag, and switching away / back does not recover, only killing does.
Many mock / real-hub attempts failed until replaying that turn's real items (local `/tmp` only, outside repo), then reliably reproducing with `MOCK_ASSISTANT_RUN=alone`:

- **An inverted lazy list may draw nothing when content suddenly shrinks below one screen**: collapse removes 2000+ points; ScrollView internally retracts a high offset to latest,
  but `LazyVStack` still lays out at the old position (`probe.chat-rows`:3 rows, latest offset, 203 pt content, 0 visible rows). Short content cannot drag;
  frozen historical reading holds new rows, so no relayout arrives and blank persists. At latest, the next push fixes it, hiding the issue normally.
  Rebuild the list once (`listBuild`) when collapse leaves at most one screen: everything is visible, no position lost. `scrollTo` row / edge fails because it believes it is already there.
- **`ChatPin` limits by `contentSize`, updated one round late by SwiftUI**: newly expanded work still reports old height, clamps offset unchanged, then header stops moving
  and no correction arrives. The frame shows fading steps (apparently blank), then header moves offscreen. Retry placement during subsequent hold rounds if not yet reached.
- **Expand / collapse means the user takes control**, like dragging, so stop following latest. End-marker visibility / `onScrollGeometryChange` lag direct pin offsets one frame;
  do not reenable following during pin holds (previously next push after hold dragged to latest, losing header; recordings showed untouched oscillation).
- **Expansion at latest must not fade steps from transparent** (#754): new inverted rows first appear at latest, with pin restoring header next frame; that first frame contains only zero-opacity steps.
  Changing pin limit to header's far content edge (`contentSize` lags) left 1 failure in 12; directly showing steps at latest gave 12/12.
- **Expansion at latest must allow the header to rise** (2026-11-04): in taller-than-screen chats, pinning bottom work header in place grows all steps behind composer, appearing ineffective;
  expanding freezes incoming content, which lands together after collapse / latest return and jumps the page.
  `ChatPin.hold(rise:)` at latest lets header rise to below top bar, exposing steps; scrolling absorbs only excess. Middle / upper expansion stays fixed.
  XCUITest labels covered steps `isHittable`, so measure header rise (`RunningChatTests.testOpeningAtTheNewestShowsTheSteps`:0 pt before, 436 pt after).
- Diagnosis: inverted-row XCUITest coordinates are wrong and tap elsewhere (sidebar); tap top headers or use `-showFullOutput card` / `-openAllWork YES`.
  When alternating simulators, `simctl uninstall` on a shutdown simulator silently fails; `boot` first.

## SSH tunnels (2026-11-03, #891, docs/operations/network.md "Connecting to hub through SSH")

- **JSON launch values are swallowed**: `-key '{"a":1}'` begins with {, so UserDefaults argument domain parses it as plist and `string(forKey:)` returns nil. Base 64 structured values (`-seedSSHTunnel`).
- **SecureField on pairing triggers Save Password?** when leaving for SSH connection, blocking later taps (UI test "Neither element nor any descendant has keyboard focus"). Tokens are one-time credentials; `.textContentType(.oneTimeCode)` prevents it.
- **URLSessionWebSocketTask.sendPing needs an outstanding `receive()`**; ping without receive never yields pong (tests hung for minutes, falsely blaming tunnels). `LiveConnection` always receives and is unaffected.
- **Adding a Swift package triggers resolution failure**: `Revision … for swift-markdown-ui … does not match previously recorded value`. Upstream moved 2.4.1 tag while shared `~/Library/org.swift.swiftpm/security/fingerprints` retained old data; adding any package re-resolves all. Do not modify shared fingerprints: copy existing `Package.resolved` from another generated worktree into `Persona.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/`, merge the new pin, use `xcodebuild -disableAutomaticPackageResolution`. Restore after `xcodegen generate` regenerates the project.
- **swift-nio-ssh is ready only after `UserAuthSuccessEvent`**: TCP / handshake completion is insufficient for channels. Login result arrives as user event; failures through `errorCaught`. NIOSSH obscures host-key / public-key rejection; record verdict in your own delegate before classifying.
- **Both Glue sides share one event loop**: local child connection and SSH child channel write to each other; use single-thread `NIOTSEventLoopGroup`. listener `childChannelInitializer` prevents reads until its future completes, so it may await SSH dial / channel without losing initial request bytes.

## Chat embedded in a sheet centers an overheight column, pushing out its header (2026-11-03, #930)

`TaskDetailSheet` combines its header and `ChatView(style: .dev)`. With two tall pending cards, chat footer
(cards + composer) exceeds sheet height; `VStack` centers the entire column, clipping header / close above and composer below. frame(maxHeight:infinity,
alignment:top) does not prevent parent layout by content height. Use GeometryReader for sheet height, pin the entire column with frame(height:), top alignment and clipped(),
clipping only chat bottom when oversized. Apply this to all sheet-embedded pages; alignment alone is insufficient.

## Pending chat cards push the header offscreen (2026-11-03, #926)

- Three pending cards (BeautifulUI expanded,~320 pt each) made chat ~1300 pt on 956 pt screen: chat region 110 pt, header / Back y≈−144, untappable. `mainFooter` stacked uncapped cards above composer.
- **`VStack` reports content height when taller than proposal**, so parent centers it and both ends overflow (tree container `{{0, -157}, {440, 1299}}`).
  To lay out by available height, measure after `.frame(minHeight: .zero, maxHeight: .infinity)` (both ends force proposed height regardless of content; max alone still reports content), not `VStack` itself, avoiding circularity.
- Store height in narrowly observed `FooterRoom`, not `ChatView` `@State`; keyboard dragging changes it every frame.
- **`containerRelativeFrame` chooses nearest scroll view**: cards capping forms at 55% of page instead use half the new card `ScrollView` and clip buttons.
  Measure original container outside scrolling (`Color.clear.containerRelativeFrame(.vertical)` in outer `background`), pass `cardRoom` environment.
- Card `@State` holds partial answers: use one `ScrollView` in both fitting / overflowing cases, not separate branches that lose text when another card crosses the threshold.
- Pixel-identical fitting scroll region: use `.scrollDisabled` / `.scrollClipDisabled` while fitting to preserve shadows, add `contentMargins` only on overflow for cut-edge shadows. Compare before / after screenshots pixelwise (`PendingCardsTests.testOneCardAsBefore`, only clock / timer differ), asserting elements lie within card-region bounds.
- **A size derived from a measurement that contains the sized view needs both measurements exact** (2026-11-09, #1580). With the keyboard up, `PendingCards.cap`
  is the page less what the footer holds besides the cards (`room.footer - shown`). The footer was written only when it moved by 1 pt or more, `shown` on every change,
  so the difference ran up to a point stale: the cap cycled 301.1 → 302.0 → 302.9 → 302.0 pt, every frame, and the app froze once a tall card's field took the keyboard.
  Never put a dead band on a measurement that feeds back into its own size: write both sides of such a difference on every change, derive the size from
  the difference (it does not move with the size), and put the dead band against float noise on that difference only (`PendingCards.footerRest`).

## Images sent to Persona disappear in chat (2026-11-03, #987)

Data is present throughout (claude -p image block, transcript, hub items); app grouping loses it because claude -p writes **image before text**
(`[image, text]`), unlike Agents pastes. App divides turns by user text, assigning preceding images to previous work and hiding them;
image-bearing pending bubble is replaced on transcript arrival and image disappears. Reorder same-line user images after text (`ChatStore.picturesAfterTheirText`).
Do this **before** `rebuild` splits by last message; only `buildRows` is too late, with images already in built previous segments.
`MOCK_ECHO_SENT=1` echoes claude -p order. Fresh-install notification permission dialogs arrive unpredictably and hide underlying accessibility; handle with `addUIInterruptionMonitor`.

## Video compression progress: obtain states before Task (2026-11-03, #981)

`AVAssetExportSession` is not Sendable: `Task { for await s in session.states(updateInterval:) … }` fails Swift 6 with
"passing closure as a 'sending' parameter risks causing data races". Obtain `let states = session.states(updateInterval: 0.25)` outside Task,
then iterate only that sequence. Use picker `preferredItemEncoding: .current`, otherwise the system first transcodes HEVC (long 4K wait);
app chooses transcoding by size (`VideoPrep`, docs/protocol/protocol.md "Sending video").
## Telegram-style reply bubbles for quotes (2026-11-03, #986)

- **Bubble width follows its widest piece**: a quote with `frame(maxWidth: .infinity)` inside normal `VStack` expands the bubble to full row width.
  `WidestColumn` (`DesignSystem/QuoteViews.swift`) first finds maximum unconstrained ideal width capped by proposal, then lays out every piece at that width.
  Unquoted bubbles retain original `Text.padding.background`: using `WidestColumn` for unquoted long text widens bubbles by a few points, violating pixel-identical defaults.
- **Accessibility combine changes test-visible elements**:
  - Applying `.accessibilityElement(children: .combine)` to the entire quote bar removes × as a separate button and title as static text,
    breaking `LarkSendTests` `staticTexts["回复 我"]` ("Reply to me").
  - Label only the tappable quote inside a bubble (already one button); composer quote title, summary and × remain separate elements.
- **Lark drafts / outbox persist between simulator UI tests**:
  - Uncancelled previous replies become drafts and reopen the quote bar. Its summary matches original message text, so `label BEGINSWITH` finds it first and long-press fails.
  - Previous #fail messages remain below newly sent ones, shifting the "just above composer" expectation by 141 pt.
  - Tests clean up by cancelling replies; before investigating failures, `simctl uninstall` for clean data then compare with `main`.
- Background `gh pr checks` waiting scripts prematurely report completion with empty output on this machine: put the waiter in a separate file, write its own log and use perl `select` rather than `sleep`.

## Register Persona MCP tools in `ToolPresentation` (2026-11-03, #677)

Chat tool rows derive labels, parameters (`ToolPresentation.verb` / `subject`) and expanded detail (`ToolDetail`) by name; unknown tools use the raw name
and first input string. `ask_user` only has a `questions` array, so the parameter is empty but BeautifulUI `ToolChipRow` still draws a box,
producing "`ask_user` + empty gray box". After answering / collapsing the card, the question and chosen answer disappear from chat, looking unrendered.
Production interactions showed creation, push and answer 21 seconds later; app rendering was wrong. When adding assistant-only tools (`assistant/tools.go`),
also give them Chinese display names / meaningful parameters in `ToolPresentation`; model-facing English results need user-readable details (`AskUserDetail` reads selected answers from results).

## Trace "two pages share a component" from actual call sites (2026-11-04, #1079→#1091)

#1079 requested Agent dictation / stop buttons matching assistant; only buttons were extracted, assuming both composers used `ShellComposerBar`.
Actually `ChatView`.Composer uses it only with `shellChat` true, set only by `AssistantView` for its `ChatView`. Agents pushes the same
`ChatView` with false default and uses `ComposerRow` / `PromptComposer`. Buttons matched but placement did not (microphone outside pill), obvious in TestFlight.
Before claiming reuse, follow both callers through environment, style and platform-width branches; screenshot both pages side by side in identical state.

### "Any online Agent?" requires more than session state (#1110)

When a computer disconnects, hub marks sessions unknown, not offline; `AgentState.isLive` remains true. To-do details checking only state missed "No Agent currently", while web also required machine online.
`model.allSessions` spans all hub groups; machine id / project-name matching alone includes identical ids / names elsewhere. For any "Agent on this computer / project now" check, filter by `isOnTodosHub` or equivalent group first, then `model.machine(...)?.online`, then session state.

## Swipe-left quote: coexistence with scrolling and Back (2026-11-04, #1058)

- **SwiftUI `DragGesture` on list rows steals scrolling**; `simultaneousGesture` makes both move.
  - Use UIKit `UIPanGestureRecognizer` subclass `LeftDragRecognizer` via `UIGestureRecognizerRepresentable`.
  - In `touchesMoved`, after 10 pt without a leftward horizontal drag, set `state`.failed.
  - Override `shouldBeRequiredToFail(by:)`, returning true for `UIScrollView` pan so scrolling waits for failure.
  - Vertical movement fails within 10 pt, delaying scroll only that much. Only subclass methods change, not `delegate` (owned by SwiftUI).
- **Left-edge right-swipe Back** is rightward and fails the first direction test, unaffected.
- **UI-test edge Back** starts x 0 with `.fast`; master also rejects slow drags from 1%.
  - Search is collapsed on return (#1052), so detect Agents filter chips (`agents.filter.`), not `searchFields`.
- **Dragging a message while the list still scrolls** (initial follow-to-latest): first touch only stops scrolling, normal iOS behavior. Wait for settling before test drags.
- **Header collapses after idle seconds** (`chromeIdleSeconds`): new buttons must also appear in the narrow strip or users / tests cannot find them there.
- **Row gestures await child gestures**: `UIGestureRecognizerRepresentable` attaches through `.gesture`, not `.simultaneousGesture` (requires SwiftUI `Gesture` and does not compile).
  - SwiftUI waits for child gesture, message long-press `LongPressAt`, to fail. Drag sometimes loses and never starts (historical BeautifulUI tab-navigation run failed 2/4).
  - In subclass, do not call `require(toFail:)` for `UILongPressGestureRecognizer`; return false from `shouldRequireFailure(of:)` and `canBePrevented(by:)`. Left-drag exceeds allowed long-press movement, so both cannot succeed. Afterwards 4/4 passed.
  - Do not use `.slow` drags in tests: failing to move 10 pt within 0.35 s activates long-press first (people drag faster).
- **View-read switches must not be `@ObservationIgnored`**: `MessageMenu.swipeQuotes` enables only in chat `onAppear` / `bindMenu`.
  - Initially ignored, pre-appearance rows never redrew or attached gestures, failing 2–3/10.
  - Recognizer logs showed no touches in failures; observing the switch gave 10/10.

## Context compaction as a divider (2026-11-04, #1064)

Claude Code writes three things: `system/compact_boundary` (`compactMetadata.trigger` `manual` / `auto`; idle compaction also `auto`), a following `isCompactSummary` user line,
and for idle compaction an `informational` "Compacted while idle…". Hub emits two notices plus original information; app drew them separately and folded post-answer notices into work.
`ChatStore.compactions` now merges to `Item.compact`; `ChatLines` places post-answer compaction below answers and no longer wraps compaction-only segments as work; `CompactDivider` draws a fine line.
Trap: everything except the answer enters work, so later notices hide in collapsed work. Unit fixtures without tools missed it; include tools.
Another trap: `ChatPin` pins the header, so expanding a last-row divider grows under composer (half visible on short screens). After opening, `ChatPin.show` scrolls the bottom within list bounds while retaining header. Apply to any final row expanding downward.

## Theme changes leave Settings row icons system blue: UIKit window `tintColor` (2026-11-04, #1188)

Outside BeautifulUI, `SettingsRow` uses system `Label`, ignoring passed colors and following tint. RootView.tint(Theme.accent) only changes SwiftUI environment;
UIKit `List` icons sometimes use window.tintColor, whose unset default is asset AccentColor (system blue `#007AFF`).
Changing theme while settings sheet stays open through `look` rebuild, or entering / leaving child pages afterwards, makes icons blue while text buttons / × retain new color.
RootView now also updates every window `tintColor` on `look` changes (`WindowTint`); default Liquid Glass already uses system blue, preserving pixels.
Reproduce with Settings home visible and `scripts/app/personactl.py set-style --color coral`, checking icon pixels for `#007AFF`. Selecting through Theme color page then Back does not always reproduce on simulator.

## iOS 27 glass-effect views reject taps outside their shape (2026-11-03, #1005)

- BeautifulUI composer (`PromptComposer` → `PromptBar`) rendered +, /, @ and model menus but menu items did not respond;
  #519 `BeautifulStyleTests/testCommandMenuTakesTaps` failed iOS 27 simulator after passing 26.
- `PromptComposer` wrapped `all` `PromptBar` in surface, which becomes `glassEffect` in glass style; menus overflow a zero-height slot above the composer.
  iOS 27 `glassEffect` (`interactive` or not) prevents child taps beyond its shape. Tried footer `zIndex` and `interactive: false` without success; removing glass worked.
- Render glass behind through `.background { Color.clear.surface(…) }`, not around overflow. Pixel comparison showed only antialiasing-level composer differences.
- General rule: overflowing menus / bubbles stay outside `glassEffect` / surface wrappers; glass is background.

## Writing `@Observable` storage in body also makes body observe it (2026-11-04, #1204)

DEBUG launches often held main at 100%: RootView body recreated `ShellView()`; `@State` initializer `ShellState(...)` runs every init
(only first retained), calling pager.slide and DEBUG `ShellTrace.shared.note(...)`. `events.append` uses generated `_modify`,
which calls `access(keyPath:)` before mutation, registering a body dependency; mutation invalidates RootView, recreating `ShellView` indefinitely
(runtime CLI ping fails, XCUITest query timeout). `sample <pid>` shows the chain.
For write-only records (trace / counts), mark storage `@ObservationIgnored`, manually `access` in computed reads and `withMutation` around writes,
so calling bodies do not observe. `@State` initializer expressions must have no side effects; they run more than once.

## Gray outside sidebar card corners: overlay after mask is not masked (2026-11-04, #1247)

The pushed rounded page's dimming / tap-to-close `Color.black.opacity(0.06)` was an `.overlay` after `.mask { card }`.
Modifiers wrap in order; mask clips only preceding content, while later rectangular `.overlay` dims sidebar beyond `all` four corners.
Anything painted on a card (dim / outline) uses its own shape, `card.fill(…)` / `card.strokeBorder(…)`; touch shape may separately be `.contentShape(.rect)`.
Inspect magnified corner crops and compare outside-corner pixels with the same sidebar row (`-openAssistantSidebar YES|0.5`, light / dark), not just whole images.

## QuickLook Office files (2026-11-04, #1300)

- xlsx displays stored formula values only, never calculates. Missing cached values leave totals blank. Write calculated values when generating workbooks (`personad office`, `docs/assistant/office.md` ("Formula values")).
- QuickLook ignores docx borders only in table styles, `startOverride` numbering, and page breaks (squares). See `docs/assistant/office.md` ("Verification").

## Ask Persona shortcut unexpectedly quits: Release stripped reflection names (2026-11-04, #990)

- TestFlight Ask Persona with a fixed question reported unexpected app exit; Debug / simulator worked.
  Stack: AppIntents → `AskPersonaIntent.perform()` → `ask()` → `question.getter` → AppIntents `_assertionFailure`.
- Release anti-tampering set `SWIFT_REFLECTION_METADATA_LEVEL: without-names`. App Intents uses reflected property names (`_question`)
  to populate `@Parameter` from Siri / Shortcuts; absent names leave missing values, asserting on nonoptional reads. Every parameterized intent is affected
  (Ask Claude, Agent tasks, Dynamic Island allow / deny / approve). `Mirror(reflecting: self).children` labels are nil.
- Reproduce simulator with **Release** (Debug always retains names), install and run fixed-parameter shortcut through `xcrun simctl openurl … "shortcuts://run-shortcut?name=…"`;
  reports in `~/Library/Logs/DiagnosticReports/Persona-*.ips`, dSYM beside Release products, `atos -i` resolves lines.
- Keep Release reflection names (`all`). Stored property names enter binary and collide with DEBUG strings:
  renamed `-fromSeq`→`-showFromSeq`, `-openFile`→`-openFilePath`, `-openTerminal`→`-openTerminalPane`, `-openWork`→`-openAllWork`, sidebar `openMachines`→`onMachines`.
- Wrong turns: assuming missing argument (optional + `needsValueError` became "Internal error"), then blaming iOS 27 result cards
  (`ShowsSnippetView`); both reproduced under Release but neither caused it, changes reverted. First reproduce unexplained crashes in simulator Release, then symbolicate with dSYM.

## Hidden WKWebView snapshots: transparency, offsets, missing text (2026-11-04, #1295)

Formulas / diagrams (`MarkdownRenderer`, `docs/apple/ios.md` ("Formulas and diagrams")) render in invisible WKWebView then `takeSnapshot`. Several traps all looked like empty images:
- **View alpha enters snapshots**: `alpha = 0.01` made nearly transparent snapshots, invisible as template images. Use alpha 1, bottom window layer and offscreen frame. Web views outside any window cannot snapshot content.
- **Window attachment adds safe-area content inset**: page shifted ~80 pt, leaving only formula top within `getBoundingClientRect` crop. Set `scrollView.contentInsetAdjustmentBehavior = .never`.
- **`file://` cannot load bundled fonts**: CORS with opaque file origin blocked KaTeX woff 2; text invisible during font block (Mermaid system fonts worked). Inline fonts as data URLs in CSS (`font-src data:`). KaTeX relatively positions numerator / scripts outside their own boxes; crop the union of all descendant rectangles and avoid drawing against page top-left.
- **Offscreen Mac windows have no `requestAnimationFrame`**: waiting two frames before measuring never finishes, leaving spinners. Race frame waits against short `setTimeout`; snapshots use `afterScreenUpdates = false`.
- Version snapshot cache keys (`MarkdownRenderer.version`): bad images already in simulator caches persist unless key changes.

## Live-view invalid token: `URLComponents.fragment` double-encodes percent (2026-11-04, #1498)

- App live view said invalid token, hub logged 401; same page after web sign-in worked.
- Token was first `addingPercentEncoding(.alphanumerics)`, then assigned to `URLComponents.fragment`.
  - Non-`percentEncoded` setters encode again: `jd_`→`jd%5F`→`jd%255F`.
  - Page decodes once, receiving `jd%5F…`.
- Mock token `mock` contained no escapable characters, missing the issue.
- Going forward:
  - Preencoded values use `percentEncodedFragment` / `percentEncodedQuery`; raw values use `fragment` / `queryItems`, never both.
  - Leave only RFC 3986 unreserved `A–Z a–z 0–9 - . _ ~`; encode + (URLSearchParams reads raw + as space).
  - Test real-shaped `jd_…` tokens, decoding exactly as the page does and comparing.

## Extension system-surface identities and test boundaries (2026-11-08, #1147)

- Use a canonical encoding for AppEntity identities shared by the app, WidgetKit and Shortcuts. JSON is reversible, but its default encoder does not promise key order; set `sortedKeys` so another process/restart reconstructs exactly the same identifier.
- Public hub health can omit `features` even with Authorization. Absence is unknown, not an old hub: read authenticated `hello` before querying a system catalog. An explicit unsupported capability must not issue that catalog request.
- A DEBUG snapshot or unit host must not read/write the developer's real system-surface App Group credentials. Mac snapshots use their private temporary directory; unit hosts disable the default store and inject an explicit test store instead.
- A new embedded watch widget changes both the provisioning target set and its expected App Group. Update the separate signing-profile tests in the same change, including `--only` selection; narrow configuration tests do not cover those call counts or arguments. Run `scripts/release/test_asc_signing_profiles.py` and `scripts/test/test_script_layout.py` before pushing signing changes, including Python 3.9 annotation guards.
- WidgetKit does not guarantee a callback when the last widget is removed. Reconcile the complete configuration list on foreground entry, timeline generation and widget background wakes; retain the previous report on enumeration failure, and clear it with an explicit empty replacement when enumeration succeeds.

## All background Intent parameters are optional (2026-11-04, #1144)

- Nonoptional parameters such as `@Parameter var interactionID: String` in background intents (Live Activity buttons, Shortcuts, Control Center) can be missing when the system launches `perform()` after upgrades, extension / app metadata mismatch or failed restoration. Reading `wrappedValue` asserts in AppIntents (`EXC_BREAKPOINT (SIGTRAP) SIGNAL 5`), crashing the app.
- Declare optional parameters (`@Parameter var interactionID: String?`), supply parameterless `init()`, check missing values in `perform()` and return safely; never request parameters or throw uncaught errors from lock-screen / background buttons.

## `@Observable` `init`: do not read observable properties before all storage is initialized (2026-11-04, #1509)

- Calculating a stored property from another just-assigned observable property, or a method reading them, can cause compile errors or runtime access to uninitialized observation state.
- Calculate values in locals, then assign storage separately. Until every stored property is initialized, never read self's observable properties or invoke dependent methods.

## Task-card sub-Agent progress: supplementary rows include running only (2026-11-04, #1609)

- Show running subtasks below main progress in secondary text.
- Titles prefer `description`, then `type`, then Subtask; only `running == true`, at most 3 joined by separators, excess "and N more".
- Main `TaskNow` computation stays independent; sub-Agents supplement, never replace main speech / steps. `Session.sameApartFromTicks` compares `subagents` for live refresh.

## Question-card outcomes: friendly 200 `already` / 410 `status` mapping (2026-11-04, #1688)

- For finished questions, `POST /api/v1/interactions/{id}/resolve` returns idempotent 200 `already: true` with original `resolution` when answered; `expired`, withdrawn or handed-back questions return 410 `code: gone` with `status`.
- Parse `already` responses and show the original answer, not incomplete. Map 410 `status` `expired` / `cancelled` / `dismissed` to Chinese explanations, never raw English errors.
- Record finished interaction ids locally; `replaceInteractions` snapshots must not restore them, preventing race-driven resurrection.



## Isolated HTML preview: rules and loadHTMLString addresses (2026-11-04, #1297)

- **Content-rule `url-filter` does not support |**: `^(about|data):` fails whole-list compilation, silently through `try?`, falling back to Cannot isolate networking. Use one `ignore-previous-rules` per protocol.
- **`loadHTMLString(_, baseURL: nil)` uses `applewebdata://…`, not about:**: about-only rules / navigation checks block the page itself. Supply `about:blank` `baseURL` and allow both protocols.

## Token / key fields trigger Save Password? despite oneTimeCode (2026-11-04, #1512)

- Market tokens, extension secret fields and keys triggered Save Password? on Back / sheet close. Reliable iOS 27 simulator reproduction requires
  **an adjacent populated normal field** (market address, page note), interpreted as username with hidden field as password. A sheet with only a hidden key does not prompt.
- `.textContentType(.oneTimeCode)` is insufficient in SwiftUI `SecureField` and direct UIKit `UITextField` (same UI test).
  Earlier SSH pairing worked because navigation pushed another page while the field remained; here it leaves the hierarchy.
- What works: **the field is empty when it leaves**. Design-system `SecretField` (`DesignSystem/SecretEntry.swift`) is `UITextField` on iOS and clears itself before
  `resignFirstResponder` or leaving its window, retaining the bound value and showing Entered… . SwiftUI `.onChange(of: focus)` clearing is too late when Back is tapped with keyboard open.
- All tokens, API keys and other-service secrets (not app login passwords) use `SecretField`, never direct `SecureField`.
  `ExtensionMarketTests.testASecretFieldIsNotAPassword` / `testManageMarkets` use `PasswordPrompt.assertNone` for system Save Password? (title `保存密码？`, "Save Password?"; button `以后`, "Later").
- #1598 replaced remaining pairing tokens, provider API keys (manual / extension), Lark bot App Secret, connector hidden login fields (Telegram api_hash / two-factor password, channel tokens),
  permission hidden fields, terminal sudo passwords and email passwords / authorization codes. Email passwords belong to servers, entered once and encrypted by hub;
  storing in Passwords creates a Persona orphan, and Chinese providers often use client-specific codes, so system saving is also disabled.
  Intentionally retained: Lark encrypted-message unlock (gateway login / vault password) is entered every unlock, not stored by hub; `.textContentType(.password)` intentionally permits Passwords autofill.
  Its single-field sheet clears on Unlock and does not resemble login (`LarkNativeEncryptionFlowTests` also assert no prompt). Mac retains `SecureField` throughout.
  `SecretFieldTests` cover two provider keys, bot, email and terminal, plus final assertNone in `PermissionInputUITests`, `ChannelSettingsTests.testBindWithAToken`, `TelegramConnectorTests`, `EngineChoiceTests.testManualPairingShowsEngineStep`.
- UIKit `SecretField` does not inherit SwiftUI `.font` / `.multilineTextAlignment` automatically: `updateUIView` sets environment alignment (right-aligned form values), fixed `Theme.Font.uiBody`;
  `identifier` parameter supplies `accessibilityIdentifier`, queried as `app.secureTextFields[<id>]`.
- Device heuristics should match, but depend on iCloud Keychain / Passwords autofill setting; disabled devices never prompt. Not reproduced on real device.
- Actively edited secure fields appear completely blank in iOS screenshots, including placeholders, before and after; not a bug.

## Mark all read returns to unread: unacknowledged WS messages (2026-11-04, #1588)

- Mark all read initially cleared the list, then every item returned unread; no `session_views` rows existed, while individual HTTP / WS marking worked.
- `sendSeen` checked only `state == .connected`, not `stalled`, and discarded both `URLSessionWebSocketTask.send` failures and HTTP `try?` errors.
  Half-open connections after hub restart / foreground return lost writes; reconnection snapshots overwrote optimistic local state.
- Sent is not delivered. Hub-mutating WS messages without receipts must use HTTP while `stalled`, or remain queued until pushed hub state proves receipt
  (here hub `seen_at`, not phone-vs-hub clocks), retry otherwise; preserve optimistic display while waiting rather than overwriting from snapshots.
  Ordinary `sendPending` already checks `connected` and `stalled`; copy that pattern for new messages.

## VNC from a sheet does nothing: root fullScreenCover swallowed (2026-11-04, #1604)

- View screen failed from machine pages inside sheets / new-shell routes, but worked from home elsewhere.
- `RootView` `fullScreenCover(item:)` cannot present while root already presents a sheet, with no error. `MacRootView` had none at all. UI tests entered from Settings tab without sheets and missed it.
- Going forward:
  - Globally available fullscreen surfaces use independent windows (iOS `UIWindow` above shell / below toast; separate Mac window), driven by one model value, not a view's presentation chain.
  - Test entry from sheet or new-shell page too.


## Changing window safe area relays out SwiftUI navigation (2026-11-04, #413)

- Shell status clearance used `rootViewController.additionalSafeAreaInsets.top`; collapsing relaid out Agents, yielding zero inset / offset for 100–200 ms and ~210 pt jumps,
  sometimes collapsing pulled-open search as scrolling. UIKit changes to SwiftUI-owned navigation, negative padding and `hidesSearchBarWhenScrolling` toggles are reset or introduce other jumps.
- Recurring transient hints must not reserve space; use non-touch `ToastWindow`. Reserve only for infrequently changing content. Inspect recordings and per-frame probes, not final positions.

## Simulator has no PiP; do not create Task per RFB message (2026-11-04, #1343)

- **PiP**: `AVPictureInPictureController.isPictureInPictureSupported()` is false on iOS 27.0 /27.1 iPhone and Duo simulators; `AVSampleBufferDisplayLayer` PiP cannot start. Fall back to full page (`AppModel.openVNC`), and `VNCTests` skip PiP when fullscreen appears. PiP recordings / acceptance require real devices.
  On unsupported devices `AVPictureInPictureController(contentSource:)` initializer actually returns nil despite Swift treating it nonoptional; first `observe` crashes `swift_dynamicCast`. Check `isPictureInPictureSupported()` before construction; DEBUG `-showPiPSource` shows only source on simulator (#1839).
- **Ordering**: initial RFB client started a `Task { try await transport.send(…) }` per pointer / key / clipboard message. Scheduling is unordered, scrambling press / release and movement / clicks, especially rapid typing. Use one `AsyncStream` outbox, drained sequentially after handshake; pre-handshake messages queue afterwards. Same for terminal keys / WS and all ordered channels.
- **Clipboard tests**: runner `UIPasteboard.general` writes while backgrounded produced empty clipboard at mock without paste prompt. Test roundtrip inside app: Copy to iPhone, then Send only to Mac clipboard, comparing mock receipt.
- **Runner reads app-copied text** (#3189): simulator asks runner to paste from Persona, blocking main-thread `UIPasteboard.general.string` and hanging until timeout. Read on background queue while main polls and taps SpringBoard Allow Paste / `允许粘贴` ("Allow Paste") (`SelectTextTests.pasted()`).

## Direct UIScrollView offset writes in geometry callbacks can suppress other geometry callbacks (2026-11-04, #1046)

`ChatPin` sets `contentOffset` in header `onGeometryChange`, correctly pinning pixels and reporting its own updated position,
but outer-row `debugLineY` geometry stays at the pre-move value until new content:
with latest work pinned, a formal streamed reply makes `probe.chat-rows` report 56 pt movement and the next entry reports reversal.
`RunningChatTests` failed, yet 120 externally captured screenshots over 60 seconds were pixel-identical in both layouts.

- Work-header probes use its own `scratch.workHeadY`, also used by pinning.
- UIKit offset compensation fails because lag direction differs by layout; `setNeedsLayout()` after offset changes does not force the missed callback.
- When reported jumps disagree, first compare `simctl io screenshot` pixels / neighboring row luminance curves, then inspect code.

## Streaming instability needs per-frame recordings and real hub event order (2026-11-04, #1046)

Screenshots miss user-reported instability and probes can report false jumps; frame decoding (`scripts/diag/stream-scroll/`) revealed real events lasting one or two frames or hundreds of milliseconds:

- **One-frame disappearance** on formalizing a reply: streaming clears immediately while background-built rows arrive next update.
  When replacing one thing with another, retain old until the update delivering new.
- **Mock order is tidier than real hub**: mock sent formal messages before clearing; real hub clears at turn end and transcript may arrive later. `&late=` reproduced it;
  read server event order before fixing.
- **Changing newest end of an inverted list moves content under the finger**: removing hundreds of streaming points while frozen, or letting streaming grow during touch, jumps rows.
  Hold during touch and preserve already-drawn content unchanged while held.
- **Lazy branch switches may retain rows**: frozen StreamingText.id("`live`") versus `ChatLiveText` with the same `live` id
  left the original store-observing `ChatLiveText` growing, never drawing frozen text (`ScrollTrace` fixed 309-character held text but rising height; `NSLog` body confirmed).
  Use one view with different values, `ChatLiveText(held:)`, not separate branch views sharing id.
- **Mac scroll phase tracks fingers only**: SwiftUI reports idle on trackpad release while inertia continues. Inspect event phase / momentumPhase yourself (`MacWheelWatch`).

## Split navigation paths cannot restore pages after tree replacement (2026-11-04)

- Sidebar `NavigationLink(value:)` does not populate bound `path` on iOS 26: when `NavigationSplitView` pushes into detail, `NavigationColumnState.ejectAll` clears `path`,
  leaving the opened page absent. `RootView` width-switching between shell and tabs cannot restore from `path`;
  reopen from separately recorded `dev.openChat`. Shared model paths also fail because the incoming split tree clears them too.

## PiP sources must survive their window, be normally sized, and start on user taps (2026-11-05, #1839)

- VNC PiP failed on real-device TestFlight 161, invisible in simulator because PiP is unsupported there.
- Causes:
  - Source `AVSampleBufferDisplayLayer` was 2×2 pt; opening via fullscreen overflow attached it to key `ScreenWindow`, then closed / released that window and source.
  - Machine-page VNC waited for connection / first frame then started PiP in `Task`, outside user-tap event; failures only in system logs, appearing ineffective or fullscreen fallback.
- Going forward:
  - Normal source size (192 pt wide here), behind content in permanent main window; not `isHidden` or zero-size.
  - Prepare ahead (on fullscreen connection) and `startPictureInPicture` in the same tap event; automatic background start uses `canStartPictureInPictureAutomaticallyFromInline`.
  - Layers with `controlTimebase` must not receive DisplayImmediately samples (header explicitly discourages it); create separate timestamped samples.
  - Show failure reasons to users through a hint and copyable diagnostic page, so device issues identify the blocking stage.
  - Do not start without a frame: `isPictureInPicturePossible` becomes true at window attachment, but empty sources silently ignore start without `failedToStart` (TestFlight 161 machine entry always did this; fullscreen with frames worked). Wait for first frame, retry a few times if silent.
  - To avoid waiting for connection (#1839 follow-up, user found delayed PiP sluggish), render a SwiftUI connection card (machine name, Connecting spinner)
    via `ImageRenderer` into `CVPixelBuffer`, timestamp with host clock, feed source, then start in tap. Later Mac frames replace it. When card stops, reenqueue newest real frame,
    preventing a final card sample following real video: VNC sends only changes, so a static Mac otherwise stays on the card.

## Page replacements hand content over at the same position (2026-11-05, #1911)

First message from New Chat appeared at top, then inverted chat put it bottom, visibly jumping. Frame recordings also showed keyboard down / up (simulator),
no initial Working row so the message shifted by one row, and pending local removal one update before transcript arrival, blanking a frame.

- Pages handing over the same content use destination layout in the source: identical `UserBubbleBody` / `DayLine`, margins (`ChatView.newestGap`),
  and placement (`FirstMessagePending`). Destination reserves the row before real content (Preparing…, `ChatView.startingTurn`).
- Old removal / new insertion happen in the same update: background-built rows publish next round, so retain local until then (`ChatStore.arriving`, same streaming trap #1046).
- `onScrollGeometryChange` has no initial callback before change; for bottom alignment use `containerRelativeFrame(.vertical, alignment: .bottom)`, not unavailable initial height.
- Measure message-to-composer distance for jumps: keyboard moves both together, not instability (simulator hardware typing, soft keyboard only after send).


## Keyboard clips question cards; `scrollDisabled` propagates to inner ScrollViews (2026-11-05, #1912)

- **Symptoms**:
  - Focusing composer under Claude is asking leaves only question and half the first two options.
  - SE-size iPhone hides 30 pt of composer behind keyboard; later options cannot be reached.
- **Causes**:
  - Cards capped at half page (#926); keyboard leaves 300–400 pt, half fits only two options.
  - Chat-list minimum height includes header-overlapped region / margins. Taller cards make footer exceed page, pushing composer below keyboard.
  - Forms scroll internally capped `room * 0.55`. If card exceeds outer region, inner form takes drags and outer stays fixed, hiding last options.
  - Outer region `.scrollDisabled(true)` when fitting propagates to every inner ScrollView, disabling forms too.
- **Fix**:
  - With `KeyboardState` open, cards may use space ceded by chat list. Use current header height (Agents collapses), allowing list height 0.
  - A single form is sized so its whole card fits the region; outer region no longer scrolls, only the form.
  - Keyboard-open region always uses scrolling layout (numbers, margins, clipping unchanged), preventing oscillation after shrinking.
  - Fitting regions use `.scrollBounceBehavior(.basedOnSize)`, not `scrollDisabled`.
- **Going forward**:
  - Disable outer movement while retaining inner scroll through basedOnSize bounce, never `scrollDisabled`.
  - Taller inner ScrollViews take all drags: fit inner to outer or disable inner scrolling.
  - Inspect keyboard UI at SE size; iOS 27.1 simulator has no SE, use 27.0.
  - Do not `swipeUp` for options in small regions: fast flings overshoot and header-start swipes do not scroll. Hold, slow-drag, pause at end (`press(forDuration:thenDragTo:withVelocity:thenHoldForDuration:)`), starting inside the scrolling area.

## Row `confirmationDialog` lacks Cancel (2026-11-05, #1830)

- Enabling an extension for a computer used `confirmationDialog`; iPhone showed only Enable, no `role: .cancel` button, with a field-name title lacking computer identity.
- iOS 27.1 simulator presents a row-attached popover, which omits cancel (outside tap cancels), not obvious to users.
- Binary confirmations use `alert` with Cancel / Enable and object name ("Enable this computer?"). Save name separately in `@State` and do not clear on dismissal or disappearing title becomes empty.
- Reserve `confirmationDialog` for choosing among several options; explicit-cancel confirmations use `alert`. Assert and tap Cancel in UI tests.

## Time-sensitive pushes treated as ordinary: missing entitlement (2026-11-04, #1572)

- Needs-you pushes always carried `interruption-level: time-sensitive` and NSE set `interruptionLevel`, yet Focus suppressed banners (also synced to watch).
- Missing `com.apple.developer.usernotifications.time-sensitive` silently downgraded to `active` without log / API errors; `requestAuthorization`'s `.timeSensitive` is deprecated and ineffective.
- Add entitlement, enable `USERNOTIFICATIONS_TIMESENSITIVE` on App ID and regenerate profiles (`scripts/release/asc_signing.py`); `scripts/release/entitlements.py check` guards it. Simulator cannot distinguish; verify real-device Focus.

## Performance tests create hitches; compensation animation can finish across one stalled frame (2026-11-05, #1123)

- Initial expand / collapse measurements showed 15–20 late frames per tap, even without redraw: UI tests polled `probe.unfold` every 0.1 s, each accessibility query occupying app main.
  Tap, wait quietly, then read once; disable 10Hz `probe.chat-rows` too. First measure idle baseline (0/96 late frames) to establish trust.
- Time-based `exp(-dt/τ)` animation across 80–100 ms layout stalls traverses most distance in one frame, still jumping; cap dt to 1/60 s.
- State borrowed by the prior action: latest work rises toward header, then expanding nested thinking consumes remaining rise and moves tapped row 46 pt;
  BeautifulUI tool pills replaced indefinite work pin with 1.5 s hold, then follow-to-latest moved pills 119–381 pt.
  A nested tap fixes outer position / state from then on (`ChatPin.hold` repins current position; `changeWork` leaves run follow state unchanged).

## Move pure logic to SPM so tests can run without the app (2026-11-05, #2095)

- Pure notebook / path tests still built the whole app and launched simulator:~140 s first worktree build,~45 s after one file, plus parallel-agent queues.
- Move Foundation-only types / tests to `apple/Packages/PersonaKit`: `swift test`~10 s cold,~2 s after edits. During migration:
  - App-used types / members must be `public`, `public private(set)`, with `public init` for structs. Package `@testable import` tests can all pass while app fails; build all three apps per batch.
  - Split files mixing pure and app assertions (views, `FilePaths`); pure tests move, app tests stay `PersonaTests` with `import PersonaKit`.
  - Package defaults `nonisolated`, app MainActor; existing `nonisolated` value types move unchanged.
  - Before moving `apple/Shared` files (batch 2 `Links`), check watch / widgets / notification extensions (`grep -rn '\bLinks\b' Shared PersonaWatch PersonaWidgets LiveActivity`); they do not depend on the package and would fail.
  - Name grep for imports also finds comment-only mentions; import only actual code users (8 unnecessary batch 2 imports were removed).
  - Extract pure functions from views (`StreamChunks`, `ZoomMath`) with preceding `///` documentation and exclusive private helpers (`ConversationTitle`'s `Character.isASCIIDigit`). Keep moved helpers internal, not `public`; app never exposed them.
  - For one or two pure methods in a type (`AgentSync` `heldUntil` / `workingGap`), extract a pure `enum` (`AgentSyncPacing`), delegate original methods, leaving app callers unchanged. Move view `static` helpers to their value type (`ConnectorSignInSteps.cannotDrawNote`→`ConnectorPrompt.cannotDrawNote`).
  - Inspect app `JSON.decoder` first: it only adds date strategy, so dateless package types decode identically with `JSONDecoder()`.
  - `swift test` writes package .build; root `.gitignore` must cover `/apple/Packages/*/.build/`. Initial `git add -A` staged 1500+ artifacts with local paths; discovered after pushing, branch deleted. Check `git diff --stat` file count before committing.

## Captured callback keys become invalid after object rename (2026-11-05, #2379)

- Hub paired through SSH tunnel later lost connection without Last failure detail.
- `onPhase` captured dial-time id `pairing`; `adopt` moved the tunnel under hub id, so `tunnels[id]` no longer matched and discarded all state.
- Find current key by object identity (`SSHTunnels.key(of:in:)`), not captured key. When removing a hub group, collect members **before** registry mutation; afterwards ids are gone (`AppModel.tunnelIDs(removing:from:)`).

## HTML subframes can borrow the parent bridge; main-frame checks are insufficient (2026-11-05, #2025)

- Real WKWebView rejected direct subframe messages via `isMainFrame`, but same-origin `srcdoc` borrowed parent bridge and appeared main-frame; instance nonce cannot distinguish. CSP `frame-src 'none'` did not prevent execution in this case.
- Cards need no subframes; navigation delegates reject all `about:` / `data:` subframes. Keep real borrowed-parent-bridge regression requests, not only forged handler arguments. Nine normal WebKit security tests passed; infinite loops / host recovery require separate validation.
- On content-process exit, revoke bridge, load ticket and scripts, show native fallback; retrying identical HTML creates a new instance, not blocked by `loaded == html`. Calling public delegate verifies recovery logic, not actual OS termination.

## Navigation-`path` changes during Back leave a blank page (2026-11-05, #1941)

TestFlight shell Agents occasionally showed only system navigation and blank / black content. `AgentsPushPopRaceTests` reproduced
by runtime CLI `navigate … keep` opening chat through `model.openChat` during Back (like notifications),~2/300 attempts
(BeautifulUI, full edge swipe, near finger release). SwiftUI `path` was empty (shell header root-only),
but UIKit still had a page with only Agents Back and empty content.

- Changing `NavigationPath` while `transitionCoordinator` remains active (push / pop animation or gesture) is accepted by SwiftUI but not reflected in UIKit, permanently diverging.
- External opening (notification, Siri, launch) waits for `StackStillWatcher.whenStill` (`ChatsView.takeOpenChat`).
  Already-idle stacks push immediately; ordinary startup / list notification timing stays unchanged.
- Manual taps cannot reliably hit it: UI tests connect directly to app runtime CLI and send at specified milliseconds, sweep tens-of-milliseconds bins
  with repeated rounds. Detect blanks by system `BackButton`, not absent list elements (once they remained in accessibility).

## `List` structure changes reconstruct every row's modifier chain (2026-11-05, #343)

- Idle Agents pushes caused 16–30 ms main frames (simulator, 140 Agents). Rows already `Equatable` and observing `AgentsFeed.Row`,
  but space reorder (~half real pushes) reevaluated list and rebuilt `NavigationLink`, `swipeActions`, `contextMenu`
  and captured full `Session` per row, costing one-fifth of each push.
- Wrap rows in key-only `Equatable` `KeyedRow` / `SlotRowKey`, including row parameters and list state (search, compact, style, groups, machine name),
  excluding pushed last-text / time read by row-owned observations. Missing nonobserved `@State` / `Binding` / `Lookup` values leaves stale rows.
- Do not additionally apply `.equatable()`: `EquatableView` blocks row traits (`listRowSeparator`, `listRowBackground`, compact `listRowInsets`)
  from reaching `List`, adding pill separators / shrinking compact rows (screenshots caught it despite passing tests). Custom `==` already gives SwiftUI comparison.
- Do not evaluate computed properties twice in one body: filter `counts(base)` recalculated `base`; `TopBar` `@ViewBuilder` arguments are constructed even when hidden.
- Generic `@Observable` `StoredValueBox<Value>` property reads instantiate runtime key paths (`swift_getKeyPath`); expensive inside every element closure. Read into locals first.
- Use Time Profiler to locate costs (`docs/operations/testing.md` ("Agents structural pushes")); body counts alone suggested expensive body, actually only one-fifth.


## Internationalization (#2457)

### `String(localized:)` cannot switch language at runtime
- Changing `AppleLanguages` and replacing `Bundle.main` with a `localizedString(forKey:…)`-overriding subclass changed SwiftUI `Text` but not `String(localized:)`, leaving mixed Chinese / English.
- Foundation fixes language at process startup; Swift `String(localized:)` bypasses `Bundle.localizedString(forKey:)` (measured macOS 27). `String(localized:locale:)` `locale` controls formatting, not language lookup; only `LocalizedStringResource.locale` chooses language.
- Save `AppleLanguages` for next launch and explain in Settings (Mac offers Reopen now). Long-lived widget extensions use `interfaceText(_:)` (`LocalizedStringResource` + App Group language) and `.environment(\.locale, …)`. See `docs/apple/ios.md` ("Interface language").

### English development region makes missing zh-Hans plurals fall back to English
- After `CFBundleDevelopmentRegion`=`en` for unsupported-language fallback, Chinese `7 分钟` ("7 minutes") became 7 min only for plurals.
- Missing zh-Hans keys fall back to `en` stringsdict and English plural forms.
- Every translated catalog key includes its zh-Hans source value; `scripts/i18n/apple_l10n.py check` verifies.

### Interpolated localized integers include grouping separators
- `String(localized: "一条最多 \(n) 字")` ("At most n characters per entry") shows 3,500 instead of old concatenated 3500; ports / ids become 8,080 too.
- Localized integers use `locale` formatting, as `Text("\(n)")` always has.
- Counts retain it; ports, ids and years use `\(String(port))`.

### `Localizable.xcstrings` rebase conflicts require key-based merging
- Both sides adding entries can line-merge into invalid JSON (`The data couldn't be read because it isn't in the correct format`), breaking all targets.
- Extract `git show ORIG_HEAD:<catalog> > ours`, `git show origin/main:<catalog> > theirs`; run `python3 scripts/i18n/apple_l10n.py merge ours theirs apple/Shared/Localization/Localizable.xcstrings` (merge keys, prefer complete `en` / ja on duplicates), then `check`.
- **`merge` brings back keys deleted on `main`, and rewrites the whole file** (2026-11-06, #5451): it keeps every key of both sides, so an entry removed
  on `main` (6 of them in one merge, for example "No Claude running") comes back as if the branch had added it. Its output also changes the
  format and order (+78k / −77k lines against `main` for 40 new keys). Use its merged entries, but write the file back in `main`'s key order and format
  (`json.dumps(d, ensure_ascii=False, indent=2) + "\n"`), add only the keys the branch added, and drop keys that were in the merge base but `main`
  no longer has. `git diff origin/main --stat` should then show only `+` lines for your keys. Git's own text merge of two branches that added the
  same key leaves a duplicate JSON key, which `check` rejects: keep one copy.

### Two PRs that add the same catalog key both pass, then break master (#5811)
- #5743 and #5757 each added `"Run"` to `Localizable.xcstrings`. Each passed `apple_l10n.py check` against its own base, and git merged them
  without a conflict, because the two entries sat at different places in the file. Master then had a duplicate JSON key: `check` failed,
  and Xcode rejects such a catalog.
- After every merge of `main` into a branch, run `python3 scripts/i18n/apple_l10n.py check`, even when git reported no conflict in the catalog.
  PR runs (scripts-tests.yml) each see their own base, so only a run on `main` itself catches two PRs that merged close together:
  run `check` on `main` right after your PR merges, too.

### An interpolated String looks up a `%@` key the catalog doesn't have (#5544)
- `String(localized: "Exit code \(code)")` with `code` a `String` (or a regex match's `Substring`) asks for `"Exit code %@"`. The catalog had
  `"Exit code %lld"`, so the lookup missed and every language, Chinese too, showed the English key. The specifier comes from each
  interpolated value's type (String → `%@`, Int → `%lld`, Double → `%lf`), and only the compiler knows the types: `check` matches keys by shape and
  cannot see this. Convert to the type the catalog expects, or add the key with that specifier.
- `apple_l10n.py strict <DerivedData>` (after a `SWIFT_EMIT_LOC_STRINGS=YES` build) lists every extracted key that differs from a catalog key only
  in its specifiers, for the app targets, BeautifulUI and PersonaKit. An incremental build keeps the unchanged files' `.stringsdata` in
  `Intermediates.noindex`, so the sweep still sees them. What can mislead is stale files left from deleted or renamed sources: build clean, or
  delete DerivedData, for an exact sweep.

### BeautifulUI's keys in the app catalog (#5574)
- The library's text is looked up in the app's `Localizable.xcstrings`, so its keys are plain English shared with the app. `"Keep"` (zh 保留,
  ja 残す) is the Managers page's button (#5451), and SelectionActions uses the same key. If SelectionActions is adopted where Keep means something
  else in Chinese or Japanese, it needs its own contextual key in the library.
- `"%lld %@ applied"` / `"Apply %lld %@"` (DiffTable) interpolate the English `"edit"` / `"edits"` as a `String`. The translations can't drop that
  `%@`: `check` requires the same placeholders in every language, so zh-Hans reads 已应用 3 edits. If DiffTable is adopted, the noun has to become
  part of a pluralized key in the library.

### Passing isolated methods as function values crashes Xcode 26.6 IRGen (#4157)
- `Binding(get:set:)` with `set: pick` (MainActor instance method) reliably crashes PersonaMac `LanguageSettingsView.swift` IRGen `report_at_maximum_capacity`; demangled SIL is `reabstraction thunk helper from ... (AppLanguage, Actor?) -> () to ... (AppLanguage) -> ()`. Clean DerivedData rebuild still fails; same code builds iOS.
- Swift 6.3 / Xcode 26.6 overflows generating a reabstraction thunk for isolated method values with implicit actor parameters, a compiler bug rather than source error. No normal `error:` line; inspect `While emitting IR SIL function` and stack dump.
- Wrap in ordinary closure `set: { pick($0) }`. Other `flatMap(Type.init(rawValue:))` calls are unaffected; changing flatMap did not help. Leave an English comment naming compiler / issue so nobody "simplifies" it back.

### `apple_l10n.py sync` from the command line also changes other people's strings (#3508)

- **Symptom**: after a `SWIFT_EMIT_LOC_STRINGS=YES` build, `apple_l10n.py sync` added this change's 82 keys, but also keys from other code that never
  made it into the catalog (a `"/"`), and rewrote 58 existing entries (merged comments, states): the diff was mostly other people's.
- **Approach**: use sync only to see which keys are new. For the commit, put the catalog back and insert only your own entries with a script
  (English key, explicit en / zh-Hans / ja), each before the first key that sorts after it, without re-sorting the file (`main`'s is
  nearly but not strictly sorted: `dict(sorted(...))` moves other people's entries), and write it back with
  `json.dumps(d, ensure_ascii=False, indent=2) + "\n"` (the format `apple_english_keys.py` wrote; check that `main`'s file round-trips
  unchanged first, and that `git diff` shows only `+` lines). Then `apple_l10n.py check`. A conflict in the catalog resolves the same way:
  take `main`'s file, insert your entries.
- **Rebased across the English-key migration (#5430)**: a branch written with Chinese keys does not need its keys hand-translated. Run
  `scripts/i18n/apple_english_keys.py <scratch worktree>` as it is on `main` against a scratch worktree of the branch's old tip (it still has the Chinese-source catalog),
  import it to get `migrate()`'s mapping, then take the migrated new files, the mapped entries, and the same key changes in the files you only
  touched. Keys it suffixes (`Mute [Voice Bar 7db2e1]`) collide with an existing English key that means something else in Chinese: keep the suffix.
  `apple_comments.py check` also fails on Chinese UI words in comments: write the English label (`Never Mind`), keep 「…」 for doc headings or text matched word for word.

## Context-block bodies always use `ContextText.inBlock` (2026-11-06, #3831)

- `ContextRef.xml` previously removed literal `</persona-context>` / `</persona-context>` tags once. Nested `</persona-</persona-context>context>` leaves a closing tag after deletion, ending the block early; third-party Inbox text handed to AI becomes apparent user speech. Case variants and whitespace `</persona-context >` are also accepted by hub / web readers and bypass single literal removal.
- `ContextRef.xml` and `TodosLogic.formatTodoContext` now use `Shared/ContextText.swift`: remove once, then replace remaining < with `&lt;`, two linear passes matching hub `prompts.ContextBody`, web `contextBody` and Android `contextBody`. All new block writers follow this. Regress by counting `ContextText.closingTag` across the whole message: only the block's own closing tag remains.

## The Agents page's top on a phone is our own component, `AgentsHeader`, not the system search drawer (#2478, 2026-11-06)

- Before, five things each moved part of the top on their own timing: the system `navigationBarDrawer`, the contentOffset fix-up that started it folded (`SearchCloseWatcher`), the blank prompt line that made room for the shell's bar (`ShellNavigationClearance`), the bar's own fade, and the animated results ↔ list swap. On closing, four changed in the same update: the list overshot ~140 pt, sprang back 138 pt in one frame, and the field stopped half folded (#314, #413, #740, #1051, #2189 were all this layer). Now the navigation bar never changes height, and what moves is computed from "searching or not" and "how far the list scrolled past its top". `AgentsTopMotionTests` checks it frame by frame (the app's `-showSearchTrace`): no turn-backs, no single-frame jumps, the bar and the field never drawn over each other. The wide split and the Mac keep the system search.
- To look like the drawer, take `UISearchBar().searchTextField` out and place it alone (its magnifier and placeholder colors are the bar's). A whole `UISearchBar` placed alone puts the field back on every layout (8 pt from the edge, centered in 64 pt) and draws glass with a border; a plain `UISearchTextField` has a thinner magnifier and a lighter placeholder. At rest the drawer's field is a flat tertiary-fill capsule (no border, radius 22), glass while searching; the ✕ is the system `.close` item in a `UINavigationBar` of our own (a hand-drawn glass button's cross is thinner).
- A List does not animate a row's height: removing a view in the row, or `frame(height:)` down to 0, relays out the list at once (at 0 not even that), and the rows below jump. Slide the list as a picture (`offset`, no relayout) and switch layouts at the moment both look the same (the animation's end or start), adding the offset in the same frame (`ListShift`, `setCompact`).
- The first keyboard of a launch stalls the main thread ~0.4 s: an animation started before it skips over that stretch. Start moving on `keyboardWillShow`.
- A list's own `.safeAreaBar(edge: .top)` whose content is fully clear (`Color.clear`) gets no scroll-edge blur; the page color at 1% opacity does, with no pixel changed. It must be on the `List` itself (a few modifiers in between, such as a background, and it is gone). The drawer's bar used the hard edge (`.scrollEdgeEffectStyle(.hard, for: .top)`): set it explicitly, and show it only while rows are really under the bar (not while the folded field's 54 pt scroll up, `scrollEdgeEffectHidden`).
- `insetGrouped` cells have a 26 pt `cornerConfiguration` and clip: whatever the first row draws to the cell's edge (the field's rounded corners) is cut. For a row without a background turn the cell's `clipsToBounds` off (the list turns it on again when it configures the cell: watch it with KVO, restore on leaving).
- Faded out, the shell's top bar was still in the accessibility tree over the risen field (☰ found and tappable): it leaves the tree once the list takes its searching layout. And the list at rest with the field folded sits a band down from its top: `ReaderAnchor` must count that as the top, or a change at launch keeps a row and throws the list ~350 pt down.
- Measuring: resting states pixel by pixel against master (screenshots of each state, diffed); motion from the app writing each block's place every frame (`SearchTrace`, a CADisplayLink reading presentation layers). The simulator's home indicator strip differs from one screenshot to the next (~6400 px): leave it out.
- The shell's chrome grows with the text (`shellScaled`, the top bar up to XXXL: 50 → 60 pt tall), the navigation bar, the shell's room and the field's band do not. A timing worked out at the standard size (the bar gone by 0.35 of the field's way) let the field into the bar's rows at XXXL while the bar was still ~8 % there (#4928): derive such timings from where things are drawn, and run motion tests at a larger size too. A 60 Hz trace can step over a crossing that lasts one frame; check the geometry, not only the frames.
- A `withAnimation { } completion:` can run before anything has moved. Opening the search from `keyboardWillShow` during the first keyboard of a launch, the change reached the list's `ListShift` (and the field) only in an update after the ~0.4 s stall. The transaction had nothing animating when it committed, so the completion ran at once (80 ms after the start, the slide still 270 ms away) and switched the list's layout early: a 54 pt jump in one frame, in about half the historical tab-navigation runs (#5999; the shell did not fail in any run). Do what has to wait for a move when the animated value itself arrives (`ListShift.arrived`), and guard against a stale arrival (`AgentsHeader.rising`).

## On an iPad the chat's sideways scroll views are inset by the sidebar (2026-11-06, #5737)

- A wide table in the iPad's chat column: bounds 788 pt, content 751 pt, `adjustedContentInset.left` 372 pt (the sidebar's width; the scroll view runs on under it). "Wider than its bounds" said it did not scroll, so a drag to the left on it quoted the reply; on an iPhone (no inset) the same check was right. Compare against what shows: `bounds.width − insets`.
- SwiftUI's `ScrollGeometry` there: `containerSize` 416 (insets left out), `contentOffset.x` from −372 to −37. `contentOffset.x + containerSize.width` never reaches the content's width, so the table's "more to the right" fade never went away at the end. `visibleRect.maxX` does (−37…751).
- XCUITest frames of a flipped chat list differ between an iPhone and an iPad, and between rows: mirror a frame against a text drawn just above it in the same block, not against the first and last messages.

## 图片分页的终态与验证（#5678）

旧 page-style `TabView` 在快速滑动后出现过 3.6667 pt 的整页偏移，截图可见邻图条带；这只证明偏移存在，不能据此断定手机每次都是同一种解码或取消事件触发。`ImagePager.swift` 让 UIKit 保持分页容器和每页的身份，SwiftUI 只向现有页交付图片；终态、减速和动画结束时对齐整页，不用计时器修补偏移。

取消测试必须让真实 `UIScrollView.panGestureRecognizer` 先经过 `.began`、`.changed`，再由 UIKit 的 `isEnabled = false` 触发 `.cancelled`；不能手写 state 或直接调用终态方法。回调入口先保存原始 state，再运行 DEBUG hook，否则 hook 在 changed 回调中改变 state 后，会提前走取消收尾，造成假阳性。测试要求实际取消回调、收尾请求、整页结果严格依次发生。

解码测试在真实 changed/panning 事件上放行解码器，记录解码完成和 UIKit 页更新仍为 `changed / isDragging=true`，再观察整页终态。固定延迟本身不能证明时序。

DEBUG 的代码可能在 `Persona.debug.dylib` 中，40 KiB 的 `Persona` 只是启动壳。二进制检查的正向对照要看实际加载的 Debug image；Release 不仅查主可执行文件，也查打包的所有 Mach-O。新增 `showViewer…` 启动参数必须登记到 release 字符串检查。

下拉关闭的闸门不能用 `isTracking` 表示「已开始拖动」：手指按下即 tracking，方向尚未确定。用 `isDragging`、`isDecelerating` 判断；外层分页 pan 拒绝以向下为主的起手，避免抢走下拉关闭。


## A live card inside an `Equatable` work step (2026-11-07, #3436)

- **`WorkStepView` compares only its item and `first`**: a value handed down from the chat (a workflow run's progress) never reaches a step that is already drawn, because the hub's pushes do not change the item. A card that changes on its own reads its data itself, by a key in the item: `WorkflowCallCard` reads `AppModel.workflow(_:call:)`, one observable cell per (session, Workflow call) set only when that run changes, so a push about another run or the rest of the session draws nothing.
- **A decision `ChatLines` makes from outside the items belongs in `LinesKey`**: `foldsIntoRun` / `showsFolded` now depend on which runs the hub lists; the lines are memoized by the store's version, so without `workflowCalls` in the key a run that appeared (or went) left the call folded into the chips (or out of the fold) until something else changed.
- **`fold_work` hides the step a card hangs on** (as on the web, `docs/lessons/web.md`): a chat opened while a run goes on has the launching turn as one fold item. Fetch the fold whose `[ts, until]` holds the run's `started_at` (`WorkflowRun.foldsHoldingCalls`). A fetch can fail (the hub redeployed, the phone offline, not connected yet: `AppModel.unfold` then returns without throwing), so judge by whether the fold left the items, and ask again after a wait that doubles from 5 s to a minute (`WorkflowCallCard.fetchFolds`, as the web's `useWorkflowFolds`). Marking it "asked" before the fetch and swallowing the error left the card buried for the whole visit.
- **A second-by-second clock per row is not free on the Mac**: `Text(timerInterval:)` needs no body, but 60 of them (a run's listed agents, all running) kept the idle Mac chat at 7.8% CPU (Debug build, `ps` over 20 s) against 0.5% without. One clock for the run, the rows to the minute (`TimelineView(.periodic(by: 60))`, all rows on one shared anchor so they turn in one update, not 60 wake-ups a minute). Measured again over 90 s (30–120 s after launch, so at least one minute tick; process CPU time from `ps -o cputime`; Debug, Mac locked): a plain chat 0.25–0.30%, the run's card with 16 running rows 0.84%, with 61 running rows 0.82%. So many agents add nothing; what remains over a plain chat (about 6 ms a second, `sample`: SwiftUI layout in `NSHostingView.layout`) is the run's one second clock. FrameStats could not run: display links do not fire while the Mac is locked (`docs/apple/mac.md`). Measure an idle card with many live rows, against the same card with few, before giving each row its own clock.
- **A UI test that drags the chat to bring a card into view**: a plain `press(forDuration:thenDragTo:)` flings the flipped list well past it. Drag slowly and hold at the end (`withVelocity: .slow, thenHoldForDuration:`), check the target's frame between drags, and assert it is hittable before tapping it; "a row is gone" alone is no proof that a block folded (the lazy list may simply have dropped the line).
- **Every expander inside a chat row needs the pin, nested ones too**: the phase blocks inside the run's card first toggled with a plain `withAnimation`; in the upside-down list a phase opening 12 rows (or 「n more」 opening 43) pushed its head and everything above it up, under the top bar. Route each one through `toggleInPlace(pin, key, …)` with `.chatPinned(key)` on what was tapped (or the head above it, for 「n more」 / Show Fewer), as the card's own head does; outside the list (`chatPin` nil: the chat's header) it animates as before, so check Reduce Motion there yourself (`toggleInPlace` falls back to `withAnimation` unconditionally).
- **Don't middle-truncate a line that ends in numbers**: the Agents row's 「后台 workflow「parity-audit」 · Verify 2/2 · 169/185+」 cut in the middle read 「…9/185+」, a wrong count. Cut only the name (`WorkflowLine`: name and details as two texts, the details laid out first).
- **The mock's times are fixed when it starts**: `mock_hub.py`'s `ago()` counts from its start, so an ended run (`ended_at` 40 minutes "ago") leaves the chat's header once the mock has run for 20 minutes, and `testCrowdedWorkflowsLeaveRoomForTheChat` failed on "the stopped one's error is missing". Start the mock afresh for each run of the workflow UI tests; `-collect-test-diagnostics never` also saved about ten minutes per `xcodebuild test` after the tests had passed.

## An icon-only themed button takes its glyph's size (2026-11-07, #58)

- On iOS, `.themedButton()` (glass, bordered and soft alike) sizes a button by its label. With a bare `Image`, each button is as big as its glyph: `ellipsis` is only a few points tall, so at the default text size the VNC page's More came out as a 37 × 19 pt sliver beside a 48 × 38 pt Close, and Help (`questionmark.circle`) was smaller too.
- Give every icon-only button in a row the same square label: `Theme.Size.icon` through `scaledFrame` (`VNCPage.barIcon`). Comparing the buttons' frames in a UI test catches it (`VNCTests.testTopBarButtonsKeepTheirShape`); a glance at one button does not.

## An open chat cannot trust its subscription alone (2026-11-07, #72)

- **What went wrong**: `AgentSync` left every subscribed session to its subscription. When the hub stopped pushing a session's `items` (a node whose transcript watch had ended), the open chat never showed the new message or reply, and the session's working status lit up the previous turn's work line, so the new turn's work read as more tools of the message before it. Reopening the chat showed everything in the right turns.
- **Now**: `AgentSync.checkStall` asks for what came after the chat's `covered` when the session's `updated_at` is newer than the chat's newest line and no batch came for 4 s (`AgentSyncPacing.catchUpAt`, the web's rule and numbers). `ChatStore.arrivedAt` counts every batch, empty ones too, and streamed words: the hub sends a span for lines without items, and a headless session's `updated_at` is the node's clock at its last event, so neither a growing transcript nor a long reply being written looks quiet. Nothing is asked before the first batch (the backlog is on its way). While a message sent from here is still on its way, the session went to work after it was written and the transcript has nothing newer than it, the work line yields to the message's getting-ready line (`ChatAwaitingTurn.workIsForPending`). UI test: `ChatCatchUpTests` (`/mock/say?drop=1`).
- **Signals that look usable but are not**: a session's `rev` is the transcript's byte count, not a line seq, so it cannot be compared with `covered` (but it can say the transcript grew since the last items came: see #1179 below); compare `updated_at` (the newest activity or the file's mtime) with the newest item's `ts`. A `revs` event moves only `rev`; `updated_at` comes with the full list. A turn's `answer` is just its latest assistant text, set while the turn still runs, so it does not say the turn ended; the session's `stateSince` against the message's `createdAt` does, but only while no line is newer than the message: the hub resets `stateSince` on every change of state (blocked and back, its own restart), and a bubble whose copy is never matched would otherwise keep a whole turn's work line dark. `AppModel.session(_:)` keeps an older `updated_at` on purpose (`sameApartFromTicks`): read `latest(_:)` to compare times.

## Time cannot tell the last line of a burst was lost (2026-11-08, #1179)

- **What went wrong**: the open chat's catch-up ignored an `updated_at` within `staleSlack` (1 s) of its newest line's `ts`, as "the same write" (the file's mtime and the line's own clock compare loosely). An agent writes in bursts: a tool call, its result and the words after it within a second. When the last line's push was lost, `updated_at` stayed within the slack of the line before, which the chat held, and the chat never asked, until the agent wrote again. Found by the web's end-to-end sync run against the mock (two replies 0.3 s apart, the second push dropped: nothing in 20 s).
- **Now**: `AgentSync` notes when each session's `rev` changes (the full list or the `revs` event; the first one heard is no change), and `AgentSyncPacing.catchUpAt` also asks when the rev moved after the chat's last items came, once it has been quiet for `staleQuiet` since both. The node reports revs on its next tick, mostly after the items they belong to, so a rev that moved for lines that did come costs one empty `after_seq` read once the chat goes quiet; that read is an arrival and ends the question. The time rule stays for sessions whose rev does not move. UI test: `ChatCatchUpTests.testALostLastLineOfABurstCatchesUp` (the mock with `MOCK_SYNC_REVS=1`, whose lines are written now; without it their times are old and `updated_at` alone tells). It writes both lines before looking at the screen: waiting for the first one on screen spaced them more than a second apart, and the old rule passed it too.

## A switch with words under it inside a menu (2026-11-07, #1307)

- **An iOS menu draws a subtitle only from a second `Text` directly in the item's label** (`Toggle { Text("ultracode"); Text(line) }`, `Button { Text; Text }`).
  Inside a `Label { Text; Text } icon:` the second `Text` is dropped, which is why the effort levels' details in `SessionModes` never show.
  A third `Image` in the same label still becomes the item's icon. The subtitle is cut after three lines (a Chinese sentence and a quota line
  did not fit), and UI tests read it as part of the item's label.
- **Words that are not an action go in the section's header, not a disabled item**: a `Button {}.disabled(true)` for the quota was drawn in the
  disabled gray (about 2.3:1 on the menu's glass) and looked like an option not available now. `Section { … } header: { Text(…) }` is drawn in
  the system's secondary style, line by line on iOS with no three-line cut (a `\n` starts a new line), and stays on top of its section in a menu
  that lists its items the other way round. Nothing in a menu takes a color: a warning can only be as clear as the words around it.
- **A menu that opens upward lists its items the other way round** (the shell composer's model menu and shared effort chips: the items declared
  first sit next to the button): a quota row declared after its switch landed above it, and a section declared after the effort went above the
  effort, out of view until a scroll. Declare them in the reverse order on iOS (`UltracodeMenuSection.leadsEffort`; the Mac keeps the declared
  order); `.menuOrder(.fixed)` would turn the whole existing menu over.
- **The same menu on the Mac** (checked by opening it in a small AppKit program and dumping the `NSMenu`, `docs/lessons/macos.md`): the second
  `Text` becomes `NSMenuItem.subtitle`, which wraps instead of being cut; a section's header is an `isSectionHeader` item drawn on one line (a `\n`
  turns into a space and a long header widens the menu); a `Label`'s second `Text` is dropped there too.
- **A mark after a title that can wrap is part of the text**: `HStack(alignment: .firstTextBaseline) { Text(title).lineLimit(2); Image }` put the
  mark after the first line of a two-line title, between its halves (the watch's list showed it). `Text(title) + Text("\u{00A0}") + Text(Image(…))`
  puts it after the last word (`UltracodeMark.after`). VoiceOver does not read an `accessibilityLabel` given to the image's own `Text`
  inside the sum (the row read 「给天气 App 加主屏幕小组件 」): label the whole `Text`.
- **A menu over the keyboard has room for only part of it**, and a UI test can't scroll it: a swipe on an item closed the menu. Type a short draft
  and put the keyboard away before opening the composer's menu, then take the draft out again (`UltracodeUITests.modesButton` / `clearDraft`).
- **A `try` in the message of `XCTAssertTrue` does not compile** (the message's autoclosure doesn't throw): evaluate first, or use `try?` there.

## The background detail read of a chat must not report a session the list lacks (#1526)

- **What went wrong**: opening a chat also reads that session's complete row from the paged session list (`AppModel.refreshSessionDetail`). The read looked the row up by paging the hub's list (up to 160 pages) and, finding nothing, threw "No matching sessions" into the chat's `error`, which the chat draws as a red line. A session the list does not have is not an error of the chat: the list can hide a session whose transcript still reads, and the read runs on its own at every open and at every "list changed" hint, so the line came back each time.
- **Now**: with `sessions_lookup` in the hub's hello (`APIClient.sessionLookup`, set in `HubLink.api`), `sessionListDetail` asks once for the exact `machine_id` + `session_id` (`summary=0`, no `since` or `cursor`; the old paging is kept for hubs without the feature, never send `session_id` to those, since they ignore it and answer with an unrelated page). It returns nil when nothing matches, and the background read then does nothing. Other failures of the request go by the same `isQuiet` rule as before: a cancellation, a timeout and no network are not shown, the rest is.
- **What this does not hide**: a session whose transcript cannot be read (for instance a main chat id that no computer has a transcript for) still shows an error. The hub's catch-up push on subscribe fails (its detail says the transcript was not found) and `AppModel` writes the hub's generic "request could not be completed" message into `chat.error`; showing an error there is right, and belongs to reading the transcript, not to this detail read. Read from the code, not seen on a device: before this change the two errors went into the same `chat.error` and the one written last showed.
- **Rule**: a read the user did not ask for, run for every open, answers "not found" with nil and keeps the error line for failures of the request. Tests: `SessionListTests` (`aHubWithLookupIsAskedOnceForTheExactSession`, `aChatOnAnUnlistedSessionShowsNoError`, `theHubLinkOffersLookupOnlyWhenTheHelloDoes`).

## An agent's live text in its chat: when to apply it and against which items (2026-11-09, #1309)

- **Apply it with the rows, not before**: `ChatStore` builds its rows off the main actor and publishes them a pass later. An agent's live text worked out at once against items whose rows are not in yet came out empty while the chat still ended at the message before: the reply was gone for a pass, then back as its row (with that step taken out, `AgentLiveReplyTests` reads `item:none`). A change asked for while a rebuild is on its way waits for it (`agentLiveDue`) and is published in the same pass as the rows. The assistant's live text solved the same problem with `endLive`; anything else drawn under the newest rows and replaced by one needs the same.
- **Check it against the newest items only**: the store's `items` also hold older pages and the steps of opened folds. A page from the end that starts in the middle of a long turn gets an older page in front of it whose last words can sit right before the newest ones, and `LiveText`'s walk back over the agent's words then reached them; opening a fold turns words followed by a folded run (the agent's latest message, as the page said) into words followed by a tool's result (an earlier message's), so a late whole copy of them came back as a duplicate. The web never has this because it keeps the newest page apart (`useTranscript`'s `latest`). Here `liveTail` starts at the latest page from the end (`newestFrom`) and folds opened work again; only pushes, the latest page and pulls (`apply`, `replace`) ask for it to be checked, never `prepend` or `unfold`.
- **It is only as fresh as the subscription**: `AppModel` keeps a chat's store after you leave it, but the hub sends `delta` to subscribers alone. The `""` that ended the live text while you were away never came, and on subscribing again the hub resends the live text only if a message is still being written (an empty one is not sent). The kept words were then matched against a turn that had moved on: neither the latest message nor a late copy of the one before, so they were drawn again under the newest one. Anything a push keeps current must be dropped when the pushes stop (`forgetAgentLive`, on the last unsubscribe and again on the first subscribe for a delta that crossed it); the web gets this for free because its chat state starts over when the chat mounts.

## Selectable text under a tappable header eats the header's taps (2026-11-08, #1542)

- **What went wrong**: Persona's folded message line (`AgentSentLine`) opened to its text with `.textSelection(.enabled)` right
  under the header. Once open, taps on the header's words did nothing: the selectable text took touches a little above its
  own frame, so only the chevron (to the right of the text's column) folded it. The UI test caught it (`SentToAgentTests`
  folding at 0.6 / 0.8 of the header failed, at 0.95 passed); without the modifier every spot folds.
- **Now**: the open text is plain, with a Copy button under it. Don't put `.textSelection(.enabled)` directly under a header
  that toggles on tap; if a selectable body is needed, give it its own gap or make the toggle a separate control. Test the
  fold by tapping the header's words, not just its chevron.

## Workspace containers need an action without live Agents (#1399)

Project ownership and workspace parentage are separate edges. Several independent workspace roots can belong to one Orca project; making every root its own repository loses the project layer. Carry the host project ID and name through discovery, group roots by the scoped project ID, and only then render their workspace trees. A child attached across projects follows its explicit parent. Acceptance must compare multiple sibling roots with the host sidebar, not only a single deep chain.

A tree can look correct while its empty rows do nothing: a fold-only button has no content to reveal when there are no running Agents. Give real containers a stable source identity, keep folding as a separate control, and test selecting an empty worktree, starting there, and creating a child under an explicitly chosen parent. Do not add a container details page just to make its title actionable: worktree titles open the first direct Agent in displayed order, while Agent-free containers retain selection and contextual creation commands. A screenshot of an expanded tree alone does not verify these flows. Directory fallback for old provider history must refuse ambiguous same-directory workspace identities. New parent selection also needs a node capability gate: older nodes can silently ignore an additive request field. On macOS a List may expose a full-width selectable accessibility row while a borderless button only takes clicks on its label: an automated click at the row's center can hit blank space. Verify the actual button hit region before diagnosing an unresponsive action. On a narrow screen, show a chosen destination's name separately from its breadcrumb; truncating a long ancestor path can otherwise hide the selected workspace entirely.

App-only workspace glyphs that use `scaledFrame` belong in `Persona/DesignSystem`, not `Shared/Design`: the watch also compiles Shared, but does not compile the app's text-size view modifiers. Verify the iOS build including its watch dependency after adding a shared-looking component.

Custom worktree Agent rows must keep the shared list's accessibility behavior when the shared row changes. At accessibility text sizes, move model and age below the title and let the row grow; fixed-width metadata beside the title can leave only a few visible characters. Check the actual narrow-screen row at an accessibility size as well as the default size, in light and dark appearances.

A recursive workspace card occupies one List cell. Agent controls inside it must not reuse a whole session row: its List insets and swipe actions propagate to the parent cell. Use a bounded button for each Agent, keep explicit context-menu actions on that control, and expose the card's accessibility children independently. Match the source status lane and List content margins as well as the card's own padding when comparing sidebar indentation.

Creation from a sidebar menu must carry the clicked container's source ID even when it has no Agents. Reusing a nearby session or enclosing project selects the wrong parent. A title's `.contextMenu` inside a recursive List cell can use the entire cell as its hit and preview region. Use an independent control for each workspace; do not attach menu traits to the cell. On iOS it is a `Menu` whose primary action opens the worktree's first direct Agent. Exercise root, nested and empty-folder entry points through the real create form, including changing only the code source for a folder. Scroll the entire title into view before a gesture: a runtime accessibility tree can report a partly clipped row as visible, while its center is behind a bottom bar. Verify the form's actual source and parent and the manager's resulting lineage, rather than counting a menu gesture as success.

The Agent menu inside that cell must follow the same rule. On Mac, its `.contextMenu` can replace the workspace menu even when right-clicking a different child's title. Workspace and Agent controls use plain buttons; a transparent AppKit secondary-click layer supplies each control's `NSHostingMenu` within its own bounds, preserving the SwiftUI environment and existing menu actions. Primary/option clicks and scrolling pass through. Verify root, Agent and child targets in an actual hosted List, then check the live workspace menu; a phone long-press alone cannot verify Mac right-click routing.

Keep a workspace's title and branch inside one control, with Agent rows and descendants outside its hit region. In the Mac repository view, legacy `NavigationLink(value:)` Agent rows can retain the split view's implicit selection while workspace buttons change the explicit detail path. Use the same path-replacing buttons for both, disable system cell selection, and draw the selected state from the opened destination. A hosted split test must open an Agent, alternate parent and child worktrees through both title and branch, then return to a specific Agent; checking only the changed path misses a detail column that stayed on the old page. Use different source and display orders in the fixture to verify which Agent opens. Selecting an empty worktree must clear navigation without inventing a details destination. Inspect the empty pane in live acceptance as well: a retained navigation root may not call `onAppear` again when its pushed page closes, so an appearance flag alone cannot prove which page is visible. Present New Session for a newly created child only after the creation sheet dismisses.

Hit routing alone does not verify a context menu. Let the worktree card supply one menu factory to its title/branch and every direct Agent control, passing the owning worktree explicitly. Keep worktree commands common and retain session commands for the clicked Agent; a multi-Agent worktree's title offers named Agent submenus rather than silently choosing one. Tests must inspect the actual hosted menu items and invoke New Child Worktree from each region, asserting the exact parent identity. A placeholder menu that only records which view was hit misses missing commands and incorrect creation targets. Keep menu equality tied to command-relevant identities, availability and Agent capabilities; transcript timestamps and sidebar status alone must not replace a menu while it is open. User-facing Orca containers are called worktrees; API and accessibility identifiers remain stable for compatibility.

Repository badges describe a boundary with the direct parent, not a difference from the outer project heading. In a mixed-repository tree, show the badge when the repository changes, then omit it on descendants in that same repository. Compare host-scoped project IDs first; older catalogs can fall back to repository paths and the nearest known repository name. Comparing every descendant to the outer heading repeats the same badge down an entire branch, while comparing names alone hides boundaries between distinct repositories with equal names.

`NSHostingMenu` can extract a custom label's underlying image at its original asset size, ignoring the SwiftUI frame around a resizable `AgentMark`. Multi-Agent submenu labels then become hundreds of points tall. Supply a native image drawn into a menu-sized box instead, and test the actual hosted `NSMenuItem.image.size` with a branded Agent fixture. A fixture without a known Agent brand only checks the fallback letter tile and misses this regression.

An optional name in the UI needs a host-owned creation mode. Generating a branch on the client and sending it as an explicit override makes Orca treat it as user-chosen, which can disable its content-based naming. Advertise automatic naming separately from parent selection for every host that can create worktrees. Hosts without a native generator choose a unique name on the node and reuse checked creation. Send a boolean intent through the hub allowlist, and omit the branch override in Orca's request. Keep manual branch/path checks and return the host's actual result. A hand-edited path without a branch is still a manual request. Test old capability responses, boolean/string flags, source HEAD, cross-project parents, and branded native menu images.

A Mac `Form` can allocate a nested `TextField` to its label while the actual editor has no useful width. Separate the visible field label from the editor, hide the TextField’s own label, apply the native rounded input style and let it fill the row. Test the real AppKit editor’s bounds and edit its value through the field editor in light and dark appearances. Keep accessibility identifiers on the Advanced toggle itself: putting one on a DisclosureGroup propagates it over the nested input identifiers.

A worktree's display name and its Agents' conversation names are different commands. Do not reuse the first Agent's rename dialog for a container: keep an explicit container ID and a separate single-field action, with Agent actions in named submenus. The legacy herdr/Claude dialog's tab and conversation fields do not describe every runtime. Send only the applicable fields, and publish a local name change only after the server accepts it. A successful request must also be checked against the resulting host name; a menu opening or an optimistic list update proves neither persistence nor the target.

## A picker's menu drops an option's second line; a DisclosureGroup's chevron ignores the tint (2026-11-09, #1643)

- **Picker in a menu**: Run Agent Here wanted to say why a launcher is unavailable before it is picked. An option drawn as
  `VStack { Text(name); Text(reason) }`, or a `Label` whose title is two `Text`s, shows only the first line in the
  `Picker`'s menu (iOS 27 simulator; a `Menu` button's second `Text` does become a subtitle, a `Picker` option's does
  not). What a picker menu does show is a `Section`'s title: the blocked launchers go under a section per reason
  (`LauncherChoices.blockedByReason`), after the usable ones. The runtime CLI's `activate` cannot choose a menu item (it
  needs a real touch through `idb`), so a DEBUG switch picks the launcher for the screenshot instead (`-showRunHereLauncher`).
- **DisclosureGroup**: `.tint(.secondary)` on it leaves its chevron in the primary color, so a gray "Details" label sat
  next to a black / white chevron that read as a navigation link. Use a borderless button with the expand / collapse icon
  in `.secondary`, as `ReadinessCard`'s Details does.

## A menu inside a menu keeps the items it was first built with (2026-11-09, #1644)

- **What went wrong**: the Persona chat's avatar menu nests "Launcher" › each launcher › its models. The models are read a moment after the bar comes up. An `.id` on the innermost menu (built from the list) was not enough: the "Launcher" menu around it had none, kept the launchers' submenus it was built with before the lists came in, and the claude submenu showed only Default and "Other Model…" for as long as the app ran, although the state had all ten models. Whether a run saw it depended on when the outer menu happened to be built first, so a passing UI test proved little.
- **Now**: every level of a nested `Menu` whose items come from state that loads or changes gets an `.id` built from that state (`AssistantTopBar.launcherMenuID` on "Launcher", the list and pick on each launcher). When a menu's items look stale, log the state first: if it is right, the menu was not built again.

## App Intents that say yes to an agent need `.requiresAuthentication` (2026-11-09, #1878)

- An App Intent without `authenticationPolicy` gets `.alwaysAllowed`. Siri runs it on a locked phone, and the hub
  credentials (`kSecAttrAccessibleAfterFirstUnlock`) are readable then, so Approve First Pending allowed permissions and
  approved plans while nobody had unlocked the phone; the notification and Live Activity buttons for the same card asked first.
- An intent that allows a permission or approves a plan declares `.requiresAuthentication`, as notification actions use
  `.authenticationRequired`, and a test pins it (`IntentsTests.approvingFromSiriNeedsUnlock`,
  `FleetIntentsTests.sayingYesNeedsUnlock`). The policy covers the whole intent, so its other replies wait for the unlock too.
  Ask Claude and Give an Agent a Task still send messages from a locked phone; changing that is a separate decision.
- An entry that never shows the plan in full approves plans in `acceptEdits`, never `bypassPermissions`.
