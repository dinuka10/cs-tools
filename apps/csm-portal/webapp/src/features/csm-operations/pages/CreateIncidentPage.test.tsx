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

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";

const navigateMock = vi.fn();
const postIncidentMutateMock = vi.fn();
const showErrorMock = vi.fn();

const ME_ID = "11111111-1111-4111-8111-111111111111";
const SERVICE_ID = "22222222-2222-4222-8222-222222222222";

vi.mock("react-router", () => ({
  useNavigate: () => navigateMock,
  useLocation: () => ({ state: undefined }),
}));
vi.mock("@context/error-banner/ErrorBannerContext", () => ({
  useErrorBanner: () => ({ showError: showErrorMock }),
}));
vi.mock("@features/csm-operations/api/usePostIncident", () => ({
  usePostIncident: () => ({ mutate: postIncidentMutateMock, isPending: false }),
}));
// Caller defaults to the signed-in user; give it one so Caller is filled.
vi.mock("@features/settings/api/useGetUsersMe", () => ({
  useGetUsersMe: () => ({ data: { id: ME_ID, firstName: "Test", lastName: "User" } }),
}));
// The real client module reads window.config at load; mock it with a real
// class so `instanceof` still works (same as CreateProblemPage.test.tsx).
vi.mock("@api/backend/client", () => ({
  BackendApiError: class BackendApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
}));
// vi.mock factories are hoisted above this file's top-level declarations, so
// a helper they reference directly has to be hoisted with them.
const { emptySearch } = vi.hoisted(() => ({
  emptySearch: (): { data: never[]; isFetching: boolean; isError: boolean } => ({
    data: [],
    isFetching: false,
    isError: false,
  }),
}));
vi.mock("@api/useSearchItServices", () => ({ useSearchItServices: emptySearch }));
vi.mock("@api/useSearchServiceOfferings", () => ({ useSearchServiceOfferings: emptySearch }));
vi.mock("@api/useSearchConfigurationItems", () => ({ useSearchConfigurationItems: emptySearch }));
vi.mock("@api/useSearchUsersByName", () => ({ useSearchInternalUsersByName: emptySearch }));
// AsyncEntitySelect is a full type-ahead Autocomplete; stub it as a plain
// labeled input that reports its id straight through onChange, same as
// CreateProblemPage.test.tsx. The Service field also hands back the picked
// service, with a support group, as the real one does.
const SUPPORT_GROUP = { id: "33333333-3333-4333-8333-333333333333", name: "MS/PC SRE Group" };
vi.mock("@components/AsyncEntitySelect", () => ({
  default: ({
    label,
    value,
    onChange,
  }: {
    label: string;
    value: string;
    onChange: (next: string, item?: unknown) => void;
  }) => (
    <input
      aria-label={label}
      value={value}
      onChange={(e) =>
        label === "Service"
          ? onChange(e.target.value, { id: e.target.value, supportGroup: SUPPORT_GROUP })
          : onChange(e.target.value)
      }
    />
  ),
}));
vi.mock("@components/AsyncEntityMultiSelect", () => ({
  default: ({ label }: { label: string }) => <input aria-label={label} readOnly />,
}));

// Imported after the mocks above so the module picks them up.
import CreateIncidentPage from "@features/csm-operations/pages/CreateIncidentPage";

const pickOption = (combobox: RegExp, option: string): void => {
  fireEvent.mouseDown(screen.getByRole("combobox", { name: combobox }));
  fireEvent.click(screen.getByRole("option", { name: option }));
};

/** Fills every required field except Subcategory. */
const fillRequiredFields = (): void => {
  fireEvent.change(screen.getByLabelText(/short description/i), {
    target: { value: "Gateway returning 502s" },
  });
  pickOption(/^category/i, "Service Interruption");
  pickOption(/^channel/i, "Email");
  pickOption(/^impact/i, "High");
  pickOption(/^urgency/i, "Low");
  fireEvent.change(screen.getByLabelText("Service"), { target: { value: SERVICE_ID } });
};

const submitButton = (): HTMLElement => screen.getByRole("button", { name: /create incident/i });

describe("CreateIncidentPage subcategory", () => {
  beforeEach(() => {
    navigateMock.mockReset();
    postIncidentMutateMock.mockReset();
    showErrorMock.mockReset();
  });

  it("creates an incident with no subcategory, omitting the field from the payload", () => {
    render(<CreateIncidentPage />);
    fillRequiredFields();

    expect(submitButton()).not.toBeDisabled();
    fireEvent.click(submitButton());

    expect(postIncidentMutateMock).toHaveBeenCalledTimes(1);
    const [payload] = postIncidentMutateMock.mock.calls[0];
    expect(payload).toEqual({
      subject: "Gateway returning 502s",
      category: "SERVICE_INTERRUPTION",
      serviceId: SERVICE_ID,
      contactType: "EMAIL",
      impact: "HIGH",
      urgency: "LOW",
      callerId: ME_ID,
    });
    expect(payload).not.toHaveProperty("subcategory");
  });

  it("still sends the subcategory when one is picked", () => {
    render(<CreateIncidentPage />);
    fillRequiredFields();
    pickOption(/^subcategory/i, "Partial Outage");

    fireEvent.click(submitButton());

    expect(postIncidentMutateMock).toHaveBeenCalledTimes(1);
    expect(postIncidentMutateMock.mock.calls[0][0]).toMatchObject({
      category: "SERVICE_INTERRUPTION",
      subcategory: "PARTIAL_OUTAGE",
    });
  });

  it("does not mark Subcategory as required, while Category still is", () => {
    render(<CreateIncidentPage />);
    // MUI's required FormControl renders an (aria-hidden) asterisk span inside
    // the field's InputLabel; renderSelect gives each label id `${key}-label`.
    const asterisk = (labelId: string): Element | null =>
      document.getElementById(labelId)?.querySelector(".MuiFormLabel-asterisk") ?? null;
    expect(asterisk("category-label")).not.toBeNull();
    expect(document.getElementById("subcategory-label")).not.toBeNull();
    expect(asterisk("subcategory-label")).toBeNull();
  });
});

describe("CreateIncidentPage channel", () => {
  beforeEach(() => {
    postIncidentMutateMock.mockReset();
  });

  it("labels the field Channel (required), with no Contact type field left", () => {
    render(<CreateIncidentPage />);
    expect(screen.getByRole("combobox", { name: /^channel/i })).toBeInTheDocument();
    expect(
      document.getElementById("channel-label")?.querySelector(".MuiFormLabel-asterisk"),
    ).not.toBeNull();
    expect(screen.queryByText(/contact type/i)).not.toBeInTheDocument();
  });

  it("sends the picked channel as the wire field contactType", () => {
    render(<CreateIncidentPage />);
    fillRequiredFields();
    // Overrides fillRequiredFields' "Email" with a value whose ServiceNow
    // key isn't its own name (SITE_247 -> "2").
    pickOption(/^channel/i, "Site 24/7");
    fireEvent.click(submitButton());

    expect(postIncidentMutateMock).toHaveBeenCalledTimes(1);
    expect(postIncidentMutateMock.mock.calls[0][0]).toMatchObject({ contactType: "SITE_247" });
  });

  it("keeps Create disabled until a channel is picked", () => {
    render(<CreateIncidentPage />);
    fireEvent.change(screen.getByLabelText(/short description/i), {
      target: { value: "Gateway returning 502s" },
    });
    pickOption(/^category/i, "Service Interruption");
    pickOption(/^impact/i, "High");
    pickOption(/^urgency/i, "Low");
    fireEvent.change(screen.getByLabelText("Service"), { target: { value: SERVICE_ID } });
    expect(submitButton()).toBeDisabled();

    pickOption(/^channel/i, "Phone");
    expect(submitButton()).not.toBeDisabled();
  });
});

describe("CreateIncidentPage assignment group", () => {
  beforeEach(() => {
    postIncidentMutateMock.mockReset();
  });

  // The backend sets the group from the service; the form only previews it.
  // Sending it too would be a second way to set the same thing, and the
  // backend refuses a create that does.
  it("shows the service's support group but never sends an assignmentGroupId", () => {
    render(<CreateIncidentPage />);
    fillRequiredFields();

    expect(screen.getByLabelText("Assignment group")).toHaveValue(SUPPORT_GROUP.name);
    fireEvent.click(submitButton());

    expect(postIncidentMutateMock).toHaveBeenCalledTimes(1);
    expect(postIncidentMutateMock.mock.calls[0][0]).not.toHaveProperty("assignmentGroupId");
  });
});
