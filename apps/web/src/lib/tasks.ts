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

// A task's in-app reminder is derived from its stored deadline, so it exists
// only while the task is open: completing or cancelling a task suppresses its
// reminder, and a rescheduled deadline moves the reminder with it. Nothing is
// claimed beyond the deadline itself — a date-only deadline keeps saying the
// cutoff time is unspecified, and a task without a readable deadline has none.
export function reminderText(task: Task): string | null {
  if (task.status !== "open") return null;
  const deadline = deadlineText(task);
  if (deadline.when === "No deadline" || deadline.when === "Deadline needs attention") return null;
  return `Reminder: ${deadline.when}`;
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

// offsetMinutes reads the IANA zone's offset at an instant from the platform's
// own tzdata. "longOffset" gives values such as GMT+05:30 or GMT-04:00.
function offsetMinutes(instant: Date, zone: string): number | null {
  try {
    const parts = new Intl.DateTimeFormat("en-GB", { timeZone: zone, timeZoneName: "longOffset" })
      .formatToParts(instant);
    const name = parts.find((part) => part.type === "timeZoneName")?.value ?? "";
    if (name === "GMT") return 0;
    const match = /^GMT([+-])(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(name);
    if (!match) return null;
    const minutes = Number(match[2]) * 60 + Number(match[3]) + Number(match[4] ?? 0);
    return match[1] === "-" ? -minutes : minutes;
  } catch {
    return null;
  }
}

function localParts(instant: Date, zone: string): string | null {
  try {
    const parts = new Intl.DateTimeFormat("en-GB", {
      timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", hourCycle: "h23",
    }).formatToParts(instant);
    const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
    return `${part("year")}-${part("month")}-${part("day")}T${part("hour")}:${part("minute")}`;
  } catch {
    return null;
  }
}

// localDeadlineIssue mirrors the API's wall-time resolution so the form can
// reject a nonexistent (spring-forward gap) or twice-existing (fall-back
// overlap) local time before sending it. The API remains authoritative; this
// only turns its 400 into an early, specific, accessible message. Offsets are
// probed a day on either side, which covers non-hour shifts like Lord Howe's.
export function localDeadlineIssue(dueLocal: string, zone: string): "gap" | "ambiguous" | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(dueLocal.trim());
  if (!match) return null; // shape and calendar checks belong to the API
  const [year, month, day, hour, minute] = match.slice(1).map(Number);
  const naive = Date.UTC(year, month - 1, day, hour, minute);
  const probe = new Date(naive);
  if (probe.getUTCFullYear() !== year || probe.getUTCMonth() !== month - 1 ||
      probe.getUTCDate() !== day || probe.getUTCHours() !== hour || probe.getUTCMinutes() !== minute) {
    return null;
  }
  const center = offsetMinutes(probe, zone);
  if (center === null) return null; // an unknown zone is rejected by the API
  const offsets = new Set<number>([center]);
  for (const delta of [-24 * 60 * 60 * 1000, 24 * 60 * 60 * 1000]) {
    const other = offsetMinutes(new Date(naive + delta), zone);
    if (other !== null) offsets.add(other);
  }
  const instants = new Set<string>();
  for (const offset of offsets) {
    const candidate = new Date(naive - offset * 60 * 1000);
    if (localParts(candidate, zone) === dueLocal.trim()) instants.add(candidate.toISOString());
  }
  if (instants.size === 0) return "gap";
  if (instants.size > 1) return "ambiguous";
  return null;
}
