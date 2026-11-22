"use client";

import { useSession } from "@clerk/nextjs";
import { useParams } from "next/navigation";
import { ApplicationDetail } from "../../_components/ApplicationDetail";

export default function ApplicationDetailPage() {
  const { session, isLoaded } = useSession();
  const params = useParams();
  const id = typeof params?.id === "string" ? params.id : "";
  if (!isLoaded) {
    return <main className="page-shell"><p className="loading" role="status">Checking your sign-in…</p></main>;
  }
  return <ApplicationDetail id={id} session={session ?? null} />;
}
