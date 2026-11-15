"use client";

import type { Application } from "@/lib/api";

export function ApplicationList({ applications, loading, loadError, busyId, onStatusChange, onRemove }: {
  applications: Application[];
  loading: boolean;
  loadError: boolean;
  busyId: string;
  onStatusChange: (application: Application, status: Application["status"]) => void;
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
              </div>
              <label className="visually-hidden" htmlFor={`status-${application.id}`}>Status for {application.role} at {application.company}</label>
              <select
                id={`status-${application.id}`}
                className={`status-select ${application.status}`}
                value={application.status}
                disabled={busyId === application.id}
                onChange={(event) => onStatusChange(application, event.target.value as Application["status"])}
              >
                <option value="saved">Saved</option>
                <option value="applied">Applied</option>
              </select>
              <button className="button delete-button" disabled={busyId === application.id} onClick={() => onRemove(application)} aria-label={`Delete ${application.role} at ${application.company}`}>
                Delete
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
