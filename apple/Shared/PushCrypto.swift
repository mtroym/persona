import CryptoKit
import Foundation
import Security
import UserNotifications

/// Pushes sealed to this phone (docs/operations/push-relay.md): a hub without the app's APNs key seals each payload to the phone's
/// public key and the relay forwards it as `e`. HPKE base mode, DHKEM(P-256, HKDF-SHA256) + HKDF-SHA256 + AES-256-GCM,
/// the same suite as the hub's `pushcrypto` package. The notification service extension (PersonaNotify) opens alerts,
/// the app opens silent pushes; anything that does not open stays 「有新消息」, never anything in the clear.
nonisolated enum PushCrypto {
    static let info = Data("persona push v1".utf8)
    static let suite = HPKE.Ciphersuite.P256_SHA256_AES_GCM_256
    /// A payload sealed longer ago than this shows only the fallback (a relay replaying old ones learns nothing new).
    static let maxAge: TimeInterval = 24 * 3600
    private static let encapsulatedKeyLength = 65

    // MARK: The key

    /// The keychain group the app and its extension share (Info.plist, `<team>.<bundle id>.push`); nil without a team
    /// (an empty one leaves just the dot).
    static var accessGroup: String? {
        NativeStateMigration.infoString("PersonaPushKeychainGroup").flatMap { $0.hasPrefix(".") ? nil : $0 }
    }
    private static let service = "persona.push"
    private static let account = "push-key"

    /// Each group of hubs seals to a key of its own (docs/protocol/multi-hub.md「推送」): the default group's is the one the
    /// phone always had, another group's has the group's id after it. The key that opens a push says whose it is.
    static func account(group: String) -> String { group.isEmpty ? account : account + "." + group }

    private static func query(group: String?, hubGroup: String = "", service name: String = service) -> [String: Any] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: name,
                                kSecAttrAccount as String: account(group: hubGroup)]
        if let group { q[kSecAttrAccessGroup as String] = group }
        #if os(macOS)
        // A Mac's default is the old file keychain, where access groups do nothing and an item belongs to the app that
        // made it: PersonaMacNotify could not read the key. The data protection keychain shares it like on a phone.
        q[kSecUseDataProtectionKeychain as String] = true
        #endif
        return q
    }

    /// The private key, if this phone made one. Readable once the phone was unlocked after boot (the extension runs locked).
    static func loadKey(group hubGroup: String = "") -> P256.KeyAgreement.PrivateKey? {
        _ = NativeStateMigration.preparePushKeys(accessGroup: accessGroup)
        for name in [service, LegacyNames.pushService] {
            var q = query(group: accessGroup, hubGroup: hubGroup, service: name)
            q[kSecReturnData as String] = true
            q[kSecMatchLimit as String] = kSecMatchLimitOne
            var out: AnyObject?
            if SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data,
               let key = try? P256.KeyAgreement.PrivateKey(rawRepresentation: data) { return key }
        }
        return nil
    }

    /// Every group's key: (group, key), the default group's first.
    static func allKeys() -> [(group: String, key: P256.KeyAgreement.PrivateKey)] {
        _ = NativeStateMigration.preparePushKeys(accessGroup: accessGroup)
        var keys: [(group: String, key: P256.KeyAgreement.PrivateKey)] = []
        var seen: Set<String> = []
        for name in [service, LegacyNames.pushService] {
            var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: name,
                                    kSecReturnData as String: true, kSecReturnAttributes as String: true,
                                    kSecMatchLimit as String: kSecMatchLimitAll]
            if let accessGroup { q[kSecAttrAccessGroup as String] = accessGroup }
            #if os(macOS)
            q[kSecUseDataProtectionKeychain as String] = true
            #endif
            var out: AnyObject?
            guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let items = out as? [[String: Any]] else { continue }
            for item in items {
                guard let accountName = item[kSecAttrAccount as String] as? String, let data = item[kSecValueData as String] as? Data,
                      let key = try? P256.KeyAgreement.PrivateKey(rawRepresentation: data) else { continue }
                let group: String
                if accountName == account { group = "" }
                else if accountName.hasPrefix(account + ".") { group = String(accountName.dropFirst(account.count + 1)) }
                else { continue }
                if seen.insert(group + ":" + key.publicKey.x963Representation.base64EncodedString()).inserted { keys.append((group, key)) }
            }
        }
        return keys.sorted { $0.group.isEmpty && !$1.group.isEmpty }
    }

    /// A group that is gone: its key goes too.
    static func removeKey(group hubGroup: String) {
        for name in [service, LegacyNames.pushService] {
            SecItemDelete(query(group: accessGroup, hubGroup: hubGroup, service: name) as CFDictionary)
            SecItemDelete(query(group: nil, hubGroup: hubGroup, service: name) as CFDictionary)
        }
    }

    /// The key, made and saved the first time. It never leaves this phone: not synced, not in backups.
    static func ensureKey(group hubGroup: String = "") -> P256.KeyAgreement.PrivateKey? {
        if let key = loadKey(group: hubGroup) { return key }
        guard NativeStateMigration.preparePushKeys(accessGroup: accessGroup) else { return nil }
        let key = P256.KeyAgreement.PrivateKey()
        var add = query(group: accessGroup, hubGroup: hubGroup)
        add[kSecValueData as String] = key.rawRepresentation
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        var status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecMissingEntitlement, accessGroup != nil {
            // A build signed without the shared group (a free personal team): the app can still open silent pushes;
            // the extension cannot read it and shows the fallback.
            add.removeValue(forKey: kSecAttrAccessGroup as String)
            status = SecItemAdd(add as CFDictionary, nil)
        }
        // Only a key that reads back: one that was not saved would make the hub seal to a key this phone does not keep.
        guard status == errSecSuccess || status == errSecDuplicateItem,
              NativeStateMigration.pushKeys(accessGroup: accessGroup) else { return nil }
        return loadKey(group: hubGroup)
    }

    /// What the hub gets: the X9.63 uncompressed point (65 bytes), base64.
    static func publicKey(_ key: P256.KeyAgreement.PrivateKey) -> String {
        key.publicKey.x963Representation.base64EncodedString()
    }

    // MARK: Opening

    static func open(_ sealed: Data, with key: P256.KeyAgreement.PrivateKey) throws -> Data {
        guard sealed.count > encapsulatedKeyLength else { throw CryptoKitError.incorrectParameterSize }
        var failure: Error = CryptoKitError.authenticationFailure
        for context in [info, Data(LegacyNames.pushInfo.utf8)] {
            do {
                var recipient = try HPKE.Recipient(privateKey: key, ciphersuite: suite, info: context,
                                                   encapsulatedKey: sealed.prefix(encapsulatedKeyLength))
                return try recipient.open(sealed.dropFirst(encapsulatedKeyLength))
            } catch { failure = error }
        }
        throw failure
    }

    /// Whether a push came through the relay (has a sealed payload).
    static func isSealed(_ userInfo: [AnyHashable: Any]) -> Bool { userInfo["e"] != nil }

    /// Invalid sealed envelopes never fall through to unauthenticated outer routing fields.
    static func notificationInfo(_ userInfo: [AnyHashable: Any],
                                 keys: () -> [(group: String, key: P256.KeyAgreement.PrivateKey)] = { allKeys() }) -> [AnyHashable: Any]? {
        guard isSealed(userInfo) else { return userInfo }
        guard let opened = opened(userInfo, keys: keys()) else { return nil }
        var info = Dictionary(uniqueKeysWithValues: opened.payload.map { (AnyHashable($0.key), $0.value) })
        info[groupKey] = opened.group
        info[LegacyNames.pushGroupKey] = opened.group
        return info
    }

    /// The payload the hub sealed into this push, or nil when there is none, it does not open, or it is too old.
    static func payload(_ userInfo: [AnyHashable: Any], key: P256.KeyAgreement.PrivateKey?, now: Date = .now) -> [String: Any]? {
        guard let key, let b64 = userInfo["e"] as? String, let sealed = Data(base64Encoded: b64),
              let plain = try? open(sealed, with: key),
              let obj = try? JSONSerialization.jsonObject(with: plain) as? [String: Any],
              let iat = (obj["iat"] as? NSNumber)?.doubleValue,
              abs(now.timeIntervalSince1970 - iat) <= maxAge else { return nil }
        return obj
    }

    /// Puts an opened payload on the notification: the texts, the buttons (category), the thread, how loud, the sound, and
    /// the ids the app reads when it is tapped. Only what the payload has replaces the fallback, except the sound: the
    /// relay's fallback always rings, and a sealed aps without one (`notify --silent`, quiet hours) is a quiet notification.
    static func apply(_ payload: [String: Any], to content: UNMutableNotificationContent) {
        let aps = payload["aps"] as? [String: Any] ?? [:]
        if let alert = aps["alert"] as? [String: Any] {
            if let v = alert["title"] as? String { content.title = v }
            if let v = alert["subtitle"] as? String { content.subtitle = v }
            if let v = alert["body"] as? String { content.body = v }
        } else if let alert = aps["alert"] as? String {
            content.body = alert
        }
        if let v = aps["category"] as? String { content.categoryIdentifier = v }
        if let v = aps["thread-id"] as? String { content.threadIdentifier = v }
        switch aps["interruption-level"] as? String {
        case "time-sensitive": content.interruptionLevel = .timeSensitive
        case "passive": content.interruptionLevel = .passive
        case "active": content.interruptionLevel = .active
        default: break
        }
        switch aps["sound"] {
        case nil: content.sound = nil
        case is String: content.sound = .default // the hub sends only "default" (named sounds don't exist on watchOS)
        default: break // a critical-alert dictionary: the hub sends none, so the fallback's stays
        }
        // The service extension removes the sealed marker; only authenticated payload fields may reach app routing.
        var info: [AnyHashable: Any] = [:]
        for (k, v) in payload where k != "aps" && k != "iat" { info[k] = v }
        content.userInfo = info
    }

    // MARK: The extension's work

    enum Outcome: String { case notSealed, noKey, unopened, opened }

    /// What the notification service extension shows for a delivered push (PersonaNotify/NotificationService.swift):
    /// the decrypted content, or the push as it came (the relay's 「有新消息」).
    static func service(_ content: UNNotificationContent, key: () -> P256.KeyAgreement.PrivateKey? = { loadKey() },
                        now: Date = .now) -> (UNNotificationContent, Outcome) {
        service(content, keys: { key().map { [("", $0)] } ?? [] }, now: now)
    }

    /// Tries every group's key; the one that opens the push names its group (`persona_group`, docs/protocol/multi-hub.md).
    static func service(_ content: UNNotificationContent, keys: () -> [(group: String, key: P256.KeyAgreement.PrivateKey)],
                        now: Date = .now) -> (UNNotificationContent, Outcome) {
        guard isSealed(content.userInfo) else { return (content, .notSealed) }
        let all = keys()
        guard !all.isEmpty else { return (content, .noKey) }
        for (group, key) in all {
            guard let payload = payload(content.userInfo, key: key, now: now),
                  let out = content.mutableCopy() as? UNMutableNotificationContent else { continue }
            apply(payload, to: out)
            var info = out.userInfo
            // Routing comes from the authenticated key, even for the default group. Ignore payload-supplied aliases.
            info[groupKey] = group
            info[LegacyNames.pushGroupKey] = group
            out.userInfo = info
            return (out, .opened)
        }
        return (content, .unopened)
    }

    /// Where an opened push says which group's key opened it (another group than the default one).
    static let groupKey = "persona_group"

    static func group(from userInfo: [AnyHashable: Any]) -> String? {
        userInfo[groupKey] as? String ?? userInfo[LegacyNames.pushGroupKey] as? String
    }

    /// A push's payload opened with any group's key, and the group.
    static func opened(_ userInfo: [AnyHashable: Any], keys: [(group: String, key: P256.KeyAgreement.PrivateKey)]) -> (group: String, payload: [String: Any])? {
        for (group, key) in keys { if let p = payload(userInfo, key: key) { return (group, p) } }
        return nil
    }

}
