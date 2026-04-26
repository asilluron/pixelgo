-- pixelgo baseline schema.
--
-- This file is a reference for bootstrapping a FRESH Supabase project; it is
-- NOT applied automatically by the Supabase CLI (it lives outside
-- supabase/migrations/ on purpose).
--
-- If you're setting up a new project:
--
--   1. Create the Supabase project and link it: `supabase link --project-ref <ref>`
--   2. Paste this file into the SQL editor, or run:
--        psql "$SUPABASE_DB_URL" -f supabase/schema.sql
--   3. `supabase db push` to apply incremental migrations in supabase/migrations/.
--
-- `create ... if not exists` is used throughout so re-running is safe.

-- ---------- Enums ----------

do $$
begin
  if not exists (select 1 from pg_type where typname = 'role') then
    create type public.role as enum ('owner', 'editor', 'viewer');
  end if;
end$$;

-- ---------- Orgs ----------
--
-- Only `id`, `name`, and `created_at` are required for pixelgo to function;
-- everything else is optional tenant metadata (logo, slug, billing address,
-- etc.) that the settings UI fills in over time.

create table if not exists public.orgs (
    id                    uuid primary key,
    name                  text not null,
    slug                  text,
    logo_url              text,
    website               text,
    description           text,
    billing_email         text,
    billing_name          text,
    billing_address_line1 text,
    billing_address_line2 text,
    billing_city          text,
    billing_region        text,
    billing_postal_code   text,
    billing_country       text,
    tax_id                text,
    created_at            timestamptz not null default now(),
    updated_at            timestamptz not null default now(),
    constraint orgs_billing_country_iso2
        check (billing_country is null or char_length(billing_country) = 2)
);

create unique index if not exists orgs_slug_unique_idx
    on public.orgs (slug)
    where slug is not null;

-- ---------- Memberships ----------

create table if not exists public.org_members (
    user_id    uuid not null references auth.users (id) on delete cascade,
    org_id     uuid not null references public.orgs (id) on delete cascade,
    role       public.role not null,
    created_at timestamptz not null default now(),
    primary key (user_id, org_id)
);

create index if not exists org_members_org_idx on public.org_members (org_id);

-- ---------- Profiles (super-admin flag) ----------

create table if not exists public.profiles (
    user_id        uuid primary key references auth.users (id) on delete cascade,
    is_super_admin boolean not null default false,
    created_at     timestamptz not null default now()
);

-- ---------- Invites ----------

create table if not exists public.invites (
    id          uuid primary key,
    org_id      uuid not null references public.orgs (id) on delete cascade,
    email       text not null,
    role        public.role not null,
    token       text not null unique,
    created_by  uuid references auth.users (id) on delete set null,
    created_at  timestamptz not null default now(),
    expires_at  timestamptz not null default (now() + interval '14 days'),
    accepted_at timestamptz
);

create index if not exists invites_org_idx on public.invites (org_id);
create index if not exists invites_token_idx on public.invites (token);
