"use client";

import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { useSession } from "@clerk/nextjs";
import {
  createApplication,
  createTask,
  deleteApplication,
  getNotificationSettings,
  listApplications,
  listTasks,
  patchApplication,
  patchNotificationSettings,
  patchTask,
  taskIcsUrl,
  toChannel,
} from "@/lib/api";
import type {
  Application, ApplicationFilters, ApplicationPage, ApplicationPatch, Task, TaskInput, TaskPage, TaskPatch, TaskStatus,
} from "@/lib/api";
import { browserTimeZone, localDeadlineIssue } from "@/lib/tasks";
import { ApplicationDashboard } from "./_components/ApplicationDashboard";
import type { TaskDraft } from "./_components/ApplicationDashboard";
import { SignInPanel } from "./_components/SignInPanel";

const noFilters: ApplicationFilters = { q: "", status: "", channel: "" };

const firstPage: ApplicationPage = {
  items: [],
  page_size: 25,
  total: 0,
  counts: { saved: 0, applied: 0, interviewing: 0, offer: 0, accepted: 0, rejected: 0, withdrawn: 0 },
  next_cursor: null,
};

const firstTaskPage: TaskPage = { items: [], page_size: 25, total: 0, next_cursor: null };

function messageFrom(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong. Please try again.";
}

export default function ApplicationTracker() {
  const { session, isLoaded } = useSession();
  if (!isLoaded) {
    return <main className="page-shell"><p className="loading" role="status">Checking your sign-in…</p></main>;
  }
  return <SessionTracker key={session?.id ?? "signed-out"} session={session ?? null} />;
}

function SessionTracker({ session }: { session: NonNullable<ReturnType<typeof useSession>["session"]> | null }) {
  const [result, setResult] = useState<ApplicationPage>(firstPage);
  const [filters, setFilters] = useState<ApplicationFilters>(noFilters);
  // Keyset pagination: history[index] is the cursor that produced the page at
  // that index, with null for the first page. Keeping the trail lets Previous
  // return to an exact position instead of recomputing an offset, and changing
  // filters or creating a record resets it to the first page.
  const [history, setHistory] = useState<Array<string | null>>([null]);
  const [pageIndex, setPageIndex] = useState(0);
  const cursor = history[pageIndex] ?? null;
  // Bumping this refetches the current query after a mutation so totals and
  // counts always come from the API instead of local arithmetic.
  const [reloadKey, setReloadKey] = useState(0);
  const [loading, setLoading] = useState(session !== null);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busyId, setBusyId] = useState("");
  // The open editor keeps its own snapshot of the record. Deriving it from the
  // current page instead would unmount the form and drop unsaved input whenever
  // a page or filter change (or a background reload) pushed the row off it.
  const [editing, setEditing] = useState<Application | null>(null);
  // Tasks reload on their own key: writing a task must not refetch the whole
  // application list, and paging applications must not disturb the Today feed.
  // Today is a bounded keyset page too; a cursor is bound to the view that
  // minted it, so toggling "finished" restarts at the first page.
  const [taskPage, setTaskPage] = useState<TaskPage>(firstTaskPage);
  const [taskHistory, setTaskHistory] = useState<Array<string | null>>([null]);
  const [taskPageIndex, setTaskPageIndex] = useState(0);
  const taskCursor = taskHistory[taskPageIndex] ?? null;
  const [taskLoading, setTaskLoading] = useState(session !== null);
  const [taskLoadError, setTaskLoadError] = useState(false);
  const [tasksShowAll, setTasksShowAll] = useState(false);
  const [taskReloadKey, setTaskReloadKey] = useState(0);
  const [taskBusyId, setTaskBusyId] = useState("");
  const [taskDraft, setTaskDraft] = useState<TaskDraft | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  // The preference mirrors the server, never a local guess: null until the
  // owner-scoped read confirms it, and reset by the per-session remount.
  const [notificationEnabled, setNotificationEnabled] = useState<boolean | null>(null);
  const [notificationLoading, setNotificationLoading] = useState(session !== null);
  const [notificationSaving, setNotificationSaving] = useState(false);
  const [notificationError, setNotificationError] = useState("");
  const [notificationNotice, setNotificationNotice] = useState("");
  const notificationRevision = useRef(0);
  const notificationWrite = useRef(false);

  async function sessionToken(): Promise<string> {
    const token = await session?.getToken();
    if (!token) throw new Error("Your session is unavailable. Please sign in again.");
    return token;
  }

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    // Set when the requested page lost its last rows to a concurrent delete;
    // loading must stay true while the previous page is fetched.
    let steppingBack = false;
    void (async () => {
      setLoading(true);
      setLoadError(false);
      setError("");
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        const next = await listApplications(token, filters, cursor);
        if (cancelled) return;
        if (next.items.length === 0 && pageIndex > 0) {
          // The cursor page is empty because rows were deleted elsewhere. Step
          // back to the previous position instead of showing a dead page.
          steppingBack = true;
          setPageIndex(pageIndex - 1);
          return;
        }
        setResult(next);
        setLoadError(false);
      } catch (caught: unknown) {
        if (!cancelled) {
          // Drop the previous page's rows: they describe a different query.
          setResult(firstPage);
          setLoadError(true);
          setError(messageFrom(caught));
        }
      } finally {
        if (!cancelled && !steppingBack) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, filters, cursor, pageIndex, reloadKey]);

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    // A cursor page emptied by a concurrent completion steps back instead of
    // showing a dead page while loading the previous position.
    let steppingBack = false;
    void (async () => {
      setTaskLoading(true);
      setTaskLoadError(false);
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        const next = await listTasks(token, tasksShowAll, taskCursor);
        if (cancelled) return;
        if (next.items.length === 0 && taskPageIndex > 0) {
          steppingBack = true;
          setTaskPageIndex(taskPageIndex - 1);
          return;
        }
        setTaskPage(next);
      } catch (caught: unknown) {
        if (!cancelled) {
          // Never leave a previous account's actions on screen after a failure.
          setTaskPage(firstTaskPage);
          setTaskLoadError(true);
          setError(messageFrom(caught));
        }
      } finally {
        if (!cancelled && !steppingBack) setTaskLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, tasksShowAll, taskCursor, taskPageIndex, taskReloadKey]);

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    const revision = notificationRevision.current;
    void (async () => {
      setNotificationLoading(true);
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        const next = await getNotificationSettings(token);
        if (cancelled || revision !== notificationRevision.current) return;
        setNotificationEnabled(next.reminders_enabled);
      } catch (caught) {
        if (!cancelled && revision === notificationRevision.current) {
          // A failure must not leave an earlier or assumed value on screen.
          setNotificationEnabled(null);
          setNotificationError(messageFrom(caught));
        }
      } finally {
        if (!cancelled) setNotificationLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session]);

  async function changeNotificationSetting(enabled: boolean) {
    if (!session || notificationLoading || notificationWrite.current) return;
    notificationWrite.current = true;
    notificationRevision.current++;
    setNotificationSaving(true);
    setNotificationError("");
    setNotificationNotice("");
    try {
      const token = await sessionToken();
      // The checkbox keeps the last confirmed value while saving; only the
      // API's response may declare the new preference.
      const next = await patchNotificationSettings(token, enabled);
      setNotificationEnabled(next.reminders_enabled);
      setNotificationNotice(next.reminders_enabled ? "Preference saved: reminders on." : "Preference saved: reminders off.");
    } catch (caught) {
      setNotificationError(messageFrom(caught));
    } finally {
      notificationRevision.current++;
      notificationWrite.current = false;
      setNotificationSaving(false);
    }
  }

  function reload() {
    setReloadKey((key) => key + 1);
  }

  function applyFilters(next: ApplicationFilters) {
    // A cursor is bound to the filters that produced it, so a new query must
    // start from the first page rather than seek from the old position.
    setHistory([null]);
    setPageIndex(0);
    setFilters(next);
  }

  function goPrevious() {
    if (pageIndex === 0) return;
    setPageIndex(pageIndex - 1);
  }

  function goNext() {
    const next = result.next_cursor;
    if (!next) return;
    setHistory((previous) => [...previous.slice(0, pageIndex + 1), next]);
    setPageIndex(pageIndex + 1);
  }

  // Today keeps its own cursor trail so paging actions never disturbs the
  // application list's position, and a new view starts at the first page.
  function toggleFinished() {
    setTaskHistory([null]);
    setTaskPageIndex(0);
    setTasksShowAll((value) => !value);
  }

  function goPreviousTask() {
    if (taskPageIndex === 0) return;
    setTaskPageIndex(taskPageIndex - 1);
  }

  function goNextTask() {
    const next = taskPage.next_cursor;
    if (!next) return;
    setTaskHistory((previous) => [...previous.slice(0, taskPageIndex + 1), next]);
    setTaskPageIndex(taskPageIndex + 1);
  }

  function restartTasks() {
    setTaskHistory([null]);
    setTaskPageIndex(0);
    setTaskReloadKey((key) => key + 1);
  }

  async function addApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    const url = String(data.get("url") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to add this application.");
      return;
    }
    if (url && !/^https?:\/\//i.test(url)) {
      setError("Enter the job posting link starting with http:// or https://.");
      return;
    }
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      const application = await createApplication(token, {
        company,
        role,
        status: String(data.get("status")) as Application["status"],
        url,
        source: String(data.get("source") ?? "").trim(),
        channel: toChannel(String(data.get("channel") ?? "").trim()),
        job_id: String(data.get("job_id") ?? "").trim(),
        applied_on: String(data.get("applied_on") ?? "").trim(),
        address: String(data.get("address") ?? "").trim(),
        notes: String(data.get("notes") ?? "").trim(),
      });
      // A successful write proves the API is reachable, so a stale load error
      // from an earlier failed refresh must not keep contradicting the new row.
      // Newest first plus cleared filters puts the new record on page one.
      setLoadError(false);
      applyFilters(noFilters);
      reload();
      form.reset();
      setNotice(`${application.role} at ${application.company} was added.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setSaving(false);
    }
  }

  async function changeStatus(application: Application, status: Application["status"]) {
    if (!session) return;
    setBusyId(application.id);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      const updated = await patchApplication(token, application.id, { status });
      reload();
      setNotice(`Status updated for ${updated.company}.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  async function saveEdit(event: FormEvent<HTMLFormElement>, id: string) {
    event.preventDefault();
    if (!session) return;
    const original = editing;
    if (!original) {
      // The edit form only exists while a record is open, so there is nothing
      // to diff against if this fires without one.
      setError("Reopen the application to edit it.");
      return;
    }
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    const url = String(data.get("url") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to save this application.");
      return;
    }
    if (url && !/^https?:\/\//i.test(url)) {
      setError("Enter the job posting link starting with http:// or https://.");
      return;
    }
    // Send only the fields the user actually changed. A full-record PATCH would
    // overwrite a field someone else edited between load and save; a partial
    // patch leaves every untouched field alone.
    const patch: ApplicationPatch = {};
    if (company !== original.company) patch.company = company;
    if (role !== original.role) patch.role = role;
    const status = String(data.get("status")) as Application["status"];
    if (status !== original.status) patch.status = status;
    if (url !== original.url) patch.url = url;
    const source = String(data.get("source") ?? "").trim();
    if (source !== original.source) patch.source = source;
    const channel = toChannel(String(data.get("channel") ?? "").trim());
    if (channel !== original.channel) patch.channel = channel;
    const jobID = String(data.get("job_id") ?? "").trim();
    if (jobID !== original.job_id) patch.job_id = jobID;
    const appliedOn = String(data.get("applied_on") ?? "").trim();
    if (appliedOn !== original.applied_on) patch.applied_on = appliedOn;
    const address = String(data.get("address") ?? "").trim();
    if (address !== original.address) patch.address = address;
    const notes = String(data.get("notes") ?? "").trim();
    if (notes !== original.notes) patch.notes = notes;

    if (Object.keys(patch).length === 0) {
      // Nothing changed; close without a pointless write that could still
      // clobber a concurrent edit.
      setEditing(null);
      setNotice("No changes to save.");
      return;
    }

    setSaving(true);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      const updated = await patchApplication(token, id, patch);
      reload();
      setEditing(null);
      setNotice(`Details updated for ${updated.company}.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setSaving(false);
    }
  }

  async function removeApplication(application: Application) {
    if (
      !session ||
      !window.confirm(`Delete the ${application.role} application at ${application.company}?`)
    )
      return;
    setBusyId(application.id);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      await deleteApplication(token, application.id);
      setTaskReloadKey((key) => key + 1);
      // Reload the same keyset position. If that was the last row on a later
      // page, the load effect steps back instead of showing a dead page.
      reload();
      setNotice(`Application at ${application.company} was deleted.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  function openTaskForm(application: Application, task: Task | null = null) {
    setTaskDraft({ application, task });
    setEditing(null);
    setError("");
    setNotice("");
  }

  // A task can point at an application on another page; keep its id intact
  // even when the full record is not in the current page of the list.
  function applicationForTask(task: Task, supplied?: Application): Application {
    if (supplied) return supplied;
    const found = result.items.find((item) => item.id === task.application_id);
    return found ?? {
      id: task.application_id, company: "", role: "Linked application", status: "saved",
      source: "", address: "", url: "", channel: "", job_id: "", applied_on: "", notes: "",
    };
  }

  async function saveTask(event: FormEvent<HTMLFormElement>, id?: string) {
    event.preventDefault();
    if (!session || !taskDraft) return;
    const data = new FormData(event.currentTarget);
    const title = String(data.get("title") ?? "").trim();
    if (!title) {
      setError("Give this task a short title.");
      return;
    }
    const precision = String(data.get("due_precision") ?? "none") as Task["due_precision"];
    const fields: TaskPatch = { title, due_precision: precision };
    if (precision === "date") {
      fields.due_date = String(data.get("due_date") ?? "").trim();
      if (!fields.due_date) {
        setError("Choose a date for this deadline.");
        return;
      }
      // Capture the browser's IANA zone at creation for approximate nudges.
      // An edit keeps the zone already saved, and an old task without one can
      // gain it on edit. An unavailable or invalid zone is omitted rather than
      // guessed, so no cutoff or local hour is invented.
      const storedZone = taskDraft.task?.due_precision === "date" ? taskDraft.task.reminder_zone : "";
      const zone = storedZone || browserTimeZone();
      if (zone) fields.reminder_zone = zone;
    }
    if (precision === "instant") {
      fields.due_local = String(data.get("due_local") ?? "").trim();
      fields.time_zone = String(data.get("time_zone") ?? "").trim();
      if (!fields.time_zone) {
        // A wall time with no zone is not an instant; ask instead of guessing.
        setError("Choose the time zone for this deadline.");
        return;
      }
      if (!fields.due_local) {
        setError("Choose a date and time for this deadline.");
        return;
      }
      const unchanged = taskDraft.task?.due_precision === "instant" &&
        fields.due_local === taskDraft.task.due_local && fields.time_zone === taskDraft.task.time_zone;
      const issue = unchanged ? null : localDeadlineIssue(fields.due_local, fields.time_zone);
      if (issue === "gap") {
        setError("That local time does not exist in the chosen zone because the clocks jump forward. Choose another time.");
        return;
      }
      if (issue === "ambiguous") {
        setError("That local time happens twice when the clocks go back. Choose another time.");
        return;
      }
    }
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      if (id) {
        await patchTask(token, id, fields);
      } else {
        const input: TaskInput = {
          application_id: String(data.get("application_id") ?? ""),
          title,
          due_precision: precision,
          ...(precision === "date" ? { due_date: fields.due_date } : {}),
          ...(precision === "date" && fields.reminder_zone ? { reminder_zone: fields.reminder_zone } : {}),
          ...(precision === "instant" ? { due_local: fields.due_local, time_zone: fields.time_zone } : {}),
        };
        await createTask(token, input);
      }
      setTaskDraft(null);
      // A new task can sort onto any page, so start from the first one; an
      // edit keeps the current keyset position.
      if (!id) restartTasks();
      setTaskReloadKey((key) => key + 1);
      setNotice(id ? "Task updated." : "Task added to Today.");
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setSaving(false);
    }
  }

  async function changeTaskStatus(task: Task, status: TaskStatus) {
    if (!session) return;
    setTaskBusyId(task.id);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      await patchTask(token, task.id, { status });
      // A fresh read proves the new state persisted and removes the action
      // from the open Today view instead of trusting a local mutation.
      setTaskReloadKey((key) => key + 1);
      setNotice(status === "done" ? `Completed “${task.title}”.` : `Cancelled “${task.title}”.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setTaskBusyId("");
    }
  }

  // Per-task calendar export: fetch the owner-scoped .ics from the API and
  // save it as a file. The export is built by the API from the stored
  // deadline, so no time or zone is invented on the client.
  async function exportTaskIcs(task: Task) {
    try {
      const token = await sessionToken();
      const response = await fetch(taskIcsUrl(task.id), { headers: { Authorization: `Bearer ${token}` } });
      if (!response.ok) throw new Error("The task calendar entry could not be exported.");
      const blob = new Blob([await response.text()], { type: "text/calendar; charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `task-${task.id}.ics`;
      try {
        document.body.appendChild(anchor);
        anchor.click();
      } finally {
        anchor.remove();
        void setTimeout(() => URL.revokeObjectURL(url), 1000);
      }
    } catch (caught) {
      setError(messageFrom(caught));
    }
  }

  if (!session) {
    return <SignInPanel />;
  }

  const editingApplication = editing;

  return <ApplicationDashboard
    email={session.user.emailAddresses[0]?.emailAddress ?? ""}
    result={result}
    filters={filters}
    page={pageIndex + 1}
    hasPrevious={pageIndex > 0}
    hasNext={result.next_cursor !== null}
    loading={loading}
    loadError={loadError}
    saving={saving}
    busyId={busyId}
    editingApplication={editingApplication}
    taskPage={taskPage}
    taskPageIndex={taskPageIndex}
    taskHasPrevious={taskPageIndex > 0}
    taskHasNext={taskPage.next_cursor !== null}
    taskLoading={taskLoading}
    taskLoadError={taskLoadError}
    tasksShowAll={tasksShowAll}
    taskBusyId={taskBusyId}
    taskDraft={taskDraft}
    error={error}
    notice={notice}
    notificationEnabled={notificationEnabled}
    notificationLoading={notificationLoading}
    notificationSaving={notificationSaving}
    notificationError={notificationError}
    notificationNotice={notificationNotice}
    onNotificationChange={changeNotificationSetting}
    onCreate={addApplication}
    onStatusChange={(application, status) => void changeStatus(application, status)}
    onEdit={(application) => { setTaskDraft(null); setEditing(application); setError(""); setNotice(""); }}
    onCancelEdit={() => setEditing(null)}
    onSaveEdit={(event, id) => void saveEdit(event, id)}
    onRemove={(application) => void removeApplication(application)}
    onAddTask={openTaskForm}
    onApplyFilters={applyFilters}
    onClearFilters={() => applyFilters(noFilters)}
    onPrevious={goPrevious}
    onNext={goNext}
    onToggleFinished={toggleFinished}
    onTaskPrevious={goPreviousTask}
    onTaskNext={goNextTask}
    onRetryApplications={reload}
    onRetryTasks={restartTasks}
    onEditTask={(task, application) => openTaskForm(applicationForTask(task, application), task)}
    onSaveTask={(event, id) => void saveTask(event, id)}
    onCancelTaskEdit={() => setTaskDraft(null)}
    onCompleteTask={(task) => void changeTaskStatus(task, "done")}
    onCancelTask={(task) => void changeTaskStatus(task, "cancelled")}
    onExport={(task) => void exportTaskIcs(task)}
  />;
}
