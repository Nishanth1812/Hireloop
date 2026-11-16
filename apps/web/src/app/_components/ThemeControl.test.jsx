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

function render() {
  delete globalThis.window;
  const html = renderToString(<ThemeControl />);
  globalThis.window = browser;
  document.body.innerHTML = `<div id="root">${html}</div>`;
  const errors = [];
  act(() => {
    root = hydrateRoot(document.getElementById("root"), <ThemeControl />, {
      onRecoverableError: (error) => errors.push(error.message),
    });
  });
  const button = document.querySelector("button");
  return { errors, button };
}

function iconKind(button) {
  const svg = button.querySelector("svg");
  if (!svg) return "none";
  return svg.querySelector("circle") ? "sun" : svg.querySelector("path") ? "moon" : "svg";
}

test("saved dark theme hydrates without mismatch, shows sun icon and light-mode action", () => {
  localStorage.setItem("hireloop-theme", "dark");
  const { errors, button } = render();
  expect(errors).toEqual([]);
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(button.disabled).toBe(false);
  expect(button.getAttribute("aria-label")).toBe("Switch to light mode");
  expect(button.getAttribute("title")).toBe("Switch to light mode");
  expect(iconKind(button)).toBe("sun");
  expect(button.querySelector("svg").getAttribute("aria-hidden")).toBe("true");
});

test("saved light theme hydrates and shows moon icon with dark-mode action", () => {
  localStorage.setItem("hireloop-theme", "light");
  const { errors, button } = render();
  expect(errors).toEqual([]);
  expect(document.documentElement.dataset.theme).toBe("light");
  expect(button.getAttribute("aria-label")).toBe("Switch to dark mode");
  expect(button.getAttribute("title")).toBe("Switch to dark mode");
  expect(iconKind(button)).toBe("moon");
});

test("no saved preference follows the system theme and stays enabled", () => {
  localStorage.removeItem("hireloop-theme");
  const { errors, button } = render();
  expect(errors).toEqual([]);
  expect(["light", "dark"]).toContain(document.documentElement.dataset.theme);
  expect(button.disabled).toBe(false);
  expect(button.getAttribute("aria-label")).toMatch(/^Switch to (light|dark) mode$/);
});

test("SSR render is disabled with a loading label before theme resolves", () => {
  const html = renderToString(<ThemeControl />);
  const temp = document.createElement("div");
  temp.innerHTML = html;
  const button = temp.querySelector("button");
  expect(button.disabled).toBe(true);
  expect(button.getAttribute("aria-label")).toBe("Loading theme");
  expect(button.getAttribute("title")).toBe("Loading theme");
});

test("clicking the toggle flips theme, icon, label and persists the choice", () => {
  localStorage.setItem("hireloop-theme", "dark");
  const { button } = render("dark");
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(iconKind(button)).toBe("sun");
  expect(button.getAttribute("aria-label")).toBe("Switch to light mode");

  act(() => button.click());
  expect(document.documentElement.dataset.theme).toBe("light");
  expect(localStorage.getItem("hireloop-theme")).toBe("light");

  const next = document.querySelector("button");
  expect(iconKind(next)).toBe("moon");
  expect(next.getAttribute("aria-label")).toBe("Switch to dark mode");
  expect(next.disabled).toBe(false);

  act(() => next.click());
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(localStorage.getItem("hireloop-theme")).toBe("dark");
});

test("toggle is focusable and activates on click", () => {
  localStorage.setItem("hireloop-theme", "dark");
  const { button } = render("dark");
  expect(button.tabIndex).toBeGreaterThanOrEqual(0);
  button.focus();
  expect(document.activeElement).toBe(button);
  act(() => button.click());
  expect(document.documentElement.dataset.theme).toBe("light");
});
