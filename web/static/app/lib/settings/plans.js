/**
 * lib/settings/plans.js — plans and usage section logic
 *
 * Handles plan display, plan switching, and usage history rendering.
 * Surface-agnostic: all render/action functions accept a container element.
 */

import { get, post } from "/app/lib/api-client.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Usage cache ────────────────────────────────────────────────────────────────

let _cachedUsage = null;
let _cachedUsageTs = 0;
const USAGE_CACHE_TTL = 30_000;

async function getUsage() {
  const now = Date.now();
  if (_cachedUsage && now - _cachedUsageTs < USAGE_CACHE_TTL) {
    return _cachedUsage;
  }
  const response = await get("/v1/usage");
  _cachedUsage = response;
  _cachedUsageTs = now;
  return response;
}

export function invalidateUsageCache() {
  _cachedUsage = null;
  _cachedUsageTs = 0;
}

// ── Plans & Usage ──────────────────────────────────────────────────────────────

/**
 * Load and render plans and current usage into a container.
 * @param {HTMLElement} container — the plans section element
 * @param {object} [options]
 * @param {string} [options.currentUserRole] — role for plan switch gating
 */
export async function loadPlansAndUsage(container, options = {}) {
  const root = container || document;
  const currentPlanName = root.querySelector("#planCurrentName");
  const currentPlanLimit = root.querySelector("#planCurrentLimit");
  const currentPlanUsage = root.querySelector("#planCurrentUsage");
  const currentPlanReset = root.querySelector("#planCurrentReset");
  const planList = root.querySelector("#planCards");
  // Template lives outside the section in settings.html — always use document.
  const planTemplate = document.querySelector("#planCardTemplate");

  try {
    const [usageResponse, plansResponse] = await Promise.all([
      getUsage(),
      get("/v1/plans"),
    ]);

    const usage = usageResponse.usage || {};
    const plans = plansResponse.plans || [];

    if (currentPlanName) {
      currentPlanName.textContent = usage.plan_display_name || "Free";
    }
    if (currentPlanLimit) {
      currentPlanLimit.textContent = usage.daily_limit
        ? `${usage.daily_limit.toLocaleString()} pages/day`
        : "No limit";
    }
    if (currentPlanUsage) {
      const dailyUsed = Number.isFinite(usage.daily_used)
        ? usage.daily_used
        : 0;
      currentPlanUsage.textContent = usage.daily_limit
        ? `${dailyUsed.toLocaleString()} used today`
        : "No usage data";
    }
    if (currentPlanReset) {
      currentPlanReset.textContent = usage.resets_at
        ? window.GNHQuota?.formatTimeUntilReset(usage.resets_at) || ""
        : "";
    }

    if (planList && planTemplate) {
      planList.replaceChildren();
      const role = options.currentUserRole || "member";

      plans.forEach((plan) => {
        const clone = planTemplate.content.cloneNode(true);
        const card = clone.querySelector(".settings-plan-card");
        const nameEl = clone.querySelector(".settings-plan-name");
        const priceEl = clone.querySelector(".settings-plan-price");
        const limitEl = clone.querySelector(".settings-plan-limit");
        const actionBtn = clone.querySelector(".settings-plan-action");

        if (card && plan.id === usage.plan_id) {
          card.classList.add("current");
        }
        if (nameEl) nameEl.textContent = plan.display_name;
        if (priceEl) {
          priceEl.textContent =
            plan.monthly_price_cents > 0
              ? `$${(plan.monthly_price_cents / 100).toFixed(0)}/month`
              : "Free";
        }
        if (limitEl) {
          limitEl.textContent = Number.isFinite(plan.daily_page_limit)
            ? `${plan.daily_page_limit.toLocaleString()} pages/day`
            : "No limit";
        }
        if (actionBtn) {
          actionBtn.dataset.planId = plan.id;
          if (plan.id === usage.plan_id) {
            actionBtn.textContent = "Current plan";
            actionBtn.disabled = true;
          } else if (role !== "admin") {
            actionBtn.textContent = "Admin only";
            actionBtn.disabled = true;
          } else if (plan.monthly_price_cents > 0) {
            actionBtn.textContent = "Upgrade";
            actionBtn.disabled = false;
            actionBtn.addEventListener("click", () =>
              startCheckout(plan.id, actionBtn)
            );
          } else {
            // Downgrading to free — cancel the Stripe subscription via the
            // billing backend (so the customer stops being charged).
            actionBtn.textContent = "Switch to Free";
            actionBtn.disabled = false;
            actionBtn.addEventListener("click", () =>
              switchToFree(container, options)
            );
          }
        }

        planList.appendChild(clone);
      });
    }
  } catch (err) {
    console.error("Failed to load plans:", err);
    toast("error", "Failed to load plan details");
  }
}

// Switch to free — cancels the active Stripe subscription via the billing
// backend so the customer stops being charged. The previous PUT
// /v1/organisations/plan path silently updated plan_id locally without
// telling Stripe, leaving subscriptions billing forever.
async function switchToFree(container, options = {}) {
  if (
    !confirm(
      "Cancel your subscription and switch to the free plan? Your current plan stays active until the end of the billing period."
    )
  ) {
    return;
  }

  try {
    await post("/v1/billing/cancel", {});
    invalidateUsageCache();
    toast("success", "Subscription cancelled");
    await loadPlansAndUsage(container, options);
    window.GNHQuota?.refresh();
  } catch (err) {
    console.error("Failed to cancel subscription:", err);
    const msg =
      err?.body?.message || "Failed to cancel subscription — please try again";
    toast("error", msg);
  }
}

async function startCheckout(planId, btn) {
  if (!planId) return;

  if (btn) {
    btn.disabled = true;
    btn.textContent = "Loading…";
  }
  try {
    const response = await post("/v1/billing/checkout", { plan_id: planId });
    if (response.url) {
      window.location.href = response.url;
    }
  } catch (err) {
    console.error("Failed to start checkout:", err);
    // Surface server messages for known cases (e.g. 409 — already subscribed).
    // Stripe Checkout in subscription mode always creates a new sub, so the
    // server rejects a second Checkout when one is already active.
    const msg =
      err?.status === 409 && err?.body?.message
        ? err.body.message
        : "Failed to open checkout — please try again";
    toast("error", msg);
    if (btn) {
      btn.disabled = false;
      btn.textContent = "Upgrade";
    }
  }
}

/**
 * Initialise the billing section — fetch current billing state and wire
 * up the "Manage billing" button. Reads has_stripe_customer from /v1/usage.
 */
export async function loadBillingSection() {
  const btn = document.getElementById("manageBillingBtn");
  const status = document.getElementById("billingStatus");
  if (!btn) return;

  let hasStripeCustomer = false;
  try {
    const usageResponse = await getUsage();
    hasStripeCustomer = !!usageResponse?.usage?.has_stripe_customer;
  } catch (err) {
    console.error("Failed to fetch billing status:", err);
  }

  if (!hasStripeCustomer) {
    // No subscription yet — keep button disabled.
    return;
  }

  if (status) {
    status.textContent =
      "Manage your subscription, update payment methods, and view invoices.";
  }
  btn.disabled = false;
  // Replace the button to avoid duplicate listeners on refresh.
  const fresh = btn.cloneNode(true);
  btn.replaceWith(fresh);
  fresh.addEventListener("click", async () => {
    fresh.disabled = true;
    fresh.textContent = "Opening…";
    try {
      const response = await post("/v1/billing/portal", {});
      if (response.url) {
        window.location.href = response.url;
      }
    } catch (err) {
      console.error("Failed to open billing portal:", err);
      toast("error", "Failed to open billing portal — please try again");
      fresh.disabled = false;
      fresh.textContent = "Manage billing";
    }
  });
}

// ── Usage history ──────────────────────────────────────────────────────────────

/**
 * Load and render usage history into a container.
 * @param {HTMLElement} container — the plans section element
 */
export async function loadUsageHistory(container) {
  const root = container || document;
  const list = root.querySelector("#usageHistoryList");
  if (!list) return;

  list.replaceChildren();
  try {
    const response = await get("/v1/usage/history?days=30");
    const entries = response.usage || [];

    if (entries.length === 0) {
      const empty = document.createElement("div");
      empty.className = "settings-muted";
      empty.textContent = "No usage history yet.";
      list.appendChild(empty);
      return;
    }

    entries.forEach((entry) => {
      const row = document.createElement("div");
      row.className = "settings-usage-row";
      const dateSpan = document.createElement("span");
      dateSpan.textContent = entry.usage_date;
      const pagesSpan = document.createElement("span");
      const pagesProcessed = Number.isFinite(entry.pages_processed)
        ? entry.pages_processed
        : 0;
      pagesSpan.textContent = `${pagesProcessed.toLocaleString()} pages`;
      row.appendChild(dateSpan);
      row.appendChild(pagesSpan);
      list.appendChild(row);
    });
  } catch (err) {
    console.error("Failed to load usage history:", err);
    const errorEl = document.createElement("div");
    errorEl.className = "settings-muted";
    errorEl.textContent = "Failed to load usage history.";
    list.appendChild(errorEl);
  }
}
