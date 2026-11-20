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
    const { email, applications, error, loading, loadError } = props;
    return <div>
      <span>{email}</span>
      <span>{loading ? "Loading" : "Ready"}</span>
      {loadError && <p data-testid="load-error">Load failed</p>}
      {error && <p role="alert">{error}</p>}
      {applications.map((item) => <p key={item.id}>{item.company}</p>)}
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
function response(company) {
  return new Response(JSON.stringify([{ id: company, company, role: "Engineer", status: "saved" }]));
}
async function render() {
  await act(async () => { root.render(<ApplicationTracker />); });
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
  globalThis.fetch = async () => {
    calls++;
    if (calls === 1) return new Response("unavailable", { status: 503 });
    return new Response(JSON.stringify({ id: "app-1", company: "Acme", role: "Engineer", status: "saved" }), { status: 201 });
  };
  session = account("a");
  await render();
  expect(container.querySelector('[data-testid="load-error"]') !== null).toBe(true);
  expect(container.textContent).toContain("Ready");

  const form = document.createElement("form");
  for (const [name, value] of Object.entries({ company: "Acme", role: "Engineer", status: "saved" })) {
    const input = document.createElement("input");
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }
  await act(async () => { await dashboardProps.onCreate({ preventDefault() {}, currentTarget: form }); });

  expect(container.querySelector('[data-testid="load-error"]') === null).toBe(true);
  expect(container.textContent).toContain("Acme");
});

test("saving an edit patches the record and updates the displayed row", async () => {
  let patchBody;
  let patchUrl;
  globalThis.fetch = async (url, options) => {
    if (options.method === "PATCH") {
      patchUrl = url;
      patchBody = JSON.parse(options.body);
      return new Response(JSON.stringify({ id: "app-1", company: "Acme Labs", role: "Engineer", status: "applied", source: "Referral", address: "me@example.com" }));
    }
    return new Response(JSON.stringify([{ id: "app-1", company: "Acme", role: "Engineer", status: "saved", source: "", address: "" }]));
  };
  session = account("a");
  await render();

  const form = document.createElement("form");
  for (const [name, value] of Object.entries({ company: "Acme Labs", role: "Engineer", status: "applied", source: "Referral", address: "me@example.com" })) {
    const input = document.createElement("input");
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }
  await act(async () => { await dashboardProps.onSaveEdit({ preventDefault() {}, currentTarget: form }, "app-1"); });

  expect(patchUrl).toBe("/api/applications/app-1");
  expect(patchBody).toEqual({ company: "Acme Labs", role: "Engineer", status: "applied", source: "Referral", address: "me@example.com" });
  expect(container.textContent).toContain("Acme Labs");
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
