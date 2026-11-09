# Push relay (end-to-end encrypted)

A self-hosted hub doesn't have the APNs key of the Apple Team that owns the App, so it can't send pushes. The push relay is a `personad relay` run by the App's publisher:
it holds the APNs key and hands pushes to APNs on behalf of every hub. The hub encrypts the notification content with the phone's public key, the relay only forwards ciphertext,
and the phone decrypts it in the Notification Service Extension before showing it. A hub that has its own APNs key (`~/.persona/apns.json`) keeps talking to APNs directly and never goes through the relay.
The Mac version works the same way: its Notification Service Extension target is `PersonaMacNotify` (product `PersonaNotify.appex`; the module and principal notification class still use `PersonaNotify`, `docs/apple/mac.md` ("Remote push (Mac)"), #432), and the private key lives in the data protection keychain, shared with the extension. Apple Vision Pro builds the same extension as `PersonaVisionNotify` (#1981, `docs/apple/vision.md`).

```
hub ──(HPKE ciphertext + grant)──▶ relay ──(APNs key)──▶ APNs ──▶ phone: Notification Service Extension decrypts → shows
 ▲                                                                 │
 └──────────── phone public key, relay grant (via the paired hub API) ◀┘
```

## Who knows what

| | Phone | hub | Relay |
| --- | --- | --- | --- |
| Private key | Yes (keychain, this device only) | No | No |
| Public key | Yes | Yes (fetched via the hub API after pairing) | **No**: the public key never passes through the relay |
| APNs device token | Yes | Yes when direct; not needed via the relay | Yes (sealed in the grant) |
| Notification content | Yes, after decryption | Yes | **No** (ciphertext only) |
| Live Activity content | Yes | Yes | **Yes** (plaintext, see below) |
| Which device, when, ciphertext size | | | Yes |

- The public key only travels over the authenticated "phone → its own hub" link (device token + the address from pairing). The relay is not on that path, so it can't swap the public key and therefore can't impersonate the phone to decrypt.
- The worst the relay can do: not forward, replay old ciphertext, or use the APNs key to send a plaintext notification directly (it has the key anyway; no relay design can prevent that).
  It can't forge a ciphertext that decrypts: only the hub has the phone's public key. For replayed ciphertext older than 24 hours, the phone no longer shows the content (`iat`).

## Encryption

- HPKE (RFC 9180) base mode: DHKEM(P-256, HKDF-SHA256) + HKDF-SHA256 + AES-256-GCM, `info = "persona push v1"`.
  Go uses the standard library `crypto/hpke`; iOS uses CryptoKit `HPKE` (`P256_SHA256_AES_GCM_256`). Ciphertext = `enc` (65 bytes) ‖ AEAD output, base64 in the push's `e` field.
- The plaintext is exactly the JSON the hub would send when direct (`aps.alert` title / subtitle / body, `category`, `thread-id`, `interruption-level`,
  `interaction_id` / `machine_id` / `session_id`, an optional question `ask`, the pairing receipt `pair`, a decision's `todo` (`../assistant/todos.md` "Waiting for the owner"), `persona_wake`), plus an `iat` (Unix seconds).
- The phone's private key: a `P256.KeyAgreement.PrivateKey` stored in the keychain access group shared by the App and the Notification Service Extension, `AfterFirstUnlockThisDeviceOnly`
  (readable by the extension on the lock screen once the device has been unlocked after boot; not in iCloud backups, regenerated on a new phone). Generated the first time it's needed, then never changes.

## What the relay receives and sends

The relay builds the APNs `aps` itself. The hub can only provide ciphertext and a few content-free parameters, so even with a grant it can't inject a plaintext notification:

| `type` from hub | What the relay sends to APNs |
| --- | --- |
| `alert` | `{"aps":{"alert":{"title":"Persona","body":"有新消息"},"mutable-content":1,"sound":"default"},"e":"<ciphertext>"}` (`有新消息` = "New message"), push type `alert` |
| `background` | `{"aps":{"content-available":1},"e":"<ciphertext>"}`, push type `background`, priority 5 |
| `liveactivity` | The `aps` given by the hub (only keys used by Live Activities are allowed), push type `liveactivity`, topic with `.push-type.liveactivity` appended |

- "New message" is the fallback: it's shown when the extension fails to decrypt, times out, the device hasn't been unlocked since boot (private key unreadable), or the ciphertext has expired. **It never falls back to plaintext.**
- The fallback always rings. Once the extension opens the push, the sealed `aps` decides the sound as well: one without `sound` (`personad notify --silent`, `"silent": true`, a push hushed by quiet hours) arrives quiet, the same as with direct APNs (`PushCrypto.apply`).
- The hub SHA-256 hashes `collapse_id` before giving it to the relay (the original value is the interaction id).

### Live Activities are plaintext

iOS reads `content-state` straight from the push for remote Live Activity updates; no extension can step in to decrypt, so via the relay **the relay can see their content**:
phase, counts, timers, TPM / load, and the items shown on the lock screen card and expanded view (session title, "machine · space › tab", what's being asked / command summary).
Decided by the user on 2026-11-01: don't cut lock screen / Dynamic Island features for this; forward as-is. Those who care can run their own relay ("Running your own relay" below).
Ordinary notifications, ask_user, Agent completion, and silent wakes are all ciphertext.

## Abuse prevention

- **Grants**: the phone registers `{apns_token, env, topic}` directly with the relay and gets back a grant: `{device token, environment, topic, issue time}` sealed with AES-GCM
  under the relay's own key (`secret_path`). The relay keeps no state; it learns the recipient by opening the grant. The phone hands the grant to its own hub,
  and the hub sends `Authorization: Bearer <grant>` with each push. A missing grant, a tampered grant, or a topic not on the relay's allowed list all get 401 / 403.
- **Only its own App**: the topic in the grant must be in `topics` in `relay.json` (the App's bundle id); the relay doesn't push for other Apps.
- **Rate limits**: 120 per device per minute (burst 30); registration 20 per source IP per minute. Request body at most 8 KB, ciphertext at most 3800 base64 characters
  (with the outer layer, within APNs's 4 KB). Before encrypting, the hub truncates titles / bodies that don't fit (the full content is in the App). A question's structured `ask` is dropped whole if it still cannot fit; its question and option labels are never clipped ([protocol](../protocol/protocol.md#answering-a-question-from-its-notification)).
- **Revocation**: when APNs reports 410 / `BadDeviceToken` / `Unregistered`, the relay returns 410 and the hub deletes the device (same as direct). It only deletes if the device's current registration still has this token; if the App registered a new token before the answer came back (relay retries and grant renewal take several seconds), the new registration and its notification settings are kept.
  To invalidate all grants, replace the relay's `secret_path`: when the hub gets a 401 on push, it re-registers once with the device token it remembers, gets a new grant, and pushes again, without waiting for the phone.
- Registration itself doesn't verify that the device is genuine. This is a known gap; closing it needs App Attest (not done yet):
  - Someone who knows an iPhone's APNs device token can register a grant and push "New message" to it;
  - Anyone can register a grant, and the Live Activity target token is not bound to the grant (it rotates and the relay doesn't know it), so someone who knows a phone's
    Live Activity token (or push-to-start token) can write arbitrary content to that phone's lock screen / Dynamic Island; alerts are only allowed together with `start`.
  - Like the device token, these tokens only exist between the phone, its hub, and APNs, and are not public; the topic is pinned by the grant, so no other App can be pushed to.

## API

Relay (`personad relay`, config `~/.persona/relay.json`):

```
POST /relay/v1/register   {"apns_token":"<hex>","env":"production|sandbox","topic":"<bundle id>"}
                          → {"grant":"<base64url>"}
POST /relay/v1/push       Authorization: Bearer <grant>
                          {"type":"alert|background|liveactivity","priority":"10|5","collapse_id":"<hex>",
                           "sealed":"<base64>",                      // alert / background
                           "token":"<hex>","aps":{…}}                // liveactivity: the activity's or push-to-start token
                          → 200 {"ok":true} | 410 {"reason":"Unregistered"}
                            | 422 {"error":"apns <status>","reason":"…"}  // APNs rejected it for good: not retried
                            | 502 {"error":"…"}                          // APNs 429/5xx or unreachable: retried
                            | other 4xx {"error":"…"}                    // the relay's own refusal
GET  /relay/v1/health     → {"ok":true,"apns":true}
```

The relay address must be `https` (the hub also only accepts `https`, or `http` to a local loopback address for end-to-end scripts): grants and Live Activity content must not cross the network in plaintext.

hub: `POST /api/v1/devices` gains three fields: `push_key` (the phone's public key, base64 of the X9.63 uncompressed point), `relay_url`, `relay_grant`.

hub routing (`push.go`): with `apns.json` → direct (exactly as before); without → via the relay if the device sent `relay_url` + `relay_grant` + `push_key`; otherwise no push.

hub retries failed relay requests (#2150): network errors (TLS handshake / connect timeout, dropped connection) and relay 5xx / 429 are retried twice more, after 1 second and 4 seconds;
it gives up only after three failures, and the log says how many attempts were made. Other relay answers (4xx, 410) are not retried; 401 still renews the grant first. Before, there was only one attempt, so any blip on the hub-to-relay network
lost alerts and Live Activity updates. Live Activity pushes carry `aps.timestamp`, so iOS drops an old state that arrives late from a retry instead of overwriting a newer one.

## App

- The build config `app.push_relay` (`config.json`, e.g. `https://<relay domain>/relay`) goes into Info.plist's `PersonaPushRelay`; when empty, the App doesn't register with a relay.
- After getting the APNs device token: make sure a push key exists → register with the relay for a grant (cached in the keychain per device token; re-registered when the token changes) →
  report `apns_token`, `push_key`, `relay_url`, `relay_grant` together to the hub.
- Notification Service Extension `PersonaNotify`: if `e` is present, decrypt it and swap in the title / subtitle / body / category (the notification's buttons) / thread / interruption level / userInfo;
  without `e` (direct pushes never carry `mutable-content`, so the extension isn't invoked) the notification is shown as-is.
- Silent pushes (`background`) are decrypted in the App process; `persona_wake` wakes the connection as before, and `resolved_interaction_ids` removes the notifications of cards that are over (#4349, only to apps that registered with `features: ["withdraw"]`).

## Which way a device is pushed (#5998)

A phone whose relay registration failed used to log one line and report empty relay fields; on a hub without `apns.json` its background pushes then went nowhere, and nothing said so.
Now every side says which way pushes take and, when none, why:

- **App**: the relay registration does not follow redirects (URLSession turns a redirected POST into a GET, so an old relay address that answers 301 could never work). The App keeps the outcome of the last attempt:
  registered, the relay's status and its own words (`403 topic not served`), a redirect and where to (`301 https://<new address>`), a network error. It also notes notifications not allowed, APNs refusing to register, and a build without a relay.
  A failure goes to the hub as `relay_error` in `POST /api/v1/devices`, which answers with the device's `push` way. **Settings › Notifications** shows "Background push: available (direct / via the relay) / unavailable", the reason and what to do.
- **hub**: `push` is `{"route":"direct|relay|none","reason"?,"detail"?,"failed_at"?}`:

  | route | reason | When |
  | --- | --- | --- |
  | `none` | `no_token` | The device has no APNs token: notifications not allowed, or APNs refused it |
  | `direct` | | The hub has `apns.json` and the device a token |
  | `relay` | | No `apns.json`; the device reported relay, grant and push key |
  | `none` | `relay_failed` | No `apns.json`; the phone's relay registration failed (`detail`: its words, kept in `devices.relay_error`) |
  | `none` | `no_relay` | No `apns.json`, and the phone names no relay (a build without one, or an App too old to say why) |
  | `direct` / `relay` | `push_failed` | The way is there, but the last push to the device's token did not go (`detail`: APNs' or the relay's answer, or the hub's own reason when it did not send the push: `seal: …` for a relay push it could not seal, `push of N bytes is over APNs' 4096-byte limit` for a direct push too long even shortened). In memory, until a push goes or the device registers a new token / relay. A Live Activity push goes to the activity's own token: its answer neither sets nor clears this |

  `GET /api/v1/auth/tokens` adds `push` (credential id → the way of the device it registered last) for **Settings › Devices**; `GET /api/v1/devices/push` lists every device (`personad hub doctor` prints it under "推送").

## Deployment (publisher)

1. On a machine with outbound internet access, put `~/.persona/relay.json` (0600):

   ```json
   {
     "listen": "127.0.0.1:18670",
     "apns": { "key_id": "<Key ID>", "team_id": "<Team ID>", "key_path": "~/.persona/AuthKey_<Key ID>.p8" },
     "topics": ["<the App's bundle id>"],
     "secret_path": "~/.persona/relay.secret",
     "proxy_secret": "<a long random string, same as the Worker's RELAY_PROXY_SECRET>"
   }
   ```

   If `secret_path` doesn't exist, it's generated on first start (32 random bytes, 0600); **if you lose it, every phone has to register again** (the App does this automatically at launch).
   `proxy_secret`: once set, any request without `X-Relay-Proxy-Secret` gets 403 (so the origin can't be reached directly, bypassing the Worker), and registration rate limits use the
   `X-Forwarded-For` (client address) the Worker provides. If unset, the direct peer address is used, which suits deployments that skip the Worker and terminate TLS themselves.
2. Run `personad relay` (as a systemd / launchd service). A local request to `http://127.0.0.1:18670/relay/v1/health` should show `"apns":true`.
   With `proxy_secret` configured, the health check is protected too: local requests must send `X-Relay-Proxy-Secret` or get 403; requests through the website have the header added by the Worker.
3. Mount it under the website domain: the website is a static site on Cloudflare Workers (`site/`), and `site/worker.mjs` forwards `/relay/*` to the relay
   (`run_worker_first` in `wrangler.jsonc`); everything else is still static files. The relay's address and secret are not in the repo:
   use Cloudflare Tunnel (`cloudflared`) to expose local port 18670 as a hostname used only by the Worker, then in `site/` run
   `npx wrangler secret put RELAY_ORIGIN` (value like `https://<tunnel hostname>`) and `npx wrangler secret put RELAY_PROXY_SECRET`
   (same as `proxy_secret`), then deploy `site/` as usual (only with `scripts/release/publish.sh`: it adds `dl/` and `install.sh` in a copy before deploying; running `wrangler deploy` directly in `site/` makes the downloads and install script 404. wrangler 4 needs Node 22+).
   When neither is set, `/relay/*` returns 503 and the rest of the site is unaffected. After deployment, `curl https://<website domain>/relay/v1/health` should return `{"ok":true,"apns":true}`.
4. Ship the App: `python3 scripts/release/asc_signing.py --only <bundle id>.notify` registers the Notification Service Extension's bundle id and creates the "Persona Notify App Store"
   profile (needed when `release.sh` exports); fill `app.push_relay` in `config.json` (e.g. `https://<website domain>/relay`); release to TestFlight as usual.
   Older App versions have no extension and don't register with the relay, so self-hosted hubs still can't push to them; install a version from after this change.

## Persistent Cloudflare Tunnel (Linux)

For a remotely managed Tunnel, configure two ingress rules in Cloudflare: first the origin hostname → `http://127.0.0.1:18670`, last
`http_status:404`. The origin DNS is a proxied CNAME pointing to `<Tunnel ID>.cfargotunnel.com`. Don't reuse an existing service's Tunnel or overwrite its ingress.

Put a `cloudflared` verified against the official release SHA-256 into `~/.persona/bin/`, the Tunnel token into `~/.persona/relay-tunnel.token` (0600),
and create `~/.config/systemd/user/persona-relay-tunnel.service`:

```ini
[Unit]
Description=Persona push relay Cloudflare Tunnel
After=network-online.target persona-relay.service
Wants=network-online.target

[Service]
ExecStart=%h/.persona/bin/cloudflared --no-autoupdate tunnel --protocol http2 run --token-file %h/.persona/relay-tunnel.token
Restart=always
RestartSec=5
NoNewPrivileges=true
UMask=0077

[Install]
WantedBy=default.target
```

`persona-relay.service` is the name of the unit that runs `personad relay` on this machine; if yours differs, use the actual name. Run
`systemctl --user daemon-reload` and `systemctl --user enable --now persona-relay-tunnel.service`, and make sure linger is enabled for the user,
or the service stops when you log out of SSH. `--token-file` keeps the token out of process arguments and logs. When UDP/7844 is blocked on the deploy machine, use HTTP/2 explicitly;
TCP/7844 outbound must still be allowed. The unit being `active` isn't enough: also check that the Tunnel has registered connections and the remote ingress is loaded.

The proxy secret and the grant encryption key are different things: when you change `proxy_secret`, update the Worker's `RELAY_PROXY_SECRET` too, but keep `relay.secret`,
or existing phones' grants become invalid. Save the new proxy secret, the Tunnel token, and a backup of the grant key together in a password manager; pass them only through temporary files with 0600 permissions,
never on the command line, and never print the values. Delete the temporary files after deployment.

## Deployment acceptance

- Website `GET /relay/v1/health`: 200, with both `ok` and `apns` `true`. This only proves the service and APNs key are loaded, not that a real device receives notifications.
- Same path on the origin hostname, without the proxy secret: 403, body `not through the proxy`; the Worker can't be bypassed.
- Registering an invalid device token: 400; a topic not on the allowed list: 403.
- Registering with the correct topic and a test token: 200, grant not printed; a push without a grant: 401.
- A valid grant with invalid ciphertext: 400, nothing sent to APNs. Don't use a real device token in tests, to avoid sending notifications by mistake.
- `app.push_relay` is filled in `config.json`, the notification extension's provisioning profile is valid, the archived `PersonaPushRelay` points to the website's `/relay`,
  and the App's and extension's shared push keychain group both pass `entitlements.py check`.
- After installing the new TestFlight build, verify ordinary notification decryption and Live Activities on a hub without its own APNs key; a simulator health check doesn't replace acceptance on a real device.

## Running your own relay

Only the holder of the APNs key of the Team that owns the App can push to that App, so "running your own relay" = signing and installing the App yourself (your own Team, your own bundle id) +
your own APNs key. In that case it's simpler to put the key straight into your hub's `apns.json` and go direct, and Live Activities don't pass through any third party either.

## Verification

- `server/internal/relay`: grant sealing / tampering / topic, rate limits, the assembled APNs request (fake APNs), 410 passthrough.
- `server/internal/hub`: without `apns.json`, ordinary notifications / interactions / completions / silent wakes / Live Activities all hit a fake relay, with no plaintext in the requests; with `apns.json`, it stays direct as before.
- Ciphertext generated by `server/internal/pushcrypto` is decrypted by PersonaTests with CryptoKit (cross-language test vectors).
- `site/worker.test.mjs` (`node --test`): static paths unchanged, 503 when unconfigured, the secret and client address added when forwarding, forged secrets and cookies dropped.
- Simulator end-to-end: `scripts/test/relay_sim_e2e.sh` takes the public key exported by the App, encrypts with the hub's Go code → local `personad relay` → a fake APNs
  receives the request (checked for no plaintext) → the extension's logic runs in the App process (`PushCrypto.service`, `NotificationServiceTests`) and decrypts each one;
  one with a corrupted byte still shows "New message".
  **`simctl push` doesn't run the Notification Service Extension** (the simulator adds it straight to Notification Center as a local notification; measured 2026-11-01 on Xcode 27.1 / iOS 27.0:
  the extension process didn't start, and a push with `mutable-content` was shown as-is as "New message"), so the extension itself (`PersonaNotify`) can only be verified on a real device.
- Real APNs and the extension on a real device: need the publisher's key and a signed build with the extension; verified after the publisher deploys (`docs/operations/release.md`).
