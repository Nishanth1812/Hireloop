"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { useSession } from "@clerk/nextjs";
import {
  createApplication,
  createTask,
  deleteApplication,
  listApplications,
  listTasks,
  patchApplication,
  patchTask,
  toChannel,
} from "@/lib/api";
import type {
  Application, ApplicationFilters, ApplicationPage, ApplicationPatch, Task, TaskInput, TaskPatch, TaskStatus,
} from "@/lib/api";
import { ApplicationDashboard } from "./_components/ApplicationDashboard";
import type { TaskDraft } from "./_components/ApplicationDashboard";
import { SignInPanel } from "./_components/SignInPanel";

const noFilters: ApplicationFilters = { q: "", status: "", channel: "" };

const firstPage: ApplicationPage = {
  items: [],
  page_size: 25,
  total: 0,
  counts: { saved: 0, applied: 0 },
  next_cursor: null,
};

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
  const [tasks, setTasks] = useState<Task[]>([]);
  const [taskLoading, setTaskLoading] = useState(session !== null);
  const [taskLoadError, setTaskLoadError] = useState(false);
  const [tasksShowAll, setTasksShowAll] = useState(false);
  const [taskReloadKey, setTaskReloadKey] = useState(0);
  const [taskBusyId, setTaskBusyId] = useState("");
  const [taskDraft, setTaskDraft] = useState<TaskDraft | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

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
    void (async () => {
      setTaskLoading(true);
      setTaskLoadError(false);
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        const next = await listTasks(token, tasksShowAll);
        if (cancelled) return;
        setTasks(next.items);
      } catch (caught: unknown) {
        if (!cancelled) {
          // Never leave a previous account's actions on screen after a failure.
          setTasks([]);
          setTaskLoadError(true);
          setError(messageFrom(caught));
        }
      } finally {
        if (!cancelled) setTaskLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, tasksShowAll, taskReloadKey]);

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
    }
    if (precision === "instant") {
      fields.due_local = String(data.get("due_local") ?? "").trim();
      fields.time_zone = String(data.get("time_zone") ?? "").trim();
      if (!fields.time_zone) {
        // A wall time with no zone is not an instant; ask instead of guessing.
        setError("Choose the time zone for this deadline.");
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
          application_id: taskDraft.application.id,
          title,
          due_precision: precision,
          ...(precision === "date" ? { due_date: fields.due_date } : {}),
          ...(precision === "instant" ? { due_local: fields.due_local, time_zone: fields.time_zone } : {}),
        };
        await createTask(token, input);
      }
      setTaskDraft(null);
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
    tasks={tasks}
    taskLoading={taskLoading}
    taskLoadError={taskLoadError}
    tasksShowAll={tasksShowAll}
    taskBusyId={taskBusyId}
    taskDraft={taskDraft}
    error={error}
    notice={notice}
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
    onToggleFinished={() => setTasksShowAll((value) => !value)}
    onEditTask={(task, application) => openTaskForm(applicationForTask(task, application), task)}
    onSaveTask={(event, id) => void saveTask(event, id)}
    onCancelTaskEdit={() => setTaskDraft(null)}
    onCompleteTask={(task) => void changeTaskStatus(task, "done")}
    onCancelTask={(task) => void changeTaskStatus(task, "cancelled")}
  />;
}