import CryptoKit
import ImageIO
import MarkdownUI
import PersonaKit
import SwiftUI
import UniformTypeIdentifiers
import WebKit

/// Formulas (KaTeX) and diagrams (Mermaid) drawn into pictures for Markdown (docs/apple/ios.md「公式和图」): one hidden web
/// view loads the bundled `render.html` (no network: its content security policy and a rule list block everything
/// but the bundle), draws one thing at a time and snapshots it; pictures are kept in memory and on disk by what was
/// drawn. Formulas are drawn black and shown as template images, so they take the text's color in both appearances;
/// a diagram is drawn once per appearance in Mermaid's light or dark theme.
@MainActor
final class MarkdownRenderer {
    static let shared = MarkdownRenderer()

    nonisolated enum Kind: String, Sendable {
        case inlineMath, displayMath, mermaid
    }

    struct Failure: Error, LocalizedError {
        var message: String
        var errorDescription: String? { message }
    }

    /// Diagrams wider than this are drawn at this width (and shown smaller, opened full size).
    static let maxWidth: CGFloat = 2048

    private let images = NSCache<NSString, UIImage>()
    private var failures: [String: String] = [:]
    private var web: WKWebView?
    private var loaded: Task<Void, Error>?
    private var queue: Task<Void, Never>?
    private let dir = URL.cachesDirectory.appending(path: "markdown-render", directoryHint: .isDirectory)
    #if os(macOS)
    private var window: NSWindow?
    #endif

    private init() { images.totalCostLimit = 48 << 20 }

    /// Part of every key: bumped when the drawing changes (the bundled scripts, the template), so old pictures go.
    nonisolated static let version = "9"

    nonisolated static func key(_ kind: Kind, _ source: String, dark: Bool, points: CGFloat) -> String {
        let id = [version, kind.rawValue, source, kind == .mermaid && dark ? "dark" : "", String(Int(points * 10))].joined(separator: "\n")
        return SHA256.hash(data: Data(id.utf8)).prefix(16).map { String(format: "%02x", $0) }.joined()
    }

    func cached(_ kind: Kind, _ source: String, dark: Bool, points: CGFloat) -> UIImage? {
        images.object(forKey: Self.key(kind, source, dark: dark, points: points) as NSString)
    }

    /// The picture, drawn when no copy is kept; throws with KaTeX's or Mermaid's message when the source is wrong.
    func image(_ kind: Kind, _ source: String, dark: Bool, points: CGFloat) async throws -> UIImage {
        let key = Self.key(kind, source, dark: dark, points: points)
        if let image = images.object(forKey: key as NSString) { return image }
        if let message = failures[key] { throw Failure(message: message) }
        // A Full Page strip waits for this (#1609).
        let fullPageLoad = FullPageLoads.shared.begin()
        defer { FullPageLoads.shared.end(fullPageLoad) }
        let file = dir.appending(path: key + ".png")
        let scale = Self.screenScale, template = kind != .mermaid
        if let image = await Task.detached(priority: .userInitiated, operation: { Self.decode(file, scale: scale, template: template) }).value {
            store(image, key)
            return image
        }
        // One at a time: the web view draws one thing.
        let before = queue
        let job = Task<Result<UIImage, Error>, Never> {
            await before?.value
            do { return .success(try await self.draw(kind, source, dark: dark, points: points)) } catch { return .failure(error) }
        }
        queue = Task { _ = await job.value }
        switch await job.value {
        case .success(let image):
            store(image, key)
            if let png = Self.png(image) {
                let dir = dir
                Task.detached(priority: .utility) {
                    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                    try? png.write(to: file, options: .atomic)
                }
            }
            return image
        case .failure(let error):
            let message = (error as? Failure)?.message ?? error.localizedDescription
            failures[key] = message
            throw Failure(message: message)
        }
    }

    private func store(_ image: UIImage, _ key: String) {
        images.setObject(image, forKey: key as NSString, cost: Int(image.size.width * image.size.height * 4 * 4))
    }

    private func draw(_ kind: Kind, _ source: String, dark: Bool, points shown: CGFloat) async throws -> UIImage {
        let web = try await page()
        // The Mac's offscreen window may be on no screen (1 pixel a point): draw twice as large and keep the picture at
        // half its size, so it is sharp on a Retina display (thin formula strokes at 1x looked gray, not black).
        let points = shown * Self.density
        let answer: Any?
        do {
            switch kind {
            case .mermaid:
                answer = try await web.callAsyncJavaScript("return await renderMermaid(source, dark, points)",
                                                           arguments: ["source": source, "dark": dark, "points": points], contentWorld: .page)
            case .inlineMath, .displayMath:
                answer = try await web.callAsyncJavaScript("return await renderMath(tex, display, points)",
                                                           arguments: ["tex": source, "display": kind == .displayMath, "points": points],
                                                           contentWorld: .page)
            }
        } catch {
            throw Failure(message: Self.scriptMessage(error))
        }
        guard let box = answer as? [NSNumber], box.count == 4, box[2].doubleValue > 0, box[3].doubleValue > 0 else {
            throw Failure(message: String(localized: "Nothing was drawn"))
        }
        let rect = CGRect(x: max(0, box[0].doubleValue), y: max(0, box[1].doubleValue),
                          width: min(box[2].doubleValue, Self.maxWidth), height: min(box[3].doubleValue, Self.maxWidth * 4))
        if web.frame.width < rect.maxX || web.frame.height < rect.maxY {
            web.frame.size = CGSize(width: max(web.frame.width, rect.maxX), height: max(web.frame.height, rect.maxY))
            try? await Task.sleep(for: .milliseconds(50))
        }
        let config = WKSnapshotConfiguration()
        config.rect = rect
        #if os(iOS) || os(visionOS)
        config.afterScreenUpdates = true
        #else
        // The offscreen window is never on screen, so a screen update may never come; WebKit paints the snapshot itself.
        config.afterScreenUpdates = false
        #endif
        let image = try await web.takeSnapshot(configuration: config)
        _ = try? await web.callAsyncJavaScript("clearOut()", arguments: [:], contentWorld: .page)
        #if os(iOS) || os(visionOS)
        return kind == .mermaid ? image : image.withRenderingMode(.alwaysTemplate)
        #else
        // A bitmap of the drawn size in pixels, shown at 1 / density: the same however many pixels the snapshot had.
        let pixels = NSSize(width: rect.width, height: rect.height)
        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(pixels.width), pixelsHigh: Int(pixels.height),
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                         colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else { return image }
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        image.draw(in: NSRect(origin: .zero, size: pixels))
        NSGraphicsContext.restoreGraphicsState()
        let sharp = NSImage(size: NSSize(width: pixels.width / Self.density, height: pixels.height / Self.density))
        sharp.addRepresentation(rep)
        sharp.isTemplate = kind != .mermaid
        return sharp
        #endif
    }

    /// The web view, made and loaded once. It lives in the window, behind everything and outside the screen, on iOS
    /// and in an offscreen window on the Mac: a web view outside any window does not draw its snapshots, and one made
    /// transparent (alpha) gives transparent snapshots.
    private func page() async throws -> WKWebView {
        if let web, let loaded { try await loaded.value; return web }
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: CGRect(x: -Self.maxWidth * 8, y: 0, width: 800, height: 600), configuration: config)
        #if os(iOS) || os(visionOS)
        web.isOpaque = false
        web.backgroundColor = .clear
        web.scrollView.backgroundColor = .clear
        // In the window it gets the safe area as content inset, which moved the page down under the snapshot's rect
        // (a formula's top showed at the picture's bottom edge).
        web.scrollView.contentInsetAdjustmentBehavior = .never
        web.scrollView.contentInset = .zero
        web.isUserInteractionEnabled = false
        web.accessibilityElementsHidden = true
        let window = UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.keyWindow }.first
            ?? UIApplication.shared.connectedScenes.compactMap { ($0 as? UIWindowScene)?.windows.first }.first
        window?.insertSubview(web, at: 0)
        #else
        web.setValue(false, forKey: "drawsBackground")
        let w = NSWindow(contentRect: NSRect(x: -10_000, y: -10_000, width: 800, height: 600), styleMask: .borderless, backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        w.contentView = web
        w.orderBack(nil)
        window = w
        #endif
        self.web = web
        let task = Task { @MainActor in
            if let rules = try? await WKContentRuleListStore.default().compileContentRuleList(
                forIdentifier: "persona-render-offline", encodedContentRuleList: Self.offline) {
                web.configuration.userContentController.add(rules)
            }
            guard let html = Bundle.main.url(forResource: "render", withExtension: "html") else { throw Failure(message: String(localized: "The render page is missing")) }
            let loader = Loader()
            web.navigationDelegate = loader
            web.loadFileURL(html, allowingReadAccessTo: html.deletingLastPathComponent())
            try await loader.done()
        }
        loaded = task
        do {
            try await task.value
        } catch {
            self.web = nil
            loaded = nil
            throw error
        }
        return web
    }

    /// Blocks every load but the bundle's own files.
    private static let offline = #"[{"trigger":{"url-filter":".*"},"action":{"type":"block"}},{"trigger":{"url-filter":"^file:"},"action":{"type":"ignore-previous-rules"}}]"#

    private final class Loader: NSObject, WKNavigationDelegate {
        private var continuation: CheckedContinuation<Void, Error>?
        private var finished: Result<Void, Error>?

        func done() async throws {
            if let finished { return try finished.get() }
            try await withCheckedThrowingContinuation { continuation = $0 }
        }

        private func finish(_ r: Result<Void, Error>) {
            guard finished == nil else { return }
            finished = r
            continuation?.resume(with: r)
            continuation = nil
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { finish(.success(())) }
        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { finish(.failure(error)) }
        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { finish(.failure(error)) }
    }

    /// KaTeX's or Mermaid's own words, without WebKit's wrapping.
    private static func scriptMessage(_ error: Error) -> String {
        let info = (error as NSError).userInfo
        let message = (info["WKJavaScriptExceptionMessage"] as? String) ?? error.localizedDescription
        return message.replacingOccurrences(of: "Error: ", with: "")
    }

    /// A kept picture, at the screen's scale, a formula as a template again.
    nonisolated private static func decode(_ file: URL, scale: CGFloat, template: Bool) -> UIImage? {
        guard let src = CGImageSourceCreateWithURL(file as CFURL, nil), let cg = CGImageSourceCreateImageAtIndex(src, 0, nil) else { return nil }
        #if os(iOS) || os(visionOS)
        let image = UIImage(cgImage: cg, scale: scale, orientation: .up)
        return template ? image.withRenderingMode(.alwaysTemplate) : image
        #else
        let image = NSImage(cgImage: cg, size: NSSize(width: CGFloat(cg.width) / density, height: CGFloat(cg.height) / density))
        image.isTemplate = template
        return image
        #endif
    }

    static var displayScale: CGFloat { screenScale }

    /// How many times larger than shown the page draws (`draw`): 2 on the Mac, 1 on the iPhone (its snapshot has the
    /// screen's pixels already).
    nonisolated static var density: CGFloat {
        #if os(iOS) || os(visionOS)
        1
        #else
        2
        #endif
    }

    private static var screenScale: CGFloat {
        #if os(iOS) || os(visionOS)
        UITraitCollection.current.displayScale
        #else
        NSScreen.main?.backingScaleFactor ?? 2
        #endif
    }

    nonisolated private static func png(_ image: UIImage) -> Data? {
        #if os(iOS) || os(visionOS)
        return image.pngData()
        #else
        guard let tiff = image.tiffRepresentation, let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .png, properties: [:])
        #endif
    }
}

/// The text size formulas are drawn at (the body's) and diagrams at (the caption's: a diagram at the body's size
/// filled the screen with one box).
@MainActor
enum RenderedText {
    static var points: CGFloat {
        #if os(iOS) || os(visionOS)
        UIFont.preferredFont(forTextStyle: .body).pointSize
        #else
        // KaTeX's own size for formulas in text (1.21 em): its serif at the body's 13 pt looked a size smaller than the
        // text around it on the Mac.
        Theme.Font.pointSize(.body) * 1.21
        #endif
    }

    static var diagramPoints: CGFloat {
        #if os(iOS) || os(visionOS)
        UIFont.preferredFont(forTextStyle: .footnote).pointSize
        #else
        Theme.Font.pointSize(.caption)
        #endif
    }

    static func points(_ kind: MarkdownRenderer.Kind) -> CGFloat { kind == .mermaid ? diagramPoints : points }
}

/// A ```math or ```mermaid fence in Markdown: the picture once drawn (a diagram opens full screen when tapped), the
/// fence as code with why underneath when it cannot be drawn.
struct RenderedBlock<Fallback: View>: View {
    let kind: MarkdownRenderer.Kind
    let source: String
    @ViewBuilder var fallback: Fallback
    @Environment(\.colorScheme) private var scheme
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var image: UIImage?
    @State private var error: String?
    @State private var full: Full?

    /// The diagram opened full screen.
    struct Full: Identifiable {
        let id = UUID()
        var payload: ImagePayload
    }

    var body: some View {
        Group {
            if let image = image ?? MarkdownRenderer.shared.cached(kind, source, dark: scheme == .dark, points: RenderedText.points(kind)) {
                picture(image)
            } else if let error {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    fallback
                    Label("Couldn't draw this: \(error)", systemImage: Theme.Icon.error)
                        .font(Theme.Font.label)
                        .foregroundStyle(Theme.cautionText)
                        .lineLimit(2)
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .scaledFrame(minHeight: Theme.Size.box56)
            }
        }
        .task(id: [source, scheme == .dark ? "dark" : "light", "\(typeSize)"]) {
            do {
                image = try await MarkdownRenderer.shared.image(kind, source, dark: scheme == .dark, points: RenderedText.points(kind))
                error = nil
            } catch {
                self.error = error.localizedDescription
            }
        }
        .fullScreenCover(item: $full) { full in ImageViewer(items: [ViewerImage(payload: full.payload, title: String(localized: "Chart"))], index: 0) }
    }

    @ViewBuilder private func picture(_ image: UIImage) -> some View {
        switch kind {
        case .mermaid:
            FittedDiagram(image: image)
                .contentShape(.rect)
                .onTapGesture { full = Self.payload(image).map { Full(payload: $0) } }
                .accessibilityLabel("Diagram: \(source.prefix(80))")
                .accessibilityAddTraits(.isImage)
                .accessibilityIdentifier("rendered-mermaid")
        default:
            let formula = Image(uiImage: image).renderingMode(.template).foregroundStyle(Color.primary)
            // Centered as a display formula is; sideways when wider than the column.
            ViewThatFits(in: .horizontal) {
                formula.frame(maxWidth: .infinity)
                ScrollView(.horizontal, showsIndicators: false) { formula }
            }
            .accessibilityLabel("Formula: \(source)")
            .accessibilityIdentifier("rendered-math")
        }
    }

    private static func payload(_ image: UIImage) -> ImagePayload? {
        #if os(iOS) || os(visionOS)
        guard let data = image.pngData() else { return nil }
        #else
        guard let tiff = image.tiffRepresentation, let data = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) else { return nil }
        #endif
        return ImagePayload(mime: "image/png", data: data.base64EncodedString())
    }
}

/// A diagram fitted to the column: scaled down to fit whole, but not below `minScale` (its text would be too small to
/// read); then it scrolls sideways, its right edge fading while there is more.
private struct FittedDiagram: View {
    let image: UIImage
    @State private var width: CGFloat = 0

    /// Diagrams are drawn at the caption's size; at 0.7 of it they are still readable.
    static let minScale: CGFloat = 0.7

    var body: some View {
        let natural = image.size
        let scale = width > 0 ? max(min(1, width / natural.width), Self.minScale) : 1
        let shown = CGSize(width: natural.width * scale, height: natural.height * scale)
        let picture = Image(uiImage: image).resizable().frame(width: shown.width, height: shown.height)
        // The column's width, taken from a frame that only fills what it is offered: measured on the picture, the
        // picture's own width widened the whole message (its text and formulas ran off the screen).
        Color.clear
            .frame(maxWidth: .infinity)
            .frame(height: shown.height)
            .overlay(alignment: .leading) {
                if width > 0, shown.width > width + 1 {
                    SidewaysScroll { picture }
                } else {
                    picture
                }
            }
            .clipped() // before the width is known the picture is at its own size
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { width = $0 }
    }
}

/// Inline formulas (`persona-math:` images, `MathMarkdown`): drawn at the body's size as template images, so they take
/// the text's color; TeX that KaTeX cannot read shows as its source in monospace. Other inline images load as before.
struct MathInlineImages: InlineImageProvider {
    func image(with url: URL, label: String) async throws -> Image {
        guard let tex = MathMarkdown.tex(url) else { return try await DefaultInlineImageProvider.default.image(with: url, label: label) }
        let points = await RenderedText.points
        if let image = try? await MarkdownRenderer.shared.image(.inlineMath, tex, dark: false, points: points) {
            return Image(uiImage: image).renderingMode(.template)
        }
        return await MainActor.run {
            let renderer = ImageRenderer(content: Text(tex).font(Theme.Font.code).foregroundStyle(Color.black))
            renderer.scale = MarkdownRenderer.displayScale
            #if os(iOS) || os(visionOS)
            return renderer.uiImage.map { Image(uiImage: $0.withRenderingMode(.alwaysTemplate)) } ?? Image(systemName: Theme.Icon.code)
            #else
            return renderer.nsImage.map { i in i.isTemplate = true; return Image(nsImage: i) } ?? Image(systemName: Theme.Icon.code)
            #endif
        }
    }
}

