import assert from "node:assert/strict";
import test from "node:test";
import { followsSystemTheme, resolveTheme } from "./theme.ts";

test("theme follows the system until an explicit preference is saved", () => {
  assert.equal(resolveTheme(null, false), "light");
  assert.equal(resolveTheme(null, true), "dark");
  assert.equal(followsSystemTheme(null), true);
  assert.equal(resolveTheme("light", true), "light");
  assert.equal(resolveTheme("dark", false), "dark");
  assert.equal(followsSystemTheme("light"), false);
  assert.equal(followsSystemTheme("dark"), false);
});
