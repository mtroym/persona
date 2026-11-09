import { extensionEnv, hubEnv } from './compat.js';
import { ExactJSON } from './exact-json.ts';
export { ExactJSON, JSONIntegerError } from './exact-json.ts';
export { ExtensionStorage, storageBytes, storageValue } from './storage.ts';
// Persona extension SDK for TypeScript (docs/extensions/extensions.md "Writing an extension: SDK, templates and CLI"): an extension program's side of
// the process protocol, for Bun. Build the program with `bun build --compile` into one executable per platform:
// the package holds only that, and the hub needs no runtime.
//
//   import { main } from "@persona/extension";
//   main({ id: "example", version: "1.0.0", ui: { home: { render: () => ({ sections: [] }) } } });

import type * as wire from "./wire.ts";
import { PageBridge, isComponentPage, componentSnapshot, type ComponentPage } from "./page-bridge.ts";
export { componentPage } from "./page-bridge.ts";

export type * from "./wire.ts";
export { COMPONENT_TYPES, ERROR_CODES, TONES } from "./wire.ts";

/** The version of the process protocol this SDK speaks. */
export const PROTOCOL = 1;

// Shared by separately bundled SDK copies in one JS realm; this marks public errors, not authorization.
const EXT_ERROR = Symbol.for("@persona/extension.ExtError");

/** An error on the wire: a message for people (never a secret or a message's body), and a code the hub acts on. */
export class ExtError extends Error {
  readonly [EXT_ERROR] = true;

  constructor(
    message: string,
    readonly code?: string,
    readonly data?: unknown,
  ) {
    super(message);
  }
}

export const noContribution = (id: string) => new ExtError(`no contribution ${id}`, "no_contribution");
export const unknownMethod = (method: string) => new ExtError(`unknown method ${method}`, "unknown_method");
export const unsupported = (why: string) => new ExtError(why, "unsupported");

/** Answers the calls of one namespace: method is without the namespace ("status" for connector.status). */
export type Handler = (method: string, params: any) => unknown | Promise<unknown>;

/** Original request params, before Number rounding. Missing params and JSON null stay distinct.
 * This view covers opted-in namespace and initialization RPC requests, not replies, events or serialization. */
export interface ExactCall {
  readonly rawParams: string | undefined;
  readonly params: ExactJSON | undefined;
}

/** Events still use legacy params and have no call metadata. */
export type ExactHandler = (method: string, params: any, call: ExactCall | undefined) => unknown | Promise<unknown>;
const EXACT_PARAMS = Symbol.for("@persona/extension.exactParams");
type OptInHandler = Handler & { [EXACT_PARAMS]?: true };

/** Explicitly opt one namespace into original JSON request params. The second argument
 * remains legacy JSON.parse data. Lexical int64 errors become public invalid_params errors. */
export function withExactParams(handler: ExactHandler): Handler {
  const wrapped = (method: string, params: any, call?: ExactCall) => handler(method, params, call);
  Object.defineProperty(wrapped, EXACT_PARAMS, { value: true });
  return wrapped;
}


/** One ui contribution: what it draws, and what its buttons do. */
export interface Page {
  render(req: UIRequest): wire.View | Promise<wire.View>;
  /** Nothing (or an empty result) draws the page again, with req.state as the action left it. */
  action?(req: UIRequest): wire.ActionResult | void | Promise<wire.ActionResult | void>;
  /** Serves a picture the page drew by ref (UI version 6: an image's src, a row's thumbnail or avatar, a tile's
   * image), fetched by the hub for the apps: image() makes the answer. Absent answers unknown_method. */
  image?(req: ImageRequest): wire.UIImage | Promise<wire.UIImage>;
}

/** A ui.render or ui.action, and the hub to call while drawing. */
export interface UIRequest extends wire.UIParams {
  hub: Hub;
  /** Derived by the host page lifecycle, never inferred from ui_version. Absent on ordinary snapshots. */
  transport?: "snapshot" | "stream";
  /** Mounted root factory lifetime; disposal or suspension aborts pending preparation. */
  signal?: AbortSignal;
}

/** A ui.image: the ref, the contribution that drew it, the pixel width the app wants (0: any), and the hub to call. */
export interface ImageRequest extends wire.UIImageParams {
  hub: Hub;
}

/** A shortcut.run: its params' values (strings), for whom; and the hub to call while running. */
export interface ShortcutRequest extends wire.ShortcutRunParams {
  hub: Hub;
}

/** Whether a page draws a widget (placement widget); its size is then the family: draw less, nothing to tap. */
export const isWidget = (req: { placement?: string }) => req.placement === "widget";

export interface Init extends wire.InitializeParams {
  /** The extension's own (id, version, directories), under whichever key the hub sent it. */
  info: wire.ExtensionInfo;
}

export interface Program {
  id: string;
  version: string;
  /** persona-extension.json, answered to manifest (an extension running elsewhere must have it). */
  manifest?: unknown;
  /** Called once with what the hub sent; says what the program contributes this run (undefined: all of it). */
  init?(init: Init, hub: Hub): Record<string, unknown> | void | Promise<Record<string, unknown> | void>;
  /** Handlers by namespace ("connector", "quota", "event", …). */
  namespaces?: Record<string, Handler>;
  /** Pages by ui contribution id (widgets too: placement widget, drawn per family; see isWidget). */
  ui?: Record<string, Page | ComponentPage>;
  /** Shortcuts by contribution id: shortcut.run goes to them (docs/extensions/extensions.md "System-level places"). */
  shortcuts?: Record<string, (req: ShortcutRequest) => wire.ShortcutResult | void | Promise<wire.ShortcutResult | void>>;
  /** If set, served on the hub's socket (runtime.http in the manifest) by main(). */
  fetch?(req: Request): Response | Promise<Response>;
}

/** Initialization RPC metadata; direct initializer calls have no raw frame and receive undefined. */
export type ExactInitializer = (this: Program, init: Init, hub: Hub, call: ExactCall | undefined) => ReturnType<NonNullable<Program["init"]>>;
const EXACT_INIT_KEY = "@persona/extension.exactInitParams";

/** Opt initialization into exact params without changing the parsed Init, Hub or receiver.
 * The symbol is resolved only when used, so importing unrelated SDK APIs has no new side effect. */
export function withExactInit(handler: ExactInitializer): NonNullable<Program["init"]> {
  const wrapped = function(this: Program, init: Init, hub: Hub, call?: ExactCall) {
    return Reflect.apply(handler, this, [init, hub, call]);
  };
  Object.defineProperty(wrapped, Symbol.for(EXACT_INIT_KEY), { value: true });
  return wrapped;
}

function initializeProgram(prog: Program, init: Init, hub: Hub, rawFrame: string): ReturnType<NonNullable<Program["init"]>> {
  const handler = prog.init;
  if (handler == null) return;
  if (typeof handler !== "function") throw new TypeError("prog.init is not a function");
  // Only the wrapper's own inert data marker opts in; inherited/getter markers do not.
  if (Object.getOwnPropertyDescriptor(handler, Symbol.for(EXACT_INIT_KEY))?.value === true) {
    const exact = ExactJSON.parse(rawFrame).field("params");
    return Reflect.apply(handler, prog, [init, hub, Object.freeze({ rawParams: exact?.raw, params: exact })]);
  }
  return Reflect.apply(handler, prog, [init, hub]);
}

const kindOf = (v: unknown) => (v === null ? "null" : Array.isArray(v) ? "array" : typeof v);

/** Reads a page's state, which the program wrote with encodeState. An object state is merged over an object fallback
 * (keys it lacks keep the fallback's); any other state is returned as itself when it is the fallback's kind (string,
 * number, array, ...), or as is when the fallback is null or undefined. A state that does not parse, or is of another
 * kind, gives the fallback. */
export function decodeState<T>(req: { state?: string }, fallback: T): T {
  if (!req.state) return fallback;
  let v: unknown;
  try {
    v = JSON.parse(req.state);
  } catch {
    return fallback;
  }
  if (fallback === null || fallback === undefined) return v as T;
  const kind = kindOf(fallback);
  if (kindOf(v) !== kind) return fallback;
  return (kind === "object" ? { ...fallback, ...(v as object) } : v) as T;
}

/** Writes a page's state (at most 16 KB). */
export const encodeState = (v: unknown) => JSON.stringify(v);

/** What ui.image answers for a picture's bytes: the type (read from the bytes when mime is ""), and how long the hub
 * may keep it in seconds (a camera 5 to 10, a map 30, a still 86400; 0 not at all). */
export const image = (mime: string, bytes: Uint8Array, maxAge = 60): wire.UIImage => ({
  mime: mime || imageType(bytes),
  data: imageBase64(bytes),
  max_age: Math.min(Math.max(maxAge, 0), 86400),
});

function imageBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let at = 0; at < bytes.length; at += 8192) binary += String.fromCharCode(...bytes.subarray(at, at + 8192));
  return btoa(binary);
}

// imageType reads a picture's type from its bytes, as the hub does (http.DetectContentType on the Go side).
function imageType(bytes: Uint8Array): string {
  if (bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47) return "image/png";
  if (bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) return "image/jpeg";
  if (bytes[0] === 0x47 && bytes[1] === 0x49 && bytes[2] === 0x46 && bytes[3] === 0x38) return "image/gif";
  if (bytes.length >= 12 && bytes[0] === 0x52 && bytes[1] === 0x49 && bytes[2] === 0x46 && bytes[3] === 0x46 &&
      bytes[8] === 0x57 && bytes[9] === 0x45 && bytes[10] === 0x42 && bytes[11] === 0x50) return "image/webp";
  return "application/octet-stream";
}

/** Readers for what an action carries (req.values) as UI version 6 writes it: a switch "true"/"false", a slider's or a
 * stepper's number, a date YYYY-MM-DD, a time HH:MM, a datetime YYYY-MM-DDTHH:MM (the user's wall clock; req.tz is
 * their zone), a multichoice a JSON array. The hub turns what an older app sends into the same. */
export const values = {
  /** A switch's or a toggle's value: true for "true" only. */
  bool(req: UIRequest, name: string): boolean { return req.values?.[name] === "true"; },
  /** A slider's, a stepper's or a number field's value; throws when it is missing or not a number. */
  number(req: UIRequest, name: string): number {
    const s = req.values?.[name]?.trim();
    if (!s) throw new Error(`${name}: no value`);
    const n = Number(s);
    if (!Number.isFinite(n)) throw new Error(`${name}: not a number: ${JSON.stringify(s)}`);
    return n;
  },
  /** A date field's calendar day (YYYY-MM-DD). */
  date(req: UIRequest, name: string): { year: number; month: number; day: number } {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(req.values?.[name] ?? "");
    if (!m) throw new Error(`${name}: a date YYYY-MM-DD, not ${JSON.stringify(req.values?.[name])}`);
    return { year: +m[1], month: +m[2], day: +m[3] };
  },
  /** A time field's hour and minute (HH:MM, 24 hours). */
  time(req: UIRequest, name: string): { hour: number; minute: number } {
    const m = /^(\d{2}):(\d{2})$/.exec(req.values?.[name] ?? "");
    if (!m) throw new Error(`${name}: a time HH:MM, not ${JSON.stringify(req.values?.[name])}`);
    return { hour: +m[1], minute: +m[2] };
  },
  /** A datetime field's wall clock (YYYY-MM-DDTHH:MM) in the user's zone (req.tz). */
  datetime(req: UIRequest, name: string): { year: number; month: number; day: number; hour: number; minute: number } {
    const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(req.values?.[name] ?? "");
    if (!m) throw new Error(`${name}: a datetime YYYY-MM-DDTHH:MM, not ${JSON.stringify(req.values?.[name])}`);
    return { year: +m[1], month: +m[2], day: +m[3], hour: +m[4], minute: +m[5] };
  },
  /** A multichoice's chosen values; [] when it is empty or not a JSON array of strings. */
  multi(req: UIRequest, name: string): string[] {
    try {
      const v = JSON.parse(req.values?.[name] ?? "[]");
      return Array.isArray(v) && v.every((x) => typeof x === "string") ? v : [];
    } catch {
      return [];
    }
  },
};

const PUSH_PREFIX = "ext.push.";

/** An action that opens a page on top of this one: ui.render of ui ("" this one) with state, title shown until it is
 * drawn. id names it for ui.action in apps before UI version 3 (the SDK answers those itself). */
export function open(id: string, ui: string, state: unknown, title?: string): wire.Action {
  return { id, push: { ...(ui ? { ui } : {}), state: encodeState(state), ...(title ? { title } : {}) } };
}

interface Frame {
  id?: string;
  method?: string;
  event?: string;
  params?: unknown;
  result?: unknown;
  error?: { message: string; code?: string; data?: unknown };
}

type Write = (line: string) => void;

/** The hub, for a program's calls and events. */
export class Hub {
  private next = 0;
  private pending = new Map<string, { resolve(v: unknown): void; reject(e: unknown): void }>();
  closed = false;

  constructor(private write: Write) {}

  /** Calls a method of the hub's (connector.secrets.load, quota.http, …). */
  call<T = unknown>(method: string, params: unknown = {}): Promise<T> {
    if (this.closed) return Promise.reject(new ExtError("the hub has gone"));
    const id = `p${++this.next}`;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject });
      this.write(JSON.stringify({ id, method, params }));
    });
  }

  /** Sends the hub an event (connector.changed, …). */
  notify(event: string, params: unknown = {}) {
    if (!this.closed) this.write(JSON.stringify({ event, params }));
  }

  /** The badge on a nav_page's entry; empty text clears it. */
  badge(contribution: string, text: string, tone?: wire.Tone) {
    this.notify("ui.badge", { contribution, text, ...(tone ? { tone } : {}) });
  }

  /** Tells the apps a ui contribution's data changed ("" is all of this extension's): the pages on screen draw again. */
  invalidate(contribution = "") {
    this.notify("ui.invalidate", contribution ? { contribution } : {});
  }

  /** @internal */
  answer(f: Frame) {
    const p = this.pending.get(f.id!);
    if (!p) return;
    this.pending.delete(f.id!);
    if (f.error) p.reject(new ExtError(f.error.message, f.error.code ?? undefined, f.error.data));
    else p.resolve(f.result);
  }

  /** @internal */
  close() {
    this.closed = true;
    for (const p of this.pending.values()) p.reject(new ExtError("the hub has gone"));
    this.pending.clear();
  }
}

/** Only SDK-branded public errors send code/data, even across bundles; native errors like ENOENT stay local. */
function wireError(err: unknown): { message: string; code?: string; data?: unknown } {
  const publicError = err as ExtError | null | undefined;
  // A thrown object can have hostile getters/proxy traps; failure to read the brand is not a public error.
  try {
    if (publicError && typeof publicError === "object" && publicError[EXT_ERROR] === true) {
      const { message, code, data } = publicError;
      if (typeof message === "string" && (code === undefined || typeof code === "string")) {
        return { message, ...(code ? { code } : {}), ...(data !== undefined ? { data } : {}) };
      }
    }
  } catch {}
  try {
    const message = (err as { message?: unknown } | null | undefined)?.message;
    if (typeof message === "string") return { message };
  } catch {}
  try {
    return { message: String(err) };
  } catch {
    return { message: "error" };
  }
}

/**
 * Encodes the answer to request id. A result JSON cannot encode (a BigInt, a cycle) is answered with an error
 * saying why, and error data it cannot encode is left out, so every request gets an answer, as with the Go SDK.
 */
function answer(id: unknown, out: { result: unknown } | { error: { message: string; code?: string; data?: unknown } }): string {
  try {
    return JSON.stringify({ id, ...out });
  } catch (err) {
    if ("error" in out) {
      const { data: _, ...error } = out.error;
      return JSON.stringify({ id, error });
    }
    return JSON.stringify({ id, error: wireError(err) });
  }
}

/** One side of the protocol: feed it lines, it writes answers with write. Returned for tests and other transports. */
export function connect(prog: Program, write: Write) {
  const hub = new Hub(write);
  const pageBridge = new PageBridge(prog.ui ?? {}, hub);
  let initialized = false;
  let contributes: Record<string, unknown> | undefined;
  const inflight = new Set<Promise<unknown>>();
  // Only negotiated page lifecycle calls are cancelable; services retain legacy semantics.
  const pageCalls = new Map<string, AbortController>();
  const cancelablePage = (method: string) => ["ui.page.mount", "ui.page.event", "ui.page.resume"].includes(method);
  let initializing: Promise<void> | undefined;
  let accepting = true;
  let shuttingDown: Promise<unknown[]> | undefined;

  async function request(method: string, params: any, rawFrame: string, signal?: AbortSignal): Promise<unknown> {
    if (method === "initialize") {
      if (params?.protocol !== PROTOCOL) {
        throw new ExtError(`protocol ${params?.protocol}, not ${PROTOCOL}`, "wrong_protocol");
      }
      if (!initialized) {
        const init: Init = { ...params, info: params.extension ?? params.plugin ?? { id: "", version: "" } };
        initializing ??= Promise.resolve(initializeProgram(prog, init, hub, rawFrame)).then((c) => {
          // An empty map says nothing, as in the Go SDK: the answer leaves contributes out (all of it).
          contributes = c && Object.keys(c).length > 0 ? c : undefined;
          initialized = true;
        });
        try {
          await initializing;
        } catch (e) {
          initializing = undefined;
          throw e;
        }
      }
      return { protocol: PROTOCOL, id: prog.id, version: prog.version,
        ...(Object.values(prog.ui ?? {}).some(isComponentPage) ? { ui_page_cancellation: 1 } : {}),
        ...(contributes ? { contributes } : {}) };
    }
    if (method === "manifest") {
      if (prog.manifest === undefined) throw new ExtError("no manifest here: it is in the package", "unknown_method");
      return answeredManifest(prog.manifest);
    }
    // A call sent right behind initialize (without waiting for its answer) waits for it.
    if (!initialized && initializing) await initializing.catch(() => {});
    if (!initialized) throw new ExtError("not initialized", "not_initialized");
    if (method === "ping") return {};
    if (method === "ui.page.supported") return { contributions: Object.entries(prog.ui ?? {}).filter(([, page]) => isComponentPage(page)).map(([id]) => id) };
    if (method.startsWith("ui.page.")) return pageBridge.call(method.slice(8), params ?? {}, signal);
    const dot = method.indexOf(".");
    if (dot < 0) throw unknownMethod(method);
    const ns = method.slice(0, dot);
    const rest = method.slice(dot + 1);
    const h = prog.namespaces?.[ns];
    if (h) {
      if ((h as OptInHandler)[EXACT_PARAMS] === true) {
        const exact = ExactJSON.parse(rawFrame).field("params");
        return (h as ExactHandler)(rest, params ?? {}, Object.freeze({ rawParams: exact?.raw, params: exact }));
      }
      return h(rest, params ?? {});
    }
    if (ns === "ui" && prog.ui) return ui(rest, params ?? {});
    if (ns === "shortcut" && prog.shortcuts) {
      if (rest !== "run") throw unknownMethod(method);
      const run = prog.shortcuts[params?.contribution];
      if (!run) throw noContribution(params?.contribution);
      return (await run({ ...params, values: params.values ?? {}, hub })) ?? {};
    }
    throw unknownMethod(method);
  }

  // Apps before UI version 3 do not know push and pop: an action that pushes gets an id of the SDK's for them, and
  // that ui.action draws the pushed page in place of this one; a result's push draws the page, a pop the page as
  // first drawn. The program writes push and pop once, for every app.
  const pushes = new Map<string, wire.Push>();
  let pushN = 0;
  const older = (p: wire.UIParams) => p.ui_version > 0 && p.ui_version < 3;
  function forOlder(v: wire.View | undefined, contribution: string) {
    const fix = (a?: wire.Action) => {
      if (!a?.push) return;
      const id = `${PUSH_PREFIX}${++pushN}`;
      pushes.set(id, { ...a.push, ui: a.push.ui || contribution });
      if (pushes.size > 512) pushes.delete(pushes.keys().next().value!);
      a.id = id;
    };
    for (const sec of v?.sections ?? []) {
      for (const c of sec.items as any[]) {
        fix(c.action);
        fix(c.button?.action);
      }
    }
  }
  async function draw(req: UIRequest, contribution: string, state: string) {
    const page = prog.ui?.[contribution];
    if (!page) throw noContribution(contribution);
    if (isComponentPage(page)) {
      if (req.placement === "widget") return componentSnapshot(page, {...req,contribution,state});
      throw new ExtError("component pages require an owner-bound host session", "unsupported");
    }
    const v = await page.render({ ...req, contribution, state, action: undefined, values: undefined });
    if (older(req)) forOlder(v, contribution);
    return v;
  }

  async function serveImage(params: wire.UIImageParams): Promise<unknown> {
    const page = prog.ui?.[params.contribution];
    if (!page || isComponentPage(page)) throw noContribution(params.contribution);
    if (!page.image) throw unknownMethod("ui.image");
    return page.image({ ...params, hub });
  }

  async function ui(method: string, params: wire.UIParams): Promise<unknown> {
    if (method === "image") return serveImage(params as unknown as wire.UIImageParams); // ui.image carries UIImageParams
    if (method !== "render" && method !== "action") throw unknownMethod(`ui.${method}`);
    const page = prog.ui![params.contribution];
    if (!page) throw noContribution(params.contribution);
    const req: UIRequest = { ...params, hub };
    if (method === "render") return draw(req, req.contribution, req.state ?? "");
    if (isComponentPage(page)) throw new ExtError("component pages require an owner-bound host session", "unsupported");
    if (older(req) && req.action?.startsWith(PUSH_PREFIX)) {
      const push = pushes.get(req.action);
      return { view: await draw(req, push?.ui || req.contribution, push ? push.state ?? "" : req.state ?? "") };
    }
    const res = await page.action?.(req);
    // UI version 5: lines, or components in place (an empty items: done, nothing to draw again), as the Go SDK.
    if (res && (res.view || res.toast || res.open_url || res.push || res.pop || res.lines || res.items)) {
      if (!older(req)) return res;
      if (res.push) return { view: await draw(req, res.push.ui || req.contribution, res.push.state ?? ""), ...(res.toast ? { toast: res.toast } : {}) };
      if (res.pop) return { view: await draw(req, req.contribution, ""), ...(res.toast ? { toast: res.toast } : {}) };
      forOlder(res.view, req.contribution);
      return res;
    }
    const v = await page.render(req);
    if (older(req)) forOlder(v, req.contribution);
    return { view: v };
  }

  function line(text: string) {
    if (!accepting) return;
    let f: Frame;
    try {
      const parsed: unknown = JSON.parse(text);
      if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return;
      f = parsed as Frame;
      // Validate dispatch fields before using them, without changing legacy routing precedence.
      if ([f.id, f.method, f.event].some(value => value != null && typeof value !== "string")) return;
      if (f.error != null && (typeof f.error !== "object" || Array.isArray(f.error)
        || typeof f.error.message !== "string" || (f.error.code != null && typeof f.error.code !== "string"))) return;
    } catch {
      return;
    }
    if (f.method) {
      const id = f.id;
      if (id && pageCalls.has(id)) { write(answer(id, { error: { message: "duplicate active page request ID" } })); return; }
      const controller = id && cancelablePage(f.method) ? new AbortController() : undefined;
      if (controller && pageCalls.size >= 256) { write(answer(id, { error: { message: "page request quota" } })); return; }
      if (id && controller) pageCalls.set(id, controller);
      const done = Promise.resolve()
        .then(() => request(f.method!, f.params, text, controller?.signal))
        .then(
          (result) => write(answer(id, { result: result ?? {} })),
          (err) => write(answer(id, { error: wireError(err) })),
        );
      inflight.add(done);
      done.finally(() => { inflight.delete(done); if (id && pageCalls.get(id) === controller) pageCalls.delete(id); });
    } else if (f.event === "rpc.cancel") {
      const id = (f.params as { id?: unknown } | undefined)?.id;
      if (typeof id === "string") pageCalls.get(id)?.abort();
    } else if (f.event) {
      const dot = f.event.indexOf(".");
      const h = initialized && dot > 0 ? prog.namespaces?.[f.event.slice(0, dot)] : undefined;
      if (h) Promise.resolve().then(() => h(f.event!.slice(dot + 1), f.params ?? {})).catch(() => {});
    } else if (f.id && (f.error != null || Object.prototype.hasOwnProperty.call(f, "result"))) {
      hub.answer(f);
    }
  }

  return {
    hub,
    line,
    /** Waits for the answers to the requests already read, without closing the hub or stopping input. */
    drain: () => Promise.all([...inflight]),
    /** Closes outbound hub calls/events and UI work; accepted service requests can still answer. */
    close: () => { pageBridge.close(); hub.close(); },
    /** After the final input frame, stops input and UI work, closes outbound calls, then drains accepted answers.
     * Repeated calls return the same promise. Keep the writer open until it settles, then flush/close it yourself.
     * Arbitrary service handlers and event work still require an external deadline. */
    shutdown: () => {
      if (!shuttingDown) {
        accepting = false;
        pageBridge.close();
        hub.close();
        shuttingDown = Promise.all([...inflight]);
      }
      return shuttingDown;
    },
  };
}

/** What the hub gives a program in its environment, under the names of any hub. */
export function env() {
  const get = (name: string) =>
    extensionEnv(name);
  const json = (s: string) => {
    try {
      return s ? JSON.parse(s) : undefined;
    } catch {
      return undefined;
    }
  };
  return {
    id: get("ID"),
    dir: get("DIR"),
    data: get("DATA"),
    socket: get("SOCKET"),
    /** For a program that does not speak the protocol (rpc: none); otherwise initialize has it. */
    config: json(get("CONFIG")),
    secrets: json(get("SECRETS")) as Record<string, string> | undefined,
    hubURL: hubEnv("URL"),
    hubToken: hubEnv("TOKEN"),
  };
}

/** The manifest as the program answers it to a hub: without `tags`, the market's (nwverse/persona#2424), which hubs
 * from before them refuse as an unknown field. */
function answeredManifest(m: unknown): unknown {
  if (m === null || typeof m !== "object" || Array.isArray(m) || !("tags" in m)) return m;
  const { tags: _tags, ...rest } = m as Record<string, unknown>;
  return rest;
}

/** Runs prog as the hub starts it: the protocol on stdin / stdout, HTTP on the hub's socket when it has fetch.
 * Exits when the hub closes stdin or stops the program. */
export async function main(prog: Program) {
  if (process.argv[2] === "manifest" && prog.manifest !== undefined) {
    process.stdout.write(JSON.stringify(answeredManifest(prog.manifest)) + "\n");
    return;
  }
  let server: { stop(): void } | undefined;
  if (prog.fetch) {
    const sock = env().socket;
    if (!sock) {
      console.error("no PERSONA_EXTENSION_SOCKET: runtime.http is not set in the manifest");
      process.exit(2);
    }
    try {
      require("node:fs").unlinkSync(sock);
    } catch {}
    const handler = prog.fetch;
    server = Bun.serve({
      unix: sock,
      fetch: (req) => (new URL(req.url).pathname === "/health" ? new Response("ok") : handler(req)),
    });
  }
  const conn = connect(prog, (l) => process.stdout.write(l + "\n"));
  for (const sig of ["SIGTERM", "SIGINT"] as const) process.on(sig, () => process.exit(0));
  const decoder = new TextDecoder();
  let buf = "";
  let spoke = false;
  for await (const chunk of Bun.stdin.stream()) {
    spoke = true;
    buf += decoder.decode(chunk, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const l = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (l) conn.line(l);
    }
  }
  // A last frame without a trailing newline is still a request (Go's bufio.Scanner returns it too).
  const last = (buf + decoder.decode()).trim();
  if (last) conn.line(last);
  await conn.shutdown();
  // rpc: none (only HTTP): the hub sends nothing on stdin; serve until it stops the program.
  if (server && !spoke) return;
  // stdout is buffered on pipes; completion of the handler's write is not completion of the actual output.
  await new Promise<void>(resolve => process.stdout.end(resolve));
  server?.stop();
  process.exit(0);
}
