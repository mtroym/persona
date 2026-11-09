import PersonaKit
import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// Images in a conversation. Inline ones decode from their base64; lazy ones (history leaves the bytes out) come from
/// the hub the first time they are shown, then from memory and from disk: an item's image never changes.
nonisolated enum ChatImageLoader {
    nonisolated(unsafe) private static let images: NSCache<NSString, UIImage> = { // NSCache is thread-safe
        let c = NSCache<NSString, UIImage>()
        c.totalCostLimit = 96 << 20
        return c
    }()
    private static let fetches = Fetches()

    private static func key(_ p: ImagePayload, _ side: CGFloat?) -> NSString {
        "\(p.identity)@\(side.map { Int($0) } ?? 0)" as NSString
    }

    /// Settings › Storage cleared the files: drop the decoded copies too.
    static func clearMemory() { images.removeAllObjects() }

    /// Already decoded: lets a row that scrolls back into view show its image without a placeholder frame.
    static func cached(_ p: ImagePayload, side: CGFloat?) -> UIImage? { images.object(forKey: key(p, side)) }

    /// The image, scaled down to fit `side` × `side` (nil: full size). Decoding runs off the main thread.
    static func image(_ p: ImagePayload, side: CGFloat?) async -> UIImage? {
        let k = key(p, side)
        if let img = images.object(forKey: k) { return img }
        // A Full Page strip waits for this (#1609).
        let fullPageLoad = FullPageLoads.shared.begin()
        defer { FullPageLoads.shared.end(fullPageLoad) }
        guard let data = await data(p) else { return nil }
        let img: UIImage? = await Task.detached(priority: .userInitiated) {
            guard let full = UIImage(data: data) else { return nil }
            guard let side else { return full.preparingForDisplay() ?? full }
            return full.preparingThumbnail(of: ZoomMath.fittedSize(image: full.size, in: CGSize(width: side, height: side))) ?? full
        }.value
        if let img { images.setObject(img, forKey: k, cost: Int(img.size.width * img.size.height * img.scale * img.scale * 4)) }
        // Downloaded bytes that are not a picture (a proxy's page, a cut body) must not stay on disk: a retry fetches again.
        else if p.data.isEmpty, let ref = p.ref { await fetches.forget(ref) }
        return img
    }

    /// The encoded bytes (for saving / sharing the original).
    static func data(_ p: ImagePayload) async -> Data? {
        if !p.data.isEmpty {
            let s = p.data
            return await Task.detached { Data(base64Encoded: s) }.value
        }
        guard let ref = p.ref else { return nil }
        return await fetches.data(ref)
    }

    /// One download per image however many views ask for it at once; finished ones are kept on disk.
    private actor Fetches {
        private var running: [String: Task<Data?, Never>] = [:]
        private let dir: URL = {
            let d = URL.cachesDirectory.appending(path: "chat-images")
            try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
            return d
        }()

        private func file(_ ref: String) -> URL {
            dir.appending(path: String(ref.map { $0.isLetter || $0.isNumber || $0 == "-" ? $0 : "_" }))
        }

        /// A picture that did not come, in its chat's Copy Diagnostics (`ChatTrace`): which item, which group's hub, why.
        /// What a phone saw can be read from there.
        @MainActor private static func note(_ ref: String, _ why: String) {
            guard let key = ImageRef.session(ref) else { return }
            // The whole item id ("<line>#<block>"): the pictures of one message differ only in their block.
            let item = ref.split(separator: "/").last.map(String.init) ?? ""
            ChatTrace.shared.note(key, "image \(item) not loaded from hub \(key.group.isEmpty ? "default" : key.group): \(why)")
        }

        nonisolated private static func describe(_ error: Error) -> String {
            if let e = error as? APIError { return "HTTP \(e.status)" + (e.message.isEmpty ? "" : " \(e.message.prefix(80))") }
            if let e = error as? URLError { return "URLError \(e.code.rawValue)" }
            return String(describing: type(of: error))
        }

        /// Drops the kept bytes of an image that did not decode.
        func forget(_ ref: String) {
            try? FileManager.default.removeItem(at: file(ref))
        }

        func data(_ ref: String) async -> Data? {
            let file = file(ref)
            if let d = try? Data(contentsOf: file) { return d }
            if let t = running[ref] { return await t.value }
            let t = Task<Data?, Never> {
                // From the hub of the chat's group, not the current one's (#1555).
                let (group, path) = ImageRef.split(ref)
                guard let api = await MainActor.run(body: { AppModel.shared.api(group) }) else {
                    await Self.note(ref, "no hub for its group")
                    return nil
                }
                do {
                    let d = try await api.image(ref: path)
                    try? d.write(to: file, options: .atomic)
                    return d
                } catch {
                    await Self.note(ref, Self.describe(error))
                    return nil
                }
            }
            running[ref] = t
            let d = await t.value
            running[ref] = nil
            return d
        }
    }
}

/// Pictures in a chat whose last load gave none, and how often each was asked again, by `ImagePayload.identity` (#559).
/// A thumbnail inside a button that opens the viewer (a sent photo, a viewed image) can't carry a Retry of its own: a
/// button in another's label leaves which one a tap reaches to chance, and VoiceOver hears only the outer one. So the
/// thumbnail records its failure here, and the tile's own button asks again instead of opening the viewer, and says so.
@MainActor @Observable final class ImageRetries {
    static let shared = ImageRetries()
    private var failures: Set<String> = []
    private var attempts: [String: Int] = [:]

    func didFail(_ p: ImagePayload) -> Bool { failures.contains(p.identity) }

    /// A tap on its tile asks it again: one fetched from the hub whose last load failed. Bytes already here that do not
    /// decode would only fail again; the viewer says why.
    func canRetry(_ p: ImagePayload?) -> Bool {
        guard let p, p.ref != nil else { return false }
        return failures.contains(p.identity)
    }

    /// Asks it again when it can (`canRetry`); false leaves the tap to open the viewer.
    @discardableResult func retry(_ p: ImagePayload?) -> Bool {
        guard canRetry(p), let p else { return false }
        failures.remove(p.identity)
        attempts[p.identity, default: 0] += 1
        return true
    }

    /// How many times it was asked again: keys the thumbnail's load, so a retry runs it once more.
    func attempt(_ p: ImagePayload) -> Int { attempts[p.identity] ?? 0 }

    /// Only a change is written: every tile on screen reads `failures`, and a write redraws them all.
    func loaded(_ p: ImagePayload, ok: Bool) {
        guard failures.contains(p.identity) == ok else { return }
        if ok { failures.remove(p.identity) } else { failures.insert(p.identity) }
    }
}

/// A square thumbnail filling its frame (the caller sizes and clips it). One that did not load says so; its caller's
/// button asks again (`ImageRetries`).
struct ChatThumbnail: View {
    var payload: ImagePayload
    var side: CGFloat = 360
    @State private var image: UIImage?
    private let retries = ImageRetries.shared

    var body: some View {
        let attempt = retries.attempt(payload)
        Group {
            if let img = image ?? ChatImageLoader.cached(payload, side: side) {
                Image(uiImage: img).resizable().scaledToFill()
            } else if retries.didFail(payload) {
                ImageNotLoaded(canRetry: payload.ref != nil)
            } else if attempt > 0 { // asked again: the tap shows it was taken
                ProgressView().controlSize(.small).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Color.clear
            }
        }
        .task(id: "\(payload.identity)#\(attempt)") {
            guard image == nil else { return }
            let loaded = await ChatImageLoader.image(payload, side: side)
            image = loaded
            if !Task.isCancelled { retries.loaded(payload, ok: loaded != nil) }
        }
    }
}

/// Said on an image's placeholder when its picture did not come (`Base64Image`, `ChatThumbnail`, `ViewedImageThumb`).
/// `canRetry`: loading it again can bring it (one fetched from the hub), not bytes already here that do not decode.
/// `retry` makes it a button; a thumbnail inside a tile's button leaves it out, the tile retries (`ImageRetries`).
struct ImageNotLoaded: View {
    var canRetry: Bool
    var retry: (() -> Void)? = nil

    var body: some View {
        if canRetry, let retry {
            Button(action: retry) { words.contentShape(.rect) }
                .buttonStyle(.plain)
                .accessibilityLabel("Image didn't load. Tap to retry")
        } else {
            words
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(canRetry ? Text("Image didn't load. Tap to retry") : Text("Can't show this image"))
        }
    }

    /// A narrow placeholder (a portrait screenshot, a small photo): the sentence, else Retry, else the symbol alone.
    private var words: some View {
        Group {
            if canRetry {
                ViewThatFits(in: .horizontal) {
                    Label("Image didn't load. Tap to retry", systemImage: Theme.Icon.retry)
                    Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry)
                    ThemedIcon(Theme.Icon.retry)
                }
            } else {
                ViewThatFits(in: .horizontal) {
                    Label("Can't show this image", systemImage: Theme.Icon.error)
                    ThemedIcon(Theme.Icon.error)
                }
            }
        }
        .font(Theme.Font.label)
        .foregroundStyle(.secondary)
        .lineLimit(1)
        .padding(.horizontal, Theme.Space.s)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
