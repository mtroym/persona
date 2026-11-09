import SwiftUI
import WatchKit

@main
struct PersonaWatchApp: App {
    @State private var model = WatchModel()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            WatchRootView()
                .onOpenURL { model.openExtensionSurface($0) }
                .environment(model)
                .tint(Theme.accent)
        }
        .onChange(of: phase) { _, p in
            if p == .active {
                Task { await model.refresh() }
                Task { await model.refreshExtensionSurfaces() }
            }
        }
        // The watch face's frog stays current while the app is not open (docs/apple/persona.md).
        .backgroundTask(.appRefresh(WatchModel.petRefresh)) { await model.backgroundRefresh() }
    }
}

/// Watch state: seeded by the phone over WatchConnectivity, refreshed from the hubs directly while open. With several
/// groups of hubs on the phone (docs/protocol/multi-hub.md「手表」) the watch gets each group's credential and shows them merged;
/// what it does goes to the group of what it acts on.
@Observable
final class WatchModel {
    /// Each group's credential ("" = the default group); one group from a phone before several hubs.
    var configs: [String: ServerConfig] = [:]
    @ObservationIgnored var extensionDeviceIDs: [String: String] = [:]
    var extensionCapabilities: [String: [String]] = [:]
    var extensionSurface: WatchExtensionDestination?
    /// What the groups are called (labels, when there are several).
    var names: [String: String] = [:]
    /// The phone's groups it could not get the watch a credential for yet (#1185); the watch says so and offers to
    /// sync again instead of using the phone's own.
    var missing: [String] = []
    /// The default (or first) group's: the watch is paired while there is one.
    var config: ServerConfig? { configs[""] ?? groups.first.flatMap { configs[$0] } }
    /// The groups, the default one first.
    var groups: [String] { configs.keys.sorted { ($0.isEmpty ? 0 : 1, $0) < ($1.isEmpty ? 0 : 1, $1) } }
    var sessions: [Session] = []
    var interactions: [Interaction] = []
    /// The inbox's Needs You (Feishu messages, mail and connectors' items the AI says need the user), newest first.
    var inbox: [InboxItem] = []
    var machines: [Machine] = []
    var error: String?
    /// Failed groups whose earlier cards and agents stay on screen: each gets a line at the top (#4369).
    var groupFailures: [String] = []
    var lastUpdate: Date?
    /// The persona chosen on the phone: the Milk Frog shows only for its own.
    var persona = UserDefaults.standard.string(forKey: WatchModel.personaKey)
    /// The Milk Frog's files on this watch.
    let pet = WatchPetPack()
    /// The Milk Frog on the watch face.
    @ObservationIgnored private let complication = WatchPetComplication()
    static let petRefresh = "pet"
    private static let petRefreshEvery: TimeInterval = 30 * 60
    private static let personaKey = "persona"
    private var petWanted: Bool { WatchPet.supports(persona) }

    /// The frog's state, once it has something to go on; nil without the Milk Frog or before its files are here.
    var petStatus: WatchPetStatus? {
        guard petWanted, pet.manifest != nil, lastUpdate != nil || error != nil else { return nil }
        #if DEBUG
        // `-showPetState needs_you|working|done|idle|sleep|offline`: the frog in that state (screenshots).
        if let raw = UserDefaults.standard.string(forKey: "showPetState"), let state = WatchPetState(rawValue: raw) {
            return WatchPetStatus(state: state, count: 3)
        }
        #endif
        return WatchPet.status(sessions: sessions, interactions: interactions, unreachable: error != nil,
                               partlyUnreachable: !groupFailures.isEmpty)
    }

    /// Each group's last successful fetch, kept in memory: a group whose refresh failed keeps it instead of being
    /// blanked by the groups that answered (#4369).
    @ObservationIgnored private var groupData: [String: WatchGroupData] = [:]
    /// Interaction uids the wrist already buzzed for, oldest first; persisted across launches and group outages.
    @ObservationIgnored private var seenInteractionUids: [String]
    private static let seenUidsKey = "seenInteractionUids"

    /// The default (or first) group's client: the inbox (each hub has its own; the watch shows the first group's).
    var api: APIClient? { groups.first.flatMap { api($0) } }
    /// A group's client.
    func api(_ group: String) -> APIClient? { configs[group].map { APIClient(config: $0, group: group) } }

    init() {
        NativeStateMigration.prepare()
        seenInteractionUids = UserDefaults.standard.stringArray(forKey: Self.seenUidsKey) ?? []
        if let data = UserDefaults.standard.data(forKey: "configs"), let all = try? JSONDecoder().decode(WatchConfigs.self, from: data) {
            (configs, names, missing) = (all.configs, all.names, all.missing ?? [])
            extensionDeviceIDs = all.deviceIDs ?? [:]
            extensionCapabilities = all.features ?? [:]
        } else if let data = UserDefaults.standard.data(forKey: "config"), let c = try? JSONDecoder().decode(ServerConfig.self, from: data) {
            configs = ["": c]
        }
        #if DEBUG
        if let s = UserDefaults.standard.string(forKey: "pairURL"), let c = ServerConfig(pairURL: s) { configs = ["": c] }
        // `-showWatchPet YES`: the Milk Frog as if chosen on the phone (screenshots).
        if UserDefaults.standard.bool(forKey: "showWatchPet") { persona = PersonaSelection.packageID }
        // `-showWatchMissing Office,Home`: those groups have no credential for the watch (#1185); `""`: the default group,
        // none at all without -pairURL.
        if let s = UserDefaults.standard.string(forKey: "showWatchMissing") {
            missing = s.split(separator: ",", omittingEmptySubsequences: false).map(String.init)
            missing.forEach { names[$0] = $0 }
        }
        #endif
        WatchSync.shared.onConfig = { [weak self] c in
            guard let self else { return }
            guard configs != ["": c] || !names.isEmpty || !missing.isEmpty ||
                  !extensionDeviceIDs.isEmpty || !extensionCapabilities.isEmpty else { return }
            configs = ["": c]
            names = [:]
            missing = []
            extensionDeviceIDs = [:]
            extensionCapabilities = [:]
            syncExtensionSurfaces()
            UserDefaults.standard.set(try? JSONEncoder().encode(c), forKey: "config")
            UserDefaults.standard.removeObject(forKey: "configs")
            Task { await self.refresh() }
            Task { await self.refreshExtensionSurfaces() }
        }
        WatchSync.shared.onConfigs = { [weak self] all in
            guard let self else { return }
            guard configs != all.configs || names != all.names || missing != (all.missing ?? []) ||
                  extensionDeviceIDs != (all.deviceIDs ?? [:]) || extensionCapabilities != (all.features ?? [:]) else { return }
            configs = all.configs
            names = all.names
            missing = all.missing ?? []
            extensionDeviceIDs = all.deviceIDs ?? [:]
            extensionCapabilities = all.features ?? [:]
            syncExtensionSurfaces()
            UserDefaults.standard.set(try? JSONEncoder().encode(all), forKey: "configs")
            Task { await self.refresh() }
            Task { await self.refreshExtensionSurfaces() }
        }
        WatchSync.shared.onPersona = { [weak self] id in
            self?.persona = id
            UserDefaults.standard.set(id, forKey: WatchModel.personaKey)
            Task { await self?.fetchPet() }
        }
        WatchSync.shared.onSummary = { [weak self] s in
            guard let self, WatchGroupRefresh.acceptsSummary(at: s.at, lastUpdate: self.lastUpdate) else { return }
            // The phone's summary is every group merged; split it back so a later failed direct fetch keeps the right
            // group's data, and take the phone's word that the groups it covers are reachable (#4369).
            var byGroup: [String: WatchGroupData] = [:]
            for x in s.sessions { byGroup[x.group, default: .empty].sessions.append(x) }
            for x in s.interactions { byGroup[x.group, default: .empty].interactions.append(x) }
            for x in s.machines { byGroup[x.group, default: .empty].machines.append(x) }
            self.groupData = WatchGroupRefresh.mergeGroups(old: self.groupData, new: byGroup, failures: []).data
                .filter { self.configs[$0.key] != nil }
            self.groupFailures = []
            self.apply(sessions: s.sessions, interactions: s.interactions, machines: s.machines,
                       at: WatchGroupRefresh.dataDate(ofSummaryAt: s.at))
            if let inbox = s.inbox { self.applyInbox(inbox) }
            self.publishPet()
        }
        syncExtensionSurfaces()
        WatchRecordingTransfer.shared.start()
        WatchSync.shared.activate()
        Task { await refreshExtensionSurfaces() }
        Task { await poll() }
    }

    private func apply(sessions: [Session], interactions: [Interaction], machines: [Machine], at: Date = .now) {
        // Agents of quiet tasks and their questions are the main conversation's to tell (#1731), by the hub's word.
        let (sessions, interactions) = QuietTasks.leavingOut(sessions: sessions, interactions: interactions, quiet: \.quiet)
        // A muted agent's or space's card never reaches the wrist (#4360): it is neither shown nor buzzed for.
        let shown = WatchInteractions.leavingMuted(interactions)
        // Only a card seen for the first time buzzes: one that vanished with a failed group and came back with it is
        // already in the persisted record (#4369).
        let freshUids = WatchGroupRefresh.newHapticUids(seen: Set(seenInteractionUids), current: shown)
        func rank(_ s: Session) -> Int { s.state.priority }
        self.sessions = sessions.filter { $0.state.isLive && !$0.assistant || $0.state == .needsYou || $0.state == .working }
            .sorted { rank($0) != rank($1) ? rank($0) < rank($1) : $0.sortDate > $1.sortDate }
        self.interactions = shown
        if !machines.isEmpty { self.machines = machines }
        lastUpdate = at
        guard !freshUids.isEmpty else { return }
        seenInteractionUids = WatchGroupRefresh.rememberSeen(seenInteractionUids, adding: freshUids)
        UserDefaults.standard.set(seenInteractionUids, forKey: Self.seenUidsKey)
        WKInterfaceDevice.current().play(.notification)
    }

    /// A card answered or ended is no longer worth remembering: its uid can buzz again if the hub ever reuses it.
    private func forgetSeen(_ uid: String) {
        guard seenInteractionUids.contains(uid) else { return }
        seenInteractionUids.removeAll { $0 == uid }
        UserDefaults.standard.set(seenInteractionUids, forKey: Self.seenUidsKey)
    }

    private func applyInbox(_ items: [InboxItem]) {
        let open = items.filter { !$0.isDone && $0.triage == .action }
        let hadNew = open.contains { it in !inbox.contains { $0.id == it.id } } && !inbox.isEmpty
        inbox = open
        if hadNew { WKInterfaceDevice.current().play(.notification) }
    }

    /// Every group's agents, requests and machines (each group's hub answers for its own), merged.
    func refresh() async {
        guard let first = api else { return }
        var fetched: [String: WatchGroupData] = [:]
        var failed: Set<String> = []
        var attempted = 0
        var failure: Error?
        let multi = configs.count > 1
        // Every group at once: one hub out of reach held the others back by its requests' 30 s every refresh (#4705).
        let apis = Dictionary(groups.compactMap { g in api(g).map { (g, $0) } }, uniquingKeysWith: { a, _ in a })
        let asked = groups.filter { apis[$0] != nil }
        attempted = asked.count
        await HubFanOut.run(asked, fetch: { g in
            let api = apis[g]!
            async let s = api.sessions()
            async let i = api.interactions()
            async let m = api.machines()
            return try await (s, i, m)
        }) { g, result in
            switch result {
            case .success(let (a, b, c)):
                fetched[g] = WatchGroupData(sessions: a, interactions: b,
                    machines: multi ? c.map { var m = $0; m.groupLabel = names[g] ?? g; return m } : c)
            case .failure(let error):
                failed.insert(g)
                failure = error
            }
        }
        // A failed group keeps its last successful data; only a group that answered replaces it (#4369). Groups the
        // watch no longer holds a credential for are dropped everywhere.
        let merged = WatchGroupRefresh.mergeGroups(old: groupData, new: fetched, failures: failed)
        groupData = merged.data.filter { configs[$0.key] != nil }
        // Every reachable group failing keeps the whole-page offline card (retry, last-updated line); only a partial
        // failure gets the per-group "earlier data" lines.
        let allFailed = failed.count == attempted
        groupFailures = allFailed ? [] : merged.staleFailed.filter { configs[$0] != nil }
        if !fetched.isEmpty {
            let kept = groups.compactMap { groupData[$0] }
            apply(sessions: kept.flatMap(\.sessions), interactions: kept.flatMap(\.interactions),
                  machines: kept.flatMap(\.machines))
        }
        if let xx = try? await first.inbox(connectors: true) { applyInbox(xx) }
        error = failure.flatMap { allFailed ? $0.watchShown : nil }
        await fetchPet()
        publishPet()
    }

    /// The watch face's frog follows what this refresh learned; a face from before the frog was chosen goes away.
    private func publishPet() {
        complication.publish(petWanted ? petStatus : nil, at: lastUpdate, pack: pet)
        if petWanted { schedulePetRefresh() }
    }

    /// Wakes the app in the background now and then, so the face does not go stale while the app is closed. watchOS
    /// decides when; past an hour without one the face says it does not know (WatchPetSnapshot.staleAfter).
    private func schedulePetRefresh() {
        WKApplication.shared().scheduleBackgroundRefresh(withPreferredDate: .now.addingTimeInterval(Self.petRefreshEvery),
                                                         userInfo: Self.petRefresh as NSString) { _ in }
    }

    func backgroundRefresh() async {
        await refresh()
    }

    /// The first group's hub, like the inbox's: every hub has the same built-in package.
    private func fetchPet() async {
        guard petWanted, let api else { return }
        await pet.fetch(api)
    }

    private func poll() async {
        while true {
            try? await Task.sleep(for: .seconds(8))
            if WKApplication.shared().applicationState == .active { await refresh() }
        }
    }

    /// A card on no session can always be answered (#4059); else as its session's source allows.
    func canAnswer(_ ix: Interaction) -> Bool { ix.canAnswer(session: sessions.first { $0.key == ix.sessionKey }) }

    func resolve(_ ix: Interaction, _ r: Resolution) async {
        // To the hub of the interaction's own group.
        guard canAnswer(ix), let api = api(ix.group) else { return }
        do {
            _ = try await api.resolve(ix.id, r)
            interactions.removeAll { $0.uid == ix.uid }
            forgetSeen(ix.uid)
            WKInterfaceDevice.current().play(.success)
        } catch let e as APIError where e.status == 410 {
            // Already ended on server: remove interaction cleanly without error
            interactions.removeAll { $0.uid == ix.uid }
            forgetSeen(ix.uid)
            WKInterfaceDevice.current().play(.directionDown)
        } catch {
            self.error = error.watchShown
            WKInterfaceDevice.current().play(.failure)
        }
    }

    /// Each message carries an id (Codex app-server and runtime sessions refuse one without, codex_send_guard); a tap
    /// again after a failure sends the same id, so the hub types it at most once (#1185).
    @ObservationIgnored private var retries = RetryIDs<Unsent>()
    private struct Unsent: Hashable {
        var key: SessionKey
        var text: String
    }

    func send(_ key: SessionKey, _ text: String) async {
        guard sessions.first(where: { $0.key == key })?.canSend == true, let api = api(key.group) else { return }
        let unsent = Unsent(key: key, text: text)
        do {
            try await api.send(key, text: text, clientMsgID: retries.id(for: unsent))
            retries.sent(unsent)
            WKInterfaceDevice.current().play(.success)
        } catch {
            self.error = error.watchShown
            WKInterfaceDevice.current().play(.failure)
        }
    }

    func setDone(_ item: InboxItem) async {
        guard let api else { return }
        do {
            try await api.setInboxDone(item.id, true)
            inbox.removeAll { $0.id == item.id }
            WKInterfaceDevice.current().play(.success)
        } catch {
            self.error = error.watchShown
            WKInterfaceDevice.current().play(.failure)
        }
    }

    func reply(_ item: InboxItem, _ text: String) async -> Bool {
        guard let api else { return false }
        do {
            try await api.inboxReply(item.id, text: text)
            inbox.removeAll { $0.id == item.id }
            WKInterfaceDevice.current().play(.success)
            return true
        } catch {
            self.error = error.watchShown
            WKInterfaceDevice.current().play(.failure)
            return false
        }
    }

    func interrupt(_ key: SessionKey) async {
        guard sessions.first(where: { $0.key == key })?.canInterrupt == true, let api = api(key.group) else { return }
        // The stop haptic says the Agent stopped, so it plays only once the hub accepts the interrupt.
        do {
            try await api.interrupt(key)
            WKInterfaceDevice.current().play(.stop)
        } catch {
            self.error = error.watchShown
            WKInterfaceDevice.current().play(.failure)
        }
    }

    func machine(_ ref: MachineRef) -> Machine? { machines.first { $0.ref == ref } }
}

struct WatchRootView: View {
    @Environment(WatchModel.self) private var model
    @State private var openedItem: InboxItem?
    @State private var recordingOpen = false
    #if DEBUG
    @State private var openedInteraction: Interaction?
    @State private var openedSession: Session?
    @State private var openedPetPage: String?
    /// `-openWatchItem running`: the list without Needs You, so the running sessions are on screen (screenshots).
    private static let runningFirst = UserDefaults.standard.string(forKey: "openWatchItem") == "running"
    /// `-openWatchItem inbox`: Needs You without the agents' requests, so the inbox rows are on screen (screenshots).
    private static let inboxFirst = UserDefaults.standard.string(forKey: "openWatchItem") == "inbox"
    #else
    private static let runningFirst = false
    private static let inboxFirst = false
    #endif

    var body: some View {
        NavigationStack {
            if model.config == nil {
                WatchPairView()
            } else {
                List {
                    if let status = model.petStatus {
                        WatchPetRow(status: status).watchCard()
                    }
                    // A group this refresh could not reach: its cards and agents below are its earlier ones (#4369).
                    if !model.groupFailures.isEmpty {
                        Section {
                            ForEach(model.groupFailures, id: \.self) { g in
                                Label {
                                    Text(model.names[g].flatMap { $0.isEmpty ? nil : $0 }
                                         .map { String(localized: "Can't reach “\($0)”; showing older data") }
                                         ?? String(localized: "Can't reach a hub group; showing older data"))
                                        .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                                } icon: {
                                    Image(systemName: Theme.Icon.offline).foregroundStyle(Theme.caution)
                                }
                                .watchCard()
                            }
                        }
                    }
                    NavigationLink { WatchRecordingView() } label: {
                        Label("Record [Recordings Section ef2821]", systemImage: Theme.Icon.recording).font(Theme.Font.secondaryMedium).lineLimit(1)
                    }.watchCard()
                    // A group the phone could not get the watch a credential for: none of its agents is below (#1185).
                    if !model.missing.isEmpty {
                        Section {
                            Text("No watch credential yet for “\(model.missing.map { model.names[$0] ?? $0 }.joined(separator: String(localized: "、", comment: "Separator between names in a list")))”")
                                .font(.footnote).foregroundStyle(.secondary).lineLimit(3)
                                .watchCard()
                            WatchSyncButton().watchCard()
                        }
                    }
                    if !model.interactions.isEmpty || !model.inbox.isEmpty, !Self.runningFirst {
                        Section(StateName.needsYou.text) {
                            ForEach(Self.inboxFirst ? [] : model.interactions, id: \.uid) { ix in
                                NavigationLink { WatchInteractionView(interaction: ix) } label: {
                                    VStack(alignment: .leading, spacing: Theme.Watch.lineSpacing) {
                                        Label(ix.kind.watchTitle, systemImage: ix.kind.watchSymbol)
                                            .font(.footnote.weight(.semibold))
                                            .foregroundStyle(Theme.needsYou)
                                        Text(ix.watchSummary).font(.headline).lineLimit(3)
                                        Text(ix.taskTitle.map { String(localized: "Task · \($0)") } ?? ix.sessionTitle ?? "")
                                            .font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                }
                                .watchCard()
                            }
                            ForEach(model.inbox) { it in
                                NavigationLink { WatchInboxItemView(item: it) } label: { WatchInboxRow(item: it) }
                                    .watchCard()
                            }
                        }
                    }
                    if let e = model.error {
                        // Offline: say so first; what is below (if anything) is from the last refresh that worked.
                        Section {
                            Label {
                                VStack(alignment: .leading, spacing: Theme.Watch.lineSpacing) {
                                    Text("Can't reach the hub").font(.headline)
                                    if let t = model.lastUpdate {
                                        Text("Data from \(t.formatted(.relative(presentation: .named)))").font(.footnote).foregroundStyle(.secondary)
                                    }
                                    Text(e).font(.footnote).foregroundStyle(.secondary).lineLimit(3)
                                }
                            } icon: {
                                Image(systemName: Theme.Icon.offline).foregroundStyle(Theme.danger)
                            }
                            .watchCard()
                            Button { Task { await model.refresh() } } label: { Text("Retry [Components b8784c]").lineLimit(1) }
                                .watchCard()
                        }
                    }
                    if model.lastUpdate != nil || model.error == nil {
                    Section("Running [Session Panes 1f0eb9]") {
                        if model.lastUpdate == nil {
                            HStack(spacing: Theme.Space.s) {
                                ProgressView().frame(width: 22, height: 22)
                                Text("Loading [Persona Watch App d04fcb]").font(.footnote).foregroundStyle(.secondary)
                            }
                            .watchCard()
                        } else if model.sessions.isEmpty {
                            Text("No agents running").font(.footnote).foregroundStyle(.secondary)
                                .watchCard()
                        }
                        ForEach(model.sessions) { s in
                            NavigationLink { WatchSessionView(session: s) } label: {
                                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                                    // On the title's first line (titles wrap to two), not centered on the row.
                                    Circle().fill(s.stateTone.dot).frame(width: 8, height: 8)
                                        .alignmentGuide(.firstTextBaseline) { $0[.bottom] + 2 }
                                    VStack(alignment: .leading, spacing: Theme.Watch.lineSpacing) {
                                        WatchTitle(session: s)
                                        Text([s.spaceName, s.slotLabel].compactMap { $0 }.joined(separator: " › ").isEmpty
                                             ? (model.machine(s.machineRef)?.title ?? s.machineID)
                                             : [s.spaceName, s.slotLabel].compactMap { $0 }.joined(separator: " › "))
                                            .font(.footnote).foregroundStyle(.secondary).lineLimit(1)
                                        // A workflow running in the background (#2935): working, not done.
                                        if let run = s.leadWorkflow { WatchWorkflowChip(run: run) }
                                    }
                                }
                            }
                            .watchCard()
                        }
                    }
                    }
                }
                .navigationTitle("Persona")
                .refreshable { await model.refresh() }
                .navigationDestination(item: $openedItem) { WatchInboxItemView(item: $0) }
                #if DEBUG
                .navigationDestination(item: $openedInteraction) { WatchInteractionView(interaction: $0) }
                .navigationDestination(item: $openedSession) { WatchSessionView(session: $0) }
                // `-openInboxFirst YES`: open the first inbox item (screenshots).
                .task {
                    guard UserDefaults.standard.bool(forKey: "openInboxFirst") else { return }
                    for _ in 0..<20 where model.inbox.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                    openedItem = model.inbox.first
                }
                // `-openWatchItem pet|complication`: the Milk Frog's page, or the watch face's frog in every family
                // (screenshots). One destination: two isPresented ones on this stack left both closed.
                .navigationDestination(item: $openedPetPage) { page in
                    if page == "complication" { PetComplicationPreview() } else { WatchPetView() }
                }
                .task {
                    guard let want = UserDefaults.standard.string(forKey: "openWatchItem"), want == "pet" || want == "complication" else { return }
                    for _ in 0..<120 where model.petStatus == nil || (want == "complication" && WatchPetSnapshot.read() == nil) {
                        try? await Task.sleep(for: .milliseconds(500))
                    }
                    openedPetPage = want
                }
                // `-openWatchItem question|plan|permission|screen|session`: open the first request of that kind, or the
                // first running session (screenshots); `session:<session id>` that one. `running` and `inbox` are handled
                // by `runningFirst` / `inboxFirst`.
                .task {
                    guard let want = UserDefaults.standard.string(forKey: "openWatchItem") else { return }
                    for _ in 0..<20 where model.lastUpdate == nil { try? await Task.sleep(for: .milliseconds(500)) }
                    if want == "session" {
                        openedSession = model.sessions.first
                    } else if want.hasPrefix("session:") {
                        openedSession = model.sessions.first { "session:" + $0.sessionID == want }
                    } else if want != "running", want != "inbox" {
                        openedInteraction = model.interactions.first { $0.kind.rawValue == want }
                    }
                }
                #endif
            }
        }
        .sheet(isPresented: $recordingOpen) { NavigationStack { WatchRecordingView() } }
        .sheet(item: Binding(get: { model.extensionSurface }, set: { model.extensionSurface = $0 })) { destination in
            NavigationStack { WatchExtensionSurfacePage(destination: destination) }
        }
        #if DEBUG
        .onAppear { recordingOpen = UserDefaults.standard.bool(forKey: "openRecording") }
        #endif
    }
}

extension View {
    /// A row of the watch's lists as a card (`Theme.Watch`): its own rounded background with a gap to the next card,
    /// padding inside, at least 44 pt tall, so a 1-line and a 3-line row have the same margins.
    func watchCard() -> some View {
        let p = Theme.Watch.cardPadding, half = max(0, Theme.Watch.cardGap - Theme.Watch.listRowSpacing) / 2
        return frame(maxWidth: .infinity, minHeight: Theme.Watch.minRowHeight - p.top - p.bottom, alignment: .leading)
            .listRowInsets(EdgeInsets(top: p.top + half, leading: p.leading, bottom: p.bottom + half, trailing: p.trailing))
            .listRowBackground(
                RoundedRectangle(cornerRadius: Theme.Watch.cardRadius, style: .continuous)
                    .fill(Theme.Watch.cardFill)
                    .padding(.vertical, half)
            )
    }
}

extension InteractionKind {
    var watchTitle: String {
        switch self {
        case .question: String(localized: "Question [Tool Views 387441]")
        case .plan: String(localized: "Plan Approval")
        case .permission: String(localized: "Permission")
        case .screen: String(localized: "Terminal")
        }
    }

    var watchSymbol: String {
        switch self {
        case .question: Theme.Icon.question
        case .plan: Theme.Icon.plan
        case .permission: Theme.Icon.askFirst
        case .screen: Theme.Icon.terminal
        }
    }
}

extension Interaction {
    var watchSummary: String {
        switch kind {
        case .question: questions?.first?.question ?? ""
        case .plan: (plan ?? "").split(separator: "\n").first.map { $0.trimmingCharacters(in: CharacterSet(charactersIn: "# ")) } ?? String(localized: "Plan [Interaction Text 2619dd]")
        case .permission:
            // The password card: its summary names the computer and every command; `secret_run` means nothing here.
            (isSecretRun ? [summary] : [toolName, summary]).compactMap { $0 }
                .joined(separator: String(localized: ": ", comment: "Separator between a label and its value"))
        case .screen: String(localized: "A dialog is open in the terminal")
        }
    }
}

struct WatchInteractionView: View {
    @Environment(WatchModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var interaction: Interaction
    @State private var picks: [String: Set<String>] = [:]

    /// Why the page is greyed out (exactly when `.disabled(!model.canAnswer(interaction))` below holds); worded like
    /// the phone's note (Android `approvalBlockedReason`).
    private var blockedReason: String? {
        guard !model.canAnswer(interaction) else { return nil }
        guard let session = model.sessions.first(where: { $0.key == interaction.sessionKey }) else {
            return String(localized: "This session isn't on the watch. Handle it on your iPhone or computer.")
        }
        return session.canAttach
            ? String(localized: "Attach live on your iPhone first, or handle it on the computer.")
            : String(localized: "This session can't be approved on the watch. Handle it on the computer.")
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Watch.blockSpacing) {
                if let blockedReason {
                    Text(blockedReason).font(Theme.Font.caption).foregroundStyle(Theme.cautionText)
                }
                switch interaction.kind {
                case .question:
                    ForEach(interaction.questions ?? []) { q in
                        Text(q.question).font(.headline)
                            .foregroundStyle(triedEarly && answer(q) == nil ? Theme.cautionText : Color.primary)
                        ForEach(q.options, id: \.label) { opt in
                            Button {
                                if q.multiSelect {
                                    var set = picks[q.question, default: []]
                                    if set.contains(opt.label) { set.remove(opt.label) } else { set.insert(opt.label) }
                                    picks[q.question] = set
                                } else {
                                    picks[q.question] = [opt.label]
                                    if (interaction.questions ?? []).count == 1 { submit() }
                                }
                            } label: {
                                // One line, like every button: "(Recommended)" becomes the Recommended tag the phone shows.
                                let (text, recommended) = QuestionOption.display(opt.label)
                                HStack(spacing: Theme.Space.sm) {
                                    Text(text).font(.footnote).lineLimit(1).minimumScaleFactor(0.8)
                                    if recommended {
                                        Text(LocalizedStringResource("watch.recommended", defaultValue: "Pick", comment: "Tag on the recommended answer, beside it on one watch button: as short as possible")).font(.caption2.weight(.semibold)).foregroundStyle(Theme.ai)
                                            .padding(.horizontal, 5).padding(.vertical, 1)
                                            .background(Theme.aiTint, in: .capsule)
                                            .fixedSize()
                                    }
                                    Spacer(minLength: 0)
                                    if picks[q.question, default: []].contains(opt.label) { Image(systemName: Theme.Icon.done) }
                                }
                            }
                        }
                    }
                    if (interaction.questions ?? []).count > 1 || (interaction.questions ?? []).contains(where: \.multiSelect) {
                        Button("Submit") { submit() }.lineLimit(1).buttonStyle(.borderedProminent).tint(Theme.accent)
                    }
                    if triedEarly && !allAnswered {
                        Label("Some questions are unanswered", systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.caution)
                    }
                case .plan:
                    Text(WatchText.shown(interaction.plan ?? "")).font(.footnote)
                    // acceptEdits like the phone's lock-screen buttons: commands still ask. Auto-run stays on the phone.
                    Button("Approve & Run") { act(Resolution(approve: true, mode: "acceptEdits")) }
                        .lineLimit(1).minimumScaleFactor(0.8).buttonStyle(.borderedProminent).tint(Theme.accent)
                    Button("Send Back [Notification Actions a20f13]") { act(Resolution(approve: false, feedback: "请重新考虑这个计划")) }.lineLimit(1) // i18n-ignore: feedback sent to the agent
                case .permission:
                    Text(interaction.summary ?? "").font(.caption2.monospaced())
                    if interaction.requiresInput {
                        // The hub takes no values from the watch's answer-only credential (#1581).
                        Text("Fill this in on your iPhone or computer to allow it").font(Theme.Font.caption)
                    } else {
                        Button { act(Resolution(allow: true)) } label: { Text("Allow [Notification Actions ce7ef2]").lineLimit(1) }
                            .buttonStyle(.borderedProminent).tint(Theme.accent)
                    }
                    Button { act(Resolution(allow: false)) } label: { Text("Deny").lineLimit(1) }
                case .screen:
                    Text(interaction.screen ?? "").font(.caption2.monospaced())
                    if let d = interaction.dialog, d.multi == true { // check boxes: ticked here, sent together
                        let ticked = picks[d.rev] ?? Set(d.options.filter { $0.kind == nil && $0.checked == true }.map(\.key))
                        ForEach(d.options.filter { $0.kind == nil }, id: \.key) { opt in
                            Button {
                                var set = ticked
                                if set.contains(opt.key) { set.remove(opt.key) } else { set.insert(opt.key) }
                                picks[d.rev] = set
                            } label: {
                                HStack(spacing: Theme.Space.sm) {
                                    Text("\(opt.key). \(opt.label)").lineLimit(1).minimumScaleFactor(0.8)
                                    Spacer(minLength: 0)
                                    if ticked.contains(opt.key) { Image(systemName: Theme.Icon.done) }
                                }
                            }
                            .font(.caption2)
                        }
                        Button(d.submit == "Next" ? String(localized: "Next [Interaction Views f82c68]") : String(localized: "Submit")) {
                            act(Resolution(options: d.options.map(\.key).filter(ticked.contains)))
                        }
                        .lineLimit(1)
                        .buttonStyle(.borderedProminent)
                        .tint(Theme.accent)
                        .disabled(ticked.isEmpty)
                    } else if let d = interaction.dialog { // the hub's reading: answered by option, checked on the computer first
                        ForEach(d.options.filter { $0.kind == nil }, id: \.key) { opt in
                            Button { act(Resolution(option: opt.key)) } label: {
                                Text("\(opt.key). \(opt.label)").lineLimit(1).minimumScaleFactor(0.8)
                            }
                            .font(.caption2)
                        }
                    } else {
                        ForEach(ScreenParser.options(interaction.screen ?? ""), id: \.key) { opt in
                            Button { act(Resolution(keys: [opt.key])) } label: {
                                Text("\(opt.key). \(opt.label)").lineLimit(1).minimumScaleFactor(0.8)
                            }
                            .font(.caption2)
                        }
                    }
                    HStack {
                        Button("Esc") { act(Resolution(keys: ["esc"])) }
                        Button("⏎") { act(Resolution(keys: ["enter"])) }
                    }
                }
                Button("Handle on Computer") { act(.dismissed) }
                    .lineLimit(1).minimumScaleFactor(0.8).font(.caption2).foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.bordered)
        .tint(Theme.primaryText)
        .navigationTitle(interaction.kind.watchTitle)
        .disabled(!model.canAnswer(interaction))
    }

    /// Submit was tapped with a question left: say so under the button until every question has a pick.
    @State private var triedEarly = false

    /// The phone's rule (`QuestionForm.answer`, without its free text): the picked labels in the options' order, nil
    /// when nothing is picked.
    private func answer(_ q: Question) -> String? {
        let set = picks[q.question, default: []]
        guard !set.isEmpty else { return nil }
        return q.options.map(\.label).filter(set.contains).joined(separator: ", ")
    }

    private var allAnswered: Bool { (interaction.questions ?? []).allSatisfy { answer($0) != nil } }

    private func submit() {
        // Like the phone: nothing goes until every question is answered (an empty answer reached the agent as the
        // user's choice and the card counted as answered, #4374).
        guard allAnswered else {
            triedEarly = true
            WKInterfaceDevice.current().play(.retry)
            return
        }
        var answers: [String: String] = [:]
        for q in interaction.questions ?? [] { answers[q.question] = answer(q) }
        act(Resolution(answers: answers))
    }

    private func act(_ r: Resolution) {
        guard model.canAnswer(interaction) else { return }
        Task {
            await model.resolve(interaction, r)
            dismiss()
        }
    }
}

struct WatchSessionView: View {
    @Environment(WatchModel.self) private var model
    @Environment(\.scenePhase) private var phase
    var session: Session
    @State private var reply = ""
    @State private var messages: [WatchMessage] = []
    @State private var liveCards = WatchAssistantFeed()
    @State private var loadingCards = false
    /// The hub's channels, to say which a message came in from (asked once; nothing from a hub without them).
    @State private var channels: [HubChannel]?

    /// The session as the latest refresh has it (the one this page was opened with goes stale): its state follows, and
    /// a new rev (the transcript grew) reloads the messages.
    private var current: Session { model.sessions.first { $0.key == session.key } ?? session }

    /// What reloads the page: a new rev (or refresh) while the app is in front, and coming back to the front.
    private struct Reload: Hashable {
        var rev: Int64? = nil
        var tick: Date? = nil
        var active: Bool
    }

    var body: some View {
        let session = current
        let active = phase == .active
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Watch.blockSpacing) {
                HStack(spacing: Theme.Space.sm) {
                    Circle().fill(session.stateTone.dot).frame(width: 7, height: 7)
                    Text(session.stateLabel).font(.caption2.weight(.semibold)).foregroundStyle(session.stateTone.text)
                }
                if let run = session.leadWorkflow { WatchWorkflowChip(run: run) }
                ForEach(liveCards.lines(messages).suffix(WatchMessage.limit)) { line in
                    switch line {
                    case .message(let it):
                    if let source = it.source {
                        WatchSourceMessage(message: it, source: source)
                    } else if let n = it.injected {
                        // Persona's message for the model (a task's notice, a scheduled run…): its line, centered, never
                        // its words (docs/protocol/protocol.md「Persona 注入的消息」).
                        Label { Text(n.line) } icon: { Image(systemName: InjectedLook.icon(n)).foregroundStyle(InjectedLook.tint(n)) }
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                            .multilineTextAlignment(.center)
                            .frame(maxWidth: .infinity)
                    } else {
                        // A bubble as wide as its words, the user's on the right, set off from the other edge. What the
                        // user sent wears the phone's bubble (`Theme.userBubble`, #2647); another session's message
                        // (`from`) stays quiet like a reply.
                        let own = it.role == "user" && it.from == nil
                        Text(it.text)
                            .font(.footnote)
                            .lineLimit(8)
                            .foregroundStyle(own ? Theme.userBubbleText : .primary)
                            .padding(Theme.Watch.bubblePadding)
                            .background(own ? Theme.userBubble : Color.white.opacity(0.08), in: .rect(cornerRadius: Theme.Radius.control))
                            .padding(it.role == "user" ? .leading : .trailing, 14)
                            .frame(maxWidth: .infinity, alignment: it.role == "user" ? .trailing : .leading)
                        // Came in from a channel (WeChat, the Feishu bot…), as under the phone's bubble.
                        if it.fromCard {
                            Text("From a card").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                                .frame(maxWidth: .infinity, alignment: .trailing)
                        }
                        if it.role == "user", let c = ChannelSource.of(it.clientMsgID, in: channels ?? []) {
                            Text("From \(c.name)").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                                .frame(maxWidth: .infinity, alignment: .trailing)
                        }
                    }
                    case .view(let item):
                        Text(liveCards.fallback(item)).font(Theme.Font.body)
                            .padding(Theme.Watch.bubblePadding)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .accessibilityIdentifier("watch-assistant-view-fallback")
                    }
                }
                if let source = session.sourceSubtitle {
                    Text(source).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                TextField(String(localized: "Reply (dictation OK)"), text: $reply)
                    .disabled(!session.canSend)
                    .onSubmit {
                        guard session.canSend else { return }
                        let t = reply
                        reply = ""
                        guard !t.isEmpty else { return }
                        Task { await model.send(session.key, t); await load() }
                    }
                HStack {
                    Button("Continue") { Task { await model.send(session.key, String(localized: "Continue")) } }.lineLimit(1).disabled(!session.canSend)
                    if session.status.isActive && session.canInterrupt {
                        Button { Task { await model.interrupt(session.key) } } label: { Image(systemName: Theme.Icon.interrupt) }
                            .tint(Theme.danger)
                    }
                }
            }
        }
        .navigationTitle(session.displayTitle)
        .task {
            try? await model.api(session.group)?.post("/api/v1/sessions/\(session.machineID)/\(session.sessionID)/seen")
        }
        // Only while the app is in front: the phone's summaries reach the watch in the background too (while it
        // records), and reloading for each of them there ran into watchOS's background CPU limit (#6256).
        .task(id: Reload(rev: session.rev, active: active)) { if active { await load() } }
        // The model refreshes every 8 s while the app is active; a session without a rev (older hub) still reloads.
        .task(id: Reload(tick: session.rev == nil ? model.lastUpdate : nil, active: active)) {
            if active, session.rev == nil { await load() }
        }
        .task(id: Reload(tick: model.lastUpdate, active: active)) { if active, session.assistant { await loadCards() } }
    }

    private func loadCards() async {
        guard !loadingCards, current.assistant, let api = model.api(session.group) else { return }
        loadingCards = true
        defer { loadingCards = false }
        // Full view snapshots refresh even when no feed item or transcript revision was added.
        for _ in 0..<20 {
            let before = liveCards.cursor
            guard let response = try? await api.feed(conversation: session.sessionID, since: before),
                  !Task.isCancelled, model.api(session.group)?.config == api.config else { return }
            liveCards.receive(response, conversation: session.sessionID)
            guard response.items.count == 500, liveCards.cursor != before else { return }
        }
    }

    private func load() async {
        guard let api = model.api(session.group), let res = try? await api.items(session.key, beforeSeq: nil, turns: 2),
              !Task.isCancelled else { return }
        messages = WatchMessage.latest(res.items)
        if channels == nil, messages.contains(where: { $0.role == "user" && ChannelSource.possible($0.clientMsgID) }) {
            channels = (try? await api.channels()) ?? []
        }
    }
}

/// Shown until the watch has the phone's pairing: pull it from the phone (when nearby) instead of only waiting. A phone
/// that is paired but could not get the watch a credential of its own says so (#1185): the watch never gets the
/// phone's.
struct WatchPairView: View {
    @Environment(WatchModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(spacing: 10) {
                Image(systemName: Theme.Icon.handoff).font(.title2).foregroundStyle(Theme.accent)
                if model.missing.isEmpty {
                    Text("Not paired yet").font(.headline)
                    Text("Once Persona on your iPhone is paired, tap Sync from iPhone below.").font(.footnote).foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                } else {
                    Text("No watch credential yet").font(.headline)
                    Text("When your iPhone is online, tap below to sync again.")
                        .font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                WatchSyncButton()
                // In the scroll view: beside it, it squeezed the page until the sync button was out of sight.
                NavigationLink { WatchRecordingView() } label: {
                    Label("Record [Recordings Section ef2821]", systemImage: Theme.Icon.recording).lineLimit(1)
                }
                .padding(.top, Theme.Space.s)
            }
            .padding()
        }
    }
}

/// Sync from iPhone: asks the phone for the watch's credentials (it tries the hub again for the ones it has not got).
struct WatchSyncButton: View {
    @Environment(WatchModel.self) private var model
    @State private var syncing = false
    @State private var failed = false

    var body: some View {
        VStack(spacing: 10) {
            Button {
                syncing = true
                failed = false
                WatchSync.shared.requestConfig { ok in
                    syncing = false
                    failed = !ok
                    WKInterfaceDevice.current().play(ok && model.missing.isEmpty ? .success : .failure)
                }
            } label: {
                HStack {
                    if syncing { ProgressView() }
                    Text("Sync from iPhone").lineLimit(1)
                }
            }
            .buttonStyle(.borderedProminent)
            .disabled(syncing)
            if failed {
                Text("Can't reach your iPhone. Open Persona on your iPhone, or tap Sync to Apple Watch in its Settings.").font(.caption2).foregroundStyle(Theme.cautionText)
                    .multilineTextAlignment(.center)
            }
        }
    }
}

// MARK: - Inbox

/// One inbox item in Needs You: where it came from, the title, the AI's summary. A connector's item says its source and
/// the label its connector gave it (the hub's words, not the watch's), with the inbox's own symbol: no source's logo.
struct WatchInboxRow: View {
    var item: InboxItem

    private var source: String {
        if item.isLark { return item.kindLabel ?? String(localized: "Lark [Inbox Event Views 7e4419]") }
        if item.connector != nil { return WatchInboxText.connectorSource(item) }
        return String(localized: "Mail [Assistant View 3d708e]")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Watch.lineSpacing) {
            Label {
                Text(source).lineLimit(1)
            } icon: {
                if item.isLark { Image(Theme.Brand.lark).resizable().scaledToFit().frame(width: 14, height: 14) }
                else if item.connector != nil { Image(systemName: Theme.Icon.inbox) }
                else { Image(systemName: Theme.Icon.mailFilled) }
            }
                .font(.footnote.weight(.semibold))
                .foregroundStyle(item.kind == "mention" ? StatusPalette.needsYou.text : Theme.accent)
            Text(item.title).font(.headline).lineLimit(2)
            Text(item.summary?.isEmpty == false ? item.summary! : item.preview).font(.footnote).foregroundStyle(.secondary).lineLimit(3)
        }
    }
}

/// An inbox item on the wrist: the summary and the latest message; AI Reply drafts a reply to read and send,
/// Done files it. A connector's item is read-only here (its reply goes through its own thread on the phone).
struct WatchInboxItemView: View {
    @Environment(WatchModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var item: InboxItem
    @State private var draft: String?
    @State private var drafting = false
    @State private var sending = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Watch.blockSpacing) {
                if let s = item.summary, !s.isEmpty {
                    Text(s).font(.headline)
                }
                Text([item.sender, item.preview].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: String(localized: ": ", comment: "Separator between a label and its value")))
                    .font(.footnote).foregroundStyle(.secondary)
                if item.connector != nil {
                    Text("Reply on Your iPhone").font(.footnote).foregroundStyle(.tertiary)
                } else if let draft {
                    Text(draft)
                        .font(.footnote)
                        .padding(Theme.Watch.bubblePadding)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.accent.opacity(0.25), in: .rect(cornerRadius: Theme.Radius.control))
                    Button {
                        sending = true
                        Task {
                            if await model.reply(item, draft) { dismiss() }
                            sending = false
                        }
                    } label: {
                        if sending { ProgressView() } else { Text("Send").lineLimit(1) }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(sending)
                } else {
                    Button {
                        drafting = true
                        Task {
                            draft = try? await model.api?.inboxDraft(item.id)
                            drafting = false
                            if draft == nil { WKInterfaceDevice.current().play(.failure) }
                        }
                    } label: {
                        if drafting { ProgressView() } else { Label("AI Reply", systemImage: Theme.Icon.ai).lineLimit(1) }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(drafting)
                }
                Button {
                    Task {
                        await model.setDone(item)
                        dismiss()
                    }
                } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done).lineLimit(1) }
            }
        }
        .navigationTitle(item.title)
        .onAppear { if draft == nil { draft = item.agentDraft } } // the AI twin's, ready to send
    }
}

private extension Error {
    /// The watch has no connection title (the phone's Waiting for Network / Connecting…): no network is still worth a line here.
    var watchShown: String? { isOffline ? localizedDescription : shown }
}

/// A running agent's title in the list, with Claude Code's ultracode mark after its last word while that is on (#1307; no
/// switch on the watch). The mark is part of the text: beside the text's frame it sat after the first line of a title on
/// two, between its halves. It comes and goes with a state change, at once with Reduce Motion.
struct WatchTitle: View {
    var session: Session
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let mark = Ultracode.rowMark(session)
        title(mark)
            .font(.headline)
            .lineLimit(2)
            .animation(reduceMotion ? nil : Theme.Motion.state, value: mark)
    }

    /// The label is the whole text's: an image's own inside a `Text` is not read.
    private func title(_ mark: Bool) -> Text {
        let title = session.displayTitle
        guard mark else { return Text(title) }
        let icon = Text(Theme.Icon.image(Theme.Icon.workflow)).font(.footnote.weight(.semibold)).foregroundStyle(Theme.ai)
        return (Text(title) + Text(verbatim: "\u{00A0}") + icon)
            .accessibilityLabel(title + String(localized: ", ", comment: "List separator") + Ultracode.onWords)
    }
}

/// A workflow its agent runs in the background (#2935), on the watch: a small chip 「workflow 169/185」, not expanded.
/// Its count rolls when it changes (#3436, `numericText` is watchOS 10); nothing else moves, no clock.
struct WatchWorkflowChip: View {
    var run: WorkflowRun
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let text = "workflow" + (run.progress.map { " \($0.done)/\($0.started)" } ?? "")
        HStack(spacing: Theme.Space.xs) {
            Image(systemName: Theme.Icon.workflow)
            // Rolls when either number changes: a new agent (169/185 → 169/186) as well as one coming back.
            Text(text).lineLimit(1)
                .contentTransition(.numericText(value: Double((run.progress?.done ?? 0) + (run.progress?.started ?? 0))))
                .animation(reduceMotion ? nil : Theme.Motion.state, value: text)
        }
        .font(.caption2.weight(.semibold))
        .foregroundStyle(Theme.ai)
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.xxs)
        .background(Theme.aiTint, in: .capsule)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(String(localized: "Background \(text)"))
    }
}
