/**
 * lib/shell-nav.js — shared shell dropdown and notifications wiring
 *
 * Shared between the dashboard and Webflow Designer extension.
 * Owns profile-menu toggling, notifications badge/dropdown behaviour,
 * and context-aware in-surface navigation.
 */

function closeSurfaceOverlays(refs, except = "") {
  if (except !== "profile") {
    refs.profileDropdown?.classList.remove("show");
    refs.profileButton?.setAttribute("aria-expanded", "false");
  }
  if (except !== "notifications") {
    refs.notificationsContainer?.classList.remove("open");
    refs.notificationsButton?.setAttribute("aria-expanded", "false");
  }
}

function normaliseNotificationsResponse(data) {
  return {
    unreadCount: Number(data?.unread_count ?? data?.data?.unread_count ?? 0),
    notifications: Array.isArray(data?.notifications)
      ? data.notifications
      : Array.isArray(data?.data?.notifications)
        ? data.data.notifications
        : [],
  };
}

function clearNode(node) {
  if (!node) {
    return;
  }
  while (node.firstChild) {
    node.removeChild(node.firstChild);
  }
}

function createNotificationEmptyState() {
  const empty = document.createElement("div");
  empty.className = "shell-notifications-empty";
  empty.textContent = "No notifications yet";
  return empty;
}

function createNotificationItem(notification) {
  const item = document.createElement("button");
  item.type = "button";
  item.className = `shell-notification-item${notification?.read_at ? "" : " unread"}`;
  item.dataset.id = String(notification?.id || "");
  item.dataset.link = String(notification?.link || "");

  const content = document.createElement("div");
  content.className = "shell-notification-item-content";

  const subject = document.createElement("div");
  subject.className = "shell-notification-item-subject";
  subject.textContent = String(notification?.subject || "Notification");

  const preview = document.createElement("div");
  preview.className = "shell-notification-item-preview";
  preview.textContent = String(notification?.preview || "");

  content.append(subject, preview);
  item.appendChild(content);
  return item;
}

function updateUnreadBadge(element, unreadCount) {
  if (!element) {
    return;
  }
  const nextCount = Number(unreadCount) || 0;
  element.textContent =
    nextCount > 9 ? "9+" : nextCount > 0 ? String(nextCount) : "";
  element.hidden = nextCount <= 0;
}

export function renderProfileMenuSummary(options = {}) {
  const {
    emailNode,
    organisationNode,
    planNode,
    usageNode,
    email = "",
    organisationName = "",
    usage = null,
  } = options;

  if (emailNode) {
    emailNode.textContent = email || "Signed in";
  }

  if (organisationNode) {
    organisationNode.textContent = organisationName || "Organisation";
  }

  const plan = usage?.plan_display_name || usage?.plan_name || "Plan";
  const limit = Number(usage?.daily_limit || 0).toLocaleString();
  const remaining = Number(usage?.daily_remaining || 0).toLocaleString();

  if (planNode) {
    planNode.textContent = usage ? `${plan} (${limit} / day)` : "Plan";
  }

  if (usageNode) {
    usageNode.textContent = usage
      ? `${remaining} remaining today`
      : "Usage unavailable";
  }
}

export function initSurfaceShell(options = {}) {
  const refs = {
    profileButton: options.profileButton || null,
    profileDropdown: options.profileDropdown || null,
    notificationsContainer: options.notificationsContainer || null,
    notificationsButton: options.notificationsButton || null,
    notificationsDropdown: options.notificationsDropdown || null,
    notificationsList: options.notificationsList || null,
    notificationsBadge: options.notificationsBadge || null,
    markAllReadButton: options.markAllReadButton || null,
  };

  const onNavigate =
    typeof options.onNavigate === "function"
      ? options.onNavigate
      : (path) => {
          window.location.assign(path);
        };
  const onSignOut =
    typeof options.onSignOut === "function" ? options.onSignOut : null;
  const fetchNotifications =
    typeof options.fetchNotifications === "function"
      ? options.fetchNotifications
      : async () => ({ unread_count: 0, notifications: [] });
  const markNotificationRead =
    typeof options.markNotificationRead === "function"
      ? options.markNotificationRead
      : async () => {};
  const markAllNotificationsRead =
    typeof options.markAllNotificationsRead === "function"
      ? options.markAllNotificationsRead
      : async () => {};
  const subscribeToNotifications =
    typeof options.subscribeToNotifications === "function"
      ? options.subscribeToNotifications
      : async () => () => {};

  let notificationsCleanup = null;
  let activeOrganisationId = "";
  let destroyed = false;

  async function refreshNotifications(limit = 1) {
    if (destroyed) {
      return { unreadCount: 0, notifications: [] };
    }

    const data = normaliseNotificationsResponse(
      await fetchNotifications(limit)
    );
    updateUnreadBadge(refs.notificationsBadge, data.unreadCount);
    return data;
  }

  async function renderNotificationsList(limit = 10) {
    if (!refs.notificationsList) {
      return;
    }

    const data = await refreshNotifications(limit);
    clearNode(refs.notificationsList);

    if (!data.notifications.length) {
      refs.notificationsList.appendChild(createNotificationEmptyState());
      return;
    }

    data.notifications.forEach((notification) => {
      refs.notificationsList.appendChild(createNotificationItem(notification));
    });
  }

  async function updateNotificationSubscription(nextOrganisationId) {
    activeOrganisationId = String(nextOrganisationId || "");

    if (notificationsCleanup) {
      notificationsCleanup();
      notificationsCleanup = null;
    }

    if (!activeOrganisationId) {
      updateUnreadBadge(refs.notificationsBadge, 0);
      if (refs.notificationsList) {
        clearNode(refs.notificationsList);
        refs.notificationsList.appendChild(createNotificationEmptyState());
      }
      return;
    }

    await refreshNotifications(1);

    notificationsCleanup = await Promise.resolve(
      subscribeToNotifications(activeOrganisationId, () => {
        window.setTimeout(() => {
          void refreshNotifications(1);
          if (refs.notificationsContainer?.classList.contains("open")) {
            void renderNotificationsList(10);
          }
        }, 200);
      })
    );
  }

  refs.profileButton?.addEventListener("click", (event) => {
    event.stopPropagation();
    const willOpen = !refs.profileDropdown?.classList.contains("show");
    closeSurfaceOverlays(refs, willOpen ? "profile" : "");
    refs.profileDropdown?.classList.toggle("show", willOpen);
    refs.profileButton?.setAttribute(
      "aria-expanded",
      willOpen ? "true" : "false"
    );
  });

  refs.profileDropdown?.addEventListener("click", (event) => {
    event.stopPropagation();
    const target =
      event.target instanceof Element
        ? event.target.closest("[data-path]")
        : null;
    if (target) {
      const path = target.getAttribute("data-path") || "";
      if (path) {
        closeSurfaceOverlays(refs);
        onNavigate(path);
      }
      return;
    }

    const signOutButton =
      event.target instanceof Element
        ? event.target.closest("[data-action='sign-out']")
        : null;
    if (signOutButton && onSignOut) {
      closeSurfaceOverlays(refs);
      void onSignOut();
    }
  });

  refs.notificationsButton?.addEventListener("click", (event) => {
    event.stopPropagation();
    const willOpen = !refs.notificationsContainer?.classList.contains("open");
    closeSurfaceOverlays(refs, willOpen ? "notifications" : "");
    refs.notificationsContainer?.classList.toggle("open", willOpen);
    refs.notificationsButton?.setAttribute(
      "aria-expanded",
      willOpen ? "true" : "false"
    );
    if (willOpen) {
      void renderNotificationsList(10);
    }
  });

  refs.notificationsDropdown?.addEventListener("click", (event) => {
    event.stopPropagation();
    const target =
      event.target instanceof Element
        ? event.target.closest("[data-path]")
        : null;
    if (target) {
      const path = target.getAttribute("data-path") || "";
      if (path) {
        closeSurfaceOverlays(refs);
        onNavigate(path);
      }
    }
  });

  refs.notificationsList?.addEventListener("click", (event) => {
    const item =
      event.target instanceof Element
        ? event.target.closest(".shell-notification-item")
        : null;
    if (!item) {
      return;
    }

    const notificationId = item.getAttribute("data-id") || "";
    const link = item.getAttribute("data-link") || "";

    void (async () => {
      if (notificationId) {
        try {
          await markNotificationRead(notificationId);
        } catch (error) {
          console.warn("shell-nav: failed to mark notification as read", error);
        }
      }

      item.classList.remove("unread");
      await refreshNotifications(1);
      closeSurfaceOverlays(refs);

      if (!link) {
        return;
      }

      if (link.startsWith("/")) {
        onNavigate(link);
        return;
      }

      try {
        const target = new URL(link, window.location.origin);
        if (target.protocol === "http:" || target.protocol === "https:") {
          window.open(target.toString(), "_blank", "noopener,noreferrer");
        }
      } catch (error) {
        console.warn("shell-nav: invalid notification link", error);
      }
    })();
  });

  refs.markAllReadButton?.addEventListener("click", () => {
    void (async () => {
      try {
        await markAllNotificationsRead();
        updateUnreadBadge(refs.notificationsBadge, 0);
        refs.notificationsList
          ?.querySelectorAll(".shell-notification-item.unread")
          .forEach((item) => item.classList.remove("unread"));
      } catch (error) {
        console.warn("shell-nav: failed to mark notifications as read", error);
      }
    })();
  });

  document.addEventListener("click", (event) => {
    if (!(event.target instanceof Node)) {
      closeSurfaceOverlays(refs);
      return;
    }
    if (
      refs.profileDropdown &&
      refs.profileButton &&
      !refs.profileDropdown.contains(event.target) &&
      !refs.profileButton.contains(event.target)
    ) {
      refs.profileDropdown.classList.remove("show");
      refs.profileButton.setAttribute("aria-expanded", "false");
    }
    if (
      refs.notificationsContainer &&
      !refs.notificationsContainer.contains(event.target)
    ) {
      refs.notificationsContainer.classList.remove("open");
      refs.notificationsButton?.setAttribute("aria-expanded", "false");
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeSurfaceOverlays(refs);
    }
  });

  return {
    refreshNotifications,
    renderNotificationsList,
    setActiveOrganisation(nextOrganisationId) {
      void updateNotificationSubscription(nextOrganisationId);
    },
    destroy() {
      destroyed = true;
      closeSurfaceOverlays(refs);
      if (notificationsCleanup) {
        notificationsCleanup();
        notificationsCleanup = null;
      }
    },
  };
}

export default {
  initSurfaceShell,
  renderProfileMenuSummary,
};
