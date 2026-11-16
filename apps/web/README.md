# Hireloop web

The first web slice supports Google sign-in and the manual application workflow: list, add, update saved/applied status, and delete applications.

## Local development

Requires Bun 1.4 or newer.

1. Copy `.env.example` to `.env.local` and set `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY`.
2. Enable Google as an OAuth provider in the Clerk Dashboard and allow `http://localhost:3000/sso-callback` as a redirect URL.
3. Start the Go API from `apps/api` with the Clerk and database environment configured.
4. From this directory, run `bun install` and `bun run dev`.

`HIRELOOP_API_URL` defaults to `http://127.0.0.1:8080`. Next.js rewrites `/api/*` to that server so browser requests keep the app's origin. Google sign-in uses Clerk OAuth, and the dashboard displays the signed-in user's email. The lockfile is `bun.lock` (`package-lock.json` was superseded).

## Checks

```sh
bun test
bun run lint
bun run build
```

`bun run build` requires `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` to be set in `.env.local` for prerendering.
