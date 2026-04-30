-- Hover seed data
-- Generated from production 2026-01-03
-- Contains minimal reference data for preview branches

SET session_replication_role = replica;
SET statement_timeout = 0;
SET lock_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET row_security = off;

-- =============================================================================
-- auth.users (Supabase managed - essential columns only)
-- =============================================================================
INSERT INTO auth.users (instance_id, id, aud, role, email, email_confirmed_at, raw_app_meta_data, raw_user_meta_data, created_at, updated_at)
VALUES
    ('00000000-0000-0000-0000-000000000000', '64d361fa-23fc-4deb-8a1b-3016a6c2e339', 'authenticated', 'authenticated', 'seed-admin@example.com', '2025-08-06 10:21:07.694503+00', '{"provider": "google", "providers": ["google"], "system_role": "system_admin"}', '{"iss": "https://accounts.google.com", "sub": "100000000000000000001", "name": "Seed Admin", "email": "seed-admin@example.com", "full_name": "Seed Admin", "provider_id": "100000000000000000001", "email_verified": true}', '2025-08-06 10:21:07.676382+00', '2025-08-06 10:21:07.676382+00'),
    ('00000000-0000-0000-0000-000000000000', 'd65db18a-47f5-4c13-bf12-8fa5a432ec5e', 'authenticated', 'authenticated', 'seed-member@example.com', '2026-02-14 00:00:00+00', '{"provider": "google", "providers": ["google"]}', '{"iss": "https://accounts.google.com", "sub": "100000000000000000002", "name": "Seed Member", "email": "seed-member@example.com", "full_name": "Seed Member", "provider_id": "100000000000000000002", "email_verified": true}', '2026-02-14 00:00:00+00', '2026-02-14 00:00:00+00')
ON CONFLICT (id) DO NOTHING;

-- Dev user: email/password auth for local development (/dev/auto-login endpoint).
-- GoTrue requires all token/change string columns to be '' not NULL.
INSERT INTO auth.users (
    instance_id, id, aud, role, email, email_confirmed_at,
    encrypted_password,
    confirmation_token, recovery_token,
    email_change, email_change_token_new, email_change_token_current,
    phone, phone_change, phone_change_token,
    raw_app_meta_data, raw_user_meta_data, created_at, updated_at
)
VALUES (
    '00000000-0000-0000-0000-000000000000',
    '00000000-0000-0000-0000-000000000002',
    'authenticated', 'authenticated',
    'dev@example.com', NOW(),
    crypt('devpassword', gen_salt('bf')),
    '', '', '', '', '', '', '', '',
    '{"provider": "email", "providers": ["email"], "system_role": "system_admin"}',
    '{"full_name": "Dev Admin", "email": "dev@example.com"}',
    NOW(), NOW()
)
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- auth.identities (email column is GENERATED, so omit it)
-- =============================================================================
INSERT INTO auth.identities (id, provider_id, user_id, identity_data, provider, last_sign_in_at, created_at, updated_at)
VALUES
    ('d3f737a6-359e-4375-9b94-67117c8dc963', '100000000000000000001', '64d361fa-23fc-4deb-8a1b-3016a6c2e339', '{"iss": "https://accounts.google.com", "sub": "100000000000000000001", "name": "Seed Admin", "email": "seed-admin@example.com", "full_name": "Seed Admin", "provider_id": "100000000000000000001", "email_verified": true}', 'google', '2025-08-06 10:21:07.690062+00', '2025-08-06 10:21:07.69012+00', '2025-08-06 10:21:07.69012+00'),
    ('f6b0435f-8e6d-4fab-9251-0f85e18ce601', '100000000000000000002', 'd65db18a-47f5-4c13-bf12-8fa5a432ec5e', '{"iss": "https://accounts.google.com", "sub": "100000000000000000002", "name": "Seed Member", "email": "seed-member@example.com", "full_name": "Seed Member", "provider_id": "100000000000000000002", "email_verified": true}', 'google', '2026-02-14 00:00:00+00', '2026-02-14 00:00:00+00', '2026-02-14 00:00:00+00'),
    ('00000000-0000-0000-0000-000000000002', 'dev@example.com', '00000000-0000-0000-0000-000000000002', '{"sub": "00000000-0000-0000-0000-000000000002", "email": "dev@example.com", "email_verified": true}', 'email', NOW(), NOW(), NOW())
ON CONFLICT (provider_id, provider) DO NOTHING;

-- =============================================================================
-- public.plans — Stripe sandbox price IDs (review/local dev only).
-- The migration sets live-mode price IDs for prod; this seed overrides them
-- with sandbox IDs so non-prod environments check out against sandbox products.
-- =============================================================================
UPDATE plans SET stripe_price_id = 'price_1TRJTFS2RiCh0hZBTaF9EjA9' WHERE name = 'starter';
UPDATE plans SET stripe_price_id = 'price_1TRImSS2RiCh0hZBRH8ZVXvD' WHERE name = 'plus';
UPDATE plans SET stripe_price_id = 'price_1TRImtS2RiCh0hZBXrKCkR4n' WHERE name = 'pro';
UPDATE plans SET stripe_price_id = 'price_1TRIm5S2RiCh0hZBxIILpcQn' WHERE name = 'ultra';
UPDATE plans SET stripe_price_id = 'price_1TRIlCS2RiCh0hZBSg0h1CkX' WHERE name = 'max';

-- =============================================================================
-- public.organisations
-- =============================================================================
INSERT INTO public.organisations (id, name, plan_id, created_at, updated_at)
VALUES
    ('96f7546c-47ea-41f8-a3a3-46b4deb84105', 'Personal Organisation', (SELECT id FROM plans WHERE name = 'ultra'), '2025-11-02 00:11:21.520651+00', '2025-11-02 00:11:21.520651+00'),
    ('2cfb393e-03e3-4acc-b19a-0958e6332060', 'Harvey', (SELECT id FROM plans WHERE name = 'ultra'), '2026-01-02 09:38:36.934168+00', '2026-01-02 09:38:36.934168+00'),
    ('da324afb-ce97-4814-975e-b6203cb51b0a', 'Merry People', (SELECT id FROM plans WHERE name = 'ultra'), '2026-01-02 09:38:43.358225+00', '2026-01-02 09:38:43.358225+00')
ON CONFLICT (id) DO UPDATE SET plan_id = EXCLUDED.plan_id;

-- =============================================================================
-- public.users
-- =============================================================================
INSERT INTO public.users (id, email, full_name, organisation_id, created_at, updated_at, active_organisation_id)
VALUES
    ('64d361fa-23fc-4deb-8a1b-3016a6c2e339', 'seed-admin@example.com', 'Seed Admin', '96f7546c-47ea-41f8-a3a3-46b4deb84105', '2025-11-02 00:11:21.520651+00', '2025-11-02 00:11:21.520651+00', '96f7546c-47ea-41f8-a3a3-46b4deb84105'),
    ('d65db18a-47f5-4c13-bf12-8fa5a432ec5e', 'seed-member@example.com', 'Seed Member', '96f7546c-47ea-41f8-a3a3-46b4deb84105', '2026-02-14 00:00:00+00', '2026-02-14 00:00:00+00', '96f7546c-47ea-41f8-a3a3-46b4deb84105'),
    ('00000000-0000-0000-0000-000000000002', 'dev@example.com', 'Dev Admin', '96f7546c-47ea-41f8-a3a3-46b4deb84105', NOW(), NOW(), '96f7546c-47ea-41f8-a3a3-46b4deb84105')
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- public.organisation_members
-- =============================================================================
INSERT INTO public.organisation_members (user_id, organisation_id, role, created_at)
VALUES
    ('64d361fa-23fc-4deb-8a1b-3016a6c2e339', '96f7546c-47ea-41f8-a3a3-46b4deb84105', 'admin', '2025-11-02 00:11:21.520651+00'),
    ('64d361fa-23fc-4deb-8a1b-3016a6c2e339', '2cfb393e-03e3-4acc-b19a-0958e6332060', 'admin', '2026-01-02 09:38:36.938885+00'),
    ('64d361fa-23fc-4deb-8a1b-3016a6c2e339', 'da324afb-ce97-4814-975e-b6203cb51b0a', 'member', '2026-01-02 09:38:43.362765+00'),
    ('d65db18a-47f5-4c13-bf12-8fa5a432ec5e', '96f7546c-47ea-41f8-a3a3-46b4deb84105', 'member', '2026-02-14 00:00:00+00'),
    ('d65db18a-47f5-4c13-bf12-8fa5a432ec5e', '2cfb393e-03e3-4acc-b19a-0958e6332060', 'member', '2026-02-14 00:00:00+00'),
    ('00000000-0000-0000-0000-000000000002', '96f7546c-47ea-41f8-a3a3-46b4deb84105', 'admin', NOW()),
    ('00000000-0000-0000-0000-000000000002', '2cfb393e-03e3-4acc-b19a-0958e6332060', 'admin', NOW())
ON CONFLICT (user_id, organisation_id) DO NOTHING;

-- =============================================================================
-- public.domains
-- =============================================================================
INSERT INTO public.domains (id, name, crawl_delay_seconds, adaptive_delay_seconds, adaptive_delay_floor_seconds, created_at)
VALUES
    (1, 'teamharvey.co', NULL, 0, 0, '2025-12-28 10:49:41.041287+00'),
    (5, 'cpsn.org.au', NULL, 0, 0, '2025-12-28 10:58:38.844544+00'),
    (11, 'envirotecture.com.au', NULL, 0, 0, '2026-01-02 09:39:15.684451+00')
ON CONFLICT (id) DO NOTHING;

-- Reset domain sequence
SELECT setval('domains_id_seq', (SELECT COALESCE(MAX(id), 1) FROM domains));

-- =============================================================================
-- public.schedulers
-- =============================================================================
INSERT INTO public.schedulers (id, domain_id, organisation_id, schedule_interval_hours, next_run_at, is_enabled, concurrency, find_links, max_pages, include_paths, exclude_paths, required_workers, created_at, updated_at)
VALUES
    ('14dd9d7a-2696-4479-831c-e43163795e36', 1, '96f7546c-47ea-41f8-a3a3-46b4deb84105', 12, NOW() + INTERVAL '12 hours', true, 20, true, 0, NULL, NULL, 1, NOW(), NOW()),
    ('4db618ce-5b05-409f-8a06-fdf4a4a9745c', 5, '96f7546c-47ea-41f8-a3a3-46b4deb84105', 12, NOW() + INTERVAL '12 hours', true, 20, true, 0, NULL, NULL, 1, NOW(), NOW())
ON CONFLICT (id) DO NOTHING;

-- Re-enable triggers
SET session_replication_role = DEFAULT;
