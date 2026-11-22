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

test("taskIcsUrl builds the owner-scoped export path", () => {
  assert.ok(api, "application API client is missing");
  assert.equal(typeof api.taskIcsUrl, "function");
  assert.equal(api.taskIcsUrl("task 1"), "/api/tasks/task%201/ics");
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

test("task client exposes the supported operations", () => {
  assert.ok(api, "task API client is missing");
  assert.equal(typeof api.listTasks, "function");
  assert.equal(typeof api.createTask, "function");
  assert.equal(typeof api.patchTask, "function");
});

test("listTasks sends the token, the view and the cursor, and reads the page envelope", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({
      items: [{ id: "t1", title: "Apply", due_precision: "date", due_date: "2026-11-12" }],
      page_size: 25, total: 42, next_cursor: "task-cursor-1",
    });
  };
  try {
    const result = await api.listTasks("access-token");
    assert.equal(request.url, "/api/tasks");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result.items.length, 1);
    assert.equal(result.total, 42);
    assert.equal(result.next_cursor, "task-cursor-1");

    await api.listTasks("access-token", true);
    assert.equal(request.url, "/api/tasks?filter=all");

    await api.listTasks("access-token", true, "task-cursor-1");
    const url = new URL(request.url, "http://localhost");
    assert.equal(url.searchParams.get("filter"), "all");
    assert.equal(url.searchParams.get("cursor"), "task-cursor-1");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("createTask posts the full deadline shape as JSON", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "t2", title: "Interview" }, { status: 201 });
  };
  try {
    const result = await api.createTask("access-token", {
      application_id: "app/1",
      title: "Interview",
      due_precision: "instant",
      due_local: "2026-11-12T18:00",
      time_zone: "Asia/Kolkata",
    });
    assert.equal(request.url, "/api/tasks");
    assert.equal(request.options.method, "POST");
    assert.equal(request.options.headers["Content-Type"], "application/json");
    assert.deepEqual(JSON.parse(request.options.body), {
      application_id: "app/1",
      title: "Interview",
      due_precision: "instant",
      due_local: "2026-11-12T18:00",
      time_zone: "Asia/Kolkata",
    });
    assert.equal(result.id, "t2");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("createTask sends the browser reminder zone for date-only tasks", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "t3", due_precision: "date" }, { status: 201 });
  };
  try {
    await api.createTask("access-token", {
      application_id: "app/1",
      title: "Submit form",
      due_precision: "date",
      due_date: "2026-11-21",
      reminder_zone: "Asia/Kolkata",
    });
    assert.deepEqual(JSON.parse(request.options.body), {
      application_id: "app/1",
      title: "Submit form",
      due_precision: "date",
      due_date: "2026-11-21",
      reminder_zone: "Asia/Kolkata",
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("patchTask sends only the status for completion and encodes the id", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "t/1", status: "done" });
  };
  try {
    await api.patchTask("access-token", "t/1", { status: "done" });
    assert.equal(request.url, "/api/tasks/t%2F1");
    assert.equal(request.options.method, "PATCH");
    assert.deepEqual(JSON.parse(request.options.body), { status: "done" });
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

test("notification client exposes the supported operations", () => {
  assert.ok(api, "notification API client is missing");
  assert.equal(typeof api.getNotificationSettings, "function");
  assert.equal(typeof api.patchNotificationSettings, "function");
});

test("getNotificationSettings sends the token and reads the preference envelope", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ reminders_enabled: false });
  };
  try {
    const result = await api.getNotificationSettings("access-token");
    assert.equal(request.url, "/api/notification-settings");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result.reminders_enabled, false);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("patchNotificationSettings sends only the boolean preference as JSON", async () => {
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ reminders_enabled: true });
  };
  try {
    const result = await api.patchNotificationSettings("access-token", true);
    assert.equal(request.url, "/api/notification-settings");
    assert.equal(request.options.method, "PATCH");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(request.options.headers["Content-Type"], "application/json");
    assert.deepEqual(JSON.parse(request.options.body), { reminders_enabled: true });
    assert.equal(result.reminders_enabled, true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("notification settings failures return a useful message", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response("unavailable", { status: 503 });
  try {
    await assert.rejects(api.getNotificationSettings("access-token"), /API or database is unavailable/);
    await assert.rejects(api.patchNotificationSettings("access-token", false), /API or database is unavailable/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("stage choices expose the seven workflow stages", () => {
  assert.ok(api, "application API client is missing");
  assert.deepEqual(api.STAGE_CHOICES, ["saved", "applied", "interviewing", "offer", "accepted", "rejected", "withdrawn"]);
  assert.equal(api.stageLabel("saved"), "Saved");
  assert.equal(api.stageLabel("interviewing"), "Interviewing");
  assert.equal(api.stageLabel("offer"), "Offer");
  assert.equal(api.stageLabel("accepted"), "Accepted");
  assert.equal(api.stageLabel("rejected"), "Rejected");
  assert.equal(api.stageLabel("withdrawn"), "Withdrawn");
});

test("getApplication fetches the owner-scoped record by encoded id", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "app/1", company: "Acme", role: "Engineer", status: "interviewing" });
  };
  try {
    const result = await api.getApplication("access-token", "app/1");
    assert.equal(request.url, "/api/applications/app%2F1");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result.status, "interviewing");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("listEvents returns the paginated stage-history envelope", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({
      items: [{
        id: "ev-1", application_id: "app/1", event_type: "created", from_status: "",
        to_status: "saved", note: "", occurred_at: "2026-11-18T09:00:00Z",
      }],
      page_size: 25,
      next_cursor: "history-cursor",
    });
  };
  try {
    const result = await api.listEvents("access-token", "app/1");
    assert.equal(request.url, "/api/applications/app%2F1/events");
    assert.equal(request.options.headers.Authorization, "Bearer access-token");
    assert.equal(result.items[0].event_type, "created");
    assert.equal(result.next_cursor, "history-cursor");

    await api.listEvents("access-token", "app/1", "history-cursor");
    assert.equal(new URL(request.url, "http://localhost").searchParams.get("cursor"), "history-cursor");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("listTasksByApplication filters the task list by application_id and view", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ items: [{ id: "t1", application_id: "app/1", title: "Apply" }], page_size: 25, total: 1, next_cursor: null });
  };
  try {
    await api.listTasksByApplication("access-token", "app/1");
    const first = new URL(request.url, "http://localhost");
    assert.equal(first.pathname, "/api/tasks");
    assert.equal(first.searchParams.get("application_id"), "app/1");
    assert.equal(first.searchParams.get("filter"), "all");
    assert.equal(first.searchParams.get("cursor"), null);

    await api.listTasksByApplication("access-token", "app/1", "task-cursor-1");
    const second = new URL(request.url, "http://localhost");
    assert.equal(second.searchParams.get("application_id"), "app/1");
    assert.equal(second.searchParams.get("filter"), "all");
    assert.equal(second.searchParams.get("cursor"), "task-cursor-1");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("patchApplication accepts an optional stage note on a stage change", async () => {
  assert.ok(api, "application API client is missing");
  const originalFetch = globalThis.fetch;
  let request;
  globalThis.fetch = async (url, options) => {
    request = { url, options };
    return Response.json({ id: "app/1", company: "Acme", role: "Engineer", status: "interviewing" });
  };
  try {
    const result = await api.patchApplication("access-token", "app/1", { status: "interviewing", stage_note: "Offer call scheduled" });
    assert.equal(request.options.method, "PATCH");
    assert.deepEqual(JSON.parse(request.options.body), { status: "interviewing", stage_note: "Offer call scheduled" });
    assert.equal(result.status, "interviewing");
  } finally {
    globalThis.fetch = originalFetch;
  }
});
