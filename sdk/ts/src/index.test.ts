import { expect, test } from "bun:test";
import { connect, decodeState, encodeState, ExtError, image, isWidget, open, values, type Program } from "./index.ts";

// A hub's end of connect(): send a request, wait for its answer.
function harness(prog: Program) {
  const answers = new Map<string, (f: any) => void>();
  const sent: any[] = [];
  const conn = connect(prog, (l) => {
    const f = JSON.parse(l);
    sent.push(f);
    if (f.id && !f.method) answers.get(f.id)?.(f);
  });
  let n = 0;
  const call = (method: string, params: unknown = {}) =>
    new Promise<any>((resolve) => {
      const id = `h${++n}`;
      answers.set(id, resolve);
      conn.line(JSON.stringify({ id, method, params }));
    });
  return { conn, call, sent };
}

const prog = (): Program => ({
  id: "example",
  version: "1.0.0",
  init: (init) => {
    expect(init.info.id).toBe("example");
  },
  namespaces: {
    thing: () => {
      throw new ExtError("no such thing", "not_found", { id: "x" });
    },
  },
  ui: {
    home: {
      render: (r) => ({ title: `${r.action ?? ""}|${r.state ?? ""}`, sections: [] }),
      action: (r) => {
        if (r.action === "toast") return { toast: "好" };
        r.state = "next";
      },
    },
  },
});

test("the protocol", async () => {
  const { call } = harness(prog());
  expect((await call("ping")).error.code).toBe("not_initialized");
  expect((await call("initialize", { protocol: 9 })).error.code).toBe("wrong_protocol");
  // A hub from before the rename sends the extension under plugin.
  const init = await call("initialize", { protocol: 1, hub: { build: 1 }, plugin: { id: "example", version: "1.0.0" }, granted: {} });
  expect(init.result).toEqual({ protocol: 1, id: "example", version: "1.0.0" });
  expect(await call("ping")).toEqual({ id: "h4", result: {} });
  expect((await call("thing.get")).error).toEqual({ message: "no such thing", code: "not_found", data: { id: "x" } });
  expect((await call("ui.render", { contribution: "nope" })).error.code).toBe("no_contribution");
  expect((await call("ui.action", { contribution: "home", action: "more", state: "first" })).result.view.title).toBe("more|next");
  expect((await call("ui.action", { contribution: "home", action: "toast" })).result).toEqual({ toast: "好" });
  expect((await call("other.method")).error.code).toBe("unknown_method");
  expect((await call("manifest")).error.code).toBe("unknown_method");
});

test("manifest goes out without tags", async () => {
  const { call } = harness({ ...prog(), manifest: { id: "example", version: "1.0.0", tags: ["示例"] } });
  expect((await call("manifest")).result).toEqual({ id: "example", version: "1.0.0" });
});

test("calls to the hub", async () => {
  const { conn, sent } = harness(prog());
  const p = conn.hub.call<{ value: string }>("connector.secrets.load", { key: "a" });
  const req = sent.at(-1);
  expect(req.method).toBe("connector.secrets.load");
  conn.line(JSON.stringify({ id: req.id, result: { value: "v" } }));
  expect(await p).toEqual({ value: "v" });
  conn.hub.invalidate("home");
  expect(sent.at(-1)).toEqual({ event: "ui.invalidate", params: { contribution: "home" } });
  conn.close();
  await expect(conn.hub.call("x")).rejects.toThrow("the hub has gone");
});

test("state", () => {
  expect(decodeState({ state: encodeState({ page: 2 }) }, { page: 0, q: "" })).toEqual({ page: 2, q: "" });
  expect(decodeState({}, { page: 0 })).toEqual({ page: 0 });
  expect(decodeState({ state: "{" }, { page: 0 })).toEqual({ page: 0 });
  expect(decodeState<Record<string, unknown>>({ state: encodeState({ q: "a", extra: 1 }) }, { q: "", page: 0 })).toEqual({ q: "a", page: 0, extra: 1 });
  // Non-object states come back as themselves, not spread index by index into an object.
  expect(decodeState({ state: encodeState("ab") }, "")).toBe("ab");
  expect(decodeState({ state: encodeState([5, 6]) }, [] as number[])).toEqual([5, 6]);
  expect(decodeState({ state: encodeState(3) }, 0)).toBe(3);
  expect(decodeState({ state: encodeState(true) }, false)).toBe(true);
  expect(decodeState({ state: encodeState({ id: "x" }) }, null as { id: string } | null)).toEqual({ id: "x" });
  expect(decodeState({ state: encodeState("ab") }, undefined as string | undefined)).toBe("ab");
  // A state of another kind than the fallback gives the fallback.
  expect(decodeState({ state: '"ab"' }, { page: 0 })).toEqual({ page: 0 });
  expect(decodeState({ state: "[5]" }, { page: 0 })).toEqual({ page: 0 });
  expect(decodeState({ state: "null" }, { page: 0 })).toEqual({ page: 0 });
  expect(decodeState({ state: '{"page":1}' }, [] as number[])).toEqual([]);
  expect(decodeState({ state: "7" }, "")).toBe("");
});

test("push for apps before UI version 3", async () => {
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    ui: {
      home: {
        render: (r) =>
          r.state
            ? { title: `详情 ${r.state}`, sections: [] }
            : { title: "列表", sections: [{ items: [{ type: "row", title: "第一条", action: open("open", "", "1", "第一条") }] }] },
        action: (r) => (r.action === "save" ? { pop: true, toast: "存好了" } : undefined),
      },
    },
  });
  await call("initialize", { protocol: 1, hub: { build: 0 }, granted: {} });
  const newer = await call("ui.render", { contribution: "home", ui_version: 3 });
  expect(newer.result.sections[0].items[0].action).toEqual({ id: "open", push: { state: '"1"', title: "第一条" } });
  const old = await call("ui.render", { contribution: "home", ui_version: 2 });
  const id = old.result.sections[0].items[0].action.id;
  expect(id.startsWith("ext.push.")).toBe(true);
  expect((await call("ui.action", { contribution: "home", ui_version: 2, action: id })).result.view.title).toBe('详情 "1"');
  expect((await call("ui.action", { contribution: "home", ui_version: 2, action: "save", state: '"1"' })).result).toEqual({
    view: { title: "列表", sections: [{ items: [{ type: "row", title: "第一条", action: { id: "ext.push.2", push: { state: '"1"', title: "第一条" } } }] }] },
    toast: "存好了",
  });
  expect((await call("ui.action", { contribution: "home", ui_version: 3, action: "save" })).result).toEqual({ pop: true, toast: "存好了" });
});

test("shortcuts and widgets", async () => {
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    ui: { glance: { render: (r) => ({ title: isWidget(r) ? `widget ${r.size}` : "page", sections: [] }) } },
    shortcuts: { count: (r) => ({ text: `${r.values.what}：3 个` }) },
  });
  await call("initialize", { protocol: 1, hub: { build: 0 }, granted: {} });
  expect((await call("shortcut.run", { contribution: "count", values: { what: "苹果" } })).result).toEqual({ text: "苹果：3 个" });
  expect((await call("shortcut.run", { contribution: "nope" })).error.code).toBe("no_contribution");
  expect((await call("shortcut.list", {})).error.code).toBe("unknown_method");
  expect((await call("ui.render", { contribution: "glance", placement: "widget", size: "small", ui_version: 3 })).result.title).toBe("widget small");
});

test("a result or error JSON cannot encode still gets one answer", async () => {
  const rejections: unknown[] = [];
  const onRejection = (why: unknown) => rejections.push(why);
  process.on("unhandledRejection", onRejection);
  try {
    const cyclic: any = { name: "loop" };
    cyclic.self = cyclic;
    const { conn, call, sent } = harness({
      id: "example",
      version: "1.0.0",
      namespaces: {
        bad: (method) => {
          if (method === "bigint") return { n: 1n };
          if (method === "cyclic") return cyclic;
          if (method === "data") throw new ExtError("bad data", "bad_data", cyclic);
          if (method === "plain") throw { message: "plain", code: "not_found" };
          if (method === "system") throw Object.assign(new Error("no file"), { code: "ENOENT" });
          throw "just a string";
        },
      },
    });
    await call("initialize", { protocol: 1, hub: { build: 1 }, extension: { id: "example", version: "1.0.0" }, granted: {} });
    const bigint = await call("bad.bigint");
    expect(bigint.result).toBeUndefined();
    expect(bigint.error.message).toBeString();
    expect(bigint.error.message.length).toBeGreaterThan(0);
    const cyc = await call("bad.cyclic");
    expect(cyc.result).toBeUndefined();
    expect(cyc.error.message).toBeString();
    expect((await call("bad.data")).error).toEqual({ message: "bad data", code: "bad_data" });
    expect((await call("bad.plain")).error).toEqual({ message: "plain" });
    expect((await call("bad.system")).error).toEqual({ message: "no file" });
    expect((await call("bad.other")).error).toEqual({ message: "just a string" });
    await conn.drain();
    await new Promise((r) => setTimeout(r, 10));
    const answers = sent.filter((f) => f.id && !f.method);
    expect(new Set(answers.map((f) => f.id)).size).toBe(answers.length);
    expect(answers.length).toBe(7);
    expect(rejections).toEqual([]);
  } finally {
    process.off("unhandledRejection", onRejection);
  }
});

// The cases below match sdk/go/ext/rpc_test.go, so the two SDKs answer the hub alike.

const initParams = { protocol: 1, hub: { build: 1 }, extension: { id: "example", version: "1.0.0" }, granted: {} };

test("a call sent right behind initialize waits for it", async () => {
  const slowInit = () => new Promise<void>((resolve) => setTimeout(resolve, 1));
  for (let i = 0; i < 50; i++) {
    const { call } = harness({ id: "example", version: "1.0.0", init: slowInit });
    // Not awaited: both lines are read before initialize answers.
    const [init, ping] = await Promise.all([call("initialize", initParams), call("ping")]);
    expect(init.error).toBeUndefined();
    expect(ping).toEqual({ id: "h2", result: {} });
  }

  const { call } = harness({ id: "example", version: "1.0.0", init: slowInit });
  expect((await call("ping")).error.code).toBe("not_initialized");
  const [wrong, behind] = await Promise.all([call("initialize", { ...initParams, protocol: 2 }), call("ping")]);
  expect(wrong.error.code).toBe("wrong_protocol");
  expect(behind.error.code).toBe("not_initialized");
  const [init, ping] = await Promise.all([call("initialize", initParams), call("ping")]);
  expect(init.error).toBeUndefined();
  expect(ping.result).toEqual({});
});

test("an init that throws can be retried", async () => {
  let runs = 0;
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    init: async () => {
      if (++runs === 1) throw new ExtError("not yet", "unavailable");
    },
  });
  const [failed, behind] = await Promise.all([call("initialize", initParams), call("ping")]);
  expect(failed.error).toEqual({ message: "not yet", code: "unavailable" });
  expect(behind.error.code).toBe("not_initialized");
  // The failed run is forgotten: the next initialize runs init again.
  expect((await call("initialize", initParams)).result).toEqual({ protocol: 1, id: "example", version: "1.0.0" });
  expect(runs).toBe(2);
  expect((await call("ping")).result).toEqual({});
});

test("contributes is left out when init returns none", async () => {
  for (const c of [undefined, {}]) {
    const { call } = harness({ id: "example", version: "1.0.0", init: () => c });
    expect((await call("initialize", initParams)).result).toEqual({ protocol: 1, id: "example", version: "1.0.0" });
  }
  const { call } = harness({ id: "example", version: "1.0.0", init: () => ({ home: false }) });
  expect((await call("initialize", initParams)).result).toEqual({ protocol: 1, id: "example", version: "1.0.0", contributes: { home: false } });
});

test("events go to their namespace once initialized", async () => {
  const events: [string, any][] = [];
  const { conn, call } = harness({
    id: "example",
    version: "1.0.0",
    namespaces: {
      quota: (method, params) => {
        events.push([method, params]);
      },
      broken: () => {
        throw new Error("boom");
      },
      rejects: async () => {
        throw new Error("boom");
      },
    },
  });
  conn.line(JSON.stringify({ event: "quota.x", params: { contribution: "q", n: "before" } }));
  await call("initialize", initParams);
  for (const event of ["quota.x", "other.x", "quota", "broken.x", "rejects.x"]) {
    conn.line(JSON.stringify({ event, params: { contribution: "q", n: "after" } }));
  }
  // The ping is answered after the events were handed out, and the handlers that threw did not break anything.
  expect((await call("ping")).result).toEqual({});
  await Bun.sleep(5);
  expect(events).toEqual([["x", { contribution: "q", n: "after" }]]);
  expect((await call("ping")).result).toEqual({});
});

test("calls to the hub: errors and the hub going", async () => {
  const { conn, sent } = harness(prog());
  const failing = conn.hub.call("connector.secrets.load", { key: "token" });
  const req = sent.at(-1);
  conn.line(JSON.stringify({ id: req.id, error: { message: "no such secret", code: "not_found", data: { key: "token" } } }));
  const e = (await failing.catch((err) => err)) as ExtError;
  expect(e).toBeInstanceOf(ExtError);
  expect([e.message, e.code, e.data]).toEqual(["no such secret", "not_found", { key: "token" }]);

  // An answer to no call of the program's is dropped.
  conn.line(JSON.stringify({ id: "p999", result: {} }));

  const pending = conn.hub.call("quota.http", {});
  expect(sent.at(-1).method).toBe("quota.http");
  conn.close();
  await expect(pending).rejects.toThrow("the hub has gone");
  await expect(pending).rejects.toBeInstanceOf(ExtError);
  const before = sent.length;
  conn.hub.notify("connector.changed", { contribution: "mail" });
  conn.hub.badge("inbox", "3");
  expect(sent.length).toBe(before);
});

test("events to the hub", () => {
  const { conn, sent } = harness(prog());
  const cases: [() => void, unknown][] = [
    [() => conn.hub.notify("connector.changed", { contribution: "mail" }), { event: "connector.changed", params: { contribution: "mail" } }],
    [() => conn.hub.notify("connector.changed"), { event: "connector.changed", params: {} }],
    [() => conn.hub.badge("inbox", "3", "positive"), { event: "ui.badge", params: { contribution: "inbox", text: "3", tone: "positive" } }],
    [() => conn.hub.badge("inbox", "3"), { event: "ui.badge", params: { contribution: "inbox", text: "3" } }],
    [() => conn.hub.invalidate("home"), { event: "ui.invalidate", params: { contribution: "home" } }],
    [() => conn.hub.invalidate(""), { event: "ui.invalidate", params: {} }],
    [() => conn.hub.invalidate(), { event: "ui.invalidate", params: {} }],
  ];
  for (const [send, want] of cases) {
    send();
    expect(sent.at(-1)).toEqual(want);
  }
});

test("action results for apps before UI version 3", async () => {
  const render = (r: { contribution: string; state?: string }) => ({ title: `${r.contribution}:${r.state ?? ""}`, sections: [] });
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    ui: {
      detail: { render },
      home: {
        render,
        action: (r) => {
          switch (r.action) {
            case "push":
              return { push: { state: '"7"' }, toast: "开了" };
            case "push-other":
              return { push: { ui: "detail", state: '"8"' } };
            case "pop":
              return { pop: true, toast: "存好了" };
            case "view":
              return {
                view: {
                  title: "新的",
                  sections: [
                    {
                      items: [
                        { type: "row", title: "第一条", action: open("open", "detail", "1", "第一条") },
                        { type: "empty", title: "没有了", button: { title: "看第二条", action: open("open2", "detail", "2") } },
                      ],
                    },
                  ],
                },
              };
          }
          return { open_url: "https://example.com" };
        },
      },
    },
  });
  await call("initialize", initParams);
  const action = async (id: string) => {
    const f = await call("ui.action", { contribution: "home", ui_version: 2, action: id, state: '"s"' });
    expect(f.error).toBeUndefined();
    return f.result;
  };
  expect(await action("push")).toEqual({ view: { title: 'home:"7"', sections: [] }, toast: "开了" });
  expect(await action("push-other")).toEqual({ view: { title: 'detail:"8"', sections: [] } });
  expect(await action("pop")).toEqual({ view: { title: "home:", sections: [] }, toast: "存好了" });

  const [row, empty] = (await action("view")).view.sections[0].items;
  expect(row.action.id.startsWith("ext.push.")).toBe(true);
  expect(empty.button.action.id.startsWith("ext.push.")).toBe(true);
  expect(row.action.id).not.toBe(empty.button.action.id);
  // The push itself stays for the apps that know it.
  expect(row.action.push).toEqual({ ui: "detail", state: '"1"', title: "第一条" });
  expect(await action(row.action.id)).toEqual({ view: { title: 'detail:"1"', sections: [] } });
  expect(await action(empty.button.action.id)).toEqual({ view: { title: 'detail:"2"', sections: [] } });

  expect(await action("url")).toEqual({ open_url: "https://example.com" });
});

test("lines and items (UI version 5) are answered as they are, not drawn again", async () => {
  let renders = 0;
  const lines = { path: "a.go", side: "new" as const, start: 3, lines: ["x := 1"], total: 9 };
  const items = [{ type: "text" as const, key: "f", text: "看过了" }];
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    ui: {
      home: {
        render: () => {
          renders++;
          return { sections: [] };
        },
        action: (r) => (r.action === "context" ? { lines } : r.action === "viewed" ? { items: [] } : { items }),
      },
    },
  });
  await call("initialize", initParams);
  const action = async (id: string) => (await call("ui.action", { contribution: "home", ui_version: 5, action: id })).result;
  expect(await action("context")).toEqual({ lines });
  // An empty items says done: nothing to draw again (a viewed mark).
  expect(await action("viewed")).toEqual({ items: [] });
  expect(await action("replace")).toEqual({ items });
  expect(renders).toBe(0);
});

test("image() reads the type from the bytes and holds max_age", () => {
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const jpg = new Uint8Array([0xff, 0xd8, 0xff, 0xe0]);
  const gif = new Uint8Array([0x47, 0x49, 0x46, 0x38, 0x39, 0x61]);
  const webp = new Uint8Array([0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50]);
  expect(image("", png).mime).toBe("image/png");
  expect(image("", jpg).mime).toBe("image/jpeg");
  expect(image("", gif).mime).toBe("image/gif");
  expect(image("", webp).mime).toBe("image/webp");
  expect(image("image/custom", png).mime).toBe("image/custom");
  expect(image("", new Uint8Array([1, 2, 3])).mime).toBe("application/octet-stream");
  expect(image("", png, 999999).max_age).toBe(86400);
  expect(image("", png, -5).max_age).toBe(0);
  expect(image("", png).data).toBe(btoa(String.fromCharCode(...png)));
});

test("value readers read UI version 6's formats", () => {
  const req: any = { tz: "Asia/Shanghai", values: {
    on: "true", off: "false", level: " 22.5 ", day: "2026-11-06", at: "07:30",
    until: "2026-11-06T22:00", rooms: '["kitchen","bedroom"]', bad: "x", notjson: "a,b",
  }};
  expect(values.bool(req, "on")).toBe(true);
  expect(values.bool(req, "off")).toBe(false);
  expect(values.bool(req, "none")).toBe(false);
  expect(values.number(req, "level")).toBe(22.5);
  expect(() => values.number(req, "bad")).toThrow();
  expect(() => values.number(req, "none")).toThrow();
  expect(values.date(req, "day")).toEqual({ year: 2026, month: 11, day: 6 });
  expect(() => values.date(req, "bad")).toThrow();
  expect(values.time(req, "at")).toEqual({ hour: 7, minute: 30 });
  expect(() => values.time(req, "bad")).toThrow();
  expect(values.datetime(req, "until")).toEqual({ year: 2026, month: 11, day: 6, hour: 22, minute: 0 });
  expect(() => values.datetime(req, "bad")).toThrow();
  expect(values.multi(req, "rooms")).toEqual(["kitchen", "bedroom"]);
  expect(values.multi(req, "notjson")).toEqual([]);
  expect(values.multi(req, "none")).toEqual([]);
});

test("ui.image serves a page's pictures and refuses what has none", async () => {
  const { call } = harness({
    id: "example",
    version: "1.0.0",
    ui: {
      cam: {
        render: () => ({ sections: [] }),
        image: () => ({ mime: "image/png", data: "AA==", max_age: 10 }),
      },
      plain: { render: () => ({ sections: [] }) },
    },
  });
  await call("initialize", { protocol: 1, hub: { build: 1 }, extension: { id: "example", version: "1.0.0" } });
  expect((await call("ui.image", { contribution: "cam", ref: "front", width: 640 })).result).toEqual({ mime: "image/png", data: "AA==", max_age: 10 });
  expect((await call("ui.image", { contribution: "plain", ref: "x" })).error.code).toBe("unknown_method");
  expect((await call("ui.image", { contribution: "nope", ref: "x" })).error.code).toBe("no_contribution");
});
