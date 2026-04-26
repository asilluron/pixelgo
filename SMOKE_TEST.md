# pixelgo manual smoke test

End-to-end sanity check against a real Supabase project + Redis. Run after
any change to the auth, signup, or invite code paths.

## Prerequisites

- `.env` populated with real `SUPABASE_*` and `PIXELGO_REDIS_URL` values.
- Redis reachable at `PIXELGO_REDIS_URL` (`docker compose up -d redis` works).
- Supabase project has `mailer_autoconfirm = true` (so signup returns a
  session without requiring a real email click).
- Postgres tables `orgs`, `org_members`, `invites`, `profiles` exist.

Start the server:

```bash
go run ./cmd/pixelgo
```

## 1. Bootstrap super-admin (first run only)

- [ ] On first boot, the log prints `bootstrap: created super-admin <email>`
  using `PIXELGO_BOOTSTRAP_EMAIL` / `PIXELGO_BOOTSTRAP_PASSWORD`.
- [ ] `select is_super_admin from public.profiles where user_id = '<uid>';`
  returns `t` for that user.
- [ ] Subsequent boots log nothing about bootstrap.

## 2. Owner signup (no invite)

- [ ] `GET /` redirects to `/signup`.
- [ ] Submit step 1 with a fresh email + password → redirected to `/signup/org`.
- [ ] `select * from auth.users where email = 'X'` shows the new user.
- [ ] `select * from public.profiles where user_id = '<uid>'` has
  `is_super_admin = f`.
- [ ] Submit step 2 with an org name → redirected to `/admin`.
- [ ] `select * from public.orgs where name = 'X'` shows the new org.
- [ ] `select role from public.org_members where user_id = '<uid>'` returns `owner`.
- [ ] Dashboard renders with the role badge "owner" and the invite form.

## 3. Pixel create + serve

- [ ] From `/admin`, create a pixel named "Smoke Test".
- [ ] `select * from pixels` — wait, that's Redis: `redis-cli KEYS 'pixel:*'`
  shows the new pixel key and an entry in the `pixels` set.
- [ ] `curl -I http://localhost:8080/p/<id>` returns 200 + `image/gif` +
  `Cache-Control: no-store`.
- [ ] Dashboard count increments within ~1s.

## 4. Invite (editor)

- [ ] As the owner, fill the invite form with a second email + role "editor"
  → dashboard re-renders with an invite link under "Outstanding invites".
- [ ] `select token, role, expires_at from public.invites where email = '<X>'`
  shows the row; `accepted_at` is NULL.
- [ ] Copy the invite URL. Open it in an incognito window.
- [ ] Landing page redirects to `/signup?invite=<token>`; the form shows
  "You're joining <Org> as editor" and pre-fills the email.
- [ ] Complete signup → redirected directly to `/admin` (no step 2).
- [ ] `select role from org_members where user_id = '<invitee-uid>'` = `editor`.
- [ ] `select accepted_at from invites where token = '<T>'` is non-null.
- [ ] Editor dashboard hides the invite form, shows the create-pixel form.

## 5. Invite (viewer)

- [ ] Repeat step 4 with role "viewer".
- [ ] Viewer dashboard hides both the invite form and the create-pixel form.
- [ ] `POST /admin/pixels` returns 403 for the viewer (try via curl with the
  session cookie).

## 6. Invite edge cases

- [ ] Open an already-accepted invite URL → "Invite unavailable" page.
- [ ] Manually set `expires_at = now() - interval '1 hour'` and re-open the
  URL → "Invite unavailable" page.
- [ ] Logged-in user (no org) opening a valid invite URL → redirected to
  `/admin` with membership applied immediately (no re-signup).

## 7. Login / logout / refresh

- [ ] Log out → redirected to `/login`; cookies cleared.
- [ ] Log back in with wrong password → `/login?error=invalid`.
- [ ] Log in with good password → `/admin` renders.
- [ ] Delete only the `pixelgo_access` cookie (keep refresh). Reload
  `/admin` → dashboard still renders; access cookie is re-issued.
- [ ] Delete both cookies → `/admin` redirects to `/login`.

## 8. Super-admin cross-org

- [ ] Log in as the bootstrap super-admin.
- [ ] Dashboard shows all orgs' pixels and an org selector on the create-
  pixel + invite forms.
- [ ] Create a pixel while selecting another org → row lands under that org.

## Cleanup

```sql
-- delete test orgs (cascades org_members + invites)
delete from public.orgs where name like 'pixelgo-it-%' or name = 'Smoke Test';
-- optional: delete test users
-- via Supabase dashboard → Authentication → Users → delete row(s).
```

## Automated alternative

The `//go:build integration` test at
`internal/server/integration_test.go` covers steps 2, 4, and the membership
side-effects automatically:

```bash
go test -tags=integration -run=TestSignupInviteAcceptFlow ./internal/server/...
```

It skips if `.env` is missing and fails loudly if Supabase/Postgres is
unreachable.
