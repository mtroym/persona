import Darwin
import OSLog
import SwiftUI
#if os(iOS)
import UIKit

/// Full Page in a chat's system screenshot (#1609): the screenshot preview's Full Page tab is one PDF page of the chat's
/// loaded lines, oldest first, with the rect the screen showed inside it.
///
/// The chat's list is lazy (rows off screen are not built) and a web view draws nothing off screen, so the page is laid
/// out apart from the list: a strip of lines at a time, in a hosting view placed off screen in the chat's window. Each
/// strip waits for its own pictures and drawn formulas (`FullPageLoads`), is measured again then (they change its
/// height), and is drawn into a PDF of its own; the strips are put one under another on the page. Past a limit (height,
/// time, free memory), when the chat is covered or gone, cancelled or on any failure, the completion gets nil and the
/// preview offers the screen only: a cut PDF is never passed off as the whole chat. The completion is called once.
/// Which shown chat answers a scene's screenshots is `FullPageScene`'s.
@MainActor
final class ChatFullPage {
    /// What the page is drawn from, taken when the screenshot is asked for.
    struct Document {
        /// The loaded lines, oldest first, each as the chat draws it.
        var lines: [Line]
        /// The chat's width and background, and the environment its rows are drawn in (the list's).
        var width: CGFloat
        var background: UIColor
        var environment: EnvironmentValues
        /// The oldest line on screen; nil when the chat is at its newest end.
        var anchor: String?
        /// The height of the chat's visible area.
        var viewport: CGFloat

        struct Line: Identifiable {
            var id: String
            var view: AnyView
        }
    }

    /// Limits that make the completion nil rather than the app slow or killed.
    struct Limits: Equatable {
        /// Taller than this is not drawn at all.
        var maxHeight: CGFloat = 200_000
        /// The whole export, preparing and drawing.
        var maxSeconds: Double = 12
        /// Free memory (`os_proc_available_memory`) under this stops it.
        var minFreeBytes: Int = 150 << 20
        /// How tall one strip drawn at a time may be.
        var stripHeight: CGFloat = 2_400
        /// How long a strip waits for its pictures and formulas.
        var loadSeconds: Double = 4
    }

    /// The outcome, with what was measured (the DEBUG export writes it out; every export logs it).
    struct Report: Codable, Equatable {
        var ok: Bool
        var reason: String?
        var lines: Int
        var strips: Int
        /// Strips drawn before it ended: fewer than `strips` on a failure part way (that PDF is thrown away).
        var stripsDrawn: Int
        var height: Double
        var width: Double
        var seconds: Double
        var memoryBaseline: Int
        var memoryPeak: Int
        var visibleRect: [Double]
        var waitedForLoads: Int
    }

    enum Failure: Error, Equatable {
        case noDocument, covered, empty, tooTall(CGFloat), timedOut, lowMemory(Int), cancelled, pdf
        var text: String {
            switch self {
            case .noDocument: "no document"
            case .covered: "the chat is covered"
            case .empty: "nothing loaded"
            case .tooTall(let h): "too tall (\(Int(h)) pt)"
            case .timedOut: "timed out"
            case .lowMemory(let free): "low memory (\(free >> 20) MB free)"
            case .cancelled: "cancelled"
            case .pdf: "the PDF could not be made"
            }
        }
    }

    /// Set by the chat page while it is shown: what the page holds right now.
    var document: (() -> Document?)?
    /// The list's environment, its rows' (`FullPageEnvironmentReader`).
    var environment: EnvironmentValues?
    var limits = Limits()
    /// The latest outcome (for the DEBUG export and the logs).
    private(set) var lastReport: Report?

    private var shown = false
    /// A view in the chat (`FullPageAnchor`): the window the page is laid out in, the controller that may be covered.
    private weak var anchor: UIView?
    /// The scene this page is shown in (`FullPageScene`).
    private weak var scene: UIWindowScene?
    private var running: Task<Void, Never>?
    /// Bumped when a request is replaced or the page goes: a stale export does not complete.
    private var generation = 0

    // MARK: Showing

    /// A view in the chat moved to a window (nil: it left it).
    func setAnchor(_ view: UIView?) {
        anchor = view
        update()
    }

    /// The chat page shown or not (another tab, a page pushed over it, the app in the background).
    func setShown(_ shown: Bool) {
        self.shown = shown
        update()
    }

    private func update() {
        let now = shown ? anchor?.window?.windowScene : nil
        if let scene, scene !== now {
            FullPageScene.hide(self, in: scene)
            self.scene = nil
            stop()
        }
        if let now, now !== scene {
            FullPageScene.show(self, in: now)
            scene = now
        }
    }

    /// An export still running completes with nil.
    private func stop() {
        generation += 1
        running?.cancel()
        running = nil
    }

    /// This page answers its scene's screenshots.
    var isAnswering: Bool { scene.map { FullPageScene.answering(in: $0) === self } ?? false }

    // MARK: Requests

    /// One request of the screenshot preview (through `FullPageScene`): `completion` gets the page's PDF, page 0 and the
    /// rect the screen showed, or nil (the preview then offers the screen only), once. A newer request, or the page
    /// going, makes this one nil, also while its last strip is drawn.
    func generate(_ completion: @escaping @Sendable (Data?, Int, CGRect) -> Void) {
        let once = Once(completion)
        stop()
        guard shown else { return once.call(nil, 0, .zero) }
        let mine = generation
        running = Task { @MainActor [weak self] in
            guard let self else { return once.call(nil, 0, .zero) }
            let current = { [weak self] in self?.generation == mine && self?.shown == true }
            let result = await self.export(limits: self.limits, current: current)
            if case .success(let pdf) = result, current() {
                once.call(pdf.data, 0, pdf.visibleRect)
            } else {
                once.call(nil, 0, .zero)
            }
            if self.generation == mine { self.running = nil }
        }
    }

    // MARK: Export

    struct PDF {
        var data: Data
        /// In the page's PDF space (origin at the bottom left).
        var visibleRect: CGRect
    }

    /// Draws the page. `current`: false once this export is stale (replaced, the page gone).
    func export(limits: Limits, current: @escaping () -> Bool = { true }) async -> Result<PDF, Failure> {
        let start = Date.now
        let baseline = FullPageMemory.footprint()
        var peak = baseline
        var report = Report(ok: false, reason: nil, lines: 0, strips: 0, stripsDrawn: 0, height: 0, width: 0, seconds: 0,
                            memoryBaseline: baseline, memoryPeak: baseline, visibleRect: [], waitedForLoads: 0)
        func finish(_ r: Result<PDF, Failure>) -> Result<PDF, Failure> {
            report.seconds = Date.now.timeIntervalSince(start)
            report.memoryPeak = max(peak, FullPageMemory.footprint())
            if case .failure(let f) = r { report.ok = false; report.reason = f.text } else { report.ok = true }
            lastReport = report
            FullPageLog.note(report)
            return r
        }
        guard let doc = document?() else { return finish(.failure(.noDocument)) }
        guard !doc.lines.isEmpty, doc.width > 0 else { return finish(.failure(.empty)) }
        report.lines = doc.lines.count
        report.width = doc.width
        guard let window = hostWindow() else { return finish(.failure(.covered)) }

        let host = UIHostingController(rootView: AnyView(EmptyView()))
        // In the window, under its status bar: the safe area would pad every line measured and shift every strip drawn.
        host.safeAreaRegions = []
        host.view.backgroundColor = .clear
        // Off screen, in the chat's window: laid out and drawn like its own rows, never seen.
        host.view.frame = CGRect(x: -doc.width * 4 - 2_000, y: 0, width: doc.width, height: 10)
        window.addSubview(host.view)
        defer { host.view.removeFromSuperview() }

        func checks() -> Failure? {
            if !current() || Task.isCancelled { return .cancelled }
            if Date.now.timeIntervalSince(start) > limits.maxSeconds { return .timedOut }
            let free = os_proc_available_memory()
            if free > 0, free < limits.minFreeBytes { return .lowMemory(free) }
            return nil
        }

        // 1. Each line's height at the chat's width, as it is before its pictures and formulas load: enough to cut the
        //    page into strips and to give up on one too tall. A moment for the app now and then: hundreds of lines.
        var heights: [CGFloat] = []
        heights.reserveCapacity(doc.lines.count)
        var turn = Date.now
        for line in doc.lines {
            if let f = checks() { return finish(.failure(f)) }
            host.rootView = AnyView(Self.wrap(line.view, doc))
            heights.append(ceil(max(host.sizeThatFits(in: CGSize(width: doc.width, height: .greatestFiniteMagnitude)).height, 0)))
            if Date.now.timeIntervalSince(turn) > 0.012 {
                try? await Task.sleep(for: .milliseconds(1))
                turn = .now
            }
        }
        let plan = FullPageLayout(heights: heights, spacing: Theme.Space.l, inset: Theme.Space.l, stripHeight: limits.stripHeight)
        report.strips = plan.strips.count
        report.height = plan.height
        guard plan.height <= limits.maxHeight else { return finish(.failure(.tooTall(plan.height))) }

        // 2. Each strip once its own pictures and formulas are in, measured again then, drawn into a PDF of its own.
        var drawn: [(data: Data, height: CGFloat, tops: [CGFloat])] = []
        for strip in plan.strips {
            if let f = checks() { return finish(.failure(f)) }
            let lines = strip.lines.map { doc.lines[$0] }
            let tops = FullPageTops()
            let since = Date.now
            host.rootView = AnyView(Self.strip(lines, doc, tops: tops))
            host.view.frame.size = CGSize(width: doc.width, height: max(strip.height, 1) + 2 * Self.seam)
            host.view.setNeedsLayout()
            host.view.layoutIfNeeded()
            report.waitedForLoads += await FullPageLoads.shared.settle(since: since, within: limits.loadSeconds)
            if let f = checks() { return finish(.failure(f)) }
            try? await Task.sleep(for: .milliseconds(34)) // two frames for SwiftUI to draw what came
            let drawnHeight = ceil(max(host.sizeThatFits(in: CGSize(width: doc.width, height: .greatestFiniteMagnitude)).height, 1))
            host.view.frame.size = CGSize(width: doc.width, height: drawnHeight)
            host.view.setNeedsLayout()
            host.view.layoutIfNeeded()
            peak = max(peak, FullPageMemory.footprint())
            let height = max(drawnHeight - 2 * Self.seam, 1) // its lines, without the seams
            guard let data = Self.drawn(host.view, width: doc.width, height: drawnHeight) else { return finish(.failure(.pdf)) }
            // Where its lines were drawn; as measured before for one that did not say.
            drawn.append((data, height, zip(strip.lines, lines).map { tops.top($1.id) ?? plan.tops[$0] - strip.y }))
            report.stripsDrawn += 1
        }
        // Cancelled, the page gone, out of time or memory while the last strip was drawn: nil, as before it.
        if let f = checks() { return finish(.failure(f)) }

        // 3. The page: the strips one under another, spaced like the list's lines.
        var lineTops: [CGFloat] = []
        var stripTops: [CGFloat] = []
        var y = Theme.Space.l
        for (i, strip) in drawn.enumerated() {
            if i > 0 { y += Theme.Space.l }
            stripTops.append(y)
            lineTops += strip.tops.map { y + $0 }
            y += strip.height
        }
        let height = y + Theme.Space.l
        report.height = height
        guard height <= limits.maxHeight else { return finish(.failure(.tooTall(height))) }
        let data = NSMutableData()
        var box = CGRect(x: 0, y: 0, width: doc.width, height: height)
        guard let consumer = CGDataConsumer(data: data as CFMutableData),
              let ctx = CGContext(consumer: consumer, mediaBox: &box, nil) else { return finish(.failure(.pdf)) }
        ctx.beginPDFPage(nil)
        ctx.setFillColor(doc.background.resolvedColor(with: UITraitCollection(userInterfaceStyle: doc.environment.colorScheme == .dark ? .dark : .light)).cgColor)
        ctx.fill(box)
        for (strip, top) in zip(drawn, stripTops) {
            guard let provider = CGDataProvider(data: strip.data as CFData), let pdf = CGPDFDocument(provider), let page = pdf.page(at: 1) else {
                ctx.endPDFPage()
                ctx.closePDF()
                return finish(.failure(.pdf))
            }
            ctx.saveGState()
            // PDF space: origin at the bottom left. Its seams overlap the gaps around it, drawn clear but for what rows
            // draw there.
            ctx.translateBy(x: 0, y: height - top - strip.height - Self.seam)
            ctx.drawPDFPage(page)
            ctx.restoreGState()
        }
        ctx.endPDFPage()
        ctx.closePDF()
        let anchor = doc.anchor.flatMap { id in doc.lines.firstIndex { $0.id == id } }
        let visible = FullPageLayout.visibleRect(tops: lineTops, height: height, anchor: anchor, viewport: doc.viewport, width: doc.width)
        report.visibleRect = [visible.minX, visible.minY, visible.width, visible.height].map(Double.init)
        return finish(.success(PDF(data: data as Data, visibleRect: visible)))
    }

    /// The chat's window, when nothing covers the chat: the chat is in the controller presented last. A sheet the chat
    /// is in counts; one over it, or an alert, does not (a screenshot then is of what covers it).
    private func hostWindow() -> UIWindow? {
        guard let view = anchor, let window = view.window, let root = window.rootViewController else { return nil }
        var top = root
        while let next = top.presentedViewController, !next.isBeingDismissed { top = next }
        var controller = view.owningController
        while let c = controller, c !== top { controller = c.parent }
        return controller == nil ? nil : window
    }

    /// A line as the page measures it: the chat's environment, its width, its side margins (the list's 16 pt).
    private static func wrap(_ view: AnyView, _ doc: Document) -> some View {
        view
            .padding(.horizontal, Theme.Space.l)
            .frame(width: doc.width, alignment: .leading)
            .fixedSize(horizontal: false, vertical: true)
            .transaction { $0.disablesAnimations = true }
            .transformEnvironment(\.self) { exported(&$0, doc) }
    }

    /// A strip of lines, spaced as the chat spaces them, each saying where it was drawn. At its own height whatever the
    /// host's frame (offered less, a table's cells were cut to a line and the space they gave up left blank above and
    /// below), from the top, with `seam` above and below for what rows draw into the gaps (a work card's edges).
    private static func strip(_ lines: [Document.Line], _ doc: Document, tops: FullPageTops) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            ForEach(lines) { line in
                line.view.onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .named(FullPageTops.space)).minY }) { tops.set(line.id, $0) }
            }
        }
        .coordinateSpace(.named(FullPageTops.space))
        .fixedSize(horizontal: false, vertical: true)
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, seam)
        .frame(width: doc.width, alignment: .topLeading)
        .frame(maxHeight: .infinity, alignment: .top)
        .transaction { $0.disablesAnimations = true } // what loaded is drawn where it ends, not on its way there
        .transformEnvironment(\.self) { exported(&$0, doc) }
    }

    /// Room drawn above and below each strip: the gap between lines, which rows draw into, is not cut at a strip's edge.
    private static let seam = Theme.Space.l

    /// The chat's environment, marked as drawn for Full Page, in one transform: an `.environment(\.fullPageExport, true)`
    /// around it was replaced with the rest, and HTML cards went on drawing their page, which is empty off screen.
    private static func exported(_ environment: inout EnvironmentValues, _ doc: Document) {
        environment = doc.environment
        environment.fullPageExport = true
    }

    /// A strip drawn into a PDF page of its own, `height` tall (UIKit's coordinates: origin at the top left).
    private static func drawn(_ view: UIView, width: CGFloat, height: CGFloat) -> Data? {
        let data = NSMutableData()
        var box = CGRect(x: 0, y: 0, width: width, height: height)
        guard let consumer = CGDataConsumer(data: data as CFMutableData), let ctx = CGContext(consumer: consumer, mediaBox: &box, nil) else {
            return nil
        }
        ctx.beginPDFPage(nil)
        ctx.translateBy(x: 0, y: height)
        ctx.scaleBy(x: 1, y: -1)
        UIGraphicsPushContext(ctx)
        view.layer.render(in: ctx)
        UIGraphicsPopContext()
        ctx.endPDFPage()
        ctx.closePDF()
        return data as Data
    }

    /// Calls a completion handler once, whatever happens after.
    private final class Once: @unchecked Sendable {
        private var handler: (@Sendable (Data?, Int, CGRect) -> Void)?
        private let lock = NSLock()
        init(_ handler: @escaping @Sendable (Data?, Int, CGRect) -> Void) { self.handler = handler }
        func call(_ data: Data?, _ page: Int, _ rect: CGRect) {
            let h = lock.withLock { () -> (@Sendable (Data?, Int, CGRect) -> Void)? in
                defer { handler = nil }
                return handler
            }
            h?(data, page, rect)
        }
    }
}

/// A scene's screenshot delegate while chat pages are shown in it (#1609): Full Page comes from the page shown last of
/// those still shown. Pages say when they show and when they go, in whatever order SwiftUI calls them (the shell's two
/// pages switching in one update, a chat pushed over another), and the delegate the scene had comes back when none is
/// shown. Before, each page put back the delegate it had found, and a hidden chat could be left answering for the page
/// over it.
@MainActor
final class FullPageScene: NSObject, UIScreenshotServiceDelegate {
    private static var scenes: [ObjectIdentifier: FullPageScene] = [:]

    private weak var scene: UIWindowScene?
    /// The scene's delegate before any page was shown.
    private weak var original: (any UIScreenshotServiceDelegate)?
    /// The pages shown, the latest last.
    private var pages: [Shown] = []

    private struct Shown {
        weak var page: ChatFullPage?
    }

    private init(_ scene: UIWindowScene, original: (any UIScreenshotServiceDelegate)?) {
        self.scene = scene
        self.original = original
    }

    static func show(_ page: ChatFullPage, in scene: UIWindowScene) {
        guard let service = scene.screenshotService else { return }
        let id = ObjectIdentifier(scene)
        // A scene gone without its chats going leaves its entry; another scene can come at the same address.
        scenes = scenes.filter { $0.value.scene != nil }
        let here = scenes[id].flatMap { $0.scene === scene ? $0 : nil } ?? FullPageScene(scene, original: service.delegate)
        scenes[id] = here
        here.pages.removeAll { $0.page == nil || $0.page === page }
        here.pages.append(Shown(page: page))
        if service.delegate !== here {
            // Set by something else since: that one comes back when no chat is shown.
            if let delegate = service.delegate { here.original = delegate }
            service.delegate = here
        }
        FullPageLog.note("shown, \(here.pages.count) in the scene")
    }

    static func hide(_ page: ChatFullPage, in scene: UIWindowScene) {
        let id = ObjectIdentifier(scene)
        guard let here = scenes[id] else { return }
        here.pages.removeAll { $0.page == nil || $0.page === page }
        guard here.pages.isEmpty else { return FullPageLog.note("gone, \(here.pages.count) left in the scene") }
        if let service = scene.screenshotService, service.delegate === here { service.delegate = here.original }
        scenes[id] = nil
        FullPageLog.note("gone, the scene's delegate \(here.original == nil ? "none" : "put back")")
    }

    /// Forgets the chats shown in `scene` and puts its delegate back (tests: the app they run in may show a chat).
    static func forget(_ scene: UIWindowScene) {
        let id = ObjectIdentifier(scene)
        guard let here = scenes[id] else { return }
        if let service = scene.screenshotService, service.delegate === here { service.delegate = here.original }
        scenes[id] = nil
    }

    /// The page that answers the scene's screenshots.
    static func answering(in scene: UIWindowScene) -> ChatFullPage? {
        scenes[ObjectIdentifier(scene)]?.pages.last { $0.page != nil }?.page
    }

    nonisolated func screenshotService(_ screenshotService: UIScreenshotService,
                                       generatePDFRepresentationWithCompletion completionHandler: @escaping @Sendable (Data?, Int, CGRect) -> Void) {
        MainActor.assumeIsolated {
            guard let page = pages.last(where: { $0.page != nil })?.page else { return completionHandler(nil, 0, .zero) }
            page.generate(completionHandler)
        }
    }
}

/// Where each line of a strip was drawn: its top in the strip, by id.
@MainActor
final class FullPageTops {
    static let space = "full-page-strip"
    private var tops: [String: CGFloat] = [:]
    func set(_ id: String, _ y: CGFloat) { tops[id] = y }
    func top(_ id: String) -> CGFloat? { tops[id] }
}

private extension UIView {
    /// The controller whose view this is in.
    var owningController: UIViewController? {
        var responder: UIResponder? = self
        while let r = responder {
            if let c = r as? UIViewController { return c }
            responder = r.next
        }
        return nil
    }
}
#endif

#if os(iOS)
/// Hands Full Page the environment of the chat's list, which its rows read (#1609): the page's rows are drawn in it.
struct FullPageEnvironmentReader: View {
    let page: ChatFullPage
    @Environment(\.self) private var environment

    var body: some View {
        let _ = (page.environment = environment)
        Color.clear
    }
}

/// A view in the chat for Full Page (#1609), told when it moves to a window or leaves it: its window is where the page is
/// laid out, its controller what may be covered.
struct FullPageAnchor: UIViewRepresentable {
    var moved: (UIView?) -> Void

    func makeUIView(context: Context) -> Probe {
        let probe = Probe()
        probe.isUserInteractionEnabled = false
        probe.moved = moved
        return probe
    }

    func updateUIView(_ probe: Probe, context: Context) { probe.moved = moved }

    final class Probe: UIView {
        var moved: ((UIView?) -> Void)?
        override func didMoveToWindow() {
            super.didMoveToWindow()
            moved?(window == nil ? nil : self)
        }
    }
}
#endif

/// Where each line goes on the page, the strips drawn at a time, and where the screen was (#1609). Pure: heights in.
nonisolated struct FullPageLayout: Equatable {
    struct Strip: Equatable {
        /// Lines (indexes), the strip's top on the page and its height.
        var lines: [Int]
        var y: CGFloat
        var height: CGFloat
    }

    /// Each line's top on the page.
    let tops: [CGFloat]
    let heights: [CGFloat]
    let strips: [Strip]
    let height: CGFloat

    /// `inset`: room above the first line and under the last. A line taller than a strip is a strip of its own.
    init(heights: [CGFloat], spacing: CGFloat, inset: CGFloat, stripHeight: CGFloat) {
        var tops: [CGFloat] = []
        var y = inset
        for (i, h) in heights.enumerated() {
            if i > 0 { y += spacing }
            tops.append(y)
            y += h
        }
        self.tops = tops
        self.heights = heights
        height = heights.isEmpty ? 0 : y + inset
        var strips: [Strip] = []
        var current: [Int] = []
        var top: CGFloat = 0
        for i in heights.indices {
            let start = tops[i]
            if current.isEmpty {
                top = start
            } else if tops[i] + heights[i] - top > stripHeight {
                let last = current.last!
                strips.append(Strip(lines: current, y: top, height: tops[last] + heights[last] - top))
                current = []
                top = start
            }
            current.append(i)
        }
        if let last = current.last {
            strips.append(Strip(lines: current, y: top, height: tops[last] + heights[last] - top))
        }
        self.strips = strips
    }

    /// The rect the screen showed, in the page's PDF space (origin at the bottom left): from the top of the oldest line
    /// on screen, a viewport high; at the newest end (no anchor), the page's last viewport.
    func visibleRect(anchor: Int?, viewport: CGFloat, width: CGFloat) -> CGRect {
        Self.visibleRect(tops: tops, height: height, anchor: anchor, viewport: viewport, width: width)
    }

    /// The same, for lines whose tops on a page `height` tall are `tops` (as drawn).
    static func visibleRect(tops: [CGFloat], height: CGFloat, anchor: Int?, viewport: CGFloat, width: CGFloat) -> CGRect {
        let h = min(max(viewport, 0), height)
        guard let anchor, tops.indices.contains(anchor) else { return CGRect(x: 0, y: 0, width: width, height: h) }
        let top = min(max(tops[anchor] - (anchor == 0 ? tops[0] : 0), 0), max(height - h, 0))
        return CGRect(x: 0, y: height - top - h, width: width, height: h)
    }
}

/// Pictures and drawn formulas loading (#1609): a strip of the page waits for the ones its own lines began, those begun
/// once it was laid out. What the rest of the app loads meanwhile, or a load that never ends, does not hold the next
/// strip up. Counted where they load (`ChatImageLoader.image`, `MarkdownRenderer`), whatever view asked.
nonisolated final class FullPageLoads: @unchecked Sendable {
    static let shared = FullPageLoads()
    private let lock = NSLock()
    private var loading: [UUID: Date] = [:]

    /// A load began: `end` it with what this returns.
    func begin() -> UUID {
        let id = UUID()
        lock.withLock { loading[id] = .now }
        return id
    }

    func end(_ id: UUID) { lock.withLock { loading[id] = nil } }

    /// Loading now, begun at `since` or later.
    func pending(since: Date) -> Int { lock.withLock { loading.values.filter { $0 >= since }.count } }

    /// Waits until the loads begun since `since` are done (or `within` seconds); returns how many there were then.
    @MainActor func settle(since: Date, within seconds: Double) async -> Int {
        // A strip just laid out begins its loads in its views' tasks: a moment for them to start.
        try? await Task.sleep(for: .milliseconds(50))
        let first = pending(since: since)
        let deadline = Date.now.addingTimeInterval(seconds)
        // Cancelled, the sleep returns at once: no spinning on the main actor until the deadline.
        while pending(since: since) > 0, Date.now < deadline, !Task.isCancelled {
            try? await Task.sleep(for: .milliseconds(40))
        }
        return first
    }
}

/// The app's memory footprint (what the system counts against it), in bytes.
nonisolated enum FullPageMemory {
    static func footprint() -> Int {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
        }
        return kr == KERN_SUCCESS ? Int(info.phys_footprint) : 0
    }
}

/// What happened to Full Page exports, for a bug report: the system log (subsystem persona, category full-page),
/// numbers only.
nonisolated enum FullPageLog {
    private static let log = Logger(subsystem: "persona", category: "full-page")

    static func note(_ line: String) {
        log.notice("\(line, privacy: .public)")
        #if DEBUG
        print("FULLPAGE \(line)")
        #endif
    }

    #if os(iOS)
    static func note(_ r: ChatFullPage.Report) {
        let rect = r.visibleRect.map { String(Int($0)) }.joined(separator: ",")
        note("\(r.ok ? "ok" : "failed: \(r.reason ?? "?")") lines \(r.lines) strips \(r.stripsDrawn)/\(r.strips) height \(Int(r.height)) "
             + "seconds \(String(format: "%.2f", r.seconds)) memory \(r.memoryBaseline >> 20)→\(r.memoryPeak >> 20) MB "
             + "waited \(r.waitedForLoads) visible \(rect)")
    }
    #endif
}

extension EnvironmentValues {
    /// Drawn for Full Page (#1609): views that would run something live (an HTML card's page) draw their static self,
    /// and the chat's rows tell the list nothing (where their headers are, what to subscribe to or fetch).
    @Entry var fullPageExport = false
}
