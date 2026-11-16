import { SignIn } from "@clerk/nextjs";
import { Suspense } from "react";
import { ThemeControl } from "../../_components/ThemeControl";

export default function SignInPage() {
  return (
    <main className="auth-shell clerk-auth-shell page-shell" aria-label="Sign in">
      <div className="clerk-auth-content">
        <div className="clerk-auth-controls"><ThemeControl /></div>
        <Suspense>
          <SignIn routing="path" path="/sign-in" />
        </Suspense>
      </div>
    </main>
  );
}
