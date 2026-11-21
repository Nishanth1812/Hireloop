"use client";

import type { Application, Task } from "@/lib/api";
import { deadlineText } from "@/lib/tasks";

function TaskCard({ task, application, busy, onEdit, onComplete, onCancel, onExport }: {
  task: Task;
  application?: Application;
  busy: boolean;
  onEdit: (task: Task, application?: Application) => void;
  onComplete: (task: Task) => void;
  onCancel: (task: Task) => void;
  onExport: (task: Task) => void;
}) {
  const deadline = deadlineText(task);
  return (
    <li className={`task-row ${task.status}`}>
      <div className="task-copy">
        <strong>{task.title}</strong>
        <span>{application ? `${application.role} at ${application.company}` : "Linked application"}</span>
        <span className="task-deadline">{deadline.when}</span>
        <span className="task-detail">{deadline.detail}</span>
        {deadline.reminder && <span className="task-detail">{deadline.reminder}</span>}
        <button className="task-export-button" type="button" disabled={busy}
          onClick={() => onExport(task)} aria-label={`Export ${task.title} calendar entry`}>Export ICS</button>
        {task.status !== "open" && <span className="task-status">Status: {task.status}</span>}
      </div>
      {task.status === "open" && (
        <div className="task-actions">
          <button className="button quiet" type="button" disabled={busy}
            onClick={() => onEdit(task, application)} aria-label={`Edit task ${task.title}`}>Edit</button>
          <button className="button complete-button" type="button" disabled={busy}
            onClick={() => onComplete(task)} aria-label={`Mark ${task.title} complete`}>Complete</button>
          <button className="button delete-button" type="button" disabled={busy}
            onClick={() => onCancel(task)} aria-label={`Cancel ${task.title}`}>Cancel</button>
        </div>
      )}
    </li>
  );
}

export function TodayPanel({ tasks, applications, total, page, hasPrevious, hasNext, loading, loadError, showAll, busyId, onToggleFinished, onPrevious, onNext, onEdit, onComplete, onCancel, onExport }: {
  tasks: Task[];
  applications: Application[];
  total: number;
  page: number;
  hasPrevious: boolean;
  hasNext: boolean;
  loading: boolean;
  loadError: boolean;
  showAll: boolean;
  busyId: string;
  onToggleFinished: () => void;
  onPrevious: () => void;
  onNext: () => void;
  onEdit: (task: Task, application?: Application) => void;
  onComplete: (task: Task) => void;
  onCancel: (task: Task) => void;
  onExport: (task: Task) => void;
}) {
  const open = tasks.filter((task) => task.status === "open");
  const finished = tasks.filter((task) => task.status !== "open");
  const applicationsByID = new Map(applications.map((application) => [application.id, application]));
  // The API's total counts every matching row, not just this page. A bounded
  // page is always labelled as such instead of hiding the remaining tasks.
  const shown = open.length + finished.length;

  return (
    <section className="today-panel" aria-labelledby="today-title" aria-busy={loading}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">What needs attention</p>
          <h2 id="today-title">Today {!showAll && <span className="count">{total}</span>}</h2>
        </div>
        <button className="button quiet" type="button" aria-pressed={showAll} onClick={onToggleFinished}>
          {showAll ? "Hide finished" : "Show finished"}
        </button>
      </div>
      <p className="results-summary" role="status">
        {loading
          ? "Loading actions…"
          : loadError
            ? "Your actions could not be loaded."
            : total === 0
              ? showAll
                ? "No actions recorded yet. Add one from any application."
                : "Nothing open right now. Add a task from any application."
              : `Showing ${shown} on this page · ${total} total`}
      </p>
      {!loading && !loadError && open.length > 0 && (
        <ul className="task-list">
          {open.map((task) => (
            <TaskCard key={task.id} task={task} application={applicationsByID.get(task.application_id)}
              busy={busyId === task.id} onEdit={onEdit} onComplete={onComplete} onCancel={onCancel} onExport={onExport} />
          ))}
        </ul>
      )}
      {showAll && !loading && !loadError && (
        <div className="finished-tasks">
          <h3>Finished</h3>
          {finished.length === 0 ? (
            <p className="muted">No completed or cancelled actions yet.</p>
          ) : (
            <ul className="task-list">
              {finished.map((task) => (
                <TaskCard key={task.id} task={task} application={applicationsByID.get(task.application_id)}
                  busy={busyId === task.id} onEdit={onEdit} onComplete={onComplete} onCancel={onCancel} onExport={onExport} />
              ))}
            </ul>
          )}
        </div>
      )}
      {!loading && !loadError && (hasPrevious || hasNext) && (
        <nav className="pagination today-pagination" aria-label="Today pages">
          <button className="button quiet" type="button" onClick={onPrevious} disabled={!hasPrevious}>Previous</button>
          <span className="page-indicator">Page {page}</span>
          <button className="button quiet" type="button" onClick={onNext} disabled={!hasNext}>Next</button>
        </nav>
      )}
    </section>
  );
}
