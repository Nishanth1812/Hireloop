"use client";

import { useEffect, useState } from "react";
import { followsSystemTheme, resolveTheme } from "@/lib/theme";
import type { Theme } from "@/lib/theme";

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

  return <button className="button quiet theme-toggle" type="button" onClick={toggleTheme} disabled={!theme} aria-label={theme ? `Switch to ${theme === "dark" ? "light" : "dark"} mode` : "Loading theme"}>
    {theme ? (theme === "dark" ? "Light mode" : "Dark mode") : "Theme"}
  </button>;
}
