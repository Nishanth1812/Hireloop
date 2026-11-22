import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import React, { act } from "react";
import { Window } from "happy-dom";

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

mock.module("next/link", () => ({ default: ({ href, children, ...props }) => <a href={href} {...props}>{children}</a> }));
mock.module("@clerk/nextjs", () => ({ SignOutButton: ({ children }) => children }));
mock.module("./ApplicationCaptureForm", () => ({
  ApplicationCaptureForm: ({ hidden }) => <aside hidden={hidden}><input id="company" /></aside>,
}));
for (const name of ["ApplicationEditForm", "ApplicationFilterBar", "ApplicationList", "NotificationControl", "TaskForm", "TodayPanel", "ThemeControl"]) {
  mock.module(`./${name}`, () => ({ [name]: () => null }));
}

const { createRoot } = await import("react-dom/client");
const { ApplicationDashboard } = await import("./ApplicationDashboard");
let root;
let container;

const noop = () => {};
function props(overrides = {}) {
  return {
    email: "user@example.test",
    result: { items: [], total: 0, counts: { saved: 0, applied: 0 }, next_cursor: null, page_size: 25 },
    filters: { q: "", status: "", channel: "" },
    page: 1,
    hasPrevious: false,
    hasNext: false,
    loading: false,
    loadError: false,
    saving: false,
    busyId: "",
    editingApplication: null,
    taskPage: { items: [], total: 0, next_cursor: null, page_size: 25 },
    taskPageIndex: 0,
    taskHasPrevious: false,
    taskHasNext: false,
    taskLoading: false,
    taskLoadError: false,
    tasksShowAll: false,
    taskBusyId: "",
    taskDraft: null,
    error: "",
    notice: "",
    notificationEnabled: true,
    notificationLoading: false,
    notificationSaving: false,
    notificationError: "",
    notificationNotice: "",
    onNotificationChange: noop,
    accountGetToken: async () => "token",
    onAccountDeletionAccepted: async () => {},
    onCreate: noop,
    onStatusChange: noop,
    onEdit: noop,
    onCancelEdit: noop,
    onSaveEdit: noop,
    onRemove: noop,
    onAddTask: noop,
    onApplyFilters: noop,
    onClearFilters: noop,
    onPrevious: noop,
    onNext: noop,
    onToggleFinished: noop,
    onTaskPrevious: noop,
    onTaskNext: noop,
    onRetryApplications: noop,
    onRetryTasks: noop,
    onEditTask: noop,
    onSaveTask: noop,
    onCancelTaskEdit: noop,
    onCompleteTask: noop,
    onCancelTask: noop,
    onExport: noop,
    ...overrides,
  };
}

async function render(overrides = {}) {
  await act(async () => root.render(<ApplicationDashboard {...props(overrides)} />));
}

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

test("the dashboard Add action focuses and scrolls to the existing capture form", async () => {
  await render();
  const company = container.querySelector("#company");
  let scrolled = false;
  company.scrollIntoView = () => { scrolled = true; };
  const add = [...container.querySelectorAll("button")].find((button) => button.classList.contains("add-application-cta"));
  expect(add).not.toBeNull();
  expect(add.disabled).toBe(false);
  await act(async () => add.click());
  expect(document.activeElement).toBe(company);
  expect(scrolled).toBe(true);
});

test("the dashboard Add action is disabled while another form hides capture", async () => {
  await render({ editingApplication: { id: "a1" } });
  const add = container.querySelector(".add-application-cta");
  expect(add.disabled).toBe(true);
  expect(document.getElementById(add.getAttribute("aria-describedby")).textContent).toMatch(/close the current form/i);
});

test("the dashboard total matches the currently filtered result", async () => {
  await render({
    result: {
      items: [],
      total: 1,
      counts: { saved: 3, applied: 1, interviewing: 2, offer: 1, accepted: 1, rejected: 2, withdrawn: 1 },
      next_cursor: null,
      page_size: 25,
    },
    filters: { q: "", status: "applied", channel: "" },
  });
  expect(container.querySelector(".summary div strong").textContent).toBe("1");
});

test("account export and deletion controls appear in the dashboard settings", async () => {
  await render();
  expect(container.textContent).toContain("Your data");
  expect([...container.querySelectorAll("button")].some((button) => button.textContent.trim() === "Download your data")).toBe(true);
  expect([...container.querySelectorAll("button")].some((button) => button.textContent.trim() === "Delete account")).toBe(true);
});
