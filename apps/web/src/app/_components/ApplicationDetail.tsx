"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import Link from "next/link";
import { SignOutButton, useSession } from "@clerk/nextjs";
import {
  STAGE_CHOICES, createTask, getApplication, listEvents, listTasksByApplication,
  patchApplication, stageLabel,
  type Application, type ApplicationEvent, type ApplicationPatch, type Task, type TaskInput,
} from "@/lib/api";
import { browserTimeZone, deadlineText, formatDateOnly, localDeadlineIssue } from "@/lib/tasks";
import { SignInPanel } from "./SignInPanel";
import { ThemeControl } from "./ThemeControl";
import { TaskForm } from "./TaskForm";

type Session = NonNullable<ReturnType<typeof useSession>["session"]>;

type CursorPage<T> = { items: T[]; next_cursor: string | null };

async function fetchAllPages<T>(fetchPage: (cursor: string | null) => Promise<CursorPage<T>>): Promise<T[]> {
  const collected: T[] = [];
  const seenCursors = new Set<string>();
  let cursor: string | null = null;
  while (true) {
    const result = await fetchPage(cursor);
    collected.push(...result.items);
    const next = result.next_cursor;
    if (!next) return collected;
    if (seenCursors.has(next)) {
      throw new Error("The record could not be read completely: the API returned a repeated page cursor.");
    }
    seenCursors.add(next);
    cursor = next;
  }
}

function messageFrom(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong. Please try again.";
}

// formatOccurredAt renders the server's UTC instant in the en-GB style used
// everywhere else; occurred_at is server-generated, so UTC is the honest
// display and never invents a time the candidate chose.
function formatOccurredAt(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString("en-GB", {
    day: "numeric", month: "short", year: "numeric",
    hour: "2-digit", minute: "2-digit", timeZone: "UTC",
  });
}

function historyText(event: ApplicationEvent): string {
  if (event.event_type === "created") {
    return event.to_status ? `Created — stage: ${stageLabel(event.to_status)}` : "Created";
  }
  const from = event.from_status ? stageLabel(event.from_status) : "";
  const to = event.to_status ? stageLabel(event.to_status) : "";
  if (from && to) return `Stage: ${from} → ${to}`;
  return to ? `Stage: ${to}` : "Stage changed";
}

export function ApplicationDetail({ id, session }: { id: string; session: Session | null }) {
  if (!session) {
    return <SignInPanel />;
  }
  return <ApplicationDetailView id={id} session={session} />;
}

function ApplicationDetailView({ id, session }: { id: string; session: Session }) {
  const [application, setApplication] = useState<Application | null>(null);
  const [events, setEvents] = useState<ApplicationEvent[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(session !== null);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  // The stage draft and note mirror the select; while the select differs from
  // the saved status, the optional note appears so it can be recorded with
  // the change.
  const [stageDraft, setStageDraft] = useState<Application["status"] | null>(null);
  const [stageNote, setStageNote] = useState("");
  const [reloadKey, setReloadKey] = useState(0);
  const [tasksReloadKey, setTasksReloadKey] = useState(0);

  async function sessionToken(): Promise<string> {
    const token = await session?.getToken();
    if (!token) throw new Error("Your session is unavailable. Please sign in again.");
    return token;
  }

  // Loads the record, its stage history (newest first) and every linked task.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      setLoading(true);
      setLoadError(false);
      setError("");
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        const [app, history, taskItems] = await Promise.all([
          getApplication(token, id),
          fetchAllPages((cursor) => listEvents(token, id, cursor)),
          fetchAllPages((cursor) => listTasksByApplication(token, id, cursor)),
        ]);
        if (cancelled) return;
        setApplication(app);
        // The API returns newest first; the history list shows oldest first.
        setEvents(history.reverse());
        setTasks(taskItems);
        setStageDraft(app.status);
      } catch (caught) {
        if (!cancelled) {
          setApplication(null);
          setEvents([]);
          setTasks([]);
          setStageDraft(null);
          setLoadError(true);
          setError(messageFrom(caught));
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [session, id, reloadKey, tasksReloadKey]);

  async function reloadEvents(): Promise<ApplicationEvent[]> {
    const token = await sessionToken();
    return (await fetchAllPages((cursor) => listEvents(token, id, cursor))).reverse();
  }

  async function changeStage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !application) return;
    const data = new FormData(event.currentTarget);
    const status = String(data.get("status")) as Application["status"];
    const note = String(data.get("stage_note") ?? "").trim();
    if (status === application.status) {
      // Re-selecting the saved stage is not a change; write nothing.
      setStageDraft(status);
      setStageNote("");
      return;
    }
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      const patch: ApplicationPatch = { status };
      if (note) patch.stage_note = note;
      const updated = await patchApplication(token, application.id, patch);
      setApplication(updated);
      setStageDraft(updated.status);
      setStageNote("");
      setNotice(`Stage updated to ${stageLabel(updated.status)}.`);
      setEvents(await reloadEvents());
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setSaving(false);
    }
  }

  async function addTask(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !application) return;
    const data = new FormData(event.currentTarget);
    const title = String(data.get("title") ?? "").trim();
    if (!title) {
      setError("Give this task a short title.");
      return;
    }
    const precision = String(data.get("due_precision") ?? "none") as Task["due_precision"];
    const fields: TaskInput = { application_id: application.id, title, due_precision: precision };
    if (precision === "date") {
      fields.due_date = String(data.get("due_date") ?? "").trim();
      if (!fields.due_date) {
        setError("Choose a date for this deadline.");
        return;
      }
      // An approximate date-only reminder is saved with the browser zone;
      // an unavailable zone means no nudge is invented.
      const zone = browserTimeZone();
      if (zone) fields.reminder_zone = zone;
    }
    if (precision === "instant") {
      fields.due_local = String(data.get("due_local") ?? "").trim();
      fields.time_zone = String(data.get("time_zone") ?? "").trim();
      if (!fields.time_zone) {
        setError("Choose the time zone for this deadline.");
        return;
      }
      if (!fields.due_local) {
        setError("Choose a date and time for this deadline.");
        return;
      }
      const issue = localDeadlineIssue(fields.due_local, fields.time_zone);
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
      // A fresh read proves the task persisted; the detail page's task list
      // only changes through its own writes.
      await createTask(token, fields);
      setTasksReloadKey((key) => key + 1);
      setNotice("Task added.");
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setSaving(false);
    }
  }

  const email = session.user.emailAddresses[0]?.emailAddress ?? "";
  const stageDraftValue = stageDraft ?? application?.status ?? "saved";

  return (
    <main className="page-shell">
      <header className="topbar">
        <Link className="wordmark" href="/" aria-label="Hireloop home"><span className="brand-mark">H</span>hireloop</Link>
        <div className="account-menu">
          <span className="account-email" title={email}>{email}</span>
          <ThemeControl />
          <SignOutButton><button className="button quiet" type="button">Sign out</button></SignOutButton>
        </div>
      </header>

      <header className="detail-header">
        <div>
          <p className="eyebrow">Application</p>
          <h1>{application?.role ?? "Application"}</h1>
          <p className="detail-subtitle">{application ? `${application.company} — ${stageLabel(application.status)}` : "Recorded work on this application"}</p>
        </div>
        <Link className="detail-back" href="/">Back to applications</Link>
      </header>

      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}

      {loading ? (
        <p className="loading" role="status">Loading application…</p>
      ) : loadError ? (
        <div className="empty-state" role="alert">
          <p>We couldn’t open this application. It may be missing or belong to a different account.</p>
          <button className="button quiet" type="button" onClick={() => setReloadKey((key) => key + 1)}>Try again</button>
        </div>
      ) : (
        application && (
          <div className="detail-grid">
            <section className="capture-panel" aria-labelledby="detail-form-title">
              <h2 id="detail-form-title">Stage</h2>
              <form className="application-form" onSubmit={(event) => void changeStage(event)}>
                <label htmlFor="detail-status">Stage</label>
                <select
                  id="detail-status"
                  name="status"
                  value={stageDraftValue}
                  disabled={saving}
                  onChange={(event) => setStageDraft(event.target.value as Application["status"])}
                >
                  {STAGE_CHOICES.map((status) => <option key={status} value={status}>{stageLabel(status)}</option>)}
                </select>
                {stageDraft !== null && stageDraft !== application.status && (
                  <>
                    <label htmlFor="detail-stage-note">Note for this change <span className="optional">optional</span></label>
                    <textarea
                      id="detail-stage-note"
                      name="stage_note"
                      rows={3}
                      maxLength={500}
                      value={stageNote}
                      placeholder="e.g. recruiter call scheduled"
                      onChange={(event) => setStageNote(event.target.value)}
                    />
                  </>
                )}
                <button className="button primary" type="submit" disabled={saving}>
                  {saving ? "Saving…" : "Change stage"}
                </button>
              </form>
              {application.applied_on && <p className="detail-meta">Applied: {formatDateOnly(application.applied_on)}</p>}
              {application.channel && <p className="detail-meta">Channel: {application.channel === "campus" ? "Campus drive" : "Off-campus"}</p>}
              {application.job_id && <p className="detail-meta">Job ID: {application.job_id}</p>}
              {application.source && <p className="detail-meta">Source: {application.source}</p>}
              {application.address && <p className="detail-meta">Address: {application.address}</p>}
              {application.url && (
                <p className="detail-meta">
                  <a className="application-link" href={application.url} target="_blank" rel="noreferrer noopener">Job posting</a>
                </p>
              )}
              {application.notes && <p className="detail-meta">{application.notes}</p>}
            </section>

            <section className="application-section" aria-labelledby="history-title">
              <h2 id="history-title">Stage history</h2>
              {events.length === 0 ? (
                <p className="muted" role="status">No recorded stage history yet.</p>
              ) : (
                <>
                  <ol className="history-list">
                  {events.map((event) => (
                      <li key={event.id} className="history-entry">
                        <time className="history-time" dateTime={event.occurred_at}>{formatOccurredAt(event.occurred_at)}</time>
                        <span className="history-copy">{historyText(event)}</span>
                        {event.note && <p className="history-note">{event.note}</p>}
                      </li>
                    ))}
                  </ol>
                </>
              )}
            </section>

            <section className="application-section" aria-labelledby="tasks-title">
              <h2 id="tasks-title">Linked tasks</h2>
              {tasks.length === 0 ? (
                <p className="muted" role="status">No tasks are linked to this application yet.</p>
              ) : (
                <>
                  <ul className="task-list">
                    {tasks.map((task) => {
                      const deadline = deadlineText(task);
                      return (
                        <li key={task.id} className={`task-row ${task.status}`}>
                          <div className="task-copy">
                            <strong>{task.title}</strong>
                            <span>{deadline.when}</span>
                            <span className="task-detail">{deadline.detail}</span>
                            {deadline.reminder && <span className="task-detail">{deadline.reminder}</span>}
                            <span className="task-status">{task.status === "open" ? "Open" : `Status: ${task.status}`}</span>
                          </div>
                        </li>
                      );
                    })}
                  </ul>
                </>
              )}
              <TaskForm
                application={application}
                task={null}
                applications={[application]}
                saving={saving}
                onSave={(event) => void addTask(event)}
                onCancel={() => {}}
              />
            </section>
          </div>
        )
      )}
      <footer className="footer-note">Hireloop keeps your job search organized, one next step at a time.</footer>
    </main>
  );
}
