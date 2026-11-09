import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Full Page's requests (#1609): the completion gets the page's PDF or nil, exactly once. Over a limit, out of time part
/// way, replaced by a newer request or with the page gone (also while its last strip is drawn), it gets nil, never a
/// cut PDF. The scene's screenshots go to the chat shown last of those still shown, whatever order they come and go in,
/// and the scene's own delegate comes back when none is shown.
@MainActor @Suite(.serialized)
struct FullPageExportTests {
    /// What a request's completion got, each call.
    final class Calls: @unchecked Sendable {
        private let lock = NSLock()
        private var got: [(data: Data?, page: Int, rect: CGRect)] = []
        func add(_ data: Data?, _ page: Int, _ rect: CGRect) { lock.withLock { got.append((data, page, rect)) } }
        var all: [(data: Data?, page: Int, rect: CGRect)] { lock.withLock { got } }

        /// Waits for the first call, then a moment for a second one that must not come.
        func settle(within seconds: Double = 20) async {
            let end = Date.now.addingTimeInterval(seconds)
            while all.isEmpty, Date.now < end { try? await Task.sleep(for: .milliseconds(20)) }
            try? await Task.sleep(for: .milliseconds(500))
        }
    }

    final class OtherDelegate: NSObject, UIScreenshotServiceDelegate {}

    /// A window of the app's scene with nothing presented, where the chat would be.
    private func makeWindow() throws -> UIWindow {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        window.rootViewController = UIViewController()
        window.isHidden = false
        return window
    }

    /// A page of `lines` lines, 390 points wide, shown in `window`: `line` draws each (a text `height` points tall when
    /// not given).
    private func makePage(_ lines: Int, height: CGFloat = 60, in window: UIWindow, limits: ChatFullPage.Limits = .init(),
                          line: ((Int) -> AnyView)? = nil) -> ChatFullPage {
        let page = ChatFullPage()
        page.limits = limits
        let draw = line ?? { i in AnyView(Text("Line \(i)").frame(height: height)) }
        page.document = {
            ChatFullPage.Document(lines: (0..<lines).map { i in .init(id: "l\(i)", view: draw(i)) }, width: 390,
                                  background: .systemBackground, environment: EnvironmentValues(), anchor: nil, viewport: 600)
        }
        let anchor = UIView()
        window.rootViewController?.view.addSubview(anchor)
        page.setAnchor(anchor)
        page.setShown(true)
        return page
    }

    private func request(_ page: ChatFullPage) -> Calls {
        let calls = Calls()
        page.generate { calls.add($0, $1, $2) }
        return calls
    }

    /// A picture that never loads: it begins when its strip shows it and ends when the strip is taken away.
    struct NeverLoads: View {
        @State private var load: UUID?
        var body: some View {
            Color.gray.frame(height: 60)
                .onAppear { load = FullPageLoads.shared.begin() }
                .onDisappear { if let load { FullPageLoads.shared.end(load) } }
        }
    }

    /// A line that is 40 points as it is first measured and 120 once its picture loaded (a load the strip waits for).
    struct GrowsOnceShown: View {
        @State private var grown = false
        var body: some View {
            Color.gray.frame(height: grown ? 120 : 40)
                .task {
                    let load = FullPageLoads.shared.begin()
                    try? await Task.sleep(for: .milliseconds(150))
                    grown = true
                    FullPageLoads.shared.end(load)
                }
        }
    }

    @Test func aPageWithinTheLimitsIsOnePDFPageWithTheScreensRect() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let page = makePage(20, in: window)
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        #expect(calls.all.count == 1)
        let got = try #require(calls.all.first)
        let data = try #require(got.data)
        let pdf = try #require(CGDataProvider(data: data as CFData).flatMap(CGPDFDocument.init))
        #expect(pdf.numberOfPages == 1)
        // 16 above, 20 lines of 60 with 16 between, 16 under.
        let box = try #require(pdf.page(at: 1)?.getBoxRect(.mediaBox))
        #expect(box.size == CGSize(width: 390, height: 16 + 20 * 60 + 19 * 16 + 16))
        #expect(got.page == 0)
        #expect(got.rect == CGRect(x: 0, y: 0, width: 390, height: 600), "the newest end: the page's last viewport")
        #expect(page.lastReport?.ok == true && page.lastReport?.stripsDrawn == page.lastReport?.strips)
    }

    /// A line whose picture loads after it was first measured is drawn at its height then, not over the next line.
    @Test func aLineIsDrawnAtItsHeightOnceItsContentIsIn() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let page = makePage(1, in: window) { _ in AnyView(GrowsOnceShown()) }
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        let data = try #require(calls.all.first?.data)
        let box = try #require(CGDataProvider(data: data as CFData).flatMap(CGPDFDocument.init)?.page(at: 1)?.getBoxRect(.mediaBox))
        #expect(abs(box.height - (16 + 120 + 16)) < 0.5, "measured once loaded, not at the 40 pt it was first: \(box.height)")
    }

    /// The screen's rect from where the oldest line on screen was drawn: the sixth of twenty 60 pt lines, 396 pt down.
    @Test func theScreensRectStartsAtTheOldestLineOnScreen() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let page = makePage(20, in: window)
        defer { page.setShown(false) }
        page.document = {
            ChatFullPage.Document(lines: (0..<20).map { i in .init(id: "l\(i)", view: AnyView(Text("Line \(i)").frame(height: 60))) },
                                  width: 390, background: .systemBackground, environment: EnvironmentValues(), anchor: "l5", viewport: 600)
        }
        let calls = request(page)
        await calls.settle()
        // 1536 pt tall: the rect's top is 396 pt down, so its bottom is 1536 - 396 - 600 up from the page's.
        #expect(calls.all.first?.rect == CGRect(x: 0, y: 540, width: 390, height: 600))
    }

    /// A chat in a sheet is what the screen shows: not covered by the sheet it is in.
    @Test func aChatInASheetIsNotCoveredByIt() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let sheet = UIViewController()
        window.rootViewController?.present(sheet, animated: false)
        defer { window.rootViewController?.dismiss(animated: false) }
        // In the window once presented (the chat's anchor says so when it moves there).
        for _ in 0..<100 where sheet.view.window == nil { try await Task.sleep(for: .milliseconds(20)) }
        let page = makePage(5, in: window)
        defer { page.setShown(false) }
        let inSheet = UIView()
        sheet.view.addSubview(inSheet)
        page.setAnchor(inSheet)
        let calls = request(page)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data != nil, "\(page.lastReport?.reason ?? "")")
    }

    @Test func tooTallIsNilAndNothingIsDrawn() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        var limits = ChatFullPage.Limits()
        limits.maxHeight = 500
        let page = makePage(20, in: window, limits: limits)
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data == nil)
        #expect(page.lastReport?.reason?.hasPrefix("too tall") == true && page.lastReport?.stripsDrawn == 0)
    }

    /// The first strip is drawn; a picture that never loads holds the next until its wait is up, by when the time is up:
    /// the strip drawn is thrown away.
    @Test func outOfTimePartWayIsNilNotACutPDF() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        var limits = ChatFullPage.Limits()
        limits.stripHeight = 200
        limits.maxSeconds = 1.2
        limits.loadSeconds = 2
        // Strips of two 60 pt lines: the first two lines are text, the rest pictures that never load.
        let page = makePage(6, in: window, limits: limits) { i in i < 2 ? AnyView(Text("Line \(i)").frame(height: 60)) : AnyView(NeverLoads()) }
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data == nil)
        let report = try #require(page.lastReport)
        #expect(report.reason == "timed out")
        #expect(report.stripsDrawn >= 1 && report.stripsDrawn < report.strips, "stopped part way: \(report.stripsDrawn)/\(report.strips)")
    }

    /// The page goes while its only strip waits for a picture: nil, not the strip drawn after it.
    @Test func thePageGoingWhileItsLastStripIsDrawnIsNil() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        var limits = ChatFullPage.Limits()
        limits.loadSeconds = 2
        let page = makePage(2, in: window, limits: limits) { _ in AnyView(NeverLoads()) }
        let calls = request(page)
        try await Task.sleep(for: .milliseconds(400))
        page.setShown(false)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data == nil)
    }

    @Test func aNewerRequestMakesTheOlderOneNil() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let page = makePage(20, in: window)
        defer { page.setShown(false) }
        let older = request(page)
        let newer = request(page)
        await newer.settle()
        await older.settle(within: 1)
        #expect(older.all.count == 1 && older.all.first?.data == nil)
        #expect(newer.all.count == 1 && newer.all.first?.data != nil)
    }

    @Test func thePageGoingMakesARunningRequestNilAndPutsTheScenesDelegateBack() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let service = try #require(window.windowScene?.screenshotService)
        // The app the tests run in may show a chat of its own in this scene.
        FullPageScene.forget(try #require(window.windowScene))
        let other = OtherDelegate()
        let before = service.delegate
        service.delegate = other
        defer { service.delegate = before }
        let page = makePage(20, in: window)
        #expect(page.isAnswering && service.delegate is FullPageScene)
        let calls = request(page)
        page.setShown(false)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data == nil)
        #expect(!page.isAnswering && service.delegate === other)
        // Shown again, then out of its window: answering, then the scene's delegate back again.
        page.setShown(true)
        #expect(page.isAnswering)
        page.setAnchor(nil)
        #expect(!page.isAnswering && service.delegate === other)
        // Not in a window, a request is nil at once.
        page.setShown(false)
        let hidden = request(page)
        await hidden.settle(within: 1)
        #expect(hidden.all.count == 1 && hidden.all.first?.data == nil)
    }

    /// Two chats in one window (the shell's two pages, a chat pushed over another): the one shown last answers, in
    /// whichever order the one going and the one coming are told; once neither is shown, the scene's delegate is back.
    @Test func theChatShownLastAnswersWhicheverOrderTheyComeAndGo() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let service = try #require(window.windowScene?.screenshotService)
        // The app the tests run in may show a chat of its own in this scene.
        FullPageScene.forget(try #require(window.windowScene))
        let other = OtherDelegate()
        let before = service.delegate
        service.delegate = other
        defer { service.delegate = before }
        let agents = makePage(5, in: window)
        // Switching to Assistant in one update: Assistant comes before Agents goes.
        let assistant = makePage(3, in: window)
        agents.setShown(false)
        #expect(assistant.isAnswering && !agents.isAnswering)
        let calls = Calls()
        service.delegate?.screenshotService?(service, generatePDFRepresentationWithCompletion: { calls.add($0, $1, $2) })
        await calls.settle()
        #expect(calls.all.first?.data != nil && assistant.lastReport?.lines == 3, "the shown chat's page")
        // A page pushed over Assistant: nothing is shown, and the hidden Agents chat does not answer for it.
        assistant.setShown(false)
        #expect(service.delegate === other)
        // The other order: Agents goes first, then Assistant comes.
        agents.setShown(true)
        agents.setShown(false)
        assistant.setShown(true)
        #expect(assistant.isAnswering && !agents.isAnswering)
        // A chat pushed over another and closed: the one under it answers again.
        agents.setShown(true)
        #expect(agents.isAnswering)
        agents.setShown(false)
        #expect(assistant.isAnswering)
        assistant.setShown(false)
        #expect(service.delegate === other)
    }

    /// What a line of the page read when it was drawn.
    final class Seen: @unchecked Sendable {
        var exporting: Bool?
        var scheme: ColorScheme?
    }

    struct Probe: View {
        let seen: Seen
        @Environment(\.fullPageExport) private var exporting
        @Environment(\.colorScheme) private var scheme

        var body: some View {
            let _ = note()
            Color.clear.frame(height: 20)
        }

        private func note() -> Bool {
            seen.exporting = exporting
            seen.scheme = scheme
            return true
        }
    }

    /// The lines are drawn in the chat's environment, marked as drawn for Full Page (the mark set around it was lost).
    @Test func theLinesReadTheChatsEnvironmentMarkedForFullPage() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let seen = Seen()
        var environment = EnvironmentValues()
        environment.colorScheme = .dark
        let page = makePage(1, in: window)
        page.document = {
            ChatFullPage.Document(lines: [.init(id: "probe", view: AnyView(Probe(seen: seen)))], width: 390,
                                  background: .systemBackground, environment: environment, anchor: nil, viewport: 600)
        }
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        #expect(calls.all.count == 1 && calls.all.first?.data != nil)
        #expect(seen.exporting == true)
        #expect(seen.scheme == .dark)
    }

    /// An HTML card is drawn as its words and the note that they stand in for its page: a web view draws nothing off
    /// screen. The PDF goes to PERSONA_SHOTS, when set, to be looked at (its words are pictures: no text to search).
    @Test func anHTMLCardIsDrawnAsItsSummary() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let card = AssistantHTMLCard(title: "三个方案的得分", html: "<p>A 62, B 88, C 45</p>", height: 220, // i18n-ignore
                                     fallback: "A 62 分，B 88 分，C 45 分；B 最高。", onReply: { _ in true }, onOpen: { _ in }) // i18n-ignore
        let page = makePage(1, in: window) { _ in AnyView(card) }
        defer { page.setShown(false) }
        let calls = request(page)
        await calls.settle()
        let data = try #require(calls.all.first?.data)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try data.write(to: URL(fileURLWithPath: dir).appending(path: "full-page-html-card.pdf"))
        }
    }

    @Test func noDocumentOrACoveredChatIsNil() async throws {
        let window = try makeWindow()
        defer { window.isHidden = true }
        let page = makePage(5, in: window)
        defer { page.setShown(false) }
        page.document = { nil }
        let none = request(page)
        await none.settle()
        #expect(none.all.count == 1 && none.all.first?.data == nil && page.lastReport?.reason == "no document")

        let shown = makePage(5, in: window)
        defer { shown.setShown(false) }
        window.rootViewController?.present(UIViewController(), animated: false)
        defer { window.rootViewController?.dismiss(animated: false) }
        let covered = request(shown)
        await covered.settle()
        #expect(covered.all.count == 1 && covered.all.first?.data == nil && shown.lastReport?.reason == "the chat is covered")
    }
}
