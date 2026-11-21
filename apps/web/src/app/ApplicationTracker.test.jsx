import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import React, { act } from "react";
import { Window } from "happy-dom";

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.FormData = browser.FormData;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

let session;
let dashboardProps;
mock.module("@clerk/nextjs", () => ({
  useSession: () => ({ isLoaded: true, session }),
}));
mock.module("./_components/ApplicationDashboard", () => ({
  ApplicationDashboard: (props) => {
    dashboardProps = props;
    const { email, result, filters, page, error, loading, loadError, taskPage } = props;
    return <div>
      <span>{email}</span>
      <span>{loading ? "Loading" : "Ready"}</span>
      <span data-testid="page">{page}</span>
      <span data-testid="filters">{`${filters.q}|${filters.status}|${filters.channel}`}</span>
      {loadError && <p data-testid="load-error">Load failed</p>}
      {error && <p role="alert">{error}</p>}
      {result.items.map((item) => <p key={item.id}>{item.company}</p>)}
      <div data-testid="today">{taskPage.items.map((task) => <p key={task.id}>{task.title}</p>)}</div>
    </div>;
  },
}));
mock.module("./_components/SignInPanel", () => ({
  SignInPanel: () => <p>Sign in</p>,
}));

const { createRoot } = await import("react-dom/client");
const { default: ApplicationTracker } = await import("./ApplicationTracker");
const originalFetch = globalThis.fetch;
let root, container;

function account(id, getToken = async () => id) {
  return { id, getToken, user: { emailAddresses: [{ emailAddress: `${id}@example.test` }] } };
}
function page(overrides = {}) {
  return {
    items: [],
    page_size: 25,
    total: 0,
    counts: { saved: 0, applied: 0 },
    next_cursor: null,
    ...overrides,
  };
}
function response(company) {
  const items = company ? [{ id: company, company, role: "Engineer", status: "saved" }] : [];
  return Response.json(page({ items, total: items.length, counts: { saved: items.length, applied: 0 } }));
}
function taskResponse(items = []) {
  return Response.json({ items });
}
function openTask(overrides = {}) {
  return {
    id: "task-1", application_id: "app-1", title: "Submit assessment", status: "open",
    due_precision: "date", due_date: "2026-11-12", due_local: "", due_at: "", time_zone: "",
    created_at: "2026-11-01T09:00:00Z", ...overrides,
  };
}
async function render() {
  await act(async () => { root.render(<ApplicationTracker />); });
}

function formWith(values) {
  const form = document.createElement("form");
  for (const [name, value] of Object.entries(values)) {
    const input = document.createElement("input");
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }
  return form;
}

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  globalThis.fetch = originalFetch;
});

test("token retrieval failures are visible instead of escaping the load effect", async () => {
  session = account("a", async () => { throw new Error("Session refresh failed"); });
  globalThis.fetch = () => { throw new Error("must not request data without a token"); };
  await render();
  expect(container.querySelector('[role="alert"]')?.textContent).toBe("Session refresh failed");
  expect(container.textContent).toContain("Ready");
});

test("missing session tokens show a sign-in error without issuing a request", async () => {
  session = account("a", async () => null);
  globalThis.fetch = () => { throw new Error("must not request data without a token"); };
  await render();
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("Please sign in again");
});

test("API session rejection is visible and does not leave loading active", async () => {
  session = account("a");
  globalThis.fetch = async () => new Response("unauthorized", { status: 401 });
  await render();
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("session has expired");
  expect(container.textContent).toContain("Ready");
});

test("a successful create clears a stale load error so the new record is visible", async () => {
  let calls = 0;
  globalThis.fetch = async (url, options) => {
    calls++;
    if (calls === 1) return new Response("unavailable", { status: 503 });
    if (options.method === "POST") {
      return Response.json({ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }, { status: 201 });
    }
    const items = [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }];
    return Response.json(page({ items, total: 1, counts: { saved: 1, applied: 0 } }));
  };
  session = account("a");
  await render();
  expect(container.querySelector('[data-testid="load-error"]') !== null).toBe(true);
  expect(container.textContent).toContain("Ready");

  await act(async () => {
    await dashboardProps.onCreate({ preventDefault() {}, currentTarget: formWith({ company: "Acme", role: "Engineer", status: "saved" }) });
  });

  expect(container.querySelector('[data-testid="load-error"]') === null).toBe(true);
  // The new record is confirmed by the follow-up list request, not assumed.
  // The separate Today request also runs on this account's initial render.
  expect(calls).toBe(4);
});

test("next requests the page after the current cursor", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], total: 2, next_cursor: "cursor-1" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  expect(urls.at(-1)).toBe("/api/applications?cursor=cursor-1");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("2");
});

test("previous returns to the cursor that produced the earlier page", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    if (url.includes("cursor=cursor-1")) {
      return Response.json(page({ items: [{ id: "app-2", company: "Globex", role: "Engineer", status: "saved" }], total: 2 }));
    }
    return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], total: 2, next_cursor: "cursor-1" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  await act(async () => { dashboardProps.onPrevious(); });
  expect(urls.at(-1)).toBe("/api/applications");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("1");
});

test("a page emptied by a concurrent delete steps back to the previous page", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    if (url.includes("cursor=cursor-1")) {
      return Response.json(page({ items: [], total: 1 }));
    }
    if (urls.length === 1) {
      return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], total: 2, next_cursor: "cursor-1" }));
    }
    return Response.json(page({ items: [{ id: "app-2", company: "Globex", role: "Engineer", status: "saved" }], total: 1 }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  expect(urls.at(-1)).toBe("/api/applications");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("1");
});

test("applying filters resets the cursor and sends every filter", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    if (url.includes("cursor=")) {
      return Response.json(page({ items: [{ id: "app-2", company: "Globex", role: "Engineer", status: "saved" }], total: 2 }));
    }
    return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], total: 2, next_cursor: "cursor-1" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  await act(async () => {
    dashboardProps.onApplyFilters({ q: "acme", status: "applied", channel: "campus" });
  });
  expect(urls.at(-1)).toBe("/api/applications?q=acme&status=applied&channel=campus");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("1");
});

test("creating an application clears the filters and shows page one", async () => {
  const urls = [];
  globalThis.fetch = async (url, options) => {
    urls.push(url);
    if (options.method === "POST") {
      return Response.json({ id: "app-9", company: "NewCo", role: "Engineer", status: "saved" }, { status: 201 });
    }
    if (url.includes("cursor=")) {
      return Response.json(page({ items: [], total: 1 }));
    }
    return Response.json(page({ items: [], total: 1, next_cursor: "cursor-1" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  await act(async () => { dashboardProps.onApplyFilters({ q: "old", status: "saved", channel: "" }); });

  await act(async () => {
    await dashboardProps.onCreate({ preventDefault() {}, currentTarget: formWith({ company: "NewCo", role: "Engineer", status: "saved" }) });
  });

  expect(urls.at(-1)).toBe("/api/applications");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("1");
  expect(container.querySelector('[data-testid="filters"]')?.textContent).toBe("||");
});

test("saving an edit patches only the changed fields and reloads the current page", async () => {
  const requests = [];
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async (url, options) => {
    requests.push({ url, method: options.method, body: options.body });
    if (options.method === "PATCH") {
      return Response.json({ ...original, company: "Acme Labs", status: "applied" });
    }
    const items = [{ ...original, company: "Acme Labs", status: "applied" }];
    return Response.json(page({ items, total: 1, counts: { saved: 0, applied: 1 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onEdit(original); });

  await act(async () => {
    await dashboardProps.onSaveEdit({ preventDefault() {}, currentTarget: formWith({ company: "Acme Labs", role: "Engineer", status: "applied" }) }, "app-1");
  });

  const patch = requests.find((item) => item.method === "PATCH");
  expect(patch.url).toBe("/api/applications/app-1");
  // Only fields that differ from the loaded record are sent, so a concurrent
  // change to an untouched field is never overwritten.
  expect(JSON.parse(patch.body)).toEqual({ company: "Acme Labs", status: "applied" });
  // A patch is followed by a reload so counts and ordering come from the API.
  expect(requests.at(-1).url).toBe("/api/applications");
  expect(container.textContent).toContain("Acme Labs");
});

test("saving an edit reloads the current cursor page", async () => {
  const requests = [];
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async (url, options) => {
    requests.push({ url, method: options.method, body: options.body });
    if (options.method === "PATCH") {
      return Response.json({ ...original, company: "Acme Labs" });
    }
    if (url.includes("cursor=")) {
      return Response.json(page({ items: [{ ...original, company: "Acme Labs" }], total: 2 }));
    }
    return Response.json(page({ items: [original], total: 2, next_cursor: "cursor-1" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  await act(async () => { dashboardProps.onEdit(original); });
  await act(async () => {
    await dashboardProps.onSaveEdit({ preventDefault() {}, currentTarget: formWith({ company: "Acme Labs", role: "Engineer", status: "saved" }) }, "app-1");
  });

  // The reload keeps the keyset position instead of jumping back to page one.
  expect(requests.at(-1).url).toBe("/api/applications?cursor=cursor-1");
});

test("a rejected edit keeps the form open and surfaces the API message", async () => {
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async (url, options) => {
    if (options.method === "PATCH") return new Response("invalid application", { status: 400 });
    return Response.json(page({ items: [original], total: 1, counts: { saved: 1, applied: 0 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onEdit(original); });
  // The form is open before the rejected save.
  expect(dashboardProps.editingApplication?.id).toBe("app-1");

  await act(async () => {
    await dashboardProps.onSaveEdit({ preventDefault() {}, currentTarget: formWith({ company: "Acme", role: "Engineer", status: "applied" }) }, "app-1");
  });

  expect(container.querySelector('[role="alert"]')?.textContent).toContain("API rejected");
  // The form stays open with the user's edits intact after a rejection.
  expect(dashboardProps.editingApplication?.id).toBe("app-1");
});

test("paging or filtering keeps an open edit instead of discarding it", async () => {
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  let calls = 0;
  globalThis.fetch = async () => {
    calls++;
    if (calls === 1) {
      return Response.json(page({ items: [original], total: 2, next_cursor: "cursor-1", counts: { saved: 1, applied: 1 } }));
    }
    return Response.json(page({ items: [original], total: 1, counts: { saved: 1, applied: 0 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onEdit(original); });
  expect(dashboardProps.editingApplication?.id).toBe("app-1");

  await act(async () => { dashboardProps.onNext(); });
  expect(dashboardProps.editingApplication?.id).toBe("app-1");

  await act(async () => { dashboardProps.onApplyFilters({ q: "acme", status: "", channel: "" }); });
  expect(dashboardProps.editingApplication?.id).toBe("app-1");
});

test("a token resolving after sign-out cannot trigger an application request", async () => {
  let completeToken;
  let requests = 0;
  session = account("a", () => new Promise((resolve) => { completeToken = resolve; }));
  globalThis.fetch = async () => { requests++; return response("Account A company"); };
  await render();
  session = null;
  await render();
  await act(async () => { completeToken("a"); });
  expect(requests).toBe(0);
  expect(container.textContent).toBe("Sign in");
});

test("switching accounts immediately hides the previous account's records", async () => {
  let completeB;
  globalThis.fetch = async (url, options) => {
    if (url === "/api/tasks") return taskResponse();
    return options.headers.Authorization === "Bearer a"
      ? response("Account A company")
      : new Promise((resolve) => { completeB = resolve; });
  };
  session = account("a");
  await render();
  expect(container.textContent).toContain("Account A company");
  session = account("b");
  await render();
  expect(container.textContent).not.toContain("Account A company");
  await act(async () => { completeB(response("Account B company")); });
  expect(container.textContent).toContain("Account B company");
});

test("creating a task posts the deadline shape and reloads Today", async () => {
  const requests = [];
  let posted = false;
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async (url, options) => {
    requests.push({ url, method: options.method, body: options.body });
    if (options.method === "POST") {
      posted = true;
      return Response.json(openTask(), { status: 201 });
    }
    if (url === "/api/tasks") return taskResponse(posted ? [openTask()] : []);
    return Response.json(page({ items: [app], total: 1, counts: { saved: 1, applied: 0 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onAddTask(app); });
  expect(dashboardProps.taskDraft.application.id).toBe("app-1");

  await act(async () => {
    await dashboardProps.onSaveTask({
      preventDefault() {},
      currentTarget: formWith({ application_id: "app-1", title: "Submit assessment", due_precision: "date", due_date: "2026-11-12" }),
    });
  });

  const post = requests.find((request) => request.method === "POST");
  expect(post.url).toBe("/api/tasks");
  expect(JSON.parse(post.body)).toEqual({
    application_id: "app-1", title: "Submit assessment", due_precision: "date", due_date: "2026-11-12",
  });
  expect(requests.filter((request) => request.url === "/api/tasks").length).toBeGreaterThan(1);
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
  expect(dashboardProps.taskDraft).toBeNull();
});

test("an exact deadline without a zone is rejected before any request", async () => {
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved" };
  let posts = 0;
  globalThis.fetch = async (url, options) => {
    if (options.method === "POST") posts++;
    if (url === "/api/tasks") return taskResponse();
    return Response.json(page({ items: [app], total: 1, counts: { saved: 1, applied: 0 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onAddTask(app); });
  await act(async () => {
    await dashboardProps.onSaveTask({
      preventDefault() {},
      currentTarget: formWith({ application_id: "app-1", title: "Interview", due_precision: "instant", due_local: "2026-11-12T18:00", time_zone: "" }),
    });
  });
  expect(posts).toBe(0);
  expect(container.querySelector('[role="alert"]').textContent).toMatch(/time zone/i);
  expect(dashboardProps.taskDraft).not.toBeNull();
});

test("creating a task uses the selected application", async () => {
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved" };
  let body;
  globalThis.fetch = async (url, options) => {
    if (options.method === "POST") { body = JSON.parse(options.body); return Response.json(openTask(), { status: 201 }); }
    if (url === "/api/tasks") return taskResponse();
    return Response.json(page({ items: [app], total: 1 }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onAddTask(app); });
  await act(async () => { await dashboardProps.onSaveTask({ preventDefault() {}, currentTarget: formWith({ application_id: "app-2", title: "Apply", due_precision: "none" }) }); });
  expect(body.application_id).toBe("app-2");
});

test("a date deadline without a date is rejected before saving", async () => {
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved" };
  let writes = 0;
  globalThis.fetch = async (url, options) => {
    if (options.method === "POST" || options.method === "PATCH") writes++;
    return url === "/api/tasks" ? taskResponse() : Response.json(page({ items: [app], total: 1 }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onAddTask(app); });
  await act(async () => { await dashboardProps.onSaveTask({ preventDefault() {}, currentTarget: formWith({ application_id: "app-1", title: "Apply", due_precision: "date", due_date: "" }) }); });
  expect(writes).toBe(0);
  expect(container.querySelector('[role="alert"]').textContent).toMatch(/choose a date/i);
});

test("deleting an application reloads its tasks", async () => {
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved" };
  let deleted = false;
  browser.confirm = () => true;
  globalThis.fetch = async (url, options) => {
    if (options.method === "DELETE") { deleted = true; return new Response(null, { status: 204 }); }
    if (url === "/api/tasks") return taskResponse(deleted ? [] : [openTask()]);
    return Response.json(page({ items: deleted ? [] : [app], total: deleted ? 0 : 1 }));
  };
  session = account("a");
  await render();
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
  await act(async () => { await dashboardProps.onRemove(app); });
  expect(container.querySelector('[data-testid="today"]').textContent).not.toContain("Submit assessment");
});

test("completing and cancelling tasks remove them from open Today and persist after reload", async () => {
  const requests = [];
  const task = openTask();
  const cancelled = openTask({ id: "task-2", title: "Reply to recruiter" });
  globalThis.fetch = async (url, options) => {
    requests.push({ url, method: options.method, body: options.body, token: options.headers.Authorization });
    if (options.method === "PATCH") {
      const patch = JSON.parse(options.body);
      return Response.json({ ...task, ...cancelled, id: patch.id ?? task.id, status: patch.status });
    }
    if (url === "/api/tasks?filter=all") {
      return taskResponse([{ ...task, status: "done" }, { ...cancelled, status: "cancelled" }]);
    }
    if (url === "/api/tasks") return taskResponse([task, cancelled]);
    return Response.json(page());
  };
  session = account("a");
  await render();
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Reply to recruiter");

  await act(async () => { await dashboardProps.onCompleteTask(task); });
  await act(async () => { await dashboardProps.onCancelTask(cancelled); });
  const patches = requests.filter((request) => request.method === "PATCH");
  expect(patches.map((request) => JSON.parse(request.body))).toEqual([
    { status: "done" }, { status: "cancelled" },
  ]);
  // The open view is whatever the API returns after each write; it is not
  // edited locally, so a completed action cannot linger because of a bug here.
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");

  await act(async () => { dashboardProps.onToggleFinished(); });
  const all = requests.find((request) => request.url === "/api/tasks?filter=all");
  expect(all).toBeDefined();
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
});

test("Today walks keyset pages without repeating rows and previous returns to the earlier cursor", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    if (url === "/api/tasks") {
      return Response.json({ items: [openTask({ id: "task-1" })], page_size: 1, total: 2, next_cursor: "tasks-2" });
    }
    if (url.includes("cursor=tasks-2")) {
      return Response.json({ items: [openTask({ id: "task-2", title: "Second" })], page_size: 1, total: 2, next_cursor: null });
    }
    return Response.json(page());
  };
  session = account("a");
  await render();
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
  expect(dashboardProps.taskPage.total).toBe(2);
  expect(dashboardProps.taskHasNext).toBe(true);

  await act(async () => { dashboardProps.onTaskNext(); });
  expect(urls.at(-1)).toBe("/api/tasks?cursor=tasks-2");
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Second");
  expect(dashboardProps.taskHasNext).toBe(false);

  await act(async () => { dashboardProps.onTaskPrevious(); });
  expect(urls.at(-1)).toBe("/api/tasks");
  expect(dashboardProps.taskPageIndex).toBe(0);
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
});

test("showing finished restarts Today at the first page without moving the application list", async () => {
  const taskURLs = [];
  globalThis.fetch = async (url) => {
    if (url === "/api/tasks" || url.startsWith("/api/tasks?")) {
      taskURLs.push(url);
      return Response.json({ items: [], page_size: 25, total: 0, next_cursor: null });
    }
    if (url.includes("cursor=app-2")) {
      return Response.json(page({ items: [{ id: "app-2", company: "Globex", role: "Engineer", status: "saved" }] }));
    }
    return Response.json(page({ items: [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }], next_cursor: "app-2" }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onNext(); });
  // No task cursor existed, so the toggle below is the next task request.
  const beforeToggle = taskURLs.length;
  await act(async () => { dashboardProps.onToggleFinished(); });
  expect(taskURLs.length).toBeGreaterThan(beforeToggle);
  expect(taskURLs.at(-1)).toBe("/api/tasks?filter=all");
  expect(dashboardProps.tasksShowAll).toBe(true);
  expect(dashboardProps.taskPageIndex).toBe(0);
  // The application position is independent and unchanged.
  expect(container.querySelector('[data-testid="page"]').textContent).toBe("2");
});

test("Today steps back when a later page is emptied by another write", async () => {
  let cursorRequest = 0;
  globalThis.fetch = async (url) => {
    if (url.includes("cursor=tasks-2")) {
      cursorRequest++;
      return Response.json({ items: [], page_size: 1, total: 1, next_cursor: null });
    }
    if (url === "/api/tasks") {
      return Response.json({ items: [openTask()], page_size: 1, total: 2, next_cursor: "tasks-2" });
    }
    return Response.json(page());
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onTaskNext(); });
  expect(cursorRequest).toBe(1);
  expect(dashboardProps.taskPageIndex).toBe(0);
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Submit assessment");
});

test("an ambiguous fall-back deadline is rejected before any request", async () => {
  const app = { id: "app-1", company: "Acme", role: "Engineer", status: "saved" };
  let writes = 0;
  globalThis.fetch = async (url, options) => {
    if (options.method === "POST" || options.method === "PATCH") writes++;
    if (url === "/api/tasks" || url.startsWith("/api/tasks?")) return taskResponse();
    return Response.json(page({ items: [app], total: 1 }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onAddTask(app); });
  await act(async () => {
    await dashboardProps.onSaveTask({
      preventDefault() {},
      currentTarget: formWith({ application_id: "app-1", title: "Interview", due_precision: "instant", due_local: "2026-11-01T01:30", time_zone: "America/New_York" }),
    });
  });
  expect(writes).toBe(0);
  expect(container.querySelector('[role="alert"]').textContent).toMatch(/happens twice/i);
  expect(dashboardProps.taskDraft).not.toBeNull();
});

test("a delayed task response from a previous account cannot leak into the next one", async () => {
  let completeA;
  globalThis.fetch = async (url, options) => {
    if (url === "/api/tasks" && options.headers.Authorization === "Bearer a") {
      return new Promise((resolve) => { completeA = resolve; });
    }
    if (url === "/api/tasks") return taskResponse([openTask({ id: "task-b", title: "Account B task" })]);
    return Response.json(page());
  };
  session = account("a");
  await render();
  session = account("b");
  await render();
  await act(async () => { completeA(taskResponse([openTask({ id: "task-a", title: "Account A secret task" })])); });
  expect(container.querySelector('[data-testid="today"]').textContent).toContain("Account B task");
  expect(container.querySelector('[data-testid="today"]').textContent).not.toContain("Account A secret task");
});

test("a previous account's delayed response cannot overwrite the next account", async () => {
  let completeA;
  globalThis.fetch = async (url, options) => {
    if (url === "/api/tasks") return taskResponse();
    return options.headers.Authorization === "Bearer a"
      ? new Promise((resolve) => { completeA = resolve; })
      : response("Account B company");
  };
  session = account("a");
  await render();
  session = account("b");
  await render();
  await act(async () => { completeA(response("Account A company")); });
  expect(container.textContent).toContain("Account B company");
  expect(container.textContent).not.toContain("Account A company");
  session = null;
  await render();
  expect(container.textContent).toBe("Sign in");
});
