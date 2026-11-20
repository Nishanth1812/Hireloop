import assert from "node:assert/strict";
import { test } from "node:test";

const api = await import("./api.ts").catch(() => null);

const page = (overrides = {}) => ({
  items: [],
  page_size: 25,
  total: 0,
  counts: { saved: 0, applied: 0 },
  next_cursor: null,
  ...overrides,
});

test("application client exposes the supported API operations", () => {
  assert.ok(api, "application API client is missing");
  assert.equal(typeof api.listApplications, "function");
  assert.equal(typeof api.createApplication, "function");
  assert.equal(typeof api.patchApplication, "function");
  assert.equal(typeof api.deleteApplication, "function");
});

test("list sends the session token and reads the keyset envelope", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], total: 1, next_cursor: "cursor-1" }));
  };
  try {
    const result = await api.listApplications("access-token", { q: "", status: "", channel: "" });
    assert.equal(request.url, "/api/applications");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result.items[0].id, "app-1");
    assert.equal(result.total, 1);
    assert.equal(result.next_cursor, "cursor-1");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("list sends only the filters that are set and the requested cursor", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json(page());
  };
  try {
    await api.listApplications("access-token", { q: "  acme  ", status: "applied", channel: "campus" }, "cursor-1");
    const url = new URL(request.url, "http://localhost");
    assert.equal(url.pathname, "/api/applications");
    assert.equal(url.searchParams.get("cursor"), "cursor-1");
    assert.equal(url.searchParams.get("q"), "acme");
    assert.equal(url.searchParams.get("status"), "applied");
    assert.equal(url.searchParams.get("channel"), "campus");

    await api.listApplications("access-token", { q: "   ", status: "", channel: "" }, null);
    const bare = new URL(request.url, "http://localhost");
    assert.equal(bare.searchParams.has("q"), false);
    assert.equal(bare.searchParams.has("status"), false);
    assert.equal(bare.searchParams.has("channel"), false);
    assert.equal(bare.searchParams.has("cursor"), false);
    // The default argument is the first page, not a cursor.
    assert.equal(bare.pathname, "/api/applications");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("toChannel narrows form values to the channels the API accepts", async () => {
  assert.ok(api, "application API client is missing");
  assert.equal(api.toChannel("campus"), "campus");
  assert.equal(api.toChannel("off-campus"), "off-campus");
  assert.equal(api.toChannel(""), "");
  assert.equal(api.toChannel("remote"), "");
});

test("create posts the validated application fields as JSON", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "app-2", company: "Northstar", role: "Designer", status: "saved" }, { status: 201 });
  };
  try {
    const result = await api.createApplication("access-token", {
      company: "Northstar",
      role: "Designer",
      status: "saved",
    });
    assert.equal(request.url, "/api/applications");
    assert.equal(request.options.method, "POST");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.deepEqual(JSON.parse(request.options.body), {
      company: "Northstar",
      role: "Designer",
      status: "saved",
    });
    assert.equal(result.id, "app-2");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("create posts the manual capture fields as JSON", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({
      id: "app-3",
      company: "Northstar",
      role: "Designer",
      status: "applied",
      url: "https://jobs.example.com/42",
      source: "Referral",
      channel: "campus",
      job_id: "REQ-42",
      applied_on: "2026-11-02",
      address: "me@example.com",
      notes: "Placement cell notice.",
    }, { status: 201 });
  };
  try {
    const result = await api.createApplication("access-token", {
      company: "Northstar",
      role: "Designer",
      status: "applied",
      url: "https://jobs.example.com/42",
      source: "Referral",
      channel: "campus",
      job_id: "REQ-42",
      applied_on: "2026-11-02",
      address: "me@example.com",
      notes: "Placement cell notice.",
    });
    assert.deepEqual(JSON.parse(request.options.body), {
      company: "Northstar",
      role: "Designer",
      status: "applied",
      url: "https://jobs.example.com/42",
      source: "Referral",
      channel: "campus",
      job_id: "REQ-42",
      applied_on: "2026-11-02",
      address: "me@example.com",
      notes: "Placement cell notice.",
    });
    assert.equal(result.channel, "campus");
    assert.equal(result.applied_on, "2026-11-02");
    assert.equal(result.notes, "Placement cell notice.");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("patch sends only the requested field to the encoded application URL", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "app/1", company: "Acme", role: "Engineer", status: "applied" });
  };
  try {
    const result = await api.patchApplication("access-token", "app/1", { status: "applied" });
    assert.equal(request.url, "/api/applications/app%2F1");
    assert.equal(request.options.method, "PATCH");
    assert.equal(request.options.headers["Content-Type"], "application/json");
    assert.deepEqual(JSON.parse(request.options.body), { status: "applied" });
    assert.equal(result.status, "applied");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("API unavailability returns a useful message", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(null, { status: 503 });
  try {
    await assert.rejects(api.listApplications("access-token", { q: "", status: "", channel: "" }), /API or database is unavailable/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("a rejected request returns a useful message", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response("invalid application query", { status: 400 });
  try {
    await assert.rejects(api.listApplications("access-token", { q: "", status: "", channel: "" }, "cursor-1"), /API rejected/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("network failure returns a useful message", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
  try {
    await assert.rejects(api.listApplications("access-token", { q: "", status: "", channel: "" }), /API or database is unavailable/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("delete accepts the API's empty success response", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return new Response(null, { status: 204 });
  };
  try {
    await api.deleteApplication("access-token", "app-1");
    assert.equal(request.url, "/api/applications/app-1");
    assert.equal(request.options.method, "DELETE");
  } finally {
    globalThis.fetch = originalFetch;
  }
});