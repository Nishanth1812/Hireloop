import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

// Guards the theme colour tokens against contrast regressions. The visual and
// screen-reader audit that Phase 1 could not complete found the company initial,
// delete button and placeholder text falling below WCAG AA in one theme; this
// test recomputes every text pairing straight from globals.css so the design
// tokens stay legible in both light and dark mode.

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "..", "app", "globals.css"), "utf8");

function varsFor(pattern, label) {
  const match = css.match(pattern);
  assert.ok(match, `expected a ${label} token block in globals.css`);
  const vars = {};
  for (const entry of match[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    vars[entry[1]] = entry[2].trim();
  }
  return vars;
}

const light = varsFor(/:root\s*\{([^}]*)\}/, "light :root");
const darkExplicit = varsFor(/:root\[data-theme="dark"\]\s*\{([^}]*)\}/, 'dark [data-theme="dark"]');
const darkSystem = varsFor(/:root:not\(\[data-theme\]\)\s*\{([^}]*)\}/, "system-dark :root:not([data-theme])");

function channel(value) {
  const c = value / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
  const raw = hex.replace("#", "");
  assert.match(raw, /^[0-9a-fA-F]{6}$/, `not a 6-digit hex colour: ${hex}`);
  const r = parseInt(raw.slice(0, 2), 16);
  const g = parseInt(raw.slice(2, 4), 16);
  const b = parseInt(raw.slice(4, 6), 16);
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a, b) {
  const x = luminance(a);
  const y = luminance(b);
  const hi = Math.max(x, y);
  const lo = Math.min(x, y);
  return (hi + 0.05) / (lo + 0.05);
}

// Each pairing is [label, foreground token, background token, minimum ratio].
// 4.5:1 is the WCAG AA threshold for normal text; focus indicators use 3:1.
const textPairs = [
  ["body ink on surface", "ink", "surface", 4.5],
  ["body ink on background", "ink", "background", 4.5],
  ["muted text on surface", "muted", "surface", 4.5],
  ["muted text on background", "muted", "background", 4.5],
  ["eyebrow accent on surface", "accent-dark", "surface", 4.5],
  ["company initial on its chip", "chip-ink", "chip-bg", 4.5],
  ["delete button on surface", "danger-ink", "surface", 4.5],
  ["delete button hover on soft orange", "danger-ink", "soft-orange", 4.5],
  ["input placeholder on surface", "placeholder", "surface", 4.5],
  ["error text on error background", "error-ink", "soft-error", 4.5],
  ["success text on success background", "success-ink", "soft-green", 4.5],
  ["primary button label on ink", "background", "ink", 4.5],
  ["focus ring on surface", "focus-ring", "surface", 3],
  ["focus ring on background", "focus-ring", "background", 3],
];

for (const [themeName, tokens] of [
  ["light", light],
  ["dark (explicit)", darkExplicit],
  ["dark (system)", darkSystem],
]) {
  test(`${themeName}: text and focus colours meet contrast minimums`, () => {
    for (const [label, fg, bg, minimum] of textPairs) {
      const ratio = contrast(tokens[`--${fg}`], tokens[`--${bg}`]);
      assert.ok(
        ratio >= minimum,
        `${themeName} ${label}: ${ratio.toFixed(2)}:1 is below ${minimum}:1 ` +
          `(${tokens[`--${fg}`]} on ${tokens[`--${bg}`]})`,
      );
    }
  });
}

test("explicit dark and system dark themes define identical tokens", () => {
  for (const token of new Set([...Object.keys(darkExplicit), ...Object.keys(darkSystem)])) {
    assert.equal(
      darkExplicit[token],
      darkSystem[token],
      `${token} differs between the [data-theme="dark"] block and the prefers-color-scheme fallback`,
    );
  }
});