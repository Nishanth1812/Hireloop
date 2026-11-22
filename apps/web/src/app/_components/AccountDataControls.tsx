"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { deleteAccount, exportAccount } from "@/lib/api";

export function AccountDataControls({ getToken, onDeletionAccepted }: {
  getToken: () => Promise<string>;
  onDeletionAccepted: () => Promise<void>;
}) {
  const [exporting, setExporting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  async function downloadExport() {
    setExporting(true);
    setError("");
    setNotice("");
    try {
      const token = await getToken();
      const blob = await exportAccount(token);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = "hireloop-account-export.json";
      try {
        document.body.appendChild(anchor);
        anchor.click();
      } finally {
        anchor.remove();
        void setTimeout(() => URL.revokeObjectURL(url), 1000);
      }
      setNotice("Your data export download is ready.");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Your data could not be exported. Please try again.");
    } finally {
      setExporting(false);
    }
  }

  async function submitDeletion(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (confirmation !== "DELETE") return;
    setDeleting(true);
    setError("");
    setNotice("");
    let result: "deleted" | "pending";
    try {
      const token = await getToken();
      result = await deleteAccount(token);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Your account could not be deleted. Please try again.");
      setDeleting(false);
      return;
    }

    setNotice(result === "deleted"
      ? "Your Hireloop account and Clerk sign-in were deleted. Signing you out."
      : "Your Hireloop data was removed. Clerk sign-in deletion is pending; access is blocked while we retry. Signing you out.");
    try {
      await onDeletionAccepted();
    } catch {
      setError("Deletion was accepted, but this browser could not finish signing out. Close this window.");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <section className="notification-control" aria-labelledby="account-data-title" aria-busy={exporting || deleting}>
      <h2 id="account-data-title">Your data</h2>
      <p className="muted">Download a copy of your Hireloop data or permanently delete your account.</p>
      <div className="account-data-actions">
        <button className="button quiet" type="button" disabled={exporting || deleting} onClick={() => void downloadExport()}>
          {exporting ? "Preparing download…" : "Download your data"}
        </button>
        {!confirming && (
          <button className="button quiet" type="button" disabled={exporting || deleting} onClick={() => { setConfirming(true); setError(""); setNotice(""); }}>
            Delete account
          </button>
        )}
      </div>
      {confirming && (
        <form className="application-form account-delete-confirmation" onSubmit={(event) => void submitDeletion(event)}>
          <p id="delete-account-confirmation-help">Type <strong>DELETE</strong> to permanently erase your Hireloop data and remove your Clerk sign-in.</p>
          <p className="muted">A reminder email already being sent by a provider cannot be recalled.</p>
          <label htmlFor="delete-account-confirmation">Confirmation</label>
          <input
            id="delete-account-confirmation"
            name="confirmation"
            autoComplete="off"
            required
            aria-describedby="delete-account-confirmation-help"
            spellCheck={false}
            value={confirmation}
            disabled={deleting}
            onChange={(event) => setConfirmation(event.target.value)}
          />
          <div className="account-data-actions">
            <button className="button primary" type="submit" disabled={exporting || deleting || confirmation !== "DELETE"}>
              {deleting ? "Deleting…" : "Permanently delete account"}
            </button>
            <button className="button quiet" type="button" disabled={deleting} onClick={() => { setConfirming(false); setConfirmation(""); }}>
              Cancel
            </button>
          </div>
        </form>
      )}
      {error && <p className="message error-message" role="alert">{error}</p>}
      {notice && <p className="message success-message" role="status">{notice}</p>}
    </section>
  );
}
