package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Harvey-AU/hover/internal/logging"
	stripe "github.com/stripe/stripe-go/v82"
	portalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
	stripesubscription "github.com/stripe/stripe-go/v82/subscription"
)

var billingLog = logging.Component("billing")

// BillingCheckout starts a paid-plan transition for the active organisation.
// POST /v1/billing/checkout
// Body: {"plan_id": "<uuid>"}
//
// Behaviour depends on whether the org already has an active Stripe
// subscription:
//   - No subscription → creates a Stripe Checkout Session and returns its URL.
//     The caller redirects the browser to checkout.stripe.com to collect
//     payment; on completion Stripe fires checkout.session.completed which
//     activates the plan via stripe_webhook.go.
//   - Existing subscription → updates the subscription's line item in place
//     via the Stripe Subscriptions API (Stripe handles proration). No browser
//     redirect to Stripe is needed; the customer.subscription.updated webhook
//     reconciles plan_id in the DB. Returns a same-origin success URL so the
//     frontend can render its standard ?billing=success toast.
func (h *Handler) BillingCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeSecretKey == "" {
		billingLog.ErrorContext(r.Context(), "Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to look up organisation member role", "error", err, "org_id", orgID, "user_id", user.ID)
		InternalError(w, r, fmt.Errorf("failed to verify membership"))
		return
	}
	if role != "admin" {
		Forbidden(w, r, "Only organisation admins can manage billing")
		return
	}

	var body struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PlanID == "" {
		BadRequest(w, r, "plan_id is required")
		return
	}

	// Fetch all plans to find the requested one with its Stripe Price ID.
	plans, err := h.DB.GetActivePlans(r.Context())
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch plans: %w", err))
		return
	}
	var stripePriceID string
	for _, p := range plans {
		if p.ID == body.PlanID {
			stripePriceID = p.StripePriceID
			break
		}
	}
	if stripePriceID == "" {
		BadRequest(w, r, "Plan not found or not available for purchase")
		return
	}

	// Branch on existing subscription state.
	existingSubID, err := h.DB.GetStripeSubscriptionID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to check existing subscription: %w", err))
		return
	}

	// Get customer ID up-front. Used both for the defensive Stripe-side check
	// below and for the new-Checkout flow further down.
	customerID, err := h.DB.GetStripeCustomerID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch stripe customer: %w", err))
		return
	}

	// Defensive: if our DB has no subscription but Stripe has an active one for
	// this customer, adopt it. Catches drift caused by missed webhooks (the
	// Apr 28 zombie scenario where signature verification failed and DB never
	// learned about the new sub) — without this, a second checkout click would
	// create a duplicate live subscription.
	if existingSubID == "" && customerID != "" {
		listParams := &stripe.SubscriptionListParams{
			Customer: stripe.String(customerID),
			Status:   stripe.String("active"),
		}
		listParams.Context = r.Context()
		iter := stripesubscription.List(listParams)
		// Adopt the first active subscription we find. If there are multiple,
		// the others are leftover zombies — Stripe Customer Portal can clean
		// those up. We only need one to take the in-place update path.
		if iter.Next() {
			adopted := iter.Subscription()
			// Recovery path — expected when our DB has fallen behind Stripe.
			// NoCapture so we don't page Sentry every time a webhook outage
			// is being healed.
			billingLog.WarnContext(logging.NoCapture(r.Context()), "Adopting orphan Stripe subscription not tracked in DB", "org_id", orgID, "subscription_id", adopted.ID)
			if err := h.DB.SetStripeSubscriptionID(r.Context(), orgID, adopted.ID); err != nil {
				billingLog.ErrorContext(r.Context(), "Failed to adopt orphan Stripe subscription", "error", err, "org_id", orgID)
			} else {
				existingSubID = adopted.ID
			}
		}
		if err := iter.Err(); err != nil {
			billingLog.WarnContext(r.Context(), "Failed to list Stripe subscriptions for orphan check", "error", err, "customer_id", customerID)
		}
	}

	baseURL := h.absoluteBaseURL(r)
	settingsURL := baseURL + "/settings/plans"

	if existingSubID != "" {
		// Paid → different paid: update the existing subscription in place.
		// Avoids duplicate live subscriptions and gives Stripe-managed proration.
		fetchParams := &stripe.SubscriptionParams{}
		fetchParams.Context = r.Context()
		sub, err := stripesubscription.Get(existingSubID, fetchParams)
		if err != nil {
			billingLog.ErrorContext(r.Context(), "Failed to fetch existing Stripe subscription", "error", err, "subscription_id", existingSubID)
			InternalError(w, r, fmt.Errorf("failed to fetch subscription"))
			return
		}
		if sub.Items == nil || len(sub.Items.Data) == 0 {
			billingLog.ErrorContext(r.Context(), "Existing subscription has no line items", "subscription_id", existingSubID)
			InternalError(w, r, fmt.Errorf("subscription has no items"))
			return
		}
		// If they're already on this price, no-op back to the success page.
		if sub.Items.Data[0].Price != nil && sub.Items.Data[0].Price.ID == stripePriceID {
			WriteSuccess(w, r, map[string]string{"url": settingsURL + "?billing=success"}, "Already on this plan")
			return
		}
		// Stripe defaults proration_behavior to "create_prorations" — credits
		// for unused time on the old plan offset the new plan, applied to the
		// next invoice. Switch to "always_invoice" if we ever need immediate
		// billing on upgrade.
		//
		// Also clear cancel_at_period_end: an admin who scheduled cancellation
		// and then picks a different tier is implicitly re-affirming the
		// subscription, so we shouldn't keep them queued for cancellation.
		updateParams := &stripe.SubscriptionParams{
			Items: []*stripe.SubscriptionItemsParams{
				{
					ID:    stripe.String(sub.Items.Data[0].ID),
					Price: stripe.String(stripePriceID),
				},
			},
			CancelAtPeriodEnd: stripe.Bool(false),
		}
		updateParams.Context = r.Context()
		_, err = stripesubscription.Update(existingSubID, updateParams)
		if err != nil {
			billingLog.ErrorContext(r.Context(), "Failed to update Stripe subscription", "error", err, "org_id", orgID, "subscription_id", existingSubID)
			InternalError(w, r, fmt.Errorf("failed to update subscription"))
			return
		}
		// Synchronously update plan_id so the immediate redirect-back to
		// /settings/plans renders the new plan without waiting for the
		// customer.subscription.updated webhook to land. The webhook handler
		// runs idempotently when it arrives.
		if err := h.DB.SetOrganisationPlan(r.Context(), orgID, body.PlanID); err != nil {
			// Don't fail the request — Stripe is already updated, the webhook
			// will correct the DB shortly. Just log so the lag is visible.
			billingLog.ErrorContext(r.Context(), "Failed to sync plan_id locally after subscription update — relying on webhook reconciliation", "error", err, "org_id", orgID, "plan_id", body.PlanID)
		}
		billingLog.InfoContext(r.Context(), "Updated Stripe subscription to new plan", "org_id", orgID, "subscription_id", existingSubID, "price_id", stripePriceID)
		WriteSuccess(w, r, map[string]string{"url": settingsURL + "?billing=success"}, "Plan updated")
		return
	}

	// No existing subscription — first-time Checkout flow.
	if customerID == "" {
		org, err := h.DB.GetOrganisation(orgID)
		if err != nil {
			InternalError(w, r, fmt.Errorf("failed to fetch organisation: %w", err))
			return
		}
		custParams := &stripe.CustomerParams{
			Email: stripe.String(user.Email),
			Name:  stripe.String(org.Name),
			Metadata: map[string]string{
				"organisation_id": orgID,
			},
		}
		custParams.Context = r.Context()
		cust, err := customer.New(custParams)
		if err != nil {
			billingLog.ErrorContext(r.Context(), "Failed to create Stripe customer", "error", err, "org_id", orgID)
			InternalError(w, r, fmt.Errorf("failed to create billing customer"))
			return
		}
		customerID = cust.ID
		billingLog.InfoContext(r.Context(), "Created Stripe customer", "customer_id", customerID, "org_id", orgID)
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, customerID); err != nil {
			billingLog.ErrorContext(r.Context(), "Failed to store Stripe customer ID", "error", err, "org_id", orgID)
			InternalError(w, r, fmt.Errorf("failed to store billing customer"))
			return
		}
	}

	sessParams := &stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(stripePriceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL:        stripe.String(settingsURL + "?billing=success"),
		CancelURL:         stripe.String(settingsURL + "?billing=cancelled"),
		ClientReferenceID: stripe.String(orgID),
	}
	// Idempotency key — Stripe returns the original session for repeated
	// requests with the same key (24h window). Protects against double-clicks,
	// network retries, and proxy retries handing the same org multiple
	// checkout URLs that could complete to duplicate subscriptions. The key
	// is org+price-scoped so legitimate switches to different prices still
	// create fresh sessions.
	sessParams.SetIdempotencyKey(fmt.Sprintf("checkout:%s:%s", orgID, stripePriceID))
	sessParams.Context = r.Context()
	sess, err := checkoutsession.New(sessParams)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to create Stripe Checkout Session", "error", err, "org_id", orgID)
		InternalError(w, r, fmt.Errorf("failed to create checkout session"))
		return
	}

	WriteSuccess(w, r, map[string]string{"url": sess.URL}, "Checkout session created")
}

// BillingPortal creates a Stripe Billing Portal session for managing subscriptions.
// POST /v1/billing/portal
// Returns: {"url": "https://billing.stripe.com/..."}
func (h *Handler) BillingPortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeSecretKey == "" {
		billingLog.ErrorContext(r.Context(), "Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to look up organisation member role", "error", err, "org_id", orgID, "user_id", user.ID)
		InternalError(w, r, fmt.Errorf("failed to verify membership"))
		return
	}
	if role != "admin" {
		Forbidden(w, r, "Only organisation admins can manage billing")
		return
	}

	customerID, err := h.DB.GetStripeCustomerID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch billing info: %w", err))
		return
	}
	if customerID == "" {
		BadRequest(w, r, "No billing account found — upgrade to a paid plan first")
		return
	}

	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(h.absoluteBaseURL(r) + "/settings/plans"),
	}
	params.Context = r.Context()
	// Optional override — when unset, Stripe falls back to the account's
	// default Customer Portal configuration.
	if h.StripePortalConfigID != "" {
		params.Configuration = stripe.String(h.StripePortalConfigID)
	}
	sess, err := portalsession.New(params)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to create Stripe Portal Session", "error", err, "org_id", orgID)
		InternalError(w, r, fmt.Errorf("failed to create portal session"))
		return
	}

	WriteSuccess(w, r, map[string]string{"url": sess.URL}, "Portal session created")
}

// BillingCancel schedules cancellation of the org's Stripe subscription at the
// end of the current billing period. POST /v1/billing/cancel. Admin-only.
//
// We use cancel_at_period_end semantics rather than immediate cancellation:
// the customer keeps paid features through the period they have already paid
// for, and Stripe transitions the subscription to "canceled" at the period
// end — at which point the customer.subscription.deleted webhook fires and
// our handler downgrades the org to free.
//
// The DB is intentionally NOT updated synchronously here — the org should
// remain on its current paid plan until the period actually ends. If the
// admin changes their mind during this window, BillingCheckout's update path
// resets cancel_at_period_end back to false.
//
// Returns the period_end unix timestamp so the frontend can render
// "Your plan stays active until <date>".
func (h *Handler) BillingCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, r)
		return
	}

	if h.StripeSecretKey == "" {
		billingLog.ErrorContext(r.Context(), "Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to look up organisation member role", "error", err, "org_id", orgID, "user_id", user.ID)
		InternalError(w, r, fmt.Errorf("failed to verify membership"))
		return
	}
	if role != "admin" {
		Forbidden(w, r, "Only organisation admins can manage billing")
		return
	}

	subID, err := h.DB.GetStripeSubscriptionID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch subscription: %w", err))
		return
	}
	if subID == "" {
		// No active subscription to cancel — nothing to do. Treat as success
		// so the caller's flow is idempotent.
		WriteSuccess(w, r, map[string]any{"success": true}, "No active subscription to cancel")
		return
	}

	cancelParams := &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(true),
	}
	cancelParams.Context = r.Context()
	sub, err := stripesubscription.Update(subID, cancelParams)
	if err != nil {
		billingLog.ErrorContext(r.Context(), "Failed to schedule subscription cancellation", "error", err, "org_id", orgID, "subscription_id", subID)
		InternalError(w, r, fmt.Errorf("failed to cancel subscription"))
		return
	}
	billingLog.InfoContext(r.Context(), "Scheduled Stripe subscription cancellation at period end", "org_id", orgID, "subscription_id", subID)

	// Pull period_end from the first line item — Stripe v82 moved it from the
	// subscription itself onto each item.
	var periodEnd int64
	if sub.Items != nil && len(sub.Items.Data) > 0 && sub.Items.Data[0] != nil {
		periodEnd = sub.Items.Data[0].CurrentPeriodEnd
	}

	WriteSuccess(w, r, map[string]any{
		"success":    true,
		"period_end": periodEnd,
	}, "Subscription cancellation scheduled")
}

// absoluteBaseURL returns the scheme+host base URL for this request.
// Uses X-Forwarded-Proto when behind a proxy, falls back to https.
func (h *Handler) absoluteBaseURL(r *http.Request) string {
	if h.SettingsURL != "" {
		// Strip trailing /settings if the caller stored the full page URL.
		base := h.SettingsURL
		if len(base) > 9 && base[len(base)-9:] == "/settings" {
			base = base[:len(base)-9]
		}
		return base
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
