import { SignIn } from "@clerk/nextjs";
import { Suspense } from "react";
import { ThemeControl } from "../../_components/ThemeControl";

export default function SignInPage() {
  return (
    <main className="auth-shell page-shell" aria-label="Sign in">
      <ThemeControl />
      <Suspense>
        <SignIn routing="path" path="/sign-in" />
      </Suspense>
    </main>
  );
}
