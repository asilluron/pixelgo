-- api_keys: bearer credentials for the customer-facing JSON API.
--
-- Two flavours:
--   type='personal' → user_id is set, org_id is null. Authorizes whatever
--                      org the user currently belongs to.
--   type='org'      → org_id is set, user_id is null. Authorizes that org
--                      directly (independent of any user's membership).
--
-- `hash` stores the bcrypt hash of the full plaintext token. `prefix` stores
-- the first N chars of the plaintext (non-secret) so lookups can be narrowed
-- without scanning every row.

create type public.api_key_type as enum ('personal', 'org');

create table public.api_keys (
    id          uuid primary key,
    type        public.api_key_type not null,
    name        text not null,
    prefix      text not null,
    hash        text not null,
    user_id     uuid references auth.users (id) on delete cascade,
    org_id      uuid references public.orgs (id) on delete cascade,
    created_by  uuid references auth.users (id) on delete set null,
    created_at  timestamptz not null default now(),
    last_used_at timestamptz,
    revoked_at  timestamptz,

    -- Exactly one of user_id / org_id must be set, matching `type`.
    constraint api_keys_owner_matches_type check (
        (type = 'personal' and user_id is not null and org_id is null)
     or (type = 'org'      and org_id  is not null and user_id is null)
    )
);

-- Prefix lookups on the hot auth path; partial index keeps revoked keys out.
create index api_keys_prefix_active_idx
    on public.api_keys (prefix)
    where revoked_at is null;

create index api_keys_user_id_idx on public.api_keys (user_id);
create index api_keys_org_id_idx  on public.api_keys (org_id);
