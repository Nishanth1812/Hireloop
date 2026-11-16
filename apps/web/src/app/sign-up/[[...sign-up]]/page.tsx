import { SignUp } from "@clerk/nextjs";
import { Suspense } from "react";
import { ThemeControl } from "../../_components/ThemeControl";

export default function SignUpPage() {
  return (
    <main className="auth-shell page-shell" aria-label="Sign up">
      <ThemeControl />
      <Suspense>
        <SignUp routing="path" path="/sign-up" />
      </Suspense>
    </main>
  );
}
