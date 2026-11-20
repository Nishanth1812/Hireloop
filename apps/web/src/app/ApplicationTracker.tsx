"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { useSession } from "@clerk/nextjs";
import {
  createApplication,
  deleteApplication,
  listApplications,
  patchApplication,
} from "@/lib/api";
import type { Application } from "@/lib/api";
import { ApplicationDashboard } from "./_components/ApplicationDashboard";
import { SignInPanel } from "./_components/SignInPanel";

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
  const [applications, setApplications] = useState<Application[]>([]);
  const [loading, setLoading] = useState(session !== null);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busyId, setBusyId] = useState("");
  const [editingId, setEditingId] = useState("");
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
    void (async () => {
      try {
        const token = await session.getToken();
        if (cancelled) return;
        if (!token) throw new Error("Your session is unavailable. Please sign in again.");
        setLoading(true);
        setLoadError(false);
        setError("");
        const items = await listApplications(token);
        if (!cancelled) {
          setApplications(items);
          setLoadError(false);
        }
      } catch (caught: unknown) {
        if (!cancelled) {
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
  }, [session]);

  async function addApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    const source = String(data.get("source") ?? "").trim();
    const address = String(data.get("address") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to add this application.");
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
        source,
        address,
      });
      // A successful write proves the API is reachable, so a stale load error
      // from an earlier failed refresh must not keep contradicting the new row.
      setLoadError(false);
      setApplications((current) => [application, ...current]);
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
      setApplications((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
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
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to save this application.");
      return;
    }
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const token = await sessionToken();
      const updated = await patchApplication(token, id, {
        company,
        role,
        status: String(data.get("status")) as Application["status"],
        source: String(data.get("source") ?? "").trim(),
        address: String(data.get("address") ?? "").trim(),
      });
      setApplications((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
      setEditingId("");
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
      setApplications((current) => current.filter((item) => item.id !== application.id));
      setNotice(`Application at ${application.company} was deleted.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  if (!session) {
    return <SignInPanel />;
  }

  const editingApplication = applications.find((item) => item.id === editingId) ?? null;

  return <ApplicationDashboard
    email={session.user.emailAddresses[0]?.emailAddress ?? ""}
    applications={applications}
    loading={loading}
    loadError={loadError}
    saving={saving}
    busyId={busyId}
    editingApplication={editingApplication}
    error={error}
    notice={notice}
    onCreate={addApplication}
    onStatusChange={(application, status) => void changeStatus(application, status)}
    onEdit={(application) => { setEditingId(application.id); setError(""); setNotice(""); }}
    onCancelEdit={() => setEditingId("")}
    onSaveEdit={(event, id) => void saveEdit(event, id)}
    onRemove={(application) => void removeApplication(application)}
  />;
}
