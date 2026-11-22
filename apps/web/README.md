# Hireloop web

Next.js front end for [Hireloop](../../README.md): Clerk Google sign-in, the manual application tracker (list, add, edit, search/filter, paginate and delete), per-application stage history and linked tasks, and a Today panel for open tasks with explicit deadline precision.

## Local development

Requires Bun 1.4 or newer and a running Hireloop API (see the root README).

1. From this directory, copy `.env.example` to `.env.local` and set `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY`.
2. In the Clerk Dashboard, enable Google as an OAuth provider and allow `http://localhost:3000/sso-callback` as a redirect URL.
3. Configure `CLERK_ISSUER`, `CLERK_JWKS_URL` and, for persistence, `DATABASE_URL` in the root `.env`, then start the API with `bun run start` from `apps/api`.
4. From this directory, run `bun install` and `bun run dev`.

`HIRELOOP_API_URL` defaults to `http://127.0.0.1:8080`. Next.js rewrites `/api/*` to that server so browser requests keep the app's origin; set the same address on the API with `HIRELOOP_API_ADDR` if you change the port. The publishable key is a public client value — never put a Clerk secret key, service-role key or database password in `.env.local` or source control. The lockfile is `bun.lock` (`package-lock.json` was superseded).

## Checks

From this directory:

```sh
bun run test
bun run lint
bun run build
```

`bun run build` requires `NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY` in `.env.local` for prerendering.
