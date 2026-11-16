"use client";

import { useEffect, useState } from "react";
import { followsSystemTheme, resolveTheme } from "@/lib/theme";
import type { Theme } from "@/lib/theme";

function SunIcon() {
  return (
    <svg
      aria-hidden="true"
      width="20"
      height="20"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="12" cy="12" r="5" />
      <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg
      aria-hidden="true"
      width="20"
      height="20"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79" />
    </svg>
  );
}

export function ThemeControl() {
  const [theme, setTheme] = useState<Theme>();

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const saved = localStorage.getItem("hireloop-theme");
    const apply = (next: Theme) => {
      document.documentElement.dataset.theme = next;
      setTheme(next);
    };
    apply(resolveTheme(saved, media.matches));
    const onSystemChange = (event: MediaQueryListEvent) => {
      if (followsSystemTheme(localStorage.getItem("hireloop-theme"))) apply(event.matches ? "dark" : "light");
    };
    media.addEventListener("change", onSystemChange);
    return () => media.removeEventListener("change", onSystemChange);
  }, []);

  function toggleTheme() {
    if (!theme) return;
    const next = theme === "dark" ? "light" : "dark";
    localStorage.setItem("hireloop-theme", next);
    document.documentElement.dataset.theme = next;
    setTheme(next);
  }

  const switchingTo = theme === "dark" ? "light" : "dark";
  const label = theme ? `Switch to ${switchingTo} mode` : "Loading theme";
  const Icon = theme === "dark" ? SunIcon : MoonIcon;

  return (
    <button
      className="button quiet theme-toggle"
      type="button"
      onClick={toggleTheme}
      disabled={!theme}
      aria-label={label}
      title={label}
    >
      <Icon />
    </button>
  );
}
