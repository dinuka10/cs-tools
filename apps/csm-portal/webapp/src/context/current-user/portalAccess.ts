// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.
import { devBypassAccessCheck } from "@config/devFlags";

/**
 * Portal roles as returned in `GET /users/me`'s `roles` (stable keys, not the
 * IdP role names). A user can hold several.
 */
export const PORTAL_ROLE = {
  viewer: "viewer",
  escalator: "escalator",
  attachmentDownloader: "attachment_downloader",
  // Renamed from support_engineer -- the wire value must stay byte-for-byte
  // in sync with the backend's own AccessGuard portalRoles key
  // (apps/csm-portal/backend/internal/handler/access.go), which returns this
  // exact string in GET /users/me's roles array.
  csEngineer: "cs_engineer",
  usageMetricsViewer: "usage_metrics_viewer",
  timecardApprover: "timecard_approver",
  dashboardDesigner: "dashboard_designer",
  admin: "admin",
  // Grants PermCreateWorkNote on the backend (internal/handler/access.go) --
  // posting a work_note-type comment on a case, nothing else. See
  // PermissionProvider.tsx's canAddWorkNotes for the one capability this
  // backs.
  worknoteCreator: "worknote_creator",
} as const;

const ALL_PORTAL_ROLES: readonly string[] = Object.values(PORTAL_ROLE);

export interface PortalAccess {
  /** Holds at least one portal role — the minimum to use the portal at all. */
  hasAnyRole: boolean;
  /**
   * Escalating or de-escalating a case. `admin` and `escalator` only —
   * `cs_engineer` does NOT hold it, mirroring the backend's `PermEscalate`.
   * Escalation is a dedicated responsibility, not something being a CS
   * engineer alone grants.
   */
  canEscalate: boolean;
  canDownloadAttachment: boolean;
  /**
   * The Operations section (incidents, change requests, problems, outages,
   * service requests). Support-portal-lite had no such area, so its
   * view-oriented roles never saw one: only full-access roles do here.
   */
  canUseOperations: boolean;
  /**
   * The Time Cards and Updates sections, and every time-card and update-level
   * call behind them. Full-access roles have it, and so does the time-card
   * approver, so viewing/managing time cards does not require being a CS
   * engineer. This governs the section as a whole, not approval specifically
   * — mirrors the backend's `PermTimeCardsAndUpdates`, which `cs_engineer`
   * still holds. Approving/rejecting a time card is a narrower, separate
   * concern gated by `useTimecardRole()` (`isApprover`/`isAdmin`), not this
   * flag — mirroring the backend's own narrower `PermApproveTimeCard`.
   */
  canUseTimeCardsAndUpdates: boolean;
  /** Every other state-changing action (create/update cases, tasks, ...). */
  canWrite: boolean;
  /**
   * Creating a new platform user. Unlike every other flag here, this is
   * `admin` only — `cs_engineer` does not hold it, mirroring the
   * backend's `PermAdmin` (the one permission `cs_engineer` does not
   * share with `admin`).
   */
  canCreateUser: boolean;
  /**
   * The Security Center section (Security reports + Vulnerabilities tabs)
   * and the API calls behind it. `admin` and `cs_engineer` only — mirrors
   * the backend's `PermViewSecurityCenter`, which (unlike `PermView`) plain
   * viewer/escalator/attachment_downloader/usage_metrics_viewer/
   * timecard_approver/dashboard_designer do not hold.
   */
  canUseSecurityCenter: boolean;
  /**
   * The PLG Customer Success Portal section. `admin` and `cs_engineer` only —
   * mirrors the backend's `PermUsePlg`, which (unlike `PermView`) the view-only
   * roles do not hold. PLG is a worklist staff act on, so a role that could open
   * it but not use it would meet a 403 on every control.
   */
  canUsePlg: boolean;
  /**
   * Authoring a PLG playbook template: creating one, editing it, changing its
   * tasks, deleting it. `admin` only, mirroring the backend's
   * `PermManagePlaybooks`.
   *
   * A `cs_engineer` holds `canUsePlg` without this: they browse templates and
   * assign them to a pairing, but the Manage Playbooks page renders read-only
   * for them.
   */
  canManagePlaybooks: boolean;
}

/**
 * What a user's `GET /users/me` roles let them see and do. Matched
 * case-insensitively. `admin` can do everything; `cs_engineer` can do
 * everything EXCEPT escalate a case (a dedicated responsibility, held only
 * by `escalator` plus `admin` — see `canEscalate`'s own doc comment) —
 * approving a time card is a similarly dedicated responsibility, but it
 * isn't a flag on this type at all, see `canUseTimeCardsAndUpdates`'s own
 * doc comment for why; `attachment_downloader` adds just that one ability;
 * every other role is view-only here.
 *
 * Mirrors the backend's `AccessGuard` policy so controls can be hidden up
 * front — but it is a UX affordance only. The backend's 403 is the real gate,
 * so if the two ever disagree the backend wins. `undefined` roles (profile not
 * loaded, or the request failed) grant nothing, so controls fail closed.
 */
export function getPortalAccess(roles: string[] | undefined): PortalAccess {
  // TEMPORARY / LOCAL DEV ONLY — see authConfig.ts's devBypassAccessCheck.
  // Grants every capability regardless of the real `roles` claim, so a local
  // account the staging backend hasn't provisioned a portal role for yet can
  // still see every nav section/action during development.
  if (devBypassAccessCheck) {
    return {
      hasAnyRole: true,
      canEscalate: true,
      canDownloadAttachment: true,
      canUseOperations: true,
      canUseTimeCardsAndUpdates: true,
      canWrite: true,
      canCreateUser: true,
      canUseSecurityCenter: true,
      canUsePlg: true,
      canManagePlaybooks: true,
    };
  }
  const held = new Set((roles ?? []).map((r) => r.toLowerCase()));
  const has = (role: string): boolean => held.has(role);
  const isAdmin = has(PORTAL_ROLE.admin);
  const full = isAdmin || has(PORTAL_ROLE.csEngineer);
  return {
    hasAnyRole: ALL_PORTAL_ROLES.some(has),
    // canEscalate deliberately checks isAdmin, not full: cs_engineer alone
    // must not grant it (see its own doc comment above).
    canEscalate: isAdmin || has(PORTAL_ROLE.escalator),
    canDownloadAttachment: full || has(PORTAL_ROLE.attachmentDownloader),
    canUseOperations: full,
    canUseTimeCardsAndUpdates: full || has(PORTAL_ROLE.timecardApprover),
    canWrite: full,
    canCreateUser: isAdmin,
    canUseSecurityCenter: full,
    canUsePlg: full,
    canManagePlaybooks: isAdmin,
  };
}
