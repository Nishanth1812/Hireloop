import type { Task } from "./api";

// Date-only values are rendered from their parts in UTC so no local offset can
// shift the day or invent a time the user never gave.
export function formatDateOnly(value: string): string {
  const [year, month, day] = value.split("-").map(Number);
  if (!year || !month || !day) return value;
  const date = new Date(Date.UTC(year, month - 1, day));
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
}

// An exact deadline is displayed in its original IANA zone, never in the
// browser's zone and never as a bare UTC timestamp.
export function formatInstant(dueAt: string, zone: string): { when: string; zoneLabel: string } | null {
  const instant = new Date(dueAt);
  if (!dueAt || !zone || Number.isNaN(instant.getTime())) return null;
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat("en-GB", {
      timeZone: zone,
      day: "numeric", month: "short", year: "numeric",
      hour: "2-digit", minute: "2-digit", hourCycle: "h23",
      timeZoneName: "shortOffset",
    }).formatToParts(instant);
  } catch {
    return null;
  }
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
  const when = `${part("day")} ${part("month")} ${part("year")}, ${part("hour")}:${part("minute")}`;
  const offset = part("timeZoneName").replace("GMT", "UTC");
  return { when, zoneLabel: offset ? `${zone} (${offset})` : zone };
}

export function deadlineText(task: Task): { when: string; detail: string } {
  if (task.due_precision === "date") {
    return {
      when: formatDateOnly(task.due_date),
      detail: "Cutoff time unspecified — no time or zone was recorded.",
    };
  }
  if (task.due_precision === "instant") {
    const formatted = formatInstant(task.due_at, task.time_zone);
    if (!formatted) {
      return { when: "Deadline needs attention", detail: "The stored deadline could not be displayed." };
    }
    return { when: formatted.when, detail: `Original zone: ${formatted.zoneLabel}.` };
  }
  return { when: "No deadline", detail: "No cutoff date was recorded." };
}

// A short, explicit list keeps the form small. An existing task's zone is
// prepended so editing never silently changes it to one of these choices.
export const TIME_ZONE_CHOICES = [
  "Asia/Kolkata",
  "UTC",
  "Europe/London",
  "Europe/Berlin",
  "America/New_York",
  "America/Los_Angeles",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
];

export function zoneChoices(current?: string): string[] {
  return current && current !== "" && !TIME_ZONE_CHOICES.includes(current)
    ? [current, ...TIME_ZONE_CHOICES]
    : TIME_ZONE_CHOICES;
}
