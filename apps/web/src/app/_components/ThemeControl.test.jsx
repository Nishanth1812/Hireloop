import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { renderToString } from "react-dom/server";
import { Window } from "happy-dom";

const browser = new Window({ url: "http://localhost:3000" });
globalThis.window = browser;
globalThis.document = browser.document;
globalThis.localStorage = browser.localStorage;
globalThis.HTMLElement = browser.HTMLElement;
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const { hydrateRoot } = await import("react-dom/client");
const { ThemeControl } = await import("./ThemeControl");
let root;

afterEach(async () => {
  if (root) await act(async () => root.unmount());
  document.body.innerHTML = "";
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

test("saved dark mode hydrates without a mismatch and shows the right action", async () => {
  localStorage.setItem("hireloop-theme", "dark");
  delete globalThis.window;
  const html = renderToString(<ThemeControl />);
  globalThis.window = browser;
  document.body.innerHTML = `<div id="root">${html}</div>`;
  const errors = [];
  await act(async () => {
    root = hydrateRoot(document.getElementById("root"), <ThemeControl />, {
      onRecoverableError: (error) => errors.push(error.message),
    });
  });
  expect(errors).toEqual([]);
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(document.querySelector("button").getAttribute("aria-label")).toBe("Switch to light mode");
  expect(document.querySelector("button").disabled).toBe(false);
});
