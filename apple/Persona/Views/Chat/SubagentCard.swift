import PersonaKit
import SwiftUI

/// The subagents of the chat's session by the call that launched them (`tool_use_id`), for their cards
/// (`SubagentCard`): set by the chat, each running or not as its own lines tell it (`Subagent.running(in:)`).
extension EnvironmentValues {
    @Entry var subagentsByCall: [String: Subagent] = [:]
    /// The chat the cards are in: a subagent's own conversation is `<session>@<agent>` on its machine, which its open
    /// card follows (`SubagentRecentSteps`). Nil where there is no chat around the card.
    @Entry var subagentParent: SessionKey? = nil
}

/// How a subagent is doing, as its card says it (#5602, the web's `AgentStatus`): by its call's result, a background
/// one by its notice.
nonisolated enum SubagentCardStatus: Equatable, Sendable {
    case running, done, failed, stopped

    /// From its Agent call, the chat's background tasks and the hub's word on it.
    static func of(_ call: ToolCall, tasks: TaskIndex, agent: Subagent?) -> SubagentCardStatus {
        if let id = call.use.toolUseID, tasks.started.contains(id) {
            // Sent to the background: its receipt is the result; how it went is its notice.
            guard let last = tasks.notes[id]?.last(where: { $0.status != "event" }) else {
                return agent?.running == false ? .stopped : .running
            }
            switch last.status {
            case "failed", "error": return .failed
            case "killed", "stopped": return .stopped
            default: return .done
            }
        }
        guard let result = call.result else { return .running }
        return result.isError == true ? .failed : .done
    }

    /// What a background subagent reported: its last notice's detail (nil before it is over).
    static func report(_ call: String, tasks: TaskIndex) -> String? {
        tasks.notes[call]?.last(where: { $0.status != "event" })?.detail.flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// What an open card shows of a subagent's own conversation: its latest few tool calls, oldest first, each with its
/// result once it is in (the web's `RecentSteps`).
nonisolated enum SubagentRecent {
    static let count = 3

    static func calls(_ items: [Item], last n: Int = count) -> [ToolCall] {
        var results: [String: Item] = [:]
        for it in items where it.kind == .toolResult { if let id = it.toolUseID { results[id] = it } }
        return items.filter { $0.kind == .toolUse }.suffix(n).map { ToolCall(use: $0, result: $0.toolUseID.flatMap { results[$0] }) }
    }

    /// The most calls a fold may hold for the card to open it (`foldToOpen`).
    static let foldLimit = 40

    /// The fold to open for the latest calls: the hub folds the work it replays (`fold_work`), and a subagent's run is
    /// one stretch of work, so only calls made since the card subscribed are out. Its newest fold when fewer than
    /// `count` calls follow it; not a long one (it is fetched whole for three rows: its next steps come in as they happen).
    static func foldToOpen(_ items: [Item]) -> Item? {
        guard let i = items.lastIndex(where: { $0.kind == .fold }), let tools = items[i].fold?.tools,
              (1...foldLimit).contains(tools),
              items[(i + 1)...].filter({ $0.kind == .toolUse }).count < count else { return nil }
        return items[i]
    }
}

/// A subagent's latest steps in its open card (#5602): it follows the subagent's own conversation while shown (the hub
/// pushes what it writes), instead of what the card knew when it opened.
struct SubagentRecentSteps: View {
    @Environment(AppModel.self) private var model
    /// Drawn for Full Page (#1609): what the store has, without following or fetching.
    @Environment(\.fullPageExport) private var exporting
    var key: SessionKey
    @State private var owner = UUID()

    var body: some View {
        let store = model.chat(key)
        let calls = SubagentRecent.calls(store.items)
        let fold = SubagentRecent.foldToOpen(store.items)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if calls.isEmpty {
                if !store.loadedOnce { ProgressView().controlSize(.mini) }
            } else {
                ForEach(calls) { ToolCallRow(call: $0, autoExpand: false) }
            }
        }
        .animation(Theme.Motion.state, value: calls.map(\.id))
        .task(id: fold?.id) { if let fold, !exporting { try? await model.unfold(key, fold) } }
        .onAppear { if !exporting { model.subscribe(key, by: owner) } }
        .onDisappear { if !exporting { model.unsubscribe(key, by: owner) } }
        .accessibilityIdentifier("chat.subagent-steps")
    }
}

/// What a subagent's card says under its title: what it is doing while it runs ("Running the tests…", or
/// "Starting…" before its first call), then how it went and how many steps it took ("Done · 7 steps · 1 min 20 s").
enum SubagentCardText {
    static func line(_ status: SubagentCardStatus, agent: Subagent?, took: Int?) -> String {
        let steps = agent?.steps.flatMap { $0 > 0 ? String(localized: "\($0) steps [Subagent Card]") : nil }
        switch status {
        case .running:
            if let t = agent?.lastTool, let what = (t.summary?.isEmpty == false ? t.summary : t.name) {
                return [what + "…", steps].compactMap { $0 }.joined(separator: " · ")
            }
            return String(localized: "Starting… [Subagent Card]")
        case .done, .failed, .stopped:
            let word = switch status {
            case .failed: String(localized: "Failed [Subagent Card]")
            case .stopped: String(localized: "Stopped [Subagent Card]")
            default: String(localized: "Done [Subagent Card]")
            }
            return [word, steps, took.map { TaskNoteText.took($0) }].compactMap { $0 }.joined(separator: " · ")
        }
    }
}

/// A subagent the agent launched (an Agent / Task call), in its work line where it launched it (#5602, as the web's
/// `SubagentCard` and Claude Desktop): its title, what it is doing or how it went with its steps; open, what it was
/// asked, what it reported, and its conversation (read-only, "View Subagent").
struct SubagentCard: View {
    @Environment(\.taskIndex) private var tasks
    @Environment(\.subagentsByCall) private var agents
    @Environment(\.openSubagent) private var openSubagent
    @Environment(\.openTools) private var openTools
    @Environment(\.chatPin) private var pin
    @Environment(\.subagentParent) private var parent
    var call: ToolCall
    @State private var openHere = Self.debugOpen

    /// Screenshots: `-showSubagentDetails YES` opens every subagent card (its latest steps) without a tap.
    #if DEBUG
    static let debugOpen = UserDefaults.standard.bool(forKey: "showSubagentDetails")
    #else
    static let debugOpen = false
    #endif

    /// An Agent / Task call: drawn as its subagent's card.
    nonisolated static func isAgent(_ call: ToolCall) -> Bool { call.name == "Agent" || call.name == "Task" }
    @State private var unfold = Unfolding()

    private var key: String { "agent-" + call.id }
    private var open: Bool { openTools?.isOpen(key) ?? openHere }
    private var agent: Subagent? { call.use.toolUseID.flatMap { agents[$0] } }
    private var status: SubagentCardStatus { .of(call, tasks: tasks, agent: agent) }

    private var title: String {
        if let d = call.input["description"]?.string, !d.isEmpty { return d }
        return agent?.title ?? String(localized: "Subagent [Task Note Row 70da69]")
    }

    /// How long it ran: from its start to its result (a background one: to its last line, its receipt is written at
    /// once); nil while it runs.
    private var took: Int? {
        guard status != .running, let start = agent?.startedAt ?? call.use.ts else { return nil }
        let id = call.use.toolUseID ?? ""
        let end = tasks.started.contains(id) ? agent?.updatedAt : call.result?.ts
        guard let end, end > start else { return nil }
        return Int(end.timeIntervalSince(start))
    }

    private var tint: Color {
        switch status {
        case .running: Theme.busy
        case .done: Theme.positive
        case .failed: Theme.danger
        case .stopped: .secondary
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, key, animation: Theme.Motion.state, unfold: $unfold, closing: open) {
                    if let openTools { openTools.set(key, !open) } else { openHere.toggle() }
                }
            } label: { header }
                .buttonStyle(.plain)
                .chatPinned(key)
            if open { details.unfolding(unfold).transition(.opacity.combined(with: .move(edge: .top))) }
        }
        .accessibilityIdentifier("chat.subagent-card")
    }

    private var header: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            ZStack {
                if WorkCardSlice.timeline { Circle().fill(Theme.pageBackground).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon) }
                Circle().fill(tint.opacity(0.14)).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                if status == .running {
                    ProgressView().controlSize(.mini).tint(tint)
                } else {
                    ThemedIcon(ToolPresentation.symbol(call.name))
                        .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xLarge)
                        .foregroundStyle(tint)
                }
            }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryStrong).lineLimit(1)
                Text(SubagentCardText.line(status, agent: agent, took: took))
                    .font(Theme.Font.label)
                    .foregroundStyle(status == .failed ? Theme.danger : Color.secondary)
                    .lineLimit(1)
                    .contentTransition(.opacity)
            }
            Spacer(minLength: Theme.Space.xs)
            ThemedIcon(Theme.Icon.disclosure)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(.tertiary)
                .rotationEffect(.degrees(open ? 90 : 0))
                .padding(.top, Theme.Space.xxs)
        }
        .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
        .padding(.horizontal, WorkCardSlice.inset)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityHint(open ? String(localized: "Collapses the details") : String(localized: "Shows the details"))
    }

    @ViewBuilder private var details: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let prompt = call.input["prompt"]?.string, !prompt.isEmpty {
                Text(prompt).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(6)
            }
            if let id = call.use.toolUseID, tasks.started.contains(id) {
                // Sent to the background: its result is the receipt; its report is its notice's (#5886).
                if let report = SubagentCardStatus.report(id, tasks: tasks) {
                    FoldedReport(id: key, text: report, isError: status == .failed) {
                        ResultBlock(text: report, isError: status == .failed, cut: nil)
                    }
                }
            } else if let result = call.result {
                // What it reported, one line until opened (its card's id keeps it open).
                FoldedReport(id: key, text: result.cutText, isError: result.isError ?? false) {
                    ResultBlock(text: result.cutText, isError: result.isError ?? false, cut: result.fullSize.map { (result.id, $0) })
                }
            }
            if let agent, let parent { SubagentRecentSteps(key: ChatView.shownKey(parent, viewing: agent)) }
            if let open = openSubagent, let id = call.use.toolUseID {
                Button { open(id) } label: {
                    Label("View Subagent", systemImage: Theme.Icon.open).font(Theme.Font.labelStrong).lineLimit(1)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
            }
        }
        .padding(.leading, WorkCardSlice.textColumn)
        .padding(.trailing, WorkCardSlice.inset)
        .padding(.bottom, Theme.Space.m)
    }
}

/// Subagents launched one after another (in parallel), as one line of a work line (#5602, the web's `AgentGrid`): "Running
/// 3 agents" while any runs, "Ran 3 agents · 12 steps" after; their cards in a grid, two side by side where there is room
/// (an iPad, a Mac), in the order they were launched. `alone`: the running ones under their folded work line.
struct SubagentGrid: View {
    @Environment(\.taskIndex) private var tasks
    @Environment(\.subagentsByCall) private var agents
    var calls: [ToolCall]
    var first: Bool
    var alone: Bool

    private var running: Int {
        calls.filter { c in SubagentCardStatus.of(c, tasks: tasks, agent: c.use.toolUseID.flatMap { agents[$0] }) == .running }.count
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            LazyVGrid(columns: [GridItem(.adaptive(minimum: Theme.Size.subagentColumn), spacing: 0, alignment: .top)],
                      alignment: .leading, spacing: 0) {
                ForEach(calls, id: \.id) { SubagentCard(call: $0) }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background {
            if alone {
                if !WorkCardSlice.timeline { WorkCardSlice(top: true, bottom: true) }
            } else {
                WorkCardSlice(top: first, bottom: false)
            }
        }
        .overlay(alignment: .top) { if !alone && !first { WorkCardSlice.separator } }
        .accessibilityIdentifier("chat.subagent-grid")
    }

    private var header: some View {
        let n = running
        return HStack(spacing: Theme.Space.m) {
            ZStack {
                if WorkCardSlice.timeline { Circle().fill(Theme.pageBackground).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon) }
                Circle().fill((n > 0 ? Theme.busy : Color.secondary).opacity(0.14)).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                if n > 0 {
                    ProgressView().controlSize(.mini).tint(Theme.busy)
                } else {
                    ThemedIcon(ToolPresentation.symbol("Agent"))
                        .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xLarge)
                        .foregroundStyle(.secondary)
                }
            }
            Text(SubagentGridText.head(count: calls.count, running: n, steps: calls.compactMap { $0.use.toolUseID.flatMap { agents[$0]?.steps } }.reduce(0, +)))
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .contentTransition(.opacity)
        }
        .padding(.top, alone || WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
        .padding(.horizontal, WorkCardSlice.inset)
        .accessibilityElement(children: .combine)
    }
}

/// What a grid of subagents says over its cards.
enum SubagentGridText {
    static func head(count: Int, running: Int, steps: Int) -> String {
        if running > 0 { return String(localized: "Running \(running) agents [Subagent Grid]") }
        let ran = String(localized: "Ran \(count) agents [Subagent Grid]")
        return steps > 0 ? ran + " · " + String(localized: "\(steps) steps in all [Subagent Grid]") : ran
    }

    /// The running work line's words while the agent waits on subagents it launched in the foreground (#5602, the
    /// web's running bar): "2 subagents working — Running the tests… · 1 more"; `short`, the count alone.
    static func working(count: Int, doing: [String], short: Bool) -> String {
        let head = String(localized: "\(count) subagents working [Work Line]")
        guard !short, let now = doing.first else { return head + (short ? "" : "…") }
        let more = doing.count > 1 ? " · " + String(localized: "\(doing.count - 1) more [Work Line]") : ""
        return head + " — " + now + "…" + more
    }
}

/// The subagents the agent waits on (#5602, the web's `agentsWorking`, Claude Desktop's "2 agents working — …"): running
/// ones it launched in the foreground whose calls have no result yet, and what they do, newest call first.
nonisolated struct AgentsWorking: Equatable, Sendable {
    var count: Int
    var doing: [String]

    /// Nil when there are none.
    static func of(_ subagents: [Subagent], tasks: TaskIndex) -> AgentsWorking? {
        let waiting = subagents.filter { a in
            guard a.running, a.background != true, let id = a.toolUseID else { return false }
            return !tasks.answered.contains(id) && !tasks.started.contains(id)
        }
        guard !waiting.isEmpty else { return nil }
        var doing: [String] = []
        for a in waiting.sorted(by: { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }) {
            guard let t = a.lastTool else { continue }
            let summary = t.summary?.trimmingCharacters(in: .whitespaces)
            guard let what = summary?.isEmpty == false ? summary : t.name, !what.isEmpty else { continue }
            if !doing.contains(what) { doing.append(what) }
        }
        return AgentsWorking(count: waiting.count, doing: doing)
    }
}
