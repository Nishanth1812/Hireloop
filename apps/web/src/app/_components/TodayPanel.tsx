"use client";

import type { Application, Task } from "@/lib/api";
import { deadlineText } from "@/lib/tasks";

function TaskCard({ task, application, busy, onEdit, onComplete, onCancel }: {
  task: Task;
  application?: Application;
  busy: boolean;
  onEdit: (task: Task, application?: Application) => void;
  onComplete: (task: Task) => void;
  onCancel: (task: Task) => void;
}) {
  const deadline = deadlineText(task);
  return (
    <li className={`task-row ${task.status}`}>
      <div className="task-copy">
        <strong>{task.title}</strong>
        <span>{application ? `${application.role} at ${application.company}` : "Linked application"}</span>
        <span className="task-deadline">{deadline.when}</span>
        <span className="task-detail">{deadline.detail}</span>
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

export function TodayPanel({ tasks, applications, loading, loadError, showAll, busyId, onToggleFinished, onEdit, onComplete, onCancel }: {
  tasks: Task[];
  applications: Application[];
  loading: boolean;
  loadError: boolean;
  showAll: boolean;
  busyId: string;
  onToggleFinished: () => void;
  onEdit: (task: Task, application?: Application) => void;
  onComplete: (task: Task) => void;
  onCancel: (task: Task) => void;
}) {
  const open = tasks.filter((task) => task.status === "open");
  const finished = tasks.filter((task) => task.status !== "open");
  const applicationsByID = new Map(applications.map((application) => [application.id, application]));

  return (
    <section className="today-panel" aria-labelledby="today-title" aria-busy={loading}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">What needs attention</p>
          <h2 id="today-title">Today <span className="count">{open.length}</span></h2>
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
            : open.length === 0
              ? "Nothing open right now. Add a task from any application."
              : `${open.length} open action${open.length === 1 ? "" : "s"}`}
      </p>
      {!loading && !loadError && open.length > 0 && (
        <ul className="task-list">
          {open.map((task) => (
            <TaskCard key={task.id} task={task} application={applicationsByID.get(task.application_id)}
              busy={busyId === task.id} onEdit={onEdit} onComplete={onComplete} onCancel={onCancel} />
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
                  busy={busyId === task.id} onEdit={onEdit} onComplete={onComplete} onCancel={onCancel} />
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}
