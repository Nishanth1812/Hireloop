import assert from "node:assert/strict";
import { test } from "node:test";

const api = await import("./api.ts").catch(() => null);

test("application client exposes the supported API operations", () => {
  assert.ok(api, "application API client is missing");
  assert.equal(typeof api.listApplications, "function");
  assert.equal(typeof api.createApplication, "function");
  assert.equal(typeof api.patchApplication, "function");
  assert.equal(typeof api.deleteApplication, "function");
});

test("list sends the session token and returns applications", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json([{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }]);
  };
  try {
    const result = await api.listApplications("access-token");
    assert.equal(request.url, "/api/applications");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result[0].id, "app-1");
  } finally {
    globalThis.fetch = originalFetch;
  }
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

test("create posts the optional source and address fields as JSON", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "app-3", company: "Northstar", role: "Designer", status: "applied", source: "Referral", address: "me@example.com" }, { status: 201 });
  };
  try {
    const result = await api.createApplication("access-token", {
      company: "Northstar",
      role: "Designer",
      status: "applied",
      source: "Referral",
      address: "me@example.com",
    });
    assert.deepEqual(JSON.parse(request.options.body), {
      company: "Northstar",
      role: "Designer",
      status: "applied",
      source: "Referral",
      address: "me@example.com",
    });
    assert.equal(result.source, "Referral");
    assert.equal(result.address, "me@example.com");
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
    await assert.rejects(api.listApplications("access-token"), /API or database is unavailable/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("network failure returns a useful message", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => { throw new TypeError("Failed to fetch"); };
  try {
    await assert.rejects(api.listApplications("access-token"), /API or database is unavailable/);
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
