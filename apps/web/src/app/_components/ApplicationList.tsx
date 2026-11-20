"use client";

import type { Application } from "@/lib/api";

export function ApplicationList({ applications, loading, loadError, busyId, saving, editingId, onStatusChange, onEdit, onRemove }: {
  applications: Application[];
  loading: boolean;
  loadError: boolean;
  busyId: string;
  saving?: boolean;
  editingId: string;
  onStatusChange: (application: Application, status: Application["status"]) => void;
  onEdit: (application: Application) => void;
  onRemove: (application: Application) => void;
}) {
  return (
    <section className="application-section" aria-labelledby="applications-title" aria-busy={loading}>
      <div className="section-heading">
        <div><p className="eyebrow">Your pipeline</p><h2 id="applications-title">All applications <span className="count">{applications.length}</span></h2></div>
      </div>
      {loading ? (
        <p className="loading" role="status">Loading applications…</p>
      ) : applications.length === 0 && !loadError ? (
        <div className="empty-state" role="status">
          <span className="empty-icon" aria-hidden="true">↗</span>
          <h3>Your next opportunity starts here.</h3>
          <p>Add a role to keep its details close and your next step clear.</p>
        </div>
      ) : loadError ? (
        <p className="loading" role="status">Your applications could not be loaded. Try again after checking your connection.</p>
      ) : (
        <ul className="application-list">
          {applications.map((application) => (
            <li className="application-row" key={application.id}>
              <div className="company-initial" aria-hidden="true">{application.company.slice(0, 1).toUpperCase()}</div>
              <div className="application-copy">
                <strong>{application.role}</strong>
                <span>{application.company}</span>
                {application.source && <span className="application-meta">via {application.source}</span>}
                {application.address && <span className="application-meta">{application.address}</span>}
              </div>
              <label className="visually-hidden" htmlFor={`status-${application.id}`}>Status for {application.role} at {application.company}</label>
              <select
                id={`status-${application.id}`}
                className={`status-select ${application.status}`}
                value={application.status}
                disabled={saving || busyId === application.id || editingId === application.id}
                onChange={(event) => onStatusChange(application, event.target.value as Application["status"])}
              >
                <option value="saved">Saved</option>
                <option value="applied">Applied</option>
              </select>
              <button
                className="button edit-button"
                id={`edit-button-${application.id}`}
                disabled={saving || busyId === application.id}
                aria-pressed={editingId === application.id}
                onClick={() => onEdit(application)}
                aria-label={`Edit ${application.role} at ${application.company}`}
              >
                Edit
              </button>
              <button className="button delete-button" disabled={saving || busyId === application.id || editingId === application.id} onClick={() => onRemove(application)} aria-label={`Delete ${application.role} at ${application.company}`}>
                Delete
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
