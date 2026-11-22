import { afterEach, beforeEach, expect, test } from "bun:test";
import React, { act } from "react";
import { Window } from "happy-dom";

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.Event = browser.Event;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const { createRoot } = await import("react-dom/client");
const { AccountDataControls } = await import("./AccountDataControls");
let root;
let container;
let requests;
let accepted;
let originalFetch;
let originalCreateObjectURL;
let originalRevokeObjectURL;
let originalAnchorClick;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  requests = [];
  accepted = 0;
  originalFetch = globalThis.fetch;
  originalCreateObjectURL = URL.createObjectURL;
  originalRevokeObjectURL = URL.revokeObjectURL;
  originalAnchorClick = browser.HTMLAnchorElement.prototype.click;
  URL.createObjectURL = () => "blob:account-export";
  URL.revokeObjectURL = () => {};
  browser.HTMLAnchorElement.prototype.click = function () { this.dataset.clicked = "true"; };
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return Response.json({});
  };
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  globalThis.fetch = originalFetch;
  URL.createObjectURL = originalCreateObjectURL;
  URL.revokeObjectURL = originalRevokeObjectURL;
  browser.HTMLAnchorElement.prototype.click = originalAnchorClick;
});

async function render() {
  await act(async () => root.render(
    <AccountDataControls getToken={async () => "account-token"} onDeletionAccepted={async () => { accepted += 1; }} />,
  ));
}

test("account export downloads a JSON file through the authenticated endpoint", async () => {
  let downloaded;
  browser.HTMLAnchorElement.prototype.click = function () { downloaded = this.download; };
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return new Response('{"schema_version":1}', { status: 200, headers: { "Content-Type": "application/json" } });
  };
  await render();

  const button = [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === "Download your data");
  await act(async () => button.click());

  expect(requests[0].url).toBe("/api/account/export");
  expect(requests[0].options.headers.Authorization).toBe("Bearer account-token");
  expect(downloaded).toBe("hireloop-account-export.json");
  expect(container.querySelector('[role="status"]').textContent).toMatch(/download is ready/i);
});

test("account deletion requires typing DELETE and signs out after 204", async () => {
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return new Response(null, { status: 204 });
  };
  await render();
  await act(async () => [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === "Delete account").click());

  const submit = [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === "Permanently delete account");
  expect(submit.disabled).toBe(true);
  const input = container.querySelector('input[name="confirmation"]');
  await act(async () => {
    Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype, "value").set.call(input, "DELETE");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(submit.disabled).toBe(false);
  const form = submit.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });

  expect(requests[0].url).toBe("/api/account");
  expect(requests[0].options.method).toBe("DELETE");
  expect(requests[0].options.headers.Authorization).toBe("Bearer account-token");
  expect(accepted).toBe(1);
  expect(container.textContent).toMatch(/Hireloop account and Clerk sign-in were deleted/i);
});

test("pending Clerk deletion explains the retry and still signs out", async () => {
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return new Response('{"status":"pending"}', { status: 202 });
  };
  await render();
  await act(async () => [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === "Delete account").click());
  const input = container.querySelector('input[name="confirmation"]');
  await act(async () => {
    Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype, "value").set.call(input, "DELETE");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const form = input.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });

  expect(accepted).toBe(1);
  expect(container.textContent).toMatch(/sign-in deletion is pending/i);
  expect(container.querySelector('[role="alert"]')).toBeNull();
});

test("a failed account deletion keeps the session and reports the error", async () => {
  globalThis.fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return new Response("unavailable", { status: 503 });
  };
  await render();
  await act(async () => [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === "Delete account").click());
  const input = container.querySelector('input[name="confirmation"]');
  await act(async () => {
    Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype, "value").set.call(input, "DELETE");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const form = input.closest("form");
  await act(async () => { form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });

  expect(accepted).toBe(0);
  expect(container.querySelector('[role="alert"]').textContent).toMatch(/API or database is unavailable/i);
});
