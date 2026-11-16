import { SignUp } from "@clerk/nextjs";
import { Suspense } from "react";
import { ThemeControl } from "../../_components/ThemeControl";

export default function SignUpPage() {
  return (
    <main className="auth-shell clerk-auth-shell page-shell" aria-label="Sign up">
      <div className="clerk-auth-content">
        <div className="clerk-auth-controls"><ThemeControl /></div>
        <Suspense>
          <SignUp routing="path" path="/sign-up" />
        </Suspense>
      </div>
    </main>
  );
}
