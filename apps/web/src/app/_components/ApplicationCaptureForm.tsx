"use client";

import type { FormEvent } from "react";

export function ApplicationCaptureForm({ saving, onSubmit }: {
  saving: boolean;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <aside className="capture-panel" aria-labelledby="add-title">
      <p className="eyebrow">Quick capture</p>
      <h2 id="add-title">Add an application</h2>
      <p className="muted">Save a role now. Only company and role are required.</p>
      <form className="application-form" onSubmit={onSubmit}>
        <label htmlFor="company">Company</label>
        <input id="company" name="company" autoComplete="organization" maxLength={200} required placeholder="e.g. Acme" />
        <label htmlFor="role">Role</label>
        <input id="role" name="role" maxLength={200} required placeholder="e.g. Product designer" />
        <label htmlFor="status">Status</label>
        <select id="status" name="status" defaultValue="saved">
          <option value="saved">Saved</option>
          <option value="applied">Applied</option>
        </select>
        <label htmlFor="url">Job posting URL <span className="optional">optional</span></label>
        <input id="url" name="url" type="url" maxLength={2048} placeholder="https://jobs.example.com/42" />
        <label htmlFor="source">Source <span className="optional">optional</span></label>
        <input id="source" name="source" maxLength={200} placeholder="e.g. LinkedIn, referral" />
        <label htmlFor="channel">Channel <span className="optional">optional</span></label>
        <select id="channel" name="channel" defaultValue="">
          <option value="">Not recorded</option>
          <option value="campus">Campus</option>
          <option value="off-campus">Off-campus</option>
        </select>
        <label htmlFor="job_id">Job ID <span className="optional">optional</span></label>
        <input id="job_id" name="job_id" maxLength={200} placeholder="e.g. REQ-42" />
        <label htmlFor="applied_on">Date applied <span className="optional">optional</span></label>
        <input id="applied_on" name="applied_on" type="date" />
        <label htmlFor="address">Address used <span className="optional">optional</span></label>
        <input id="address" name="address" maxLength={200} autoComplete="email" placeholder="e.g. me@example.com" />
        <label htmlFor="notes">Notes <span className="optional">optional</span></label>
        <textarea id="notes" name="notes" rows={3} maxLength={2000} placeholder="Anything worth remembering about this application." />
        <button className="button primary" type="submit" disabled={saving}>
          {saving ? "Adding…" : "Add application"}
        </button>
      </form>
    </aside>
  );
}