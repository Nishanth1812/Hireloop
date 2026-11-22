import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import React, { act } from "react";
import { Window } from "happy-dom";

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.localStorage = browser.localStorage;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.FormData = browser.FormData;
globalThis.Event = browser.Event;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

let session;
mock.module("@clerk/nextjs", () => ({
  useSession: () => ({ isLoaded: true, session }),
  SignOutButton: ({ children }) => <div>{children}</div>,
}));
mock.module("next/link", () => ({
  __esModule: true,
  default: ({ children, href }) => <a href={href}>{children}</a>,
}));

const { createRoot } = await import("react-dom/client");
const { ApplicationDetail } = await import("./_components/ApplicationDetail");
const originalFetch = globalThis.fetch;
let root, container, requests, responder;

function account(id, getToken = async () => id) {
  return { id, getToken, user: { emailAddresses: [{ emailAddress: `${id}@example.test` }] } };
}
function application(overrides = {}) {
  return {
    id: "app-1", company: "Acme", role: "Engineer", status: "saved",
    source: "LinkedIn", address: "", url: "", channel: "", job_id: "",
    applied_on: "2026-11-01", notes: "", ...overrides,
  };
}
function event(overrides = {}) {
  return {
    id: "ev-1", application_id: "app-1", event_type: "created", from_status: null,
    to_status: null, note: "", occurred_at: "2026-11-18T09:00:00Z", ...overrides,
  };
}
function task(overrides = {}) {
  return {
    id: "task-1", application_id: "app-1", title: "Submit assessment", status: "open",
    due_precision: "date", due_date: "2026-11-12", due_local: "", due_at: "", time_zone: "",
    created_at: "2026-11-01T09:00:00Z", ...overrides,
  };
}
function emptyPage() {
  return { items: [], page_size: 25, total: 0, next_cursor: null };
}
function defaultResponder(url, options) {
  if (url === "/api/applications/app-1" && !options.method) return Response.json(application());
  if (url === "/api/applications/app-1/events") return Response.json({ items: [], page_size: 25, next_cursor: null });
  if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
  return Response.json({});
}

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  requests = [];
  responder = defaultResponder;
  session = account("a");
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return responder(url, options);
  };
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  globalThis.fetch = originalFetch;
});

async function render() {
  await act(async () => { root.render(<ApplicationDetail id="app-1" session={session} />); });
}

function historyEntries() {
  return [...container.querySelectorAll(".history-entry")];
}
function noticeText() {
  return [...container.querySelectorAll('[role="status"]')].map((element) => element.textContent).join("\n");
}

test("renders the record, chronological stage history and every linked task", async () => {
  responder = (url, options) => {
    if (url === "/api/applications/app-1" && !options.method) return Response.json(application({ status: "applied" }));
    if (url === "/api/applications/app-1/events") {
      return Response.json({
        items: [
          event({ id: "ev-2", event_type: "stage_changed", from_status: "saved", to_status: "interviewing", occurred_at: "2026-11-19T10:00:00Z" }),
          event({ id: "ev-1", event_type: "created", to_status: "saved", occurred_at: "2026-11-18T09:00:00Z" }),
        ],
        page_size: 25,
        next_cursor: null,
      });
    }
    if (url.startsWith("/api/tasks")) {
      return Response.json({
        items: [task({ id: "task-1", title: "Submit assessment" }), task({ id: "task-2", title: "Dress rehearsal" })],
        page_size: 25, total: 2, next_cursor: null,
      });
    }
    return Response.json({});
  };
  await render();

  const entries = historyEntries();
  expect(entries.length).toBe(2);
  expect(entries[0].textContent).toContain("Created");
  expect(entries[1].textContent).toContain("Stage: Saved → Interviewing");

  const titles = [...container.querySelectorAll(".task-row strong")].map((element) => element.textContent);
  expect(titles).toEqual(["Submit assessment", "Dress rehearsal"]);
  expect(container.querySelector("#detail-status").value).toBe("applied");

  expect(requests.length).toBeGreaterThan(0);
  for (const request of requests) expect(request.options.headers.Authorization).toBe("Bearer a");
  const taskRequest = requests.find((request) => request.url.startsWith("/api/tasks"));
  expect(taskRequest.url).toContain("application_id=app-1");
});

test("walks every event page so the history is complete", async () => {
  responder = (url, options) => {
    if (url === "/api/applications/app-1" && !options.method) return Response.json(application());
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    if (url.startsWith("/api/applications/app-1/events")) {
      const cursor = new URL(url, "http://localhost").searchParams.get("cursor");
      if (cursor) {
        return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved", occurred_at: "2026-11-18T09:00:00Z" })], page_size: 25, next_cursor: null });
      }
      return Response.json({ items: [event({ id: "ev-2", event_type: "stage_changed", from_status: "saved", to_status: "interviewing", occurred_at: "2026-11-19T10:00:00Z" })], page_size: 25, next_cursor: "events-page-2" });
    }
    return Response.json({});
  };
  await render();
  const entries = historyEntries();
  expect(entries.length).toBe(2);
  expect(entries[0].textContent).toContain("Created");
  expect(entries[1].textContent).toContain("Stage: Saved → Interviewing");
  const eventRequests = requests.filter((request) => request.url.startsWith("/api/applications/app-1/events"));
  expect(eventRequests.length).toBe(2);
  expect(new URL(eventRequests[1].url, "http://localhost").searchParams.get("cursor")).toBe("events-page-2");
});

test("shows every linked task including done and cancelled", async () => {
  responder = (url, options) => {
    if (url === "/api/applications/app-1" && !options.method) return Response.json(application());
    if (url.startsWith("/api/applications/app-1/events")) return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved" })], page_size: 25, next_cursor: null });
    if (url.startsWith("/api/tasks")) {
      return Response.json({
        items: [task({ id: "task-a", title: "Apply online", status: "done" }), task({ id: "task-b", title: "Send follow-up", status: "cancelled" })],
        page_size: 25, total: 2, next_cursor: null,
      });
    }
    return Response.json({});
  };
  await render();
  const rows = [...container.querySelectorAll(".task-row")].map((row) => row.textContent);
  expect(rows.some((text) => text.includes("Apply online") && text.includes("Status: done"))).toBe(true);
  expect(rows.some((text) => text.includes("Send follow-up") && text.includes("Status: cancelled"))).toBe(true);
  const taskRequest = requests.find((request) => request.url.startsWith("/api/tasks"));
  const parsed = new URL(taskRequest.url, "http://localhost");
  expect(parsed.searchParams.get("application_id")).toBe("app-1");
  expect(parsed.searchParams.get("filter")).toBe("all");
});

test("a repeated cursor while loading fails with an honest message", async () => {
  responder = (url, options) => {
    if (url === "/api/applications/app-1" && !options.method) return Response.json(application());
    if (url.startsWith("/api/applications/app-1/events")) return Response.json({ items: [event({ id: "ev-1" })], page_size: 25, next_cursor: "looping-cursor" });
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();
  const alerts = [...container.querySelectorAll('[role="alert"]')].map((element) => element.textContent).join("\n");
  expect(alerts).toMatch(/repeated page cursor/i);
});

test("history loads every page beyond the former page cap", async () => {
  let count = 0;
  responder = (url) => {
    if (url === "/api/applications/app-1") return Response.json(application());
    if (url.startsWith("/api/applications/app-1/events")) {
      const index = count++;
      return Response.json({
        items: [event({ id: `ev-${index}`, occurred_at: `2026-11-20T10:${String(index % 60).padStart(2, "0")}:00Z` })],
        page_size: 25,
        next_cursor: index < 50 ? `cursor-${index}` : null,
      });
    }
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();
  const eventRequests = requests.filter((request) => request.url.startsWith("/api/applications/app-1/events"));
  expect(eventRequests.length).toBe(51);
  expect(container.querySelectorAll(".history-entry").length).toBe(51);
  expect(container.textContent).not.toMatch(/newest pages of the stage history are shown/i);
});

test("empty history and task lists announce their empty states", async () => {
  await render();
  expect(container.textContent).toContain("No recorded stage history yet.");
  expect(container.textContent).toContain("No tasks are linked to this application yet.");
});

test("the stage control exposes all seven workflow stages", async () => {
  await render();
  const select = container.querySelector("#detail-status");
  expect([...select.options].map((option) => option.value))
    .toEqual(["saved", "applied", "interviewing", "offer", "accepted", "rejected", "withdrawn"]);
  expect(select.options[2].textContent).toContain("Interviewing");
  expect(select.options[3].textContent).toContain("Offer");
});

test("changing the stage records an optional note on the change", async () => {
  let patchBody;
  responder = (url, options) => {
    if (url === "/api/applications/app-1") {
      if (options.method === "PATCH") {
        patchBody = JSON.parse(options.body);
        return Response.json(application({ status: "interviewing" }));
      }
      return Response.json(application());
    }
    if (url === "/api/applications/app-1/events") return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved" })], page_size: 25, next_cursor: null });
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();

  const select = container.querySelector("#detail-status");
  expect(container.querySelector("#detail-stage-note")).toBeNull();
  await act(async () => {
    select.value = "interviewing";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  const note = container.querySelector("#detail-stage-note");
  expect(note).not.toBeNull();
  expect(note.getAttribute("maxlength")).toBe("500");
  await act(async () => {
    note.value = "Recruiter call scheduled";
    note.dispatchEvent(new Event("input", { bubbles: true }));
  });

  const form = select.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  expect(patchBody).toEqual({ status: "interviewing", stage_note: "Recruiter call scheduled" });
  expect(noticeText()).toContain("Stage updated to Interviewing.");
});

test("omitting the note still records the stage change", async () => {
  const patchBodies = [];
  responder = (url, options) => {
    if (url === "/api/applications/app-1") {
      if (options.method === "PATCH") {
        patchBodies.push(JSON.parse(options.body));
        return Response.json(application({ status: "offer" }));
      }
      return Response.json(application());
    }
    if (url === "/api/applications/app-1/events") return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved" })], page_size: 25, next_cursor: null });
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();

  const select = container.querySelector("#detail-status");
  await act(async () => {
    select.value = "offer";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  const form = select.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  expect(patchBodies).toEqual([{ status: "offer" }]);
});

test("re-selecting the saved stage writes nothing", async () => {
  const patchBodies = [];
  responder = (url, options) => {
    if (url === "/api/applications/app-1") {
      if (options.method === "PATCH") {
        patchBodies.push(JSON.parse(options.body));
        return Response.json(application());
      }
      return Response.json(application());
    }
    if (url.startsWith("/api/applications/app-1/events")) return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved" })], page_size: 25, next_cursor: null });
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();

  const select = container.querySelector("#detail-status");
  // The saved stage is already selected; submitting without a change writes nothing.
  await act(async () => { select.dispatchEvent(new Event("change", { bubbles: true })); });
  const form = select.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  expect(patchBodies).toEqual([]);
});

test("stage-change failures are announced in place", async () => {
  responder = (url, options) => {
    if (url === "/api/applications/app-1") {
      if (options.method === "PATCH") return new Response("no", { status: 500 });
      return Response.json(application());
    }
    if (url === "/api/applications/app-1/events") return Response.json({ items: [], page_size: 25, next_cursor: null });
    if (url.startsWith("/api/tasks")) return Response.json(emptyPage());
    return Response.json({});
  };
  await render();

  const select = container.querySelector("#detail-status");
  await act(async () => {
    select.value = "interviewing";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  const form = select.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  const alerts = [...container.querySelectorAll('[role="alert"]')].map((element) => element.textContent);
  expect(alerts.some((text) => text.includes("Could not complete the request (500)"))).toBe(true);
});

test("a missing application is announced with a retry action", async () => {
  responder = () => new Response("missing", { status: 404 });
  await render();
  expect(container.querySelector('[role="alert"]').textContent).toContain("Could not complete the request (404)");
  const retry = [...container.querySelectorAll("button")].find((button) => button.textContent.trim() === "Try again");
  expect(retry).toBeTruthy();
  responder = defaultResponder;
  await act(async () => retry.click());
  expect(container.querySelector("#detail-status")).not.toBeNull();
});

test("a task can be added bound to this application", async () => {
  let posted;
  responder = (url, options) => {
    if (url === "/api/applications/app-1" && !options.method) return Response.json(application());
    if (url === "/api/applications/app-1/events") return Response.json({ items: [event({ id: "ev-1", event_type: "created", to_status: "saved" })], page_size: 25, next_cursor: null });
    if (url === "/api/tasks" && options.method === "POST") {
      posted = JSON.parse(options.body);
      return Response.json(task({ id: "task-9", title: "Submit assessment" }), { status: 201 });
    }
    if (url.startsWith("/api/tasks")) {
      return Response.json({ items: [task({ id: "task-9", title: "Submit assessment" })], page_size: 25, total: 1, next_cursor: null });
    }
    return Response.json({});
  };
  await render();

  const titleInput = container.querySelector("#task-title-input");
  expect(titleInput).not.toBeNull();
  await act(async () => {
    titleInput.value = "Submit assessment";
    titleInput.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const form = titleInput.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
  expect(posted).toEqual({ application_id: "app-1", title: "Submit assessment", due_precision: "none" });
  expect(noticeText()).toContain("Task added.");
  expect(container.textContent).toContain("Submit assessment");
});
