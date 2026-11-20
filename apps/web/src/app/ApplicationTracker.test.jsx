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
    const { email, result, filters, page, error, loading, loadError } = props;
    return <div>
      <span>{email}</span>
      <span>{loading ? "Loading" : "Ready"}</span>
      <span data-testid="page">{page}</span>
      <span data-testid="filters">{`${filters.q}|${filters.status}|${filters.channel}`}</span>
      {loadError && <p data-testid="load-error">Load failed</p>}
      {error && <p role="alert">{error}</p>}
      {result.items.map((item) => <p key={item.id}>{item.company}</p>)}
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
    page: 1,
    pageSize: 25,
    total: 0,
    totalPages: 0,
    counts: { saved: 0, applied: 0 },
    ...overrides,
  };
}
function response(company) {
  const items = company ? [{ id: company, company, role: "Engineer", status: "saved" }] : [];
  return Response.json(page({ items, total: items.length, totalPages: items.length ? 1 : 0, counts: { saved: items.length, applied: 0 } }));
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
    return Response.json(page({ items, total: 1, totalPages: 1, counts: { saved: 1, applied: 0 } }));
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
  expect(calls).toBe(3);
});

test("changing the page requests that page from the API", async () => {
  const urls = [];
  globalThis.fetch = async (url) => { urls.push(url); return response(null); };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onPageChange(2); });
  expect(urls.at(-1)).toBe("/api/applications?page=2");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("2");
});

test("a page beyond the last one settles on the last real page", async () => {
  const urls = [];
  globalThis.fetch = async (url) => {
    urls.push(url);
    if (url.includes("page=3")) {
      return Response.json(page({ items: [], page: 3, total: 42, totalPages: 2, counts: { saved: 40, applied: 2 } }));
    }
    const items = [{ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }];
    return Response.json(page({ items, total: 42, totalPages: 2, counts: { saved: 40, applied: 2 } }));
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onPageChange(3); });
  expect(urls.at(-1)).toBe("/api/applications?page=2");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("2");
});

test("applying filters returns to page one and sends every filter", async () => {
  const urls = [];
  globalThis.fetch = async (url) => { urls.push(url); return response(null); };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onPageChange(3); });
  await act(async () => {
    dashboardProps.onApplyFilters({ q: "acme", status: "applied", channel: "campus" });
  });
  expect(urls.at(-1)).toBe("/api/applications?page=1&q=acme&status=applied&channel=campus");
  expect(container.querySelector('[data-testid="page"]')?.textContent).toBe("1");
});

test("creating an application clears the filters and shows page one", async () => {
  const urls = [];
  globalThis.fetch = async (url, options) => {
    urls.push(url);
    if (options.method === "POST") {
      return Response.json({ id: "app-9", company: "NewCo", role: "Engineer", status: "saved" }, { status: 201 });
    }
    return response(null);
  };
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onPageChange(3); });
  await act(async () => { dashboardProps.onApplyFilters({ q: "old", status: "saved", channel: "" }); });

  await act(async () => {
    await dashboardProps.onCreate({ preventDefault() {}, currentTarget: formWith({ company: "NewCo", role: "Engineer", status: "saved" }) });
  });

  expect(urls.at(-1)).toBe("/api/applications?page=1");
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
    return Response.json(page({ items, total: 1, totalPages: 1, counts: { saved: 0, applied: 1 } }));
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
  expect(requests.at(-1).url).toBe("/api/applications?page=1");
  expect(container.textContent).toContain("Acme Labs");
});

test("a rejected edit keeps the form open and surfaces the API message", async () => {
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async (url, options) => {
    if (options.method === "PATCH") return new Response("invalid application", { status: 400 });
    return Response.json(page({ items: [original], total: 1, totalPages: 1, counts: { saved: 1, applied: 0 } }));
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

test("changing the page or filters keeps an open edit instead of discarding it", async () => {
  const original = { id: "app-1", company: "Acme", role: "Engineer", status: "saved", url: "", source: "", channel: "", job_id: "", applied_on: "", address: "", notes: "" };
  globalThis.fetch = async () => Response.json(page({ items: [original], total: 1, totalPages: 1, counts: { saved: 1, applied: 0 } }));
  session = account("a");
  await render();
  await act(async () => { dashboardProps.onEdit(original); });
  expect(dashboardProps.editingApplication?.id).toBe("app-1");

  await act(async () => { dashboardProps.onPageChange(2); });
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
  globalThis.fetch = async (_url, options) => options.headers.Authorization === "Bearer a"
    ? response("Account A company")
    : new Promise((resolve) => { completeB = resolve; });
  session = account("a");
  await render();
  expect(container.textContent).toContain("Account A company");
  session = account("b");
  await render();
  expect(container.textContent).not.toContain("Account A company");
  await act(async () => { completeB(response("Account B company")); });
  expect(container.textContent).toContain("Account B company");
});

test("a previous account's delayed response cannot overwrite the next account", async () => {
  let completeA;
  globalThis.fetch = async (_url, options) => options.headers.Authorization === "Bearer a"
    ? new Promise((resolve) => { completeA = resolve; })
    : response("Account B company");
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