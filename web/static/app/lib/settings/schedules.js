/**
 * lib/settings/schedules.js — automated schedules section logic
 *
 * Handles schedule listing, enable/disable toggle, deletion, and
 * navigation to schedule job history. Surface-agnostic.
 */

import {
  deleteScheduler,
  disableScheduler,
  getScheduler,
  listSchedulers,
  updateScheduler,
} from "/app/lib/scheduler-api.js";
import { showToast as _showToast } from "/app/components/hover-toast.js";

function toast(variant, message) {
  _showToast(message, { variant });
}

// ── Helpers ────────────────────────────────────────────────────────────────────

export function formatNextRunTime(timestamp) {
  if (!timestamp) return "Not scheduled";
  const nextRun = new Date(timestamp);
  if (Number.isNaN(nextRun.getTime())) return "Not scheduled";
  const now = new Date();
  const diffMs = nextRun - now;
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  const diffHours = Math.floor(
    (diffMs % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60)
  );
  const diffMins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60));

  if (diffMs < 0) return "Overdue";
  if (diffDays > 0) return `In ${diffDays}d ${diffHours}h`;
  if (diffHours > 0) return `In ${diffHours}h ${diffMins}m`;
  return `In ${diffMins}m`;
}

// ── Data loading ───────────────────────────────────────────────────────────────

/**
 * Load and render schedules into a container.
 * @param {HTMLElement} container — the schedules section element
 */
export async function loadSchedules(container) {
  const root = container || document;
  const schedulesList = root.querySelector("#settingsSchedulesList");
  const emptyState = root.querySelector("#settingsSchedulesEmpty");
  if (!schedulesList) return;

  const template = schedulesList.querySelector(
    '[data-settings-template="schedule"]'
  );
  if (!template) return;

  try {
    const schedules = await listSchedulers();

    // Remove existing rendered cards (keep the template)
    const existing = schedulesList.querySelectorAll(
      '.gnh-job-card:not([data-settings-template="schedule"])'
    );
    existing.forEach((node) => node.remove());

    if (!schedules || schedules.length === 0) {
      if (emptyState) emptyState.style.display = "block";
      return;
    }
    if (emptyState) emptyState.style.display = "none";

    schedules.forEach((schedule) => {
      const clone = template.cloneNode(true);
      clone.style.display = "block";
      clone.removeAttribute("data-settings-template");

      const domainEl = clone.querySelector(".gnh-job-domain");
      if (domainEl) domainEl.textContent = schedule.domain;

      const scheduleInfo = clone.querySelector(".gnh-schedule-info");
      if (scheduleInfo) {
        scheduleInfo.replaceChildren();
        const hoursSpan = document.createElement("span");
        const intervalHours = schedule.schedule_interval_hours ?? "\u2014";
        hoursSpan.textContent = `${intervalHours} hours`;
        const statusSpan = document.createElement("span");
        statusSpan.className = `gnh-schedule-status gnh-schedule-${schedule.is_enabled ? "enabled" : "disabled"}`;
        statusSpan.textContent = schedule.is_enabled ? "Enabled" : "Disabled";
        scheduleInfo.appendChild(hoursSpan);
        scheduleInfo.appendChild(statusSpan);
      }

      const nextRunContainer = clone.querySelector(".gnh-job-footer > div");
      if (nextRunContainer) {
        nextRunContainer.replaceChildren();
        const label = document.createElement("span");
        label.style.fontWeight = "500";
        label.textContent = "Next run: ";
        const value = document.createElement("span");
        value.textContent = formatNextRunTime(schedule.next_run_at);
        nextRunContainer.appendChild(label);
        nextRunContainer.appendChild(value);
      }

      const toggleBtn = clone.querySelector('[data-schedule-action="toggle"]');
      const deleteBtn = clone.querySelector('[data-schedule-action="delete"]');
      const viewBtn = clone.querySelector('[data-schedule-action="view-jobs"]');
      if (toggleBtn) {
        toggleBtn.dataset.schedulerId = schedule.id;
        toggleBtn.textContent = schedule.is_enabled ? "Disable" : "Enable";
      }
      if (deleteBtn) deleteBtn.dataset.schedulerId = schedule.id;
      if (viewBtn) viewBtn.dataset.schedulerId = schedule.id;

      schedulesList.appendChild(clone);
    });
  } catch (err) {
    console.error("Failed to load schedules:", err);
    toast("error", "Failed to load schedules");
  }
}

// ── Actions ────────────────────────────────────────────────────────────────────

async function toggleSchedule(schedulerId, container) {
  try {
    const scheduler = await getScheduler(schedulerId);
    if (!scheduler) {
      throw new Error("Schedule not found");
    }

    const updated = scheduler.is_enabled
      ? await disableScheduler(schedulerId, {
          expectedIsEnabled: scheduler.is_enabled,
        })
      : await updateScheduler(schedulerId, {
          is_enabled: true,
          expected_is_enabled: scheduler.is_enabled,
        });
    toast("success", `Schedule ${updated.is_enabled ? "enabled" : "disabled"}`);
    await loadSchedules(container);
  } catch (err) {
    console.error("Failed to toggle schedule:", err);
    toast("error", "Failed to toggle schedule");
  }
}

async function deleteSchedule(schedulerId, container) {
  if (!confirm("Are you sure you want to delete this schedule?")) return;

  try {
    await deleteScheduler(schedulerId);
    toast("success", "Schedule deleted");
    await loadSchedules(container);
  } catch (err) {
    console.error("Failed to delete schedule:", err);
    toast("error", "Failed to delete schedule");
  }
}

function viewScheduleJobs(schedulerId) {
  window.location.href = `/jobs?scheduler_id=${encodeURIComponent(schedulerId)}`;
}

// ── Setup ──────────────────────────────────────────────────────────────────────

/**
 * Wire up schedule section event listeners within a container.
 * @param {HTMLElement} container — the schedules section element
 */
export function setupSchedulesActions(container) {
  const root = container || document;

  const refreshBtn = root.querySelector("#autoCrawlSchedulesRefresh");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", () => loadSchedules(container));
  }

  const schedulesList = root.querySelector("#settingsSchedulesList");
  if (!schedulesList) return;

  schedulesList.addEventListener("click", (event) => {
    const actionEl = event.target.closest("[data-schedule-action]");
    if (!actionEl) return;

    const schedulerId = actionEl.dataset.schedulerId;
    if (!schedulerId) return;

    const action = actionEl.dataset.scheduleAction;
    if (action === "toggle") {
      toggleSchedule(schedulerId, container);
    } else if (action === "delete") {
      deleteSchedule(schedulerId, container);
    } else if (action === "view-jobs") {
      viewScheduleJobs(schedulerId);
    }
  });
}
