import type { Metadata } from "next";
import type { ReactNode } from "react";
import { ClerkProvider } from "@clerk/nextjs";
import "./globals.css";

export const metadata: Metadata = {
  title: "Hireloop — Your job search, in one place",
  description: "Keep track of your job applications and next steps.",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <ClerkProvider signInUrl="/sign-in" signUpUrl="/sign-up" appearance={{ variables: {
      colorBackground: "var(--surface)",
      colorInput: "var(--surface)",
      colorForeground: "var(--ink)",
      colorInputForeground: "var(--ink)",
      colorMutedForeground: "var(--muted)",
      colorNeutral: "var(--ink)",
      colorPrimary: "var(--ink)",
      colorPrimaryForeground: "var(--background)",
      colorDanger: "#b8493a",
      colorSuccess: "#347151",
    } }}>
          {children}
        </ClerkProvider>
      </body>
    </html>
  );
}
