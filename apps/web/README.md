# Hireloop web

The first web slice supports Google sign-in and the manual application workflow: list, add, update saved/applied status, and delete applications.

## Local development

Requires Node.js 22.6 or newer.

1. Copy `.env.example` to `.env.local` and set the Supabase project URL and publishable key.
2. Enable Google as an auth provider in Supabase and allow `http://localhost:3000` as a redirect URL.
3. Start the Go API from `apps/api` with the Supabase and database environment configured.
4. From this directory, run `npm install` and `npm run dev`.

`HIRELOOP_API_URL` defaults to `http://127.0.0.1:8080`. Next.js rewrites `/api/*` to that server so browser requests keep the app's origin. Google sign-in uses Supabase Auth, and the dashboard displays the signed-in user's email.

## Checks

```sh
npm test
npm run lint
npm run build
```
