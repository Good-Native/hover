package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"
	stripe "github.com/stripe/stripe-go/v82"
	portalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	"github.com/stripe/stripe-go/v82/customer"
	stripesubscription "github.com/stripe/stripe-go/v82/subscription"
)

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
		log.Error().Msg("Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Str("user_id", user.ID).Msg("Failed to look up organisation member role")
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

	baseURL := h.absoluteBaseURL(r)
	settingsURL := baseURL + "/settings/plans"

	if existingSubID != "" {
		// Paid → different paid: update the existing subscription in place.
		// Avoids duplicate live subscriptions and gives Stripe-managed proration.
		sub, err := stripesubscription.Get(existingSubID, nil)
		if err != nil {
			log.Error().Err(err).Str("subscription_id", existingSubID).Msg("Failed to fetch existing Stripe subscription")
			InternalError(w, r, fmt.Errorf("failed to fetch subscription"))
			return
		}
		if sub.Items == nil || len(sub.Items.Data) == 0 {
			log.Error().Str("subscription_id", existingSubID).Msg("Existing subscription has no line items")
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
		_, err = stripesubscription.Update(existingSubID, &stripe.SubscriptionParams{
			Items: []*stripe.SubscriptionItemsParams{
				{
					ID:    stripe.String(sub.Items.Data[0].ID),
					Price: stripe.String(stripePriceID),
				},
			},
		})
		if err != nil {
			log.Error().Err(err).Str("org_id", orgID).Str("subscription_id", existingSubID).Msg("Failed to update Stripe subscription")
			InternalError(w, r, fmt.Errorf("failed to update subscription"))
			return
		}
		// customer.subscription.updated webhook will reconcile plan_id in DB.
		log.Info().Str("org_id", orgID).Str("subscription_id", existingSubID).Str("price_id", stripePriceID).Msg("Updated Stripe subscription to new plan")
		WriteSuccess(w, r, map[string]string{"url": settingsURL + "?billing=success"}, "Plan updated")
		return
	}

	// No existing subscription — first-time Checkout flow.
	customerID, err := h.DB.GetStripeCustomerID(r.Context(), orgID)
	if err != nil {
		InternalError(w, r, fmt.Errorf("failed to fetch stripe customer: %w", err))
		return
	}
	if customerID == "" {
		org, err := h.DB.GetOrganisation(orgID)
		if err != nil {
			InternalError(w, r, fmt.Errorf("failed to fetch organisation: %w", err))
			return
		}
		cust, err := customer.New(&stripe.CustomerParams{
			Email: stripe.String(user.Email),
			Name:  stripe.String(org.Name),
			Metadata: map[string]string{
				"organisation_id": orgID,
			},
		})
		if err != nil {
			log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe customer")
			InternalError(w, r, fmt.Errorf("failed to create billing customer"))
			return
		}
		customerID = cust.ID
		log.Info().Str("customer_id", customerID).Str("org_id", orgID).Msg("Created Stripe customer")
		if err := h.DB.SetStripeCustomerID(r.Context(), orgID, customerID); err != nil {
			log.Error().Err(err).Str("org_id", orgID).Msg("Failed to store Stripe customer ID")
			InternalError(w, r, fmt.Errorf("failed to store billing customer"))
			return
		}
	}

	sess, err := checkoutsession.New(&stripe.CheckoutSessionParams{
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
	})
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe Checkout Session")
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
		log.Error().Msg("Stripe secret key not configured")
		InternalError(w, r, fmt.Errorf("billing not configured"))
		return
	}

	user, orgID, ok := h.GetActiveOrganisationWithUser(w, r)
	if !ok {
		return
	}

	role, err := h.DB.GetOrganisationMemberRole(r.Context(), user.ID, orgID)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Str("user_id", user.ID).Msg("Failed to look up organisation member role")
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
	// Optional override — when unset, Stripe falls back to the account's
	// default Customer Portal configuration.
	if h.StripePortalConfigID != "" {
		params.Configuration = stripe.String(h.StripePortalConfigID)
	}
	sess, err := portalsession.New(params)
	if err != nil {
		log.Error().Err(err).Str("org_id", orgID).Msg("Failed to create Stripe Portal Session")
		InternalError(w, r, fmt.Errorf("failed to create portal session"))
		return
	}

	WriteSuccess(w, r, map[string]string{"url": sess.URL}, "Portal session created")
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
