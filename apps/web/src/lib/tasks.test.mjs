import assert from "node:assert/strict";
import { test } from "node:test";

const tasks = await import("./tasks.ts").catch(() => null);

const task = (overrides = {}) => ({
  id: "t1",
  application_id: "a1",
  title: "Submit assessment",
  status: "open",
  due_precision: "date",
  due_date: "2026-11-12",
  due_local: "",
  due_at: "",
  time_zone: "",
  created_at: "2026-11-01T09:00:00Z",
  ...overrides,
});

test("date-only deadlines show a day and say the cutoff time is unspecified", () => {
  assert.ok(tasks, "task display helpers are missing");
  const result = tasks.deadlineText(task());
  assert.equal(result.when, "12 Nov 2026");
  assert.match(result.detail, /cutoff time unspecified/i);
  // No invented time or zone anywhere in the rendered text.
  assert.doesNotMatch(result.when, /\d{1,2}:\d{2}/);
  assert.doesNotMatch(result.detail, /Asia\/|UTC|GMT/);
});

test("date-only formatting never shifts the day with the machine's timezone", () => {
  assert.equal(tasks.formatDateOnly("2026-01-01"), "1 Jan 2026");
  assert.equal(tasks.formatDateOnly("2026-12-31"), "31 Dec 2026");
  assert.equal(tasks.formatDateOnly(""), "");
});

test("exact deadlines render the stored instant in its original zone", () => {
  const instant = task({
    due_precision: "instant",
    due_at: "2026-11-12T12:30:00Z",
    due_local: "2026-11-12T18:00",
    time_zone: "Asia/Kolkata",
  });
  const result = tasks.deadlineText(instant);
  assert.equal(result.when, "12 Nov 2026, 18:00");
  assert.match(result.detail, /Asia\/Kolkata/);
  assert.match(result.detail, /5:30/);
});

test("exact deadlines respect the zone across a daylight-saving boundary", () => {
  // The same UTC instant before and after the New York fall-back has two
  // different local clock times; each is shown with its own offset.
  const before = tasks.formatInstant("2026-11-01T05:30:00Z", "America/New_York");
  const after = tasks.formatInstant("2026-11-01T06:30:00Z", "America/New_York");
  assert.equal(before.when, "1 Nov 2026, 01:30");
  assert.equal(after.when, "1 Nov 2026, 01:30");
  assert.match(before.zoneLabel, /UTC-4/);
  assert.match(after.zoneLabel, /UTC-5/);
});

test("a task with no deadline says so instead of inventing a date", () => {
  const result = tasks.deadlineText(task({ due_precision: "none", due_date: "", due_at: "", time_zone: "" }));
  assert.equal(result.when, "No deadline");
  assert.match(result.detail, /no cutoff date/i);
});

test("an invalid instant is reported rather than shown as a wrong time", () => {
  const result = tasks.deadlineText(task({ due_precision: "instant", due_at: "not-a-date", time_zone: "UTC" }));
  assert.match(result.when, /needs attention/i);
});

test("ambiguous fall-back and nonexistent spring-forward local times are caught", () => {
  assert.ok(tasks, "task display helpers are missing");
  assert.equal(tasks.localDeadlineIssue("2026-11-01T01:30", "America/New_York"), "ambiguous");
  assert.equal(tasks.localDeadlineIssue("2026-03-08T02:30", "America/New_York"), "gap");
  // Lord Howe changes by 30 minutes, so neither boundary is on the hour.
  assert.equal(tasks.localDeadlineIssue("2026-04-05T01:45", "Australia/Lord_Howe"), "ambiguous");
  assert.equal(tasks.localDeadlineIssue("2026-10-04T02:15", "Australia/Lord_Howe"), "gap");
});

test("valid wall times are not flagged, including non-hour zones and date-only deadlines", () => {
  assert.ok(tasks, "task display helpers are missing");
  assert.equal(tasks.localDeadlineIssue("2026-11-01T00:30", "America/New_York"), null);
  assert.equal(tasks.localDeadlineIssue("2026-11-01T02:30", "America/New_York"), null);
  assert.equal(tasks.localDeadlineIssue("2026-04-05T01:15", "Australia/Lord_Howe"), null);
  assert.equal(tasks.localDeadlineIssue("2026-11-12T18:00", "Asia/Kolkata"), null);
  assert.equal(tasks.localDeadlineIssue("2026-11-12", ""), null);
});

test("an edited task's zone stays available in the choice list", () => {
  assert.ok(tasks.TIME_ZONE_CHOICES.includes("Asia/Kolkata"));
  assert.deepEqual(tasks.zoneChoices("Pacific/Fiji").slice(0, 1), ["Pacific/Fiji"]);
  assert.deepEqual(tasks.zoneChoices("UTC"), tasks.TIME_ZONE_CHOICES);
});

test("open date-only tasks show a reminder on the day, with no invented time", () => {
  assert.ok(tasks, "task display helpers are missing");
  const reminder = tasks.reminderText(task());
  assert.equal(reminder, "Reminder: 12 Nov 2026");
  assert.doesNotMatch(reminder, /\d{1,2}:\d{2}/);
});

test("finished tasks have no reminder — completion and cancellation suppress it", () => {
  assert.equal(tasks.reminderText(task({ status: "done" })), null);
  assert.equal(tasks.reminderText(task({ status: "cancelled" })), null);
});

test("tasks without a readable deadline have no reminder", () => {
  assert.equal(tasks.reminderText(task({ due_precision: "none", due_date: "", due_at: "", time_zone: "" })), null);
  assert.equal(tasks.reminderText(task({ due_precision: "instant", due_at: "not-a-date", time_zone: "UTC" })), null);
});

test("an instant reminder keeps the stored wall time", () => {
  const instant = task({
    due_precision: "instant", due_at: "2026-11-12T12:30:00Z", due_local: "2026-11-12T18:00", time_zone: "Asia/Kolkata",
  });
  assert.equal(tasks.reminderText(instant), "Reminder: 12 Nov 2026, 18:00");
});
