"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import type { Session } from "@supabase/supabase-js";
import {
  createApplication,
  deleteApplication,
  listApplications,
  patchApplication,
} from "@/lib/api";
import type { Application } from "@/lib/api";
import { getSupabaseClient } from "@/lib/supabase";
import { ApplicationDashboard } from "./_components/ApplicationDashboard";
import { SignInPanel } from "./_components/SignInPanel";

function messageFrom(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong. Please try again.";
}

export default function ApplicationTracker() {
  const [session, setSession] = useState<Session | null>(null);
  const [authReady, setAuthReady] = useState(false);
  const [authError, setAuthError] = useState("");
  const [applications, setApplications] = useState<Application[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busyId, setBusyId] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    let mounted = true;
    let unsubscribe: (() => void) | undefined;
    void Promise.resolve()
      .then(() => {
        if (!mounted) return;
        const client = getSupabaseClient();
        const { data: { subscription } } = client.auth.onAuthStateChange((_event, nextSession) => {
          if (!mounted) return;
          setSession(nextSession);
          setLoading(Boolean(nextSession));
          setLoadError(false);
          setError("");
          if (!nextSession) setApplications([]);
          setAuthReady(true);
        });
        unsubscribe = () => subscription.unsubscribe();
        return client.auth.getSession();
      })
      .then((result) => {
        if (!mounted || !result) return;
        const { data, error: sessionError } = result;
        if (sessionError) setAuthError(messageFrom(sessionError));
        setSession(data.session);
        setLoading(Boolean(data.session));
        setAuthReady(true);
      })
      .catch((caught: unknown) => {
        if (!mounted) return;
        setAuthError(messageFrom(caught));
        setAuthReady(true);
        setLoading(false);
      });

    return () => {
      mounted = false;
      unsubscribe?.();
    };
  }, []);

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    void listApplications(session.access_token)
      .then((items) => {
        if (!cancelled) {
          setApplications(items);
          setLoadError(false);
        }
      })
      .catch((caught: unknown) => {
        if (!cancelled) {
          setLoadError(true);
          setError(messageFrom(caught));
        }
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [session]);

  async function signIn() {
    setAuthError("");
    try {
      const client = getSupabaseClient();
      const { error: signInError } = await client.auth.signInWithOAuth({
        provider: "google",
        options: { redirectTo: window.location.origin },
      });
      if (signInError) throw signInError;
    } catch (caught) {
      setAuthError(messageFrom(caught));
    }
  }

  async function signOut() {
    try {
      const { error: signOutError } = await getSupabaseClient().auth.signOut();
      if (signOutError) setError(messageFrom(signOutError));
    } catch (caught) {
      setError(messageFrom(caught));
    }
  }

  async function addApplication(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const company = String(data.get("company") ?? "").trim();
    const role = String(data.get("role") ?? "").trim();
    if (!company || !role) {
      setError("Enter a company and role to add this application.");
      return;
    }
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const application = await createApplication(session.access_token, {
        company,
        role,
        status: String(data.get("status")) as Application["status"],
      });
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
      const updated = await patchApplication(session.access_token, application.id, { status });
      setApplications((current) => current.map((item) => item.id === updated.id ? updated : item));
      setNotice(`Status updated for ${updated.company}.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  async function removeApplication(application: Application) {
    if (!session || !window.confirm(`Delete the ${application.role} application at ${application.company}?`)) return;
    setBusyId(application.id);
    setError("");
    setNotice("");
    try {
      await deleteApplication(session.access_token, application.id);
      setApplications((current) => current.filter((item) => item.id !== application.id));
      setNotice(`Application at ${application.company} was deleted.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  if (!authReady) {
    return <main className="page-shell"><p className="loading" role="status">Checking your sign-in…</p></main>;
  }

  if (!session) {
    return <SignInPanel error={authError} onSignIn={signIn} />;
  }

  return <ApplicationDashboard
    session={session}
    applications={applications}
    loading={loading}
    loadError={loadError}
    saving={saving}
    busyId={busyId}
    error={error}
    notice={notice}
    onSignOut={signOut}
    onCreate={addApplication}
    onStatusChange={(application, status) => void changeStatus(application, status)}
    onRemove={(application) => void removeApplication(application)}
  />;
}
