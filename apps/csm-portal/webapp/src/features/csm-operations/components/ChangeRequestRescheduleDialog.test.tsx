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


import type { ComponentProps } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import ChangeRequestRescheduleDialog from "@features/csm-operations/components/ChangeRequestRescheduleDialog";
import { clearUserPreferredTimeZone, setUserPreferredTimeZone } from "@utils/dateTime";
import type { BeChangeRequestDetail } from "@api/backend/types";

const CR: BeChangeRequestDetail = {
  id: "chg-1",
  number: "CHG0009988",
  subject: "Upgrade the gateway cluster",
  createdOn: "2026-01-01T00:00:00Z",
  state: "customer_approval",
  type: "normal",
  plannedStartOn: "2030-03-01 09:00:00",
  plannedEndOn: "2030-03-01 11:00:00",
};

function renderDialog(
  props: Partial<Omit<ComponentProps<typeof ChangeRequestRescheduleDialog>, "cr">> & { cr?: Partial<BeChangeRequestDetail> } = {},
): { onSubmit: ReturnType<typeof vi.fn>; onClose: ReturnType<typeof vi.fn> } {
  const onSubmit = vi.fn();
  const onClose = vi.fn();
  const { cr, ...rest } = props;
  render(
    <ChangeRequestRescheduleDialog
      cr={{ ...CR, ...cr }}
      isSubmitting={false}
      onClose={onClose}
      onSubmit={onSubmit}
      {...rest}
    />,
  );
  return { onSubmit, onClose };
}

// The picker's hidden <input> carries the whole value as text; changing it is
// how a complete date is entered without driving every section.
function pickerInput(label: string): HTMLInputElement {
  const group = screen.getAllByText(label)[0].closest(".MuiFormControl-root") as HTMLElement;
  return group.querySelector("input") as HTMLInputElement;
}

const submitButton = (): HTMLElement => screen.getByRole("button", { name: "Re-schedule" });

describe("ChangeRequestRescheduleDialog", () => {
  beforeEach(() => setUserPreferredTimeZone("UTC"));
  afterEach(() => clearUserPreferredTimeZone());

  it("is prefilled with the current planned window and blocks submitting until one end changes", () => {
    renderDialog();
    expect(pickerInput("Planned start").value).toBe("03/01/2030 09:00 AM");
    expect(pickerInput("Planned end").value).toBe("03/01/2030 11:00 AM");
    expect(submitButton()).toBeDisabled();
    expect(screen.getByText(/change the planned start or end to re-schedule/i)).toBeInTheDocument();
  });

  it("re-entering the current value is not a change", () => {
    renderDialog();
    fireEvent.change(pickerInput("Planned start"), { target: { value: "03/01/2030 09:00 AM" } });
    expect(submitButton()).toBeDisabled();
  });

  it("sends only the changed end as UTC, with state authorize", () => {
    const { onSubmit } = renderDialog();
    fireEvent.change(pickerInput("Planned end"), { target: { value: "03/01/2030 01:00 PM" } });
    expect(submitButton()).toBeEnabled();
    fireEvent.click(submitButton());
    expect(onSubmit).toHaveBeenCalledWith({ state: "authorize", plannedEndOn: "2030-03-01 13:00:00" }, "");
  });

  it("sends both ends and the trimmed optional reason", () => {
    const { onSubmit } = renderDialog();
    fireEvent.change(pickerInput("Planned start"), { target: { value: "03/08/2030 09:00 AM" } });
    fireEvent.change(pickerInput("Planned end"), { target: { value: "03/08/2030 11:00 AM" } });
    fireEvent.change(screen.getByLabelText(/reason \(optional\)/i), { target: { value: "  Customer freeze.  " } });
    fireEvent.click(submitButton());
    expect(onSubmit).toHaveBeenCalledWith(
      { state: "authorize", plannedStartOn: "2030-03-08 09:00:00", plannedEndOn: "2030-03-08 11:00:00" },
      "Customer freeze.",
    );
  });

  it("blocks an end that is not after the start", () => {
    renderDialog();
    fireEvent.change(pickerInput("Planned start"), { target: { value: "03/01/2030 12:00 PM" } });
    expect(screen.getByText(/planned end must be after planned start/i)).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();
  });

  it("shows the backend's refusal verbatim", () => {
    renderDialog({ error: "re-scheduling requires a changed planned start or end" });
    expect(screen.getByRole("alert")).toHaveTextContent("re-scheduling requires a changed planned start or end");
  });

  it("explains a normal change goes back to Authorize for CAB approval", () => {
    renderDialog();
    expect(screen.getByText(/back to Authorize for CAB approval again/i)).toBeInTheDocument();
  });

  it("says ECAB for an emergency change and nothing about Authorize for a standard one", () => {
    renderDialog({ cr: { type: "emergency" } });
    expect(screen.getByText(/ECAB approval again/i)).toBeInTheDocument();
  });

  it("explains a standard change stays in Customer Approval", () => {
    renderDialog({ cr: { type: "standard" } });
    expect(screen.getByText(/stays in Customer Approval/i)).toBeInTheDocument();
    expect(screen.queryByText(/back to Authorize/i)).not.toBeInTheDocument();
  });

  it("closes via Close", () => {
    const { onClose } = renderDialog();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("disables the inputs and both actions while submitting", () => {
    renderDialog({ isSubmitting: true });
    expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" })).toBeDisabled();
    expect(submitButton()).toBeDisabled();
    expect(screen.getByLabelText(/reason \(optional\)/i)).toBeDisabled();
  });

  it("locks the reason once it is recorded, so a retry can't silently drop an edit", () => {
    renderDialog({ reasonRecorded: true });
    expect(screen.getByLabelText(/reason \(optional\)/i)).toBeDisabled();
    expect(
      screen.getByText("Already recorded as a work note — retrying will only re-schedule."),
    ).toBeInTheDocument();
  });

  it("keeps the reason editable until it has been recorded", () => {
    renderDialog({ reasonRecorded: false });
    expect(screen.getByLabelText(/reason \(optional\)/i)).toBeEnabled();
    expect(screen.getByText("Recorded as an internal work note.")).toBeInTheDocument();
  });
});
