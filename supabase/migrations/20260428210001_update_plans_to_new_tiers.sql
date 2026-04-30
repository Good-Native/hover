-- Migrate plans table to new tier structure.
--
-- Old tiers (now replaced):
--   free (500/day, $0), starter (2000/day, $50), pro (5000/day, $80),
--   business (10000/day, $150), enterprise (100000/day, $400)
--
-- New tiers:
--   free (500/day, $0), starter (200/day, $19), plus (1000/day, $49),
--   pro (10000/day, $149), ultra (100000/day, $399), max (500000/day, $849)
--
-- No paying customers existed before Stripe integration, so updating
-- existing plan values directly is safe.

-- Update 'starter': new page limit and price.
UPDATE plans
SET display_name        = 'Starter',
    daily_page_limit    = 200,
    monthly_price_cents = 1900,
    sort_order          = 10,
    stripe_price_id     = 'price_1TJAjJS2RiCh0hZBfgrnoI0C',
    updated_at          = NOW()
WHERE name = 'starter';

-- Update 'pro': new page limit and price.
UPDATE plans
SET display_name        = 'Pro',
    daily_page_limit    = 10000,
    monthly_price_cents = 14900,
    sort_order          = 30,
    stripe_price_id     = 'price_1TJAm4S2RiCh0hZBK8u1HbqG',
    updated_at          = NOW()
WHERE name = 'pro';

-- Deactivate old tiers replaced by new ones.
UPDATE plans
SET is_active  = false,
    updated_at = NOW()
WHERE name IN ('business', 'enterprise');

-- Insert new 'plus' tier.
INSERT INTO plans (name, display_name, daily_page_limit, monthly_price_cents, sort_order, stripe_price_id)
VALUES ('plus', 'Plus', 1000, 4900, 20, 'price_1TJAwBS2RiCh0hZBeS17btD7')
ON CONFLICT (name) DO UPDATE
    SET display_name        = EXCLUDED.display_name,
        daily_page_limit    = EXCLUDED.daily_page_limit,
        monthly_price_cents = EXCLUDED.monthly_price_cents,
        sort_order          = EXCLUDED.sort_order,
        stripe_price_id     = EXCLUDED.stripe_price_id,
        is_active           = true,
        updated_at          = NOW();

-- Insert new 'ultra' tier.
INSERT INTO plans (name, display_name, daily_page_limit, monthly_price_cents, sort_order, stripe_price_id)
VALUES ('ultra', 'Ultra', 100000, 39900, 40, 'price_1TJAx1S2RiCh0hZBRLcnk0zD')
ON CONFLICT (name) DO UPDATE
    SET display_name        = EXCLUDED.display_name,
        daily_page_limit    = EXCLUDED.daily_page_limit,
        monthly_price_cents = EXCLUDED.monthly_price_cents,
        sort_order          = EXCLUDED.sort_order,
        stripe_price_id     = EXCLUDED.stripe_price_id,
        is_active           = true,
        updated_at          = NOW();

-- Insert new 'max' tier.
INSERT INTO plans (name, display_name, daily_page_limit, monthly_price_cents, sort_order, stripe_price_id)
VALUES ('max', 'Max', 500000, 84900, 50, 'price_1TJB0IS2RiCh0hZBwfOB0aoE')
ON CONFLICT (name) DO UPDATE
    SET display_name        = EXCLUDED.display_name,
        daily_page_limit    = EXCLUDED.daily_page_limit,
        monthly_price_cents = EXCLUDED.monthly_price_cents,
        sort_order          = EXCLUDED.sort_order,
        stripe_price_id     = EXCLUDED.stripe_price_id,
        is_active           = true,
        updated_at          = NOW();

-- Move any orgs on deactivated plans to their nearest replacement.
-- (No paying customers yet, but handles dev/test data cleanly.)
UPDATE organisations
SET plan_id    = (SELECT id FROM plans WHERE name = 'pro'),
    updated_at = NOW()
WHERE plan_id = (SELECT id FROM plans WHERE name = 'business');

UPDATE organisations
SET plan_id    = (SELECT id FROM plans WHERE name = 'ultra'),
    updated_at = NOW()
WHERE plan_id = (SELECT id FROM plans WHERE name = 'enterprise');
