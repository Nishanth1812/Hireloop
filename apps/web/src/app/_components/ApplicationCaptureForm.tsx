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
      <p className="muted">Save a role now. You can update its status as you go.</p>
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
        <button className="button primary" type="submit" disabled={saving}>
          {saving ? "Adding…" : "Add application"}
        </button>
      </form>
    </aside>
  );
}
