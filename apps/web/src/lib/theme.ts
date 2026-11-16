export type Theme = "light" | "dark";

export function resolveTheme(saved: string | null, systemPrefersDark: boolean): Theme {
  if (saved === "light" || saved === "dark") return saved;
  return systemPrefersDark ? "dark" : "light";
}

export function followsSystemTheme(saved: string | null): boolean {
  return saved !== "light" && saved !== "dark";
}
