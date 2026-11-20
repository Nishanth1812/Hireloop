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

function noop() {}

function listProps(overrides = {}) {
  return {
    applications: applications(),
    loading: false,
    loadError: false,
    busyId: "",
    onStatusChange: noop,
    onRemove: noop,
    ...overrides,
  };
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

test("each application row exposes a labelled status control and a named delete button", async () => {
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
  }
});

test("the list announces loading and marks the region busy while fetching", async () => {
  await render(<ApplicationList {...listProps({ loading: true, applications: [] })} />);
  const section = container.querySelector(".application-section");
  expect(section.getAttribute("aria-busy")).toBe("true");
  const status = container.querySelector('[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/Loading applications/);
});

test("the empty state is announced through a live region", async () => {
  await render(<ApplicationList {...listProps({ applications: [] })} />);
  const status = container.querySelector('.empty-state[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/next opportunity/i);
});

test("a load failure is announced and does not render stale rows", async () => {
  await render(<ApplicationList {...listProps({ loadError: true, applications: [] })} />);
  expect(container.querySelectorAll(".application-row").length).toBe(0);
  const status = container.querySelector('[role="status"]');
  expect(status).not.toBeNull();
  expect(status.textContent).toMatch(/could not be loaded/i);
});

test("a row being updated disables only its own controls", async () => {
  await render(<ApplicationList {...listProps({ busyId: "a1" })} />);
  const rows = [...container.querySelectorAll(".application-row")];
  expect(rows[0].querySelector("select").disabled).toBe(true);
  expect(rows[0].querySelector("button.delete-button").disabled).toBe(true);
  expect(rows[1].querySelector("select").disabled).toBe(false);
  expect(rows[1].querySelector("button.delete-button").disabled).toBe(false);
});

test("capture form labels every field and marks company and role required", async () => {
  await render(<ApplicationCaptureForm saving={false} onSubmit={noop} />);
  const labelled = ["company", "role", "status"];
  for (const id of labelled) {
    const field = container.querySelector(`#${id}`);
    expect(field).not.toBeNull();
    assertFocusableWithName(field);
    const label = container.querySelector(`label[for="${id}"]`);
    expect(label).not.toBeNull();
    expect(label.textContent.trim().length).toBeGreaterThan(0);
  }
  expect(container.querySelector("#company").hasAttribute("required")).toBe(true);
  expect(container.querySelector("#role").hasAttribute("required")).toBe(true);
  expect(container.querySelector("#status").tagName).toBe("SELECT");
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