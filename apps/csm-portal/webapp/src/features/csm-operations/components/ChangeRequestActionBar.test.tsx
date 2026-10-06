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

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import ChangeRequestActionBar from "@features/csm-operations/components/ChangeRequestActionBar";
import type { BeChangeRequestDetail } from "@api/backend/types";

const BASE_CR: BeChangeRequestDetail = {
  id: "chg-1",
  number: "CHG0009988",
  subject: "Upgrade the gateway cluster",
  createdOn: "2026-01-01T00:00:00Z",
  state: "new",
  type: "normal",
  assignedTeam: { id: "team-1", name: "Platform" },
};

function renderBar(
  overrides: Partial<BeChangeRequestDetail>,
  { isPending = false, onAction = vi.fn() } = {},
): { onAction: ReturnType<typeof vi.fn>; container: HTMLElement } {
  const { container } = render(
    <ChangeRequestActionBar
      cr={{ ...BASE_CR, ...overrides }}
      isPending={isPending}
      onAction={onAction}
    />,
  );
  return { onAction, container };
}

/** Open the overflow menu, which must exist for this to succeed. */
function openMenu(): void {
  fireEvent.click(screen.getByRole("button", { name: /change state/i }));
}

describe("ChangeRequestActionBar — driven only by legalNextStates", () => {
  it("renders nothing when legalNextStates is absent", () => {
    const { container } = renderBar({ legalNextStates: undefined });
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when legalNextStates is empty", () => {
    const { container } = renderBar({ legalNextStates: [] });
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when the only entry is the CR's own current state", () => {
    const { container } = renderBar({ state: "assess", legalNextStates: ["assess"] });
    expect(container).toBeEmptyDOMElement();
  });

  it("offers only the states present in legalNextStates, not the whole lifecycle", () => {
    renderBar({ state: "scheduled", legalNextStates: ["implement", "canceled"] });
    // "Start implementation" is the forward move -> primary button.
    expect(
      screen.getByRole("button", { name: /start implementation/i }),
    ).toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    // Never offered: legal elsewhere in the lifecycle, but not in this array.
    expect(screen.queryByRole("menuitem", { name: /^close$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /roll back/i })).not.toBeInTheDocument();
  });

  it("renders a state it has no curated config for, via the generic fallback", () => {
    renderBar({ state: "review", legalNextStates: ["closed", "awaiting_vendor"] });
    openMenu();
    // Sentence-cased from the raw value — no frontend change was needed for it.
    expect(
      screen.getByRole("menuitem", { name: /^awaiting vendor$/i }),
    ).toBeInTheDocument();
  });

  it("dispatches an uncurated state verbatim, not a normalised guess at it", () => {
    const { onAction } = renderBar({
      state: "review",
      legalNextStates: ["closed", "awaiting_vendor"],
    });
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /^awaiting vendor$/i }));
    expect(onAction).toHaveBeenCalledWith("awaiting_vendor");
  });
});

describe("ChangeRequestActionBar — exactly one primary button", () => {
  it("promotes only the first forward move, even with six legal targets", () => {
    renderBar({
      state: "new",
      legalNextStates: [
        "closed",
        "customer_review",
        "review",
        "implement",
        "scheduled",
        "assess",
        "rollback",
        "canceled",
      ],
    });
    const contained = screen
      .getAllByRole("button")
      .filter((b) => b.className.includes("MuiButton-contained"));
    expect(contained).toHaveLength(1);
    expect(contained[0]).toHaveTextContent(/request approval/i);
  });

  it("puts every non-promoted target behind the Change state menu", () => {
    renderBar({ state: "new", legalNextStates: ["assess", "implement", "canceled"] });
    expect(screen.getByRole("button", { name: /request approval/i })).toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /start implementation/i })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("renders no overflow menu at all when the single legal target is the primary one", () => {
    renderBar({ state: "scheduled", legalNextStates: ["implement"] });
    expect(screen.getByRole("button", { name: /start implementation/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /change state/i })).not.toBeInTheDocument();
  });

  it("never promotes a destructive target: with only cancel legal, there is no primary button", () => {
    renderBar({ state: "implement", legalNextStates: ["canceled"] });
    expect(screen.queryByRole("button", { name: /cancel change/i })).not.toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("never promotes an uncurated target, even when it is the only legal one", () => {
    renderBar({ state: "review", legalNextStates: ["awaiting_vendor"] });
    expect(
      screen.queryByRole("button", { name: /^awaiting vendor$/i }),
    ).not.toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /^awaiting vendor$/i })).toBeInTheDocument();
  });
});

describe("ChangeRequestActionBar — labels are the action, not the destination", () => {
  it.each([
    ["assess", /^request approval$/i],
    ["implement", /start implementation/i],
    ["review", /mark implemented/i],
    ["customer_review", /send for customer review/i],
    ["closed", /^close$/i],
  ])("labels the %s transition as the action taken", (target, label) => {
    renderBar({ state: "new", legalNextStates: [target] });
    expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
  });
});

describe("ChangeRequestActionBar — dispatch", () => {
  it("calls onAction with the target when the primary button is clicked", () => {
    const { onAction } = renderBar({ state: "new", legalNextStates: ["assess"] });
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    expect(onAction).toHaveBeenCalledWith("assess");
  });

  it("calls onAction with the target when a menu item is clicked, and closes the menu", () => {
    const { onAction } = renderBar({
      state: "implement",
      legalNextStates: ["review", "canceled"],
    });
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /cancel change/i }));
    expect(onAction).toHaveBeenCalledWith("canceled");
  });
});

/**
 * Neither state is human-enterable in the backing system, so the bar must not
 * offer them however they arrive in `legalNextStates`. See
 * `NEVER_OFFERED_TARGETS` for why the filter exists — these tests are what
 * stops it being removed as dead code.
 */
describe("ChangeRequestActionBar — states the bar never offers", () => {
  it("renders neither rollback (outside the review states) nor customer approval, as a button or a menu item", () => {
    renderBar({
      state: "implement",
      legalNextStates: ["review", "rollback", "customer_approval", "canceled"],
    });
    expect(screen.queryByRole("button", { name: /roll back/i })).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /customer approval/i }),
    ).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /roll back/i })).not.toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: /customer approval/i }),
    ).not.toBeInTheDocument();
  });

  it("still renders the other legal targets normally alongside them", () => {
    renderBar({
      state: "implement",
      legalNextStates: ["review", "rollback", "customer_approval", "canceled"],
    });
    expect(screen.getByRole("button", { name: /mark implemented/i })).toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("excludes them even when they would otherwise render through the generic fallback", () => {
    // `customer_approval` has no curated action label, so without the
    // exclusion it would still be renderable via `DEFAULT_TARGET_CONFIG`.
    renderBar({ state: "assess", legalNextStates: ["customer_approval", "awaiting_vendor"] });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /^awaiting vendor$/i })).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: /customer approval/i }),
    ).not.toBeInTheDocument();
  });

  it("renders no bar at all when every legal target is excluded", () => {
    const { container } = renderBar({
      state: "implement",
      legalNextStates: ["rollback", "customer_approval"],
    });
    expect(container).toBeEmptyDOMElement();
  });

  it("never offers rollback from any state but review and customer_review, even if the backend listed it", () => {
    for (const state of [
      "new", "assess", "authorize", "customer_approval", "scheduled", "implement",
      "closed", "canceled", "rollback",
    ]) {
      cleanup();
      const { container } = renderBar({ state, legalNextStates: ["rollback"] });
      expect(container, state).toBeEmptyDOMElement();
    }
  });

  /**
   * Authorize must only ever be reached as the automatic side effect of an
   * approver approving in the Approvers section (`ChangeRequestApprovals.tsx`)
   * — never via a direct click here, even from Assess, where it would
   * otherwise be the obvious next forward move. Same exclusion mechanism as
   * rollback/customer_approval above, for a different reason (a human
   * decision made elsewhere in the UI, not automation).
   */
  it("never offers authorize from Assess, as a button or a menu item", () => {
    renderBar({
      state: "assess",
      legalNextStates: ["authorize", "canceled"],
    });
    expect(screen.queryByRole("button", { name: /^authorize$/i })).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /^authorize$/i })).not.toBeInTheDocument();
  });

  it("excludes authorize even when it would otherwise render through the generic fallback", () => {
    // `authorize` has no curated action label, so without the exclusion it
    // would still be renderable via `DEFAULT_TARGET_CONFIG`.
    renderBar({ state: "assess", legalNextStates: ["authorize", "awaiting_vendor"] });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /^awaiting vendor$/i })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /^authorize$/i })).not.toBeInTheDocument();
  });

  it("renders no bar at all from Assess when authorize is the only legal target", () => {
    const { container } = renderBar({
      state: "assess",
      legalNextStates: ["authorize"],
    });
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ChangeRequestActionBar — pending state", () => {
  it("disables the primary button while a transition is in flight", () => {
    renderBar({ state: "new", legalNextStates: ["assess", "canceled"] }, { isPending: true });
    expect(screen.getByRole("button", { name: /request approval/i })).toBeDisabled();
  });

  it("disables the Change state menu trigger while a transition is in flight", () => {
    renderBar({ state: "new", legalNextStates: ["assess", "canceled"] }, { isPending: true });
    expect(screen.getByRole("button", { name: /change state/i })).toBeDisabled();
  });
});

/**
 * "Request Approval" (New -> Assess) requires an assigned team: its members
 * are who the Peer Approval stage is provisioned for.
 */
describe("ChangeRequestActionBar — per-target blocked reasons", () => {
  it("disables the assess transition when the CR has no assigned team", () => {
    const { onAction } = renderBar({
      state: "new",
      legalNextStates: ["assess"],
      assignedTeam: null,
    });
    const button = screen.getByRole("button", { name: /request approval/i });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onAction).not.toHaveBeenCalled();
  });

  it("exposes the blocked reason to keyboard users via a focusable, labelled wrapper", () => {
    renderBar({ state: "new", legalNextStates: ["assess"], assignedTeam: null });
    const focusTarget = screen
      .getByRole("button", { name: /request approval/i })
      .closest('[tabindex="0"]');
    expect(focusTarget).not.toBeNull();
    expect(focusTarget).toHaveAttribute(
      "aria-label",
      "Request Approval: Set an assigned team before requesting approval",
    );
  });

  it("leaves the transition enabled once the prerequisite is met", () => {
    renderBar({ state: "new", legalNextStates: ["assess"] });
    expect(screen.getByRole("button", { name: /request approval/i })).toBeEnabled();
  });

  it("blocks only the target with the unmet prerequisite, leaving the others clickable", () => {
    // `assess` is blocked *and* is first in FORWARD_ORDER, so it stays the
    // promoted (disabled) primary while `canceled` stays usable behind the
    // menu — a blocked target must not take the rest of the bar down with it.
    const { onAction } = renderBar({
      state: "new",
      legalNextStates: ["canceled", "assess"],
      assignedTeam: null,
    });
    expect(screen.getByRole("button", { name: /request approval/i })).toBeDisabled();
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /cancel change/i }));
    expect(onAction).toHaveBeenCalledWith("canceled");
  });
});

/**
 * CAB (or ECAB) approval moves a CR to Scheduled automatically, and a Standard
 * change goes straight there from Request Approval -- there is no manual
 * "Schedule" button. The backend no longer lists `scheduled` in
 * `legalNextStates`; the bar also filters it defensively.
 */
describe("ChangeRequestActionBar — Request Approval flow, no manual Schedule", () => {
  it("shows 'Request Approval' and never 'Move to Assess' for a new CR", () => {
    renderBar({ state: "new", legalNextStates: ["assess", "canceled"] });
    expect(screen.getByRole("button", { name: "Request Approval" })).toBeInTheDocument();
    expect(screen.queryByText(/move to assess/i)).not.toBeInTheDocument();
  });

  it("never offers Schedule, as a button or menu item, even if the backend lists scheduled", () => {
    renderBar({ state: "authorize", legalNextStates: ["scheduled", "canceled"] });
    expect(screen.queryByRole("button", { name: /schedule/i })).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /schedule/i })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("renders no bar at all when scheduled is the only legal target", () => {
    const { container } = renderBar({ state: "authorize", legalNextStates: ["scheduled"] });
    expect(container).toBeEmptyDOMElement();
  });

  it("offers no Schedule for an Assess-stage CR (approval pending), only Cancel", () => {
    renderBar({ state: "assess", legalNextStates: ["authorize", "scheduled", "canceled"] });
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /schedule|authorize/i })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("from Scheduled, the forward move is Start implementation (the CR got there automatically)", () => {
    renderBar({ state: "scheduled", legalNextStates: ["implement", "canceled"] });
    expect(screen.getByRole("button", { name: /start implementation/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /schedule/i })).not.toBeInTheDocument();
  });
});

/**
 * Customer Approval / Customer Review gates. `scheduled` is a manual target in
 * exactly one place: leaving `customer_approval`, where it records the
 * customer's approval. `legalNextStates` stays the single source of truth for
 * which of Close / Send for customer review the Review state offers.
 */
describe("ChangeRequestActionBar — customer approval and customer review gates", () => {
  it("from customer_approval offers 'Record customer approval' (primary) and Cancel", () => {
    const { onAction } = renderBar({
      state: "customer_approval",
      customerApprovalRequired: true,
      legalNextStates: ["scheduled", "canceled"],
    });
    const record = screen.getByRole("button", { name: "Record customer approval" });
    expect(record).toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    // Cancel and Record are the only actions; no Schedule wording.
    expect(screen.queryByText(/^schedule/i)).not.toBeInTheDocument();
    fireEvent.click(record);
    // Sent as a plain PATCH {state:"scheduled"} by the caller.
    expect(onAction).toHaveBeenCalledWith("scheduled");
  });

  it("never offers the customer_approval state itself as an action, even when listed", () => {
    renderBar({
      state: "authorize",
      legalNextStates: ["customer_approval", "canceled"],
    });
    expect(screen.queryByRole("button", { name: /customer approval/i })).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /customer approval/i })).not.toBeInTheDocument();
  });

  it("offers no Record customer approval outside customer_approval, even if scheduled is listed", () => {
    for (const state of ["new", "assess", "authorize", "scheduled", "implement", "review", "customer_review"]) {
      const { container } = renderBar({
        state,
        legalNextStates: ["scheduled"],
      });
      expect(container).toBeEmptyDOMElement();
      expect(screen.queryByText(/record customer approval/i)).not.toBeInTheDocument();
      cleanup();
    }
  });

  it("offers no button or menu item labelled Schedule/Scheduled in any state", () => {
    for (const state of ["new", "assess", "authorize", "customer_approval", "scheduled", "implement", "review", "customer_review"]) {
      renderBar({
        state,
        legalNextStates: ["assess", "scheduled", "implement", "review", "customer_review", "closed", "canceled"],
      });
      expect(screen.queryByRole("button", { name: /schedul/i })).not.toBeInTheDocument();
      const trigger = screen.queryByRole("button", { name: /change state/i });
      if (trigger) {
        fireEvent.click(trigger);
        expect(screen.queryByRole("menuitem", { name: /schedul/i })).not.toBeInTheDocument();
      }
      cleanup();
    }
  });

  it("Review with customer review NOT required offers Close and Cancel, and no customer review", () => {
    renderBar({
      state: "review",
      customerReviewRequired: false,
      legalNextStates: ["closed", "canceled"],
    });
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
    expect(screen.queryByText(/send for customer review/i)).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /customer review/i })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("Review with customer review required offers Send for customer review and Cancel, and no Close", () => {
    renderBar({
      state: "review",
      customerReviewRequired: true,
      legalNextStates: ["customer_review", "canceled"],
    });
    expect(screen.getByRole("button", { name: "Send for customer review" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Close" })).not.toBeInTheDocument();
    openMenu();
    expect(screen.queryByRole("menuitem", { name: /^close$/i })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("customer_review offers Close and Cancel", () => {
    renderBar({
      state: "customer_review",
      customerReviewRequired: true,
      legalNextStates: ["closed", "canceled"],
    });
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
    expect(screen.queryByText(/send for customer review/i)).not.toBeInTheDocument();
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });
});

/**
 * With a live Customer Approval / Customer Review stage the backend offers only
 * `canceled`, so the bar shows Cancel alone; with no customer group (no stage)
 * it keeps offering the manual path. The bar never second-guesses the server.
 */
describe("ChangeRequestActionBar — customer gates with and without a live customer stage", () => {
  it("customer_approval with legalNextStates=[canceled] offers only Cancel (no Record customer approval)", () => {
    renderBar({ state: "customer_approval", customerApprovalRequired: true, legalNextStates: ["canceled"] });
    expect(screen.queryByRole("button", { name: /record customer approval/i })).not.toBeInTheDocument();
    // Cancel is destructive: menu-only, so it is the sole item behind "Change state".
    openMenu();
    expect(screen.getAllByRole("menuitem")).toHaveLength(1);
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("customer_approval fallback legalNextStates=[scheduled, canceled] still offers Record customer approval", () => {
    renderBar({ state: "customer_approval", customerApprovalRequired: true, legalNextStates: ["scheduled", "canceled"] });
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();
  });

  it("customer_review with legalNextStates=[canceled] offers no Close", () => {
    renderBar({ state: "customer_review", customerReviewRequired: true, legalNextStates: ["canceled"] });
    expect(screen.queryByRole("button", { name: /^close$/i })).not.toBeInTheDocument();
    openMenu();
    expect(screen.getAllByRole("menuitem")).toHaveLength(1);
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  });

  it("customer_review fallback legalNextStates=[closed, canceled] offers Close", () => {
    renderBar({ state: "customer_review", customerReviewRequired: true, legalNextStates: ["closed", "canceled"] });
    expect(screen.getByRole("button", { name: /^close$/i })).toBeInTheDocument();
  });
});

/** "Roll back": the failed-review off-ramp, offered from the two review states only. */
describe("ChangeRequestActionBar — Roll back", () => {
  it.each([
    ["review", ["closed", "rollback", "canceled"], /^close$/i],
    ["review", ["customer_review", "rollback", "canceled"], /send for customer review/i],
    ["customer_review", ["closed", "rollback", "canceled"], /^close$/i],
  ])("from %s (%j) offers Roll back as a destructive menu item next to the forward move", (state, legal, forward) => {
    const { onAction } = renderBar({ state, legalNextStates: legal });
    // Exactly one primary button: the forward move, never Roll back.
    expect(screen.getByRole("button", { name: forward })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /roll back/i })).not.toBeInTheDocument();
    openMenu();
    const items = screen.getAllByRole("menuitem").map((i) => i.textContent);
    expect(items).toEqual(["Roll back", "Cancel change"]);
    const item = screen.getByRole("menuitem", { name: /roll back/i });
    // Error colour, same as Cancel change.
    expect(item.querySelector("span")).toHaveStyle({ color: "rgb(211, 47, 47)" });
    fireEvent.click(item);
    expect(onAction).toHaveBeenCalledWith("rollback");
  });

  it("is not offered while only Cancel is legal (a live customer stage), nor from a terminal state", () => {
    renderBar({ state: "customer_review", legalNextStates: ["canceled"] });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    expect(screen.queryByText(/roll back/i)).not.toBeInTheDocument();
    cleanup();
    const { container } = renderBar({ state: "rollback", legalNextStates: [] });
    expect(container).toBeEmptyDOMElement();
  });
});

/**
 * "Re-schedule": `authorize` from Customer Approval -- the planned time changed,
 * so the change goes back through internal approval. A secondary (outlined)
 * button next to the primary move; offered from `customer_approval` only.
 */
describe("ChangeRequestActionBar — Re-schedule", () => {
  it("is an outlined button next to Record customer approval, with Cancel in the menu", () => {
    const { onAction } = renderBar({
      state: "customer_approval",
      legalNextStates: ["scheduled", "authorize", "canceled"],
    });
    const contained = screen.getAllByRole("button").filter((b) => b.className.includes("MuiButton-contained"));
    expect(contained).toHaveLength(1);
    expect(contained[0]).toHaveTextContent("Record customer approval");
    const reschedule = screen.getByRole("button", { name: "Re-schedule" });
    expect(reschedule.className).toContain("MuiButton-outlined");
    openMenu();
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Cancel change"]);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(reschedule);
    expect(onAction).toHaveBeenCalledWith("authorize");
  });

  it("stays on offer while a customer group's approval is pending (Cancel is the only other action)", () => {
    renderBar({ state: "customer_approval", legalNextStates: ["authorize", "canceled"] });
    expect(screen.getByRole("button", { name: "Re-schedule" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();
    openMenu();
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Cancel change"]);
  });

  it("is disabled while a transition is in flight", () => {
    renderBar({ state: "customer_approval", legalNextStates: ["authorize", "canceled"] }, { isPending: true });
    expect(screen.getByRole("button", { name: "Re-schedule" })).toBeDisabled();
  });

  it("is never offered from any other state, even if the backend listed authorize", () => {
    for (const state of [
      "new", "assess", "authorize", "scheduled", "implement", "review", "customer_review",
      "closed", "canceled", "rollback",
    ]) {
      cleanup();
      renderBar({ state, legalNextStates: ["authorize"] });
      expect(screen.queryByRole("button", { name: /re-schedule/i }), state).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /authorize/i }), state).not.toBeInTheDocument();
    }
  });
});
