-- Add Stripe Price ID mapping to plans table.
-- Populated manually after creating Prices in Stripe Dashboard.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS stripe_price_id TEXT;

-- Add Stripe customer and subscription tracking to organisations.
ALTER TABLE organisations
  ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT,
  ADD COLUMN IF NOT EXISTS stripe_subscription_id TEXT;

-- Fast webhook lookup by Stripe customer ID.
CREATE INDEX IF NOT EXISTS idx_organisations_stripe_customer_id
  ON organisations(stripe_customer_id)
  WHERE stripe_customer_id IS NOT NULL;
