"use client";

import { HandleSSOCallback } from "@clerk/nextjs";
import { useRouter } from "next/navigation";

// Completes an OAuth (e.g. Google) sign-in started by `signIn.sso()`.
// After the identity provider redirects the user back here, Clerk exchanges the
// authorization code, establishes the session, then redirects to the app home.
export default function SSOCallback() {
  const router = useRouter();

  return (
    <HandleSSOCallback
      navigateToApp={({ decorateUrl }) => {
        // A session was created: send the user back to the app home. `decorateUrl`
        // wraps the destination so Clerk can refresh cookies (e.g. Safari ITP).
        const url = decorateUrl("/");
        if (url.startsWith("http")) {
          window.location.href = url;
        } else {
          router.push(url);
        }
      }}
      // The prebuilt sign-in page handles additional authentication steps.
      navigateToSignIn={() => router.push("/sign-in")}
      // The prebuilt sign-up page collects requirements configured in Clerk.
      navigateToSignUp={() => router.push("/sign-up")}
    />
  );
}
