-- org_profile: optional display + billing metadata for public.orgs.
--
-- The baseline orgs table (name + created_at) is enough for pixelgo to
-- function — orgs, memberships, invites, pixels, and API keys all work
-- without knowing anything else about an org. This migration adds the
-- columns a real tenant needs once they grow past the free tier: a logo,
-- a slug for nicer URLs, and the billing address that gets printed on
-- invoices.
--
-- Every field here is nullable on purpose. Nothing except `name` is
-- required for pixelgo to operate; the settings UI can fill these in
-- over time.

alter table public.orgs
    add column if not exists slug                 text,
    add column if not exists logo_url             text,
    add column if not exists website              text,
    add column if not exists description          text,
    add column if not exists billing_email        text,
    add column if not exists billing_name         text,
    add column if not exists billing_address_line1 text,
    add column if not exists billing_address_line2 text,
    add column if not exists billing_city         text,
    add column if not exists billing_region       text,
    add column if not exists billing_postal_code  text,
    add column if not exists billing_country      text,
    add column if not exists tax_id               text,
    add column if not exists updated_at           timestamptz not null default now();

-- Slugs are optional, but when present must be unique so they can be used
-- as URL handles. A partial unique index lets us keep multiple NULLs
-- (orgs that haven't picked one yet) without collisions.
create unique index if not exists orgs_slug_unique_idx
    on public.orgs (slug)
    where slug is not null;

-- ISO-3166-1 alpha-2 ("US", "GB", ...) — kept as a soft check so that
-- legacy rows with NULL stay valid and the UI can enforce casing.
alter table public.orgs
    drop constraint if exists orgs_billing_country_iso2;
alter table public.orgs
    add  constraint orgs_billing_country_iso2
    check (billing_country is null or char_length(billing_country) = 2);
