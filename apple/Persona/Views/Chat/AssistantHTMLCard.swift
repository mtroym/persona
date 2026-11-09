import SwiftUI
import WebKit

// The assistant's free drawing (#2025, docs/assistant/assistant-views.md「自由绘制」): an `html` component is a self-contained page
// (HTML / SVG / canvas / JS) the assistant wrote. It runs in a web view that can reach nothing: no network (a content
// rule list blocks every load but the page itself and inline data), no cookies or storage of the app (a non-persistent
// data store), no navigation away, no new windows, no downloads. Its only way out is `window.persona.reply(text)` /
// `window.persona.open(url)`, one message handler named `persona`; both are shown to the user in a native dialog first,
// and a card sends at most one reply. It runs only while visible in the active tab and scene, and is torn down otherwise.

/// What a card's page asked the app for, read from what it posted to the `persona` handler.
nonisolated enum HTMLCardRequest: Equatable, Sendable {
    /// Send this as the user's reply in the conversation.
    case reply(String)
    /// Open this in the app (`AssistantViewLink.allowed`).
    case open(URL)

    /// A reply's longest text (the same as an action's `reply`).
    static let maxReply = 2000
    /// A link's longest text.
    static let maxURL = 4096

    /// The request in a posted message, nil for anything else: another frame's, another shape, too long, empty, or a
    /// link the app does not open.
    static func parse(_ body: Any, mainFrame: Bool) -> HTMLCardRequest? {
        guard mainFrame, let m = body as? [String: Any], let kind = m["kind"] as? String else { return nil }
        switch kind {
        case "reply":
            guard let t = m["text"] as? String else { return nil }
            let text = t.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty, text.count <= maxReply else { return nil }
            return .reply(text)
        case "open":
            guard let s = m["url"] as? String, s.count <= maxURL, AssistantViewLink.allowed(s), let url = URL(string: s) else { return nil }
            return .open(url)
        default:
            return nil
        }
    }
}

/// What a card may open (an `open` action, a page's `persona.open`): an https page, or one of the app's own places
/// `persona://session|task|kb|file/<something>`. Nothing else: no http, no other app's scheme, no javascript:.
nonisolated enum AssistantViewLink {
    static let places: Set<String> = ["session", "task", "kb", "file"]

    static func allowed(_ url: URL) -> Bool { allowed(url.absoluteString) }

    /// Inspect the original spelling before URL normalizes malformed percent escapes or control characters.
    static func allowed(_ raw: String) -> Bool {
        guard raw.count <= HTMLCardRequest.maxURL, !raw.contains("\\"), validEscapes(raw),
              let decoded = raw.removingPercentEncoding,
              !decoded.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains),
              let components = URLComponents(string: raw), let url = components.url,
              components.user == nil, components.password == nil else { return false }
        switch components.scheme?.lowercased() {
        case "https":
            return url.host()?.isEmpty == false
        case "persona", LegacyNames.urlScheme:
            guard let host = components.host?.lowercased(), places.contains(host),
                  components.port == nil, components.query == nil, components.fragment == nil else { return false }
            let path = components.percentEncodedPath.split(separator: "/", omittingEmptySubsequences: false)
            guard path.first == "" else { return false }
            let parts = path.dropFirst().compactMap { String($0).removingPercentEncoding }
            guard parts.count == path.count - 1,
                  parts.allSatisfy({ !$0.isEmpty && !$0.contains("\\") }) else { return false }
            if host == "file" {
                // The hub's wire format is machine plus one percent-encoded absolute path, not many URL segments.
                return parts.count == 2 && !parts[0].contains("/") && parts[1].hasPrefix("/")
            }
            guard parts.allSatisfy({ !$0.contains("/") }) else { return false }
            switch host {
            case "session": return parts.count == 2
            case "task": return parts.count == 1
            case "kb": return !parts.isEmpty && parts.allSatisfy { $0 != "." && $0 != ".." }
            default: return false
            }
        default:
            return false
        }
    }

    private static func validEscapes(_ text: String) -> Bool {
        let bytes = Array(text.utf8)
        func hex(_ byte: UInt8) -> Bool { (48...57).contains(byte) || (65...70).contains(byte) || (97...102).contains(byte) }
        var i = 0
        while i < bytes.count {
            if bytes[i] == 37 {
                guard i + 2 < bytes.count, hex(bytes[i + 1]), hex(bytes[i + 2]) else { return false }
                i += 3
            } else {
                i += 1
            }
        }
        return true
    }
}

/// One native confirmation at a time and one accepted reply for the card's lifetime. Opens remain usable.
/// Restarting a page may unmute declined requests, but does not clear a completed reply.
nonisolated struct HTMLCardActions: Equatable, Sendable {
    private(set) var pending: HTMLCardRequest?
    private(set) var sending = false
    private(set) var consumed = false
    private(set) var muted = false

    mutating func receive(_ request: HTMLCardRequest, answered: Bool) {
        guard !muted, !sending, pending == nil else { return }
        if case .reply = request, answered || consumed { return }
        pending = request
    }

    mutating func approve(_ request: HTMLCardRequest, answered: Bool) -> Bool {
        guard pending == request, !sending else { return false }
        if case .reply = request, answered || consumed {
            pending = nil
            return false
        }
        pending = nil
        sending = true
        return true
    }

    mutating func complete(_ request: HTMLCardRequest, success: Bool) {
        sending = false
        if case .reply = request { consumed = consumed || success }
    }

    mutating func decline() {
        guard pending != nil else { return }
        pending = nil
        muted = true
    }

    mutating func dismiss() { pending = nil }
    mutating func restart() { pending = nil; muted = false }
}

/// An `html` component in the assistant's reply: its title and Run; once run, the page in its sandbox. A page asking
/// to reply or open something is shown what it asks first.
struct AssistantHTMLCard: View {
    var title: String?
    var html: String
    /// The height the assistant asked for (160–640; kept in that range).
    var height: Int?
    /// What it says in words (shown when it cannot run here).
    var fallback: String?
    /// A reply from this card was already sent (here or on another device): it sends no more.
    var answered = false
    /// Whether this platform and hub allow execution. Disabled cards show only their fallback.
    var enabled = true
    /// Cards automatically run only while visible. The sample may opt out to exercise a manual retry.
    var autoRun = true
    /// Sends the reply (the view action endpoint); false when it did not go.
    var onReply: (String) async -> Bool
    var onOpen: (URL) -> Void
    var version: Int64 = 1
    var bridgeEnabled = true
    var onConfirmedRequest: ((HTMLCardRequest, Int64) async -> Bool)? = nil

    @Environment(\.scenePhase) private var phase
    @Environment(\.tabIsActive) private var tabActive
    /// Drawn for Full Page (#1609): a web view draws nothing off screen, so the card is its title and its words.
    @Environment(\.fullPageExport) private var exporting
    @State private var onScreen = false
    @State private var stopped = false
    @State private var running = false
    @State private var actions = HTMLCardActions()
    @State private var pendingVersion: Int64?
    @State private var executionOwner = UUID()

    private var eligible: Bool { enabled && !exporting && onScreen && tabActive && phase == .active && (running || (autoRun && !stopped)) }
    private var executing: Bool { eligible && HTMLExecutionBudget.shared.owners.contains(executionOwner) }

    static let minHeight = 160
    static let maxHeight = 640
    static let defaultHeight = 320

    static func clampedHeight(_ h: Int?) -> CGFloat {
        CGFloat(min(max(h ?? defaultHeight, minHeight), maxHeight))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                if let title, !title.isEmpty {
                    Text(title).font(Theme.Font.heading).lineLimit(2).accessibilityAddTraits(.isHeader)
                }
                Spacer(minLength: Theme.Space.s)
                if executing {
                    Button("Stop [Assistant H T M L Card ca4d97]", systemImage: Theme.Icon.interrupt) { stop() }
                        .labelStyle(.iconOnly)
                        .themedButton()
                        .accessibilityIdentifier("html-card-stop")
                }
            }
            if executing {
                Text("Page drawn by the assistant · Sandboxed").font(Theme.Font.caption).foregroundStyle(.secondary)
                SandboxedHTMLView(html: html, onRequest: { request in
                    guard executing, bridgeEnabled else { return }
                    actions.receive(request, answered: answered)
                    if actions.pending == request, pendingVersion == nil { pendingVersion = version }
                }, onUnavailable: { stop() })
                .id(version)
                .frame(height: Self.clampedHeight(height))
                .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .accessibilityIdentifier("html-card-page")
            } else {
                idle
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .onScrollVisibilityChange(threshold: Theme.Visibility.richCardThreshold) { onScreen = $0 }
        // Scrolling away, inactive tabs and background scenes release the WebView even in retained shells.
        .onChange(of: eligible) { _, eligible in
            if eligible { _ = HTMLExecutionBudget.shared.acquire(executionOwner) } else { HTMLExecutionBudget.shared.release(executionOwner) }
        }
        .onChange(of: HTMLExecutionBudget.shared.owners) { _, _ in
            if eligible { _ = HTMLExecutionBudget.shared.acquire(executionOwner) }
        }
        .onChange(of: executing) { _, executing in if !executing { actions.dismiss(); pendingVersion = nil } }
        .onDisappear {
            onScreen = false; running = false; actions.dismiss(); pendingVersion = nil
            HTMLExecutionBudget.shared.release(executionOwner)
        }
        .onChange(of: answered) { _, answered in if answered { actions.dismiss() } }
        .onChange(of: html) { _, _ in actions.dismiss(); pendingVersion = nil }
        .onChange(of: version) { _, _ in actions.dismiss(); pendingVersion = nil }
        .alert(alertTitle, isPresented: Binding(get: { actions.pending != nil }, set: { if !$0 { actions.decline() } }), presenting: actions.pending) { request in
            Button(confirmTitle(request)) {
                // Claim the confirmation before yielding: a second tap or an automatic dismissal cannot race it.
                guard let expectedVersion = pendingVersion, expectedVersion == version,
                      actions.approve(request, answered: answered) else { return }
                pendingVersion = nil
                Task { await accept(request, version: expectedVersion) }
            }
            Button("Cancel", role: .cancel) { actions.decline() }
        } message: { request in
            switch request {
            case .reply(let text): Text(text)
            case .open(let url): Text(url.absoluteString)
            }
        }
    }

    private var idle: some View {
        // Full Page draws the card as the words it gave, whole, and says it is their summary (no Run on paper).
        let live = enabled && !exporting
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let fallback, !fallback.isEmpty {
                Text(fallback).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(live ? 4 : nil)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(fallback)
                    .accessibilityIdentifier("html-card-fallback")
            }
            if live {
                Button { running = true; stopped = false; actions.restart() } label: {
                    Label(autoRun ? String(localized: "Retry [Components b8784c]") : String(localized: "Run"), systemImage: Theme.Icon.run).lineLimit(1)
                }
                .themedButton(prominent: true)
                .accessibilityIdentifier("html-card-run")
            }
            Text(live ? String(localized: "Page drawn by the assistant · Sandboxed. Replies and links need your confirmation.") : String(localized: "Page drawn by the assistant · Showing text summary"))
                .font(Theme.Font.caption).foregroundStyle(.secondary)
        }
    }

    private var alertTitle: String {
        switch actions.pending {
        case .open: String(localized: "The Card Wants to Open a Link")
        default: String(localized: "The Card Wants to Reply for You")
        }
    }

    private func confirmTitle(_ r: HTMLCardRequest) -> String {
        if case .open = r { return String(localized: "Open") }
        return String(localized: "Send")
    }

    private func stop() {
        running = false
        stopped = true
        actions.dismiss()
    }

    private func accept(_ r: HTMLCardRequest, version: Int64) async {
        if let onConfirmedRequest {
            actions.complete(r, success: await onConfirmedRequest(r, version))
            return
        }
        switch r {
        case .open(let url):
            onOpen(url)
            actions.complete(r, success: true)
        case .reply(let text):
            actions.complete(r, success: await onReply(text))
        }
    }
}

// MARK: - The sandbox

/// Blocks every load but the page itself (`loadHTMLString` with no base URL is about:blank) and inline data: the
/// page's fetch, XHR, images, fonts, frames from anywhere else never leave the device. One rule a scheme (content rule
/// filters have no `|`).
private let sandboxRules = "[" + (
    [#"{"trigger":{"url-filter":".*"},"action":{"type":"block"}}"#] +
    ["about", "data", "blob"].map { #"{"trigger":{"url-filter":"^\#($0):"},"action":{"type":"ignore-previous-rules"}}"# }
).joined(separator: ",") + "]"

private let sandboxDocumentPrefix = """
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'">
<meta name="viewport" content="width=device-width,initial-scale=1">
"""

/// The only script the app adds: `window.persona`, which posts to the `persona` handler, and a quiet light / dark
/// default the page's own CSS overrides.
private func bridgeScript(instance: String) -> String {
    """
(() => {
  const h = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.persona;
  const send = (m) => { try { h && h.postMessage({ ...m, instance: "\(instance)" }); } catch (_) {} };
  Object.defineProperty(window, "persona", {
    value: Object.freeze({
      reply(text) { send({ kind: "reply", text: String(text) }); },
      open(url) { send({ kind: "open", url: String(url) }); },
    }),
    writable: false, configurable: false,
  });
  const s = document.createElement("style");
  s.textContent = ":root{color-scheme:light dark}body{margin:0;font:-apple-system-body;font-family:-apple-system,system-ui,sans-serif}";
  (document.head || document.documentElement).prepend(s);
})();
"""
}

/// A web view for a card's page that can reach nothing (see the top of this file); its requests come to `onRequest`.
@MainActor
final class SandboxedHTMLCoordinator: NSObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    var onRequest: (HTMLCardRequest) -> Void
    var onUnavailable: () -> Void = {}
    /// The page was put in: from now on every main-frame navigation is cancelled.
    private var started = false
    private var loaded: String?
    private var generation: UInt = 0
    private var loadTask: Task<Void, Never>?
    private var navigation: WKNavigation?
    private var acceptsMessages = false
    private(set) var instance: String?
    private let rulesLoader: @MainActor () async throws -> WKContentRuleList

    init(rulesLoader: @escaping @MainActor () async throws -> WKContentRuleList = SandboxedHTMLCoordinator.compiledRules,
         onRequest: @escaping (HTMLCardRequest) -> Void) {
        self.rulesLoader = rulesLoader
        self.onRequest = onRequest
    }

    static let handler = "persona"

    /// The rules compiled once for the app's life (a few ms; the store keeps them).
    private static var rules: WKContentRuleList?

    static func compiledRules() async throws -> WKContentRuleList {
        if let rules { return rules }
        guard let compiled = try await WKContentRuleListStore.default().compileContentRuleList(
            forIdentifier: "persona-html-card-sandbox", encodedContentRuleList: sandboxRules) else {
            throw CocoaError(.featureUnsupported)
        }
        rules = compiled
        return compiled
    }

    static func makeWebView(_ coordinator: SandboxedHTMLCoordinator) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.defaultWebpagePreferences.allowsContentJavaScript = true
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        config.preferences.isFraudulentWebsiteWarningEnabled = false // no lookup leaves the device
        config.mediaTypesRequiringUserActionForPlayback = .all
        #if os(iOS) || os(visionOS)
        config.dataDetectorTypes = []
        config.allowsPictureInPictureMediaPlayback = false
        config.allowsAirPlayForMediaPlayback = false
        config.allowsInlineMediaPlayback = true
        #endif
        let content = config.userContentController
        // A weak hop: the controller keeps its handler alive, which would keep the coordinator (and its closures).
        content.add(WeakScriptHandler(coordinator), name: handler)
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = coordinator
        web.uiDelegate = coordinator
        web.allowsLinkPreview = false
        web.allowsBackForwardNavigationGestures = false
        #if os(iOS) || os(visionOS)
        web.isOpaque = false
        web.backgroundColor = .clear
        web.scrollView.backgroundColor = .clear
        web.scrollView.bounces = false
        #endif
        return web
    }

    /// Puts the page in once the rules are in place; without the rules nothing is loaded at all.
    func load(_ html: String, in web: WKWebView) {
        guard loaded != html else { return }
        loadTask?.cancel()
        generation &+= 1
        let ticket = generation
        let loader = rulesLoader
        loaded = html
        navigation = nil
        instance = nil
        acceptsMessages = false
        web.stopLoading()
        loadTask = Task { [weak self, weak web] in
            let rules = try? await loader()
            // Compilation may finish after a page update or teardown. Never resurrect that old page.
            guard !Task.isCancelled, let self, let web, generation == ticket else { return }
            started = false
            guard let rules else {
                web.loadHTMLString("<p>\(String(localized: "Couldn't isolate the network, so this card won't run"))</p>", baseURL: nil)
                return
            }
            let controller = web.configuration.userContentController
            controller.removeAllContentRuleLists()
            controller.add(rules)
            controller.removeAllUserScripts()
            let instance = UUID().uuidString
            self.instance = instance
            controller.addUserScript(WKUserScript(source: bridgeScript(instance: instance), injectionTime: .atDocumentStart, forMainFrameOnly: true))
            navigation = web.loadHTMLString(sandboxDocumentPrefix + html, baseURL: nil)
        }
    }

    /// Cancel pending compilation as well as navigation, and detach every bridge before the representable disappears.
    func dismantle(_ web: WKWebView) {
        generation &+= 1
        loadTask?.cancel()
        loadTask = nil
        loaded = nil
        navigation = nil
        instance = nil
        acceptsMessages = false
        web.stopLoading()
        web.navigationDelegate = nil
        web.uiDelegate = nil
        web.configuration.userContentController.removeAllScriptMessageHandlers()
        web.configuration.userContentController.removeAllUserScripts()
        // Clear running timers even if a snapshot temporarily retains the view after SwiftUI releases it.
        web.loadHTMLString("", baseURL: nil)
    }

    /// A terminated content process has no valid bridge. Retry must load even the same HTML into a fresh instance.
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        guard loaded != nil else { return }
        generation &+= 1
        loadTask?.cancel()
        loadTask = nil
        loaded = nil
        navigation = nil
        instance = nil
        acceptsMessages = false
        started = false
        webView.configuration.userContentController.removeAllUserScripts()
        onUnavailable()
    }

    func webView(_ webView: WKWebView, didCommit navigation: WKNavigation!) {
        guard let navigation, navigation === self.navigation else { return }
        acceptsMessages = true
    }

    // MARK: Navigation: the page itself, nothing else

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 preferences: WKWebpagePreferences) async -> (WKNavigationActionPolicy, WKWebpagePreferences) {
        (allows(action), preferences)
    }

    private func allows(_ action: WKNavigationAction) -> WKNavigationActionPolicy {
        if action.shouldPerformDownload { return .cancel }
        let scheme = action.request.url?.scheme?.lowercased() ?? ""
        if action.targetFrame?.isMainFrame == false {
            // A same-origin srcdoc frame can call the parent's bridge. Cards do not need child frames.
            return .cancel
        }
        // The main frame: the one load of the page, then no navigation away (links, location =, form posts).
        guard !started, scheme == "about" else { return .cancel }
        started = true
        return .allow
    }

    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse) async -> WKNavigationResponsePolicy {
        response.canShowMIMEType ? .allow : .cancel
    }

    /// target=_blank, window.open: no new windows.
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction,
                 windowFeatures: WKWindowFeatures) -> WKWebView? { nil }

    /// Page-script dialogs are not a bridge: ignore them instead of letting arbitrary HTML open native prompts.
    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) { completionHandler() }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) { completionHandler(false) }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) { completionHandler(nil) }

    // MARK: The bridge

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard acceptsMessages, message.name == Self.handler,
              let instance, let body = message.body as? [String: Any], body["instance"] as? String == instance,
              let request = HTMLCardRequest.parse(body, mainFrame: message.frameInfo.isMainFrame) else { return }
        onRequest(request)
    }
}

/// Holds the coordinator weakly for the user content controller.
@MainActor
private final class WeakScriptHandler: NSObject, WKScriptMessageHandler {
    weak var target: SandboxedHTMLCoordinator?
    init(_ target: SandboxedHTMLCoordinator) { self.target = target }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(controller, didReceive: message)
    }
}

#if os(iOS) || os(visionOS)
struct SandboxedHTMLView: UIViewRepresentable {
    let html: String
    var onRequest: (HTMLCardRequest) -> Void
    var onUnavailable: () -> Void = {}

    func makeCoordinator() -> SandboxedHTMLCoordinator {
        let coordinator = SandboxedHTMLCoordinator(onRequest: onRequest)
        coordinator.onUnavailable = onUnavailable
        return coordinator
    }

    func makeUIView(context: Context) -> WKWebView {
        let web = SandboxedHTMLCoordinator.makeWebView(context.coordinator)
        context.coordinator.load(html, in: web)
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {
        context.coordinator.onRequest = onRequest
        context.coordinator.onUnavailable = onUnavailable
        context.coordinator.load(html, in: web)
    }

    static func dismantleUIView(_ web: WKWebView, coordinator: SandboxedHTMLCoordinator) {
        coordinator.dismantle(web)
    }
}
#else
struct SandboxedHTMLView: NSViewRepresentable {
    let html: String
    var onRequest: (HTMLCardRequest) -> Void
    var onUnavailable: () -> Void = {}

    func makeCoordinator() -> SandboxedHTMLCoordinator {
        let coordinator = SandboxedHTMLCoordinator(onRequest: onRequest)
        coordinator.onUnavailable = onUnavailable
        return coordinator
    }

    func makeNSView(context: Context) -> WKWebView {
        let web = SandboxedHTMLCoordinator.makeWebView(context.coordinator)
        context.coordinator.load(html, in: web)
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        context.coordinator.onRequest = onRequest
        context.coordinator.onUnavailable = onUnavailable
        context.coordinator.load(html, in: web)
    }

    static func dismantleNSView(_ web: WKWebView, coordinator: SandboxedHTMLCoordinator) {
        coordinator.dismantle(web)
    }
}
#endif

#if DEBUG
extension View {
    /// `-showSampleHTMLCard YES`: html cards in a sheet, one waiting for Run and one running, for screenshots.
    func htmlCardSample() -> some View { modifier(HTMLCardSample()) }
}

private struct HTMLCardSample: ViewModifier {
    @State private var shown = UserDefaults.standard.bool(forKey: "showSampleHTMLCard") || UserDefaults.standard.string(forKey: "showSampleHTMLCard") == "open"
    @State private var replied: String?
    @State private var opened: URL?
    private let showingLinks = UserDefaults.standard.string(forKey: "showSampleHTMLCard") == "open"

    // i18n-ignore
    private static let linkPage = """
        <!doctype html><meta name="viewport" content="width=device-width,initial-scale=1">
        <p>只有确认后才会打开链接。</p>
        <button onclick="persona.open('https://example.com/card')">打开链接</button>
        """

    // i18n-ignore
    static let page = """
        <!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1">
        <style>
          body{padding:12px;color:#1c1c1e} @media (prefers-color-scheme:dark){body{color:#f2f2f7}}
          .bars{display:flex;gap:10px;align-items:flex-end;height:150px}
          .bar{flex:1;border-radius:8px 8px 4px 4px;background:linear-gradient(#5e8bff,#3a63e0);position:relative}
          .bar span{position:absolute;bottom:-20px;width:100%;text-align:center;font-size:12px;opacity:.7}
          button{margin-top:34px;font:inherit;padding:8px 14px;border-radius:10px;border:0;background:#3a63e0;color:#fff}
        </style></head><body>
        <div class="bars" role="img" aria-label="三个方案的得分：A 62，B 88，C 45">
          <div class="bar" style="height:62%"><span>A</span></div>
          <div class="bar" style="height:88%"><span>B</span></div>
          <div class="bar" style="height:45%"><span>C</span></div>
        </div>
        <button onclick="persona.reply('选 B')">选 B</button>
        </body></html>
        """

    func body(content: Content) -> some View {
        content.sheet(isPresented: $shown) {
            NavigationStack {
                ScrollView {
                    VStack(spacing: Theme.Space.l) {
                        AssistantHTMLCard(title: showingLinks ? "链接确认" : "三个方案的得分", // i18n-ignore
                                          html: showingLinks ? Self.linkPage : Self.page, height: 240,
                                          fallback: showingLinks ? "打开前需要确认。" : "A 62 分，B 88 分，C 45 分；B 最高。", // i18n-ignore
                                          autoRun: false, onReply: { replied = $0; return true }, onOpen: { opened = $0 })
                        AssistantHTMLCard(title: showingLinks ? "链接确认（运行中）" : "三个方案的得分（运行中）", // i18n-ignore
                                          html: showingLinks ? Self.linkPage : Self.page, height: 240,
                                          fallback: showingLinks ? "打开前需要确认。" : "A 62 分，B 88 分，C 45 分。", // i18n-ignore
                                          autoRun: true, onReply: { replied = $0; return true }, onOpen: { opened = $0 })
                        if let replied { Text("已回复：\(replied)").font(Theme.Font.secondary).accessibilityIdentifier("html-card-replied") } // i18n-ignore
                        if let opened { Text("已打开：\(opened.absoluteString)").font(Theme.Font.secondary).accessibilityIdentifier("html-card-opened") } // i18n-ignore
                    }
                    .padding(Theme.Space.page)
                }
                .navigationTitle("HTML 卡片") // i18n-ignore
            }
        }
    }
}
#endif
