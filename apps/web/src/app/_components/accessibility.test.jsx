import { afterEach, beforeEach, expect, test } from "bun:test";
import React, { act } from "react";
import { Window } from "happy-dom";

// Keyboard and screen-reader guarantees for the tracker's presentational
// components. Phase 1 could not run a live screen-reader pass, so these tests
// pin the structural prerequisites: every control is a native focusable element
// with a programmatic name, labels are associated, and loading/error/empty
// states announce themselves through a live region.

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const { createRoot } = await import("react-dom/client");
const { ApplicationList } = await import("./ApplicationList");
const { ApplicationCaptureForm } = await import("./ApplicationCaptureForm");
const { ApplicationEditForm } = await import("./ApplicationEditForm");
const { ApplicationFilterBar } = await import("./ApplicationFilterBar");

let root;
let container;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

function render(node) {
  return act(async () => {
    root.render(node);
  });
}

const applications = () => [
  { id: "a1", company: "Acme", role: "Designer", status: "saved" },
  { id: "a2", company: "Globex", role: "Engineer", status: "applied" },
];

const noFilters = { q: "", status: "", channel: "" };

function pageOf(items, overrides = {}) {
  return {
    items,
    page: 1,
    pageSize: 25,
    total: items.length,
    totalPages: items.length ? 1 : 0,
    counts: { saved: items.length, applied: 0 },
    ...overrides,
  };
}

function noop() {}

function listProps(overrides = {}) {
  const result = overrides.result ?? pageOf(applications());
  return {
    result,
    filters: noFilters,
    page: result.page,
    loading: false,
    loadError: false,
    busyId: "",
    editingId: "",
    onStatusChange: noop,
    onEdit: noop,
    onRemove: noop,
    onPageChange: noop,
    onClearFilters: noop,
    ...overrides,
  };
}

function filterProps(overrides = {}) {
  return { filters: noFilters, disabled: false, onApply: noop, onClear: noop, ...overrides };
}

function accessibleName(element) {
  const label = element.getAttribute("aria-label");
  if (label) return label.trim();
  if (element.id) {
    const associated = document.querySelector(`label[for="${element.id}"]`);
    if (associated) return associated.textContent.trim();
  }
  // Buttons and links take their accessible name from their text content.
  return (element.textContent || "").trim();
}

const focusableTags = new Set(["A", "BUTTON", "INPUT", "SELECT", "TEXTAREA", "SUMMARY"]);

function assertFocusableWithName(element) {
  expect(element.getAttribute("aria-hidden")).not.toBe("true");
  expect(focusableTags.has(element.tagName)).toBe(true);
  expect(Number(element.getAttribute("tabindex") ?? 0)).toBeGreaterThanOrEqual(0);
  expect(accessibleName(element).length).toBeGreaterThan(0);
}

test("each application row exposes a labelled status control and named edit and delete buttons", async () => {
  await render(<ApplicationList {...listProps()} />);
  const rows = [...container.querySelectorAll(".application-row")];
  expect(rows.length).toBe(2);
  for (const row of rows) {
    const initial = row.querySelector(".company-initial");
    expect(initial.getAttribute("aria-hidden")).toBe("true");

    const select = row.querySelector("select.status-select");
    assertFocusableWithName(select);
    expect(select.tagName).toBe("SELECT");

    const label = row.querySelector("label.visually-hidden");
    expect(label.getAttribute("for")).toBe(select.id);
    expect(label.textContent.trim().length).toBeGreaterThan(0);

    const remove = row.querySelector("button.delete-button");
    assertFocusableWithName(remove);
    expect(remove.tagName).toBe("BUTTON");
    expect(remove.getAttribute("aria-label")).toMatch(/^Delete .+ at .+$/);

    const edit = row.querySelector("button.edit-button");
    assertFocusableWithName(edit);
    expect(edit.tagName).toBe("BUTTON");
    expect(edit.getAttribute("aria-label")).toMatch(/^Edit .+ at .+$/);
  }
});

test("the list announces loading and marks the region busy while fetching", async () => {
  await render(<ApplicationList {...listProps({ loading: true, result: pageOf([]) })} />);
  const section = container.querySelector(".application-section");
  expect(section.getAttribute("aria-busy")).toBe("true");
  const status = container.querySelector('[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/Loading applications/);
});

test("the empty state is announced through a live region", async () => {
  await render(<ApplicationList {...listProps({ result: pageOf([]) })} />);
  const status = container.querySelector('.empty-state[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/next opportunity/i);
});

test("a filtered-out list offers a clear action instead of an empty pipeline", async () => {
  await render(<ApplicationList {...listProps({ result: pageOf([]), filters: { q: "acme", status: "", channel: "" } })} />);
  const status = container.querySelector('.empty-state[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/nothing matches/i);
  const clear = [...container.querySelectorAll("button")].find((button) => button.textContent.trim() === "Clear filters");
  assertFocusableWithName(clear);
});

test("a load failure is announced and does not render stale rows", async () => {
  await render(<ApplicationList {...listProps({ loadError: true, result: pageOf([]) })} />);
  expect(container.querySelectorAll(".application-row").length).toBe(0);
  const status = container.querySelector('[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/could not be loaded/i);
});

test("the result summary reports the honest total, not the page size", async () => {
  await render(<ApplicationList {...listProps({ result: pageOf(applications(), { total: 42, totalPages: 2, counts: { saved: 40, applied: 2 } }) })} />);
  const summary = container.querySelector(".results-summary");
  expect(summary.getAttribute("role")).toBe("status");
  expect(summary.textContent).toMatch(/Showing 1–25 of 42/);
});

test("pagination is native, named, and bounded at both ends", async () => {
  await render(<ApplicationList {...listProps({ result: pageOf(applications(), { total: 42, totalPages: 2 }) })} />);
  let nav = container.querySelector("nav.pagination");
  expect(nav).not.toBeNull();
  expect(nav.getAttribute("aria-label")).toMatch(/pages/i);
  expect(nav.textContent).toMatch(/Page 1 of 2/);
  const buttons = [...nav.querySelectorAll("button")];
  expect(buttons.map((button) => button.textContent.trim())).toEqual(["Previous", "Next"]);
  for (const button of buttons) assertFocusableWithName(button);
  expect(buttons[0].disabled).toBe(true);
  expect(buttons[1].disabled).toBe(false);

  await render(<ApplicationList {...listProps({ result: pageOf(applications(), { page: 2, total: 42, totalPages: 2 }) })} />);
  nav = container.querySelector("nav.pagination");
  expect(nav.textContent).toMatch(/Page 2 of 2/);
  const lastButtons = [...nav.querySelectorAll("button")];
  expect(lastButtons[0].disabled).toBe(false);
  expect(lastButtons[1].disabled).toBe(true);
});

test("a single page of results does not render dead pagination controls", async () => {
  await render(<ApplicationList {...listProps({ result: pageOf(applications()) })} />);
  expect(container.querySelector("nav.pagination")).toBeNull();
});

test("a row being updated disables only its own controls", async () => {
  await render(<ApplicationList {...listProps({ busyId: "a1" })} />);
  const rows = [...container.querySelectorAll(".application-row")];
  expect(rows[0].querySelector("select").disabled).toBe(true);
  expect(rows[0].querySelector("button.delete-button").disabled).toBe(true);
  expect(rows[1].querySelector("select").disabled).toBe(false);
  expect(rows[1].querySelector("button.delete-button").disabled).toBe(false);
});

test("editing prevents conflicting row changes and saving locks row actions", async () => {
  await render(<ApplicationList {...listProps({ editingId: "a1" })} />);
  let rows = [...container.querySelectorAll(".application-row")];
  expect(rows[0].querySelector("select").disabled).toBe(true);
  expect(rows[0].querySelector(".delete-button").disabled).toBe(true);
  expect(rows[1].querySelector("select").disabled).toBe(false);
  await render(<ApplicationList {...listProps({ editingId: "a1", saving: true })} />);
  rows = [...container.querySelectorAll(".application-row")];
  for (const row of rows) {
    for (const control of row.querySelectorAll("select, button")) expect(control.disabled).toBe(true);
  }
});

test("the filter bar labels every control and applies on submit or change", async () => {
  const applied = [];
  await render(<ApplicationFilterBar {...filterProps({ onApply: (filters) => applied.push(filters) })} />);
  for (const id of ["filter-q", "filter-status", "filter-channel"]) {
    const field = container.querySelector(`#${id}`);
    expect(field).not.toBeNull();
    assertFocusableWithName(field);
    const label = container.querySelector(`label[for="${id}"]`);
    expect(label).not.toBeNull();
    expect(label.textContent.trim().length).toBeGreaterThan(0);
    expect(field.hasAttribute("required")).toBe(false);
  }
  const submit = container.querySelector('button[type="submit"]');
  assertFocusableWithName(submit);

  const search = container.querySelector("#filter-q");
  const setValue = Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype, "value").set;
  await act(async () => {
    setValue.call(search, "acme");
    search.dispatchEvent(new browser.Event("input", { bubbles: true }));
  });
  await act(async () => {
    container.querySelector("form").dispatchEvent(new browser.Event("submit", { bubbles: true, cancelable: true }));
  });
  expect(applied.at(-1)).toEqual({ q: "acme", status: "", channel: "" });
});

test("changing a filter select applies immediately", async () => {
  const applied = [];
  await render(<ApplicationFilterBar {...filterProps({ onApply: (filters) => applied.push(filters) })} />);
  const status = container.querySelector("#filter-status");
  await act(async () => {
    status.value = "applied";
    status.dispatchEvent(new browser.Event("change", { bubbles: true }));
  });
  expect(applied.at(-1).status).toBe("applied");
});

test("the filter bar is disabled while the list is loading or saving", async () => {
  await render(<ApplicationFilterBar {...filterProps({ disabled: true })} />);
  for (const id of ["filter-q", "filter-status", "filter-channel"]) {
    expect(container.querySelector(`#${id}`).disabled).toBe(true);
  }
  expect(container.querySelector('button[type="submit"]').disabled).toBe(true);
});

test("the capture panel is hidden while an edit is open without unmounting the form", async () => {
  await render(<ApplicationCaptureForm saving={false} hidden onSubmit={noop} />);
  const panel = container.querySelector(".capture-panel");
  expect(panel.hasAttribute("hidden")).toBe(true);
  // The form itself is still mounted, so half-typed capture is not lost.
  expect(container.querySelector("#company")).not.toBeNull();
});

test("capture form labels every field and marks company and role required", async () => {
  await render(<ApplicationCaptureForm saving={false} onSubmit={noop} />);
  for (const id of ["company", "role", "status", "url", "source", "channel", "job_id", "applied_on", "address", "notes"]) {
    const field = container.querySelector(`#${id}`);
    expect(field).not.toBeNull();
    assertFocusableWithName(field);
    const label = container.querySelector(`label[for="${id}"]`);
    expect(label).not.toBeNull();
    expect(label.textContent.trim().length).toBeGreaterThan(0);
  }
  expect(container.querySelector("#company").hasAttribute("required")).toBe(true);
  expect(container.querySelector("#role").hasAttribute("required")).toBe(true);
  for (const id of ["status", "url", "source", "channel", "job_id", "applied_on", "address", "notes"]) {
    expect(container.querySelector(`#${id}`).hasAttribute("required")).toBe(false);
  }
  expect(container.querySelector("#status").tagName).toBe("SELECT");
  expect(container.querySelector("#channel").tagName).toBe("SELECT");
  // The date field is date-only: no time can be recorded here.
  expect(container.querySelector("#applied_on").getAttribute("type")).toBe("date");
});

test("the submit button is a native button that reflects the saving state", async () => {
  await render(<ApplicationCaptureForm saving={false} onSubmit={noop} />);
  let button = container.querySelector('button[type="submit"]');
  assertFocusableWithName(button);
  expect(button.disabled).toBe(false);
  expect(button.textContent).toBe("Add application");

  await render(<ApplicationCaptureForm saving={true} onSubmit={noop} />);
  button = container.querySelector('button[type="submit"]');
  expect(button.disabled).toBe(true);
  expect(button.textContent).toBe("Adding…");
});

test("the edit form labels every field, pre-fills values, and offers save and cancel", async () => {
  const application = {
    id: "a1",
    company: "Acme",
    role: "Designer",
    status: "applied",
    source: "Referral",
    address: "me@example.com",
    url: "https://jobs.example.com/42",
    channel: "campus",
    job_id: "REQ-42",
    applied_on: "2026-11-02",
    notes: "Placement cell notice.",
  };
  await render(<ApplicationEditForm application={application} saving={false} onSave={noop} onCancel={noop} />);
  for (const id of ["edit-company", "edit-role", "edit-status", "edit-url", "edit-source", "edit-channel", "edit-job_id", "edit-applied_on", "edit-address", "edit-notes"]) {
    const field = container.querySelector(`#${id}`);
    expect(field).not.toBeNull();
    assertFocusableWithName(field);
    const label = container.querySelector(`label[for="${id}"]`);
    expect(label).not.toBeNull();
    expect(label.textContent.trim().length).toBeGreaterThan(0);
  }
  expect(container.querySelector("#edit-company").value).toBe("Acme");
  expect(container.querySelector("#edit-source").value).toBe("Referral");
  expect(container.querySelector("#edit-address").value).toBe("me@example.com");
  expect(container.querySelector("#edit-url").value).toBe("https://jobs.example.com/42");
  expect(container.querySelector("#edit-channel").value).toBe("campus");
  expect(container.querySelector("#edit-job_id").value).toBe("REQ-42");
  expect(container.querySelector("#edit-applied_on").value).toBe("2026-11-02");
  expect(container.querySelector("#edit-notes").value).toBe("Placement cell notice.");
  const submit = container.querySelector('button[type="submit"]');
  assertFocusableWithName(submit);
  const cancel = [...container.querySelectorAll("button")].find((b) => b.textContent.trim() === "Cancel");
  assertFocusableWithName(cancel);
});