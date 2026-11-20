"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { useSession } from "@clerk/nextjs";
import {
  createApplication,
  deleteApplication,
  listApplications,
  patchApplication,
  toChannel,
} from "@/lib/api";
import type { Application, ApplicationFilters, ApplicationPage } from "@/lib/api";
import { ApplicationDashboard } from "./_components/ApplicationDashboard";
import { SignInPanel } from "./_components/SignInPanel";

const noFilters: ApplicationFilters = { q: "", status: "", channel: "" };

const firstPage: ApplicationPage = {
  items: [],
  page: 1,
  pageSize: 25,
  total: 0,
  totalPages: 0,
  counts: { saved: 0, applied: 0 },
};

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
  const [result, setResult] = useState<ApplicationPage>(firstPage);
  const [filters, setFilters] = useState<ApplicationFilters>(noFilters);
  const [page, setPage] = useState(1);
  // Bumping this refetches the current query after a mutation so totals and
  // counts always come from the API instead of local arithmetic.
  const [reloadKey, setReloadKey] = useState(0);
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
        const next = await listApplications(token, filters, page);
        if (!cancelled) {
          setResult(next);
          setLoadError(false);
          // A page can fall out of range when rows are deleted elsewhere; settle
          // on the last real page instead of showing a misleading empty page.
          if (next.totalPages > 0 && page > next.totalPages) {
            setPage(next.totalPages);
          }
        }
      } catch (caught: unknown) {
        if (!cancelled) {
          // Drop the previous page's rows: they describe a different query.
          setResult(firstPage);
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
  }, [session, filters, page, reloadKey]);

  function reload() {
    setReloadKey((key) => key + 1);
  }

  function applyFilters(next: ApplicationFilters) {
    setPage(1);
    setFilters(next);
  }

  async function addApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    const url = String(data.get("url") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to add this application.");
      return;
    }
    if (url && !/^https?:\/\//i.test(url)) {
      setError("Enter the job posting link starting with http:// or https://.");
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
        url,
        source: String(data.get("source") ?? "").trim(),
        channel: toChannel(String(data.get("channel") ?? "").trim()),
        job_id: String(data.get("job_id") ?? "").trim(),
        applied_on: String(data.get("applied_on") ?? "").trim(),
        address: String(data.get("address") ?? "").trim(),
        notes: String(data.get("notes") ?? "").trim(),
      });
      // A successful write proves the API is reachable, so a stale load error
      // from an earlier failed refresh must not keep contradicting the new row.
      // Newest first plus cleared filters puts the new record on page one.
      setLoadError(false);
      applyFilters(noFilters);
      reload();
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
      reload();
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
    const url = String(data.get("url") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to save this application.");
      return;
    }
    if (url && !/^https?:\/\//i.test(url)) {
      setError("Enter the job posting link starting with http:// or https://.");
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
        url,
        source: String(data.get("source") ?? "").trim(),
        channel: toChannel(String(data.get("channel") ?? "").trim()),
        job_id: String(data.get("job_id") ?? "").trim(),
        applied_on: String(data.get("applied_on") ?? "").trim(),
        address: String(data.get("address") ?? "").trim(),
        notes: String(data.get("notes") ?? "").trim(),
      });
      reload();
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
      // Deleting the last row of a later page must move back a page instead of
      // leaving the user on an empty page.
      if (result.items.length === 1 && page > 1) {
        setPage(page - 1);
      } else {
        reload();
      }
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

  const editingApplication = result.items.find((item) => item.id === editingId) ?? null;

  return <ApplicationDashboard
    email={session.user.emailAddresses[0]?.emailAddress ?? ""}
    result={result}
    filters={filters}
    page={page}
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
    onApplyFilters={applyFilters}
    onClearFilters={() => applyFilters(noFilters)}
    onPageChange={setPage}
  />;
}