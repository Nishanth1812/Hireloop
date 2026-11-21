"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import type { Application, Task, TaskPrecision } from "@/lib/api";
import { zoneChoices } from "@/lib/tasks";

export function TaskForm({ application, task, applications, saving, onSave, onCancel }: {
  application: Application;
  task: Task | null;
  applications: Application[];
  saving: boolean;
  onSave: (event: FormEvent<HTMLFormElement>, id?: string) => void;
  onCancel: () => void;
}) {
  const [precision, setPrecision] = useState<TaskPrecision>(task?.due_precision ?? "none");
  const zones = zoneChoices(task?.time_zone);
  const options = applications.some((item) => item.id === application.id)
    ? applications
    : [application, ...applications];

  return (
    <aside className="capture-panel" aria-labelledby="task-form-title" key={task?.id ?? application.id}>
      <p className="eyebrow">{task ? "Edit action" : "Next action"}</p>
      <h2 id="task-form-title">{task ? "Update task" : "Add a task"}</h2>
      <p className="muted">Turn this application into a concrete next step. A deadline is optional.</p>
      <form className="application-form" onSubmit={(event) => onSave(event, task?.id)}>
        <label htmlFor="task-application">Application</label>
        <select id="task-application" name="application_id" defaultValue={application.id} disabled={!!task || saving}>
          {options.map((item) => (
            <option key={item.id} value={item.id}>{item.role} at {item.company}</option>
          ))}
        </select>

        <label htmlFor="task-title-input">What needs doing</label>
        <input id="task-title-input" name="title" maxLength={200} required autoFocus
          defaultValue={task?.title} placeholder="e.g. Submit the online assessment" />

        <label htmlFor="task-precision">Deadline</label>
        <select id="task-precision" name="due_precision" value={precision}
          onChange={(event) => setPrecision(event.target.value as TaskPrecision)} disabled={saving}>
          <option value="none">No deadline</option>
          <option value="date">Date only (cutoff time unspecified)</option>
          <option value="instant">Exact time in a chosen zone</option>
        </select>

        {precision === "date" && (
          <>
            <label htmlFor="task-due-date">Due date</label>
            <input id="task-due-date" name="due_date" type="date" defaultValue={task?.due_date} required />
            <p className="field-hint">A date-only deadline never implies a time or a time zone.</p>
            <p className="field-hint">Your browser zone is saved with the task for two approximate reminders (the day before and the morning of). They are labelled approximate and are not a guaranteed cutoff.</p>
          </>
        )}

        {precision === "instant" && (
          <>
            <label htmlFor="task-due-local">Local date and time</label>
            <input id="task-due-local" name="due_local" type="datetime-local" defaultValue={task?.due_local} required />
            <label htmlFor="task-time-zone">Time zone of that local time</label>
            <select id="task-time-zone" name="time_zone" defaultValue={task?.time_zone ?? ""} required>
              <option value="">Choose a time zone</option>
              {zones.map((zone) => <option key={zone} value={zone}>{zone}</option>)}
            </select>
            <p className="field-hint">The exact instant is stored in UTC and always shown again in this zone.</p>
          </>
        )}

        <button className="button primary" type="submit" disabled={saving}>
          {saving ? "Saving…" : task ? "Save task" : "Add task"}
        </button>
        <button className="button quiet" type="button" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
      </form>
    </aside>
  );
}
