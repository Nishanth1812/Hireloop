"use client";

import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { useAuth, useSession, useSignIn } from "@clerk/nextjs";
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
  const { session } = useSession();
  // `useAuth()` reports load state via `isLoaded` (not `isLoading`).
  const { isLoaded } = useAuth();
  // `signIn` may be undefined until Clerk is ready; guard before use.
  const { signIn } = useSignIn();

  const [applications, setApplications] = useState<Application[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busyId, setBusyId] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [authError, setAuthError] = useState("");

  async function sessionToken(): Promise<string | null> {
    if (!session) return null;
    return session.getToken();
  }

  useEffect(() => {
    // Clerk loads asynchronously. While it is initializing, show the
    // checking shell; once loaded, either sign in or load applications.
    if (!isLoaded) return;
    if (!session) {
      // No active session: clear previously loaded applications so data from
      // a prior user is never shown after sign-out (user isolation). Defer
      // the update to avoid calling setState synchronously inside the effect
      // body, which React flags as cascading renders.
      let cancelled = false;
      queueMicrotask(() => {
        if (cancelled) return;
        setApplications([]);
        setLoading(false);
      });
      return () => {
        cancelled = true;
      };
    }
    let cancelled = false;
    void (async () => {
      const token = await session.getToken();
      if (cancelled || !token) return;
      setLoading(true);
      setLoadError(false);
      setError("");
      try {
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
  }, [session, isLoaded]);

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
      const token = await sessionToken();
      if (!token) return;
      const application = await createApplication(token, {
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
      const token = await sessionToken();
      if (!token) return;
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
      if (!token) return;
      await deleteApplication(token, application.id);
      setApplications((current) => current.filter((item) => item.id !== application.id));
      setNotice(`Application at ${application.company} was deleted.`);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setBusyId("");
    }
  }

  async function handleSignIn() {
    setAuthError("");
    if (!signIn) return;
    // Clerk v7: start the Google OAuth redirect via the supported `signIn.sso`
    // API on the resource returned by `useSignIn()`. `redirectUrl` is where the
    // user lands after a successful sign-in (the app home); `redirectCallbackUrl`
    // is the route Clerk calls back to (which renders `<HandleSSOCallback />`).
    // No `__internal_future` hop is needed anymore.
    const { error } = await signIn.sso({
      strategy: "oauth_google",
      redirectUrl: window.location.origin,
      redirectCallbackUrl: `${window.location.origin}/sso-callback`,
    });
    if (error) {
      setAuthError(error.message ?? "Sign-in failed. Please try again.");
    }
  }

  if (!isLoaded) {
    return <main className="page-shell"><p className="loading" role="status">Checking your sign-in…</p></main>;
  }

  if (!session) {
    return <SignInPanel error={authError} onSignIn={handleSignIn} />;
  }

  return <ApplicationDashboard
    email={session.user.emailAddresses[0]?.emailAddress ?? ""}
    applications={applications}
    loading={loading}
    loadError={loadError}
    saving={saving}
    busyId={busyId}
    error={error}
    notice={notice}
    onCreate={addApplication}
    onStatusChange={(application, status) => void changeStatus(application, status)}
    onRemove={(application) => void removeApplication(application)}
  />;
}
