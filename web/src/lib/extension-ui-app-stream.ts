// One owner-bound page on the existing authenticated app socket. No automatic write retries.
import { credentialOwner } from "./api";
import { appSocketReady, getState, onAppSocket, onPrivateUIStream, send } from "./store";
import { decodeStreamEnvelope, ExtensionUIStreamSession, NATIVE_STREAM_COMPONENTS, STREAM_LIMITS, same, type StreamPageOptions } from "./extension-ui-stream";
import type { Action, UIStreamActionResult, UIStreamEvent, UIStreamReceipt, UIStreamRequest } from "./extension-wire";
import type { Interaction } from "./types";
import { canTryUIStream } from "./extension-ui-stream-gate";
export type NativePagePermission = Interaction & { manager?: string };

export { canTryUIStream } from "./extension-ui-stream-gate";
type Waiting = { request: UIStreamRequest; action?: Action; resolve: (accepted?: boolean) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout>; receipt?: UIStreamReceipt };
export interface AppStreamChannel {
  send(request: UIStreamRequest): void;
  frames(listener: (raw: string) => void): () => void;
  connection(listener: (connected: boolean) => void): () => void;
  connected(): boolean;
}
export class AppUIStreamPage {
  readonly session: ExtensionUIStreamSession;
  private waiting = new Map<string, Waiting>();
  private receipts = new Map<string, UIStreamReceipt>();
  private presentations: Array<{ receipt: UIStreamReceipt; event: UIStreamEvent }> = [];
  private off: Array<() => void>;
  private disposed = false;
  constructor(options: Omit<StreamPageOptions, "transport">, readonly manager: string, private channel: AppStreamChannel,
    private effect: (result: UIStreamActionResult, event: UIStreamEvent) => void, private failed: (error: Error, fallback: boolean) => void) {
    this.session = new ExtensionUIStreamSession({ ...options, transport: { send: (request) => this.deliver(request) } });
    this.session.setConnected(channel.connected());
    this.off = [channel.frames((raw) => this.receive(raw)), channel.connection((connected) => {
      this.session.setConnected(connected);
      if (!connected) this.rejectAll(new Error("App socket disconnected"));
    }), this.session.subscribe(() => this.flush())];
    if (typeof window !== "undefined") {
      const hide = (event: PageTransitionEvent) => { if (!event.persisted) this.dispose(); };
      window.addEventListener("pagehide", hide);
      this.off.push(() => window.removeEventListener("pagehide", hide));
    }
  }
  private deliver(request: UIStreamRequest): Promise<void | boolean> {
    if (this.disposed || !this.channel.connected()) return Promise.reject(new Error("App socket unavailable"));
    if (this.waiting.size >= 64 || this.waiting.has(request.request_id)) return Promise.reject(new Error("Stream request limit or duplicate correlation"));
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        const error = new Error("Stream response timeout");
        this.waiting.delete(request.request_id); reject(error);
        if (request.request === "open") this.failed(error, true);
      }, STREAM_LIMITS.delivery);
      const event = request.request === "event" ? request.interaction : undefined;
      const action = event ? this.session.callback(event.key, event.action, event.event) : undefined;
      this.waiting.set(request.request_id, { request, action, resolve, reject, timer });
      try { this.channel.send(request); }
      catch (error) { this.settle(request.request_id, error instanceof Error ? error : new Error(String(error))); }
    });
  }
  /** Native page sessions are not Agent sessions. Only the manager's exact pending saved-action
   * confirmation belongs here; this grants neither generic Agent approval nor source authority. */
  ownsHostConfirmation = (ix: NativePagePermission): boolean => {
    if (this.disposed || !this.channel.connected() || ix.manager !== this.manager || ix.kind !== "permission" || ix.tool_name !== "extension.ui" || ix.machine_id !== "") return false;
    const state = this.session.getState();
    if (state.phase !== "ready") return false;
    return [...this.waiting.values()].some((pending) => {
      if (pending.request.request !== "event" || !pending.action?.confirm) return false;
      const event = pending.request.interaction;
      return ix.session_id === event.session && event.session === state.snapshot?.session && event.epoch === state.snapshot.epoch && event.version === state.snapshot.version && pending.action.confirm === ix.summary && same(pending.action, ix.tool_input);
    });
  };
  private settle(id: string | undefined, error?: Error, accepted?: boolean) {
    if (!id) return;
    const pending = this.waiting.get(id);
    if (!pending) return;
    clearTimeout(pending.timer); this.waiting.delete(id);
    if (error) pending.reject(error); else pending.resolve(accepted);
  }
  private rejectAll(error: Error) { for (const id of this.waiting.keys()) this.settle(id, error); }
  private identity(receipt: Pick<UIStreamReceipt, "session" | "epoch" | "event_id">) { return JSON.stringify([receipt.session, receipt.epoch, receipt.event_id]); }
  readResult = async (event: Readonly<UIStreamEvent>) => {
    const receipt = this.receipts.get(this.identity(event));
    return receipt?.status === "accepted" ? { ...(receipt.result?.lines ? { lines: receipt.result.lines } : {}) } : null;
  };
  private receive(raw: string) {
    if (this.disposed) return;
    try {
      const frame = decodeStreamEnvelope(raw);
      if (frame.manager !== this.manager) return;
      const pending = frame.request_id ? this.waiting.get(frame.request_id) : undefined;
      const current = this.session.getState().snapshot;
      if (frame.kind === "snapshot") {
        const read = pending && ["open", "snapshot", "resume"].includes(pending.request.request);
        if (read || current?.session === frame.snapshot.session) {
          // Large updates may arrive as fitted snapshots instead of deltas. They advance the
          // tree but must not settle an event before its separately correlated receipt.
          if (this.session.receiveSnapshot(frame.snapshot) && read) this.settle(frame.request_id);
        }
      } else if (frame.kind === "delta") {
        if (current?.session === frame.delta.session && current.epoch === frame.delta.epoch) this.session.receiveDelta(frame.delta);
      } else if (frame.kind === "receipt") {
        const event = pending?.request.request === "event" ? pending.request.interaction : undefined;
        const r = frame.receipt;
        if (!event || r.session !== event.session || r.epoch !== event.epoch || r.event_id !== event.event_id) return;
        if (!["accepted", "ambiguous", "rejected"].includes(r.status) || r.status !== "accepted" && r.result) throw new Error("Invalid receipt status/result");
        if (r.status !== "rejected" && r.version !== event.version + 1) throw new Error("Invalid consumed-event fence");
        if (new TextEncoder().encode(JSON.stringify(r.result ?? {})).length > 64 * 1024 || new TextEncoder().encode(r.result?.push?.state ?? "").length > 16 * 1024) throw new Error("Receipt result byte limit");
        if (r.result?.open_url) {
          const url = new URL(r.result.open_url);
          if (url.protocol !== "https:" || url.username || url.password) throw new Error("Unsafe receipt URL");
        }
        const old = this.receipts.get(this.identity(r));
        if (old && !same(old, r)) throw new Error("Conflicting receipt replay");
        pending!.receipt = r; this.flush();
      } else if (frame.kind === "error") {
        if (!pending) return;
        const error = new Error(frame.error.message || frame.error.code);
        this.settle(frame.request_id, error);
        const request = pending.request;
        if ((request.request === "resume" || request.request === "snapshot") && frame.error.code === "expired" && current?.session === request.session && current.epoch === request.epoch) {
          this.rejectAll(error); this.receipts.clear(); this.presentations = [];
          this.session.reopenExpired(request.session, request.epoch);
        } else {
          // Transient/unknown failures retain the old page for an explicit read-only retry.
          // Event failures never replay via HTTP or allocate a fresh session.
          this.failed(error, request.request === "open");
        }
      } else if (frame.kind === "suspended") {
        if (current?.session === frame.session && pending?.request.request === "suspend") this.settle(frame.request_id);
      } else if (frame.kind === "closed" && current?.session === frame.session) {
        this.settle(frame.request_id);
        if (this.session.getState().phase !== "closed") { this.failed(new Error("Stream session closed"), true); this.dispose(); }
      }
      this.flush();
    } catch (error) {
      // A malformed private envelope cannot lend correlation or authority to another page.
      this.failed(error instanceof Error ? error : new Error(String(error)), false);
      this.session.refresh();
    }
  }
  private flushing = false;
  private flush() {
    if (this.flushing || this.disposed) return;
    this.flushing = true;
    try {
      const state = this.session.getState(), snapshot = state.snapshot;
      if (!snapshot || state.phase !== "ready") return;
      for (const [id, pending] of this.waiting) {
        const r = pending.receipt;
        if (!r || r.session !== snapshot.session || r.epoch !== snapshot.epoch || snapshot.version < r.version) continue;
        const key = this.identity(r);
        if (!this.receipts.has(key)) {
          if (this.receipts.size >= 256) this.receipts.delete(this.receipts.keys().next().value!);
          this.receipts.set(key, r); // Mark before presentation, including reentrant navigation.
          if (r.status === "accepted" && r.result && pending.request.request === "event") this.presentations.push({ receipt: r, event: pending.request.interaction });
          else if (r.status !== "accepted") this.failed(new Error(r.status), false);
        }
        // A consumed ambiguous callback is settled, never transformed into a fresh action.
        this.settle(id, undefined, r.status === "accepted");
      }
      // Navigation may synchronously unmount this page. Wait until the event pump has
      // settled its caller and retired the active request, not just the transport promise.
      if (this.session.getState().pending === 0) for (const presentation of this.presentations.splice(0)) {
        if (this.disposed) break;
        if (presentation.receipt.session === snapshot.session && presentation.receipt.epoch === snapshot.epoch) this.effect(presentation.receipt.result!, presentation.event);
      }
    } finally { this.flushing = false; }
  }
  dispose() {
    if (this.disposed) return;
    // An open may still allocate after its navigation page unmounts. Reap that correlated snapshot,
    // not an unrelated owner's session, before dropping the bounded cleanup subscription.
    const opening = new Set([...this.waiting.values()].filter((p) => p.request.request === "open").map((p) => p.request.request_id));
    if (opening.size && !this.session.getState().snapshot) {
      const off = this.channel.frames((raw) => {
        try {
          const frame = decodeStreamEnvelope(raw);
          if (frame.manager !== this.manager || !frame.request_id || !opening.has(frame.request_id)) return;
          if (frame.kind === "snapshot" && this.channel.connected()) this.channel.send({ request: "close", protocol: 1, request_id: frame.request_id + "-dispose", session: frame.snapshot.session, epoch: frame.snapshot.epoch });
          if (frame.kind === "snapshot" || frame.kind === "error") { clearTimeout(timer); off(); }
        } catch { /* A malformed reply cannot grant cleanup authority. */ }
      });
      const timer = setTimeout(off, STREAM_LIMITS.delivery);
    }
    this.session.close();
    this.disposed = true;
    this.rejectAll(new Error("Stream page disposed"));
    this.off.forEach((off) => off()); this.receipts.clear(); this.presentations = [];
  }
}

export function appUIStreamPage(options: Omit<StreamPageOptions, "transport">, manager: string,
  effect: (result: UIStreamActionResult, event: UIStreamEvent) => void, failed: (error: Error, fallback: boolean) => void) {
  const owner = credentialOwner(), origin = location.origin;
  const group = () => getState().mesh.find((h) => h.self)?.hub_id || location.origin;
  const hubGroup = group();
  const channel: AppStreamChannel = {
    connected: () => appSocketReady() && credentialOwner() === owner && location.origin === origin && group() === hubGroup && canTryUIStream(getState().features),
    send: (request) => {
      if (!channel.connected()) throw new Error("Stream owner changed or socket unavailable");
      send({ type: "extension_ui_stream", manager, request });
    },
    frames: (listener) => onPrivateUIStream((raw) => { if (channel.connected()) listener(raw); }),
    connection: (listener) => onAppSocket((connected) => listener(connected && channel.connected())),
  };
  return new AppUIStreamPage({ ...options, context: { ...options.context, stream: { protocol: 1, components: [...NATIVE_STREAM_COMPONENTS] } } }, manager, channel, effect, failed);
}
