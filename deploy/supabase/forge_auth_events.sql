-- forge auth events for Supabase: sign-ups and sign-ins, reported by the
-- auth database itself to the app's /_forge/auth/hooks/supabase endpoint.
-- Shipped by forge (github.com/reliant-labs/forge, pkg/authhooks); print the
-- copy matching your forge with `forge auth hooks sql supabase`.
--
-- Safe by construction:
--   * Asynchronous. The triggers queue an HTTP request through pg_net, which
--     sends it after the transaction commits. Nothing waits on the app.
--   * Never blocks or fails auth. Every function swallows its own errors, and
--     a missing secret, a down endpoint or a bug records nothing instead.
--   * Minimal. The payload is the user's opaque id, the time, the sign-in
--     provider's name ("email", "google", ...), whether the new account is
--     verified and has a password, and a dedupe key. Never the email, phone,
--     IP address or user agent.
--   * Idempotent. Run it again to upgrade; it replaces what it created.
--
-- Setup, once per Supabase project (Dashboard > SQL editor, as postgres):
--   1. Store the endpoint and the hook secret in Vault, outside migrations:
--        select vault.create_secret('https://api.example.com/_forge/auth/hooks/supabase', 'forge_auth_hook_url');
--        select vault.create_secret('<the secret the app passes as authhooks.Options.Secret>', 'forge_auth_hook_secret');
--      (To rotate: vault.update_secret(id, new_secret) with the id from
--      vault.secrets where name = 'forge_auth_hook_secret'.)
--   2. Run this file.
--   3. Sign up a test user, then check the request went out:
--        select status_code, error_msg, created from net._http_response order by created desc limit 5;
--
-- Events:
--   signed_up  a real (non-anonymous) account appears, or an anonymous one is
--              upgraded (is_anonymous flips to false). dedupe = the user id.
--   signed_in  a new auth session: a credential sign-in (refreshing a token
--              does not create a session). Anonymous users are skipped.
--              dedupe = the session id.
--   Sign-outs are not reported: deleting a session is also expiry and
--   revocation, so it does not say the person signed out.
--
-- Uninstall (stops every event at once):
--   drop trigger if exists forge_auth_user on auth.users;
--   drop trigger if exists forge_auth_session on auth.sessions;
--   drop schema if exists forge_auth cascade;

create extension if not exists pg_net;

create schema if not exists forge_auth;
revoke all on schema forge_auth from public;

-- emit posts one event to the endpoint in Vault, authenticated by the hook
-- secret. With either secret missing it does nothing.
create or replace function forge_auth.emit(ev jsonb) returns void
language plpgsql security definer set search_path = '' as $$
declare
  hook_url text;
  hook_secret text;
begin
  select decrypted_secret into hook_url from vault.decrypted_secrets where name = 'forge_auth_hook_url';
  select decrypted_secret into hook_secret from vault.decrypted_secrets where name = 'forge_auth_hook_secret';
  if coalesce(hook_url, '') = '' or coalesce(hook_secret, '') = '' then
    return;
  end if;
  perform net.http_post(
    url := hook_url,
    body := ev,
    headers := jsonb_build_object('content-type', 'application/json', 'authorization', 'Bearer ' || hook_secret),
    timeout_milliseconds := 5000
  );
exception when others then
  return; -- analytics must never block or fail auth
end $$;

-- sign_in_provider is the first non-anonymous provider the account signs in
-- with ("email", "phone", "google", "github", "sso:<id>", ...).
create or replace function forge_auth.sign_in_provider(app_meta jsonb) returns text
language sql immutable set search_path = '' as $$
  select coalesce(
    (select p from jsonb_array_elements_text(coalesce(app_meta -> 'providers', '[]'::jsonb)) as p
      where p <> 'anonymous' limit 1),
    nullif(app_meta ->> 'provider', 'anonymous'),
    ''
  )
$$;

-- on_user: signed_up when a real account appears or an anonymous one is
-- upgraded.
create or replace function forge_auth.on_user() returns trigger
language plpgsql security definer set search_path = '' as $$
begin
  if not coalesce(new.is_anonymous, false)
     and (tg_op = 'INSERT' or coalesce(old.is_anonymous, false)) then
    perform forge_auth.emit(jsonb_build_object(
      'v', 1,
      'event', 'signed_up',
      'user_id', new.id,
      'at', now(),
      'provider', forge_auth.sign_in_provider(new.raw_app_meta_data),
      'password', coalesce(new.encrypted_password, '') <> '',
      'verified', new.email_confirmed_at is not null,
      'dedupe', new.id
    ));
  end if;
  return new;
exception when others then
  return new; -- analytics must never block or fail auth
end $$;

-- on_session: signed_in for each new session of a non-anonymous user.
create or replace function forge_auth.on_session() returns trigger
language plpgsql security definer set search_path = '' as $$
declare
  u record;
begin
  select is_anonymous, raw_app_meta_data into u from auth.users where id = new.user_id;
  if found and not coalesce(u.is_anonymous, false) then
    perform forge_auth.emit(jsonb_build_object(
      'v', 1,
      'event', 'signed_in',
      'user_id', new.user_id,
      'at', now(),
      'provider', forge_auth.sign_in_provider(u.raw_app_meta_data),
      'dedupe', new.id
    ));
  end if;
  return new;
exception when others then
  return new; -- analytics must never block or fail auth
end $$;

revoke all on function forge_auth.emit(jsonb) from public;
revoke all on function forge_auth.sign_in_provider(jsonb) from public;
revoke all on function forge_auth.on_user() from public;
revoke all on function forge_auth.on_session() from public;

create or replace trigger forge_auth_user
  after insert or update of is_anonymous on auth.users
  for each row execute function forge_auth.on_user();

create or replace trigger forge_auth_session
  after insert on auth.sessions
  for each row execute function forge_auth.on_session();
