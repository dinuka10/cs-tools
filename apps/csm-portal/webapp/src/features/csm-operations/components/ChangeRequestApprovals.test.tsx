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
import type { UseQueryResult } from "@tanstack/react-query";
import type { BeChangeRequestApprovalsView } from "@api/backend/types";

const useGetChangeRequestApprovalsMock = vi.fn();
const useCurrentUserMock = vi.fn();
const useDecideChangeRequestApprovalMock = vi.fn();
const showErrorMock = vi.fn();
const decideMutateMock = vi.fn();

// The backend client reads runtime config (`CSM_PORTAL_BACKEND_BASE_URL`) at
// module load, which isn't present under vitest. QueryErrorState imports
// `BackendApiError` from it, so stub the module (same approach as
// CsmAnnouncementsPage.test.tsx).
vi.mock("@api/backend/client", () => ({
  BackendApiError: class BackendApiError extends Error {},
  useBackendApi: () => ({ get: vi.fn(), post: vi.fn() }),
}));

vi.mock("@features/csm-operations/api/useGetChangeRequestApprovals", () => ({
  useGetChangeRequestApprovals: () => useGetChangeRequestApprovalsMock(),
}));

vi.mock("@context/current-user/CurrentUserContext", () => ({
  useCurrentUser: () => useCurrentUserMock(),
}));

vi.mock("@context/error-banner/ErrorBannerContext", () => ({
  useErrorBanner: () => ({ showError: showErrorMock }),
}));

vi.mock("@features/csm-operations/api/useDecideChangeRequestApproval", () => ({
  useDecideChangeRequestApproval: () => useDecideChangeRequestApprovalMock(),
}));

// Imported after the mocks above so the modules pick them up.
import ChangeRequestApprovals from "@features/csm-operations/components/ChangeRequestApprovals";

function mockQueryResult(
  overrides: Partial<UseQueryResult<BeChangeRequestApprovalsView | null, Error>>,
): void {
  useGetChangeRequestApprovalsMock.mockReturnValue({
    data: null,
    isLoading: false,
    isError: false,
    error: null,
    ...overrides,
  });
}

function mockCurrentUser(id?: string): void {
  useCurrentUserMock.mockReturnValue({
    user: id ? { id } : undefined,
    isLoading: false,
    isError: false,
  });
}

function mockDecideMutation(overrides: { isPending?: boolean } = {}): void {
  useDecideChangeRequestApprovalMock.mockReturnValue({
    mutate: decideMutateMock,
    isPending: overrides.isPending ?? false,
  });
}

describe("ChangeRequestApprovals", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockCurrentUser(undefined);
    mockDecideMutation();
  });

  it("renders every approver as its own flat row -- no per-stage grouping or expand/collapse", () => {
    // Real ServiceNow's own Approvers list has no stage concept at all: every
    // approver record for the change request is one row in one flat table.
    // This test locks that in -- both approvers must be visible immediately,
    // with no accordion/disclosure to open first.
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REQUESTED",
            approvers: [
              { id: "a1", name: "Approver One", status: "REQUESTED" },
              { id: "a2", name: "Approver Two", status: "REQUESTED" },
            ],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    expect(screen.getByText("Approver One")).toBeInTheDocument();
    expect(screen.getByText("Approver Two")).toBeInTheDocument();
    // The stage label itself is not shown anywhere -- only the assignment
    // group, per row.
    expect(screen.queryByText("Authorize")).not.toBeInTheDocument();
    expect(screen.getAllByText("Devops Approval")).toHaveLength(2);
  });

  it("shows NOT_REQUIRED approvers inline, flat, like every other row", () => {
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REQUESTED",
            approvers: [
              { id: "a1", name: "Not Needed One", status: "NOT_REQUIRED" },
              { id: "a2", name: "Approved Alice", status: "APPROVED" },
            ],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    expect(screen.getByText("Approved Alice")).toBeInTheDocument();
    expect(screen.getByText("Not Needed One")).toBeInTheDocument();
  });

  it("flattens approvers from multiple approval stages into one table, each carrying its own assignment group", () => {
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Assess",
            approverType: "STATIC_GROUP",
            approverName: "SRE Team",
            status: "APPROVED",
            approvers: [{ id: "a1", name: "Assess Approver", status: "APPROVED" }],
          },
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REQUESTED",
            approvers: [{ id: "a2", name: "Authorize Approver", status: "REQUESTED" }],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    expect(screen.getByText("Assess Approver")).toBeInTheDocument();
    expect(screen.getByText("SRE Team")).toBeInTheDocument();
    expect(screen.getByText("Authorize Approver")).toBeInTheDocument();
    expect(screen.getByText("Devops Approval")).toBeInTheDocument();
  });

  it("renders a friendly fallback for an approver with no name, without an alarming 'unknown' label", () => {
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REQUESTED",
            approvers: [{ id: "no-name-id", name: null, status: "REQUESTED" }],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    expect(screen.getByText("Unnamed approver")).toBeInTheDocument();
    expect(screen.queryByText("Unknown approver")).not.toBeInTheDocument();
  });

  it("shows a dash for comments when the approver has none", () => {
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REQUESTED",
            approvers: [{ id: "a1", name: "No Comment", status: "REQUESTED", comments: null }],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    const row = screen.getByText("No Comment").closest("tr");
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain("—");
  });

  it("shows the approver's own comment text when present", () => {
    mockQueryResult({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "REJECTED",
            approvers: [
              { id: "a1", name: "Commenter", status: "REJECTED", comments: "Needs a rollback plan first" },
            ],
          },
        ],
      },
    });
    render(<ChangeRequestApprovals id="chg-1" />);

    expect(screen.getByText("Needs a rollback plan first")).toBeInTheDocument();
  });

  describe("approval decision action", () => {
    const approvalsWithMyPending = {
      approvals: [
        {
          stage: "Authorize",
          approverType: "STATIC_GROUP" as const,
          approverName: "Devops Approval",
          status: "REQUESTED",
          approvers: [
            { id: "me-id", name: "Current User", status: "REQUESTED" },
            { id: "other-id", name: "Other Approver", status: "REQUESTED" },
          ],
        },
      ],
    };

    it("shows Approve/Reject only on the current user's own pending approval row", () => {
      mockQueryResult({ data: approvalsWithMyPending });
      mockCurrentUser("me-id");
      render(<ChangeRequestApprovals id="chg-1" />);

      expect(screen.getByText("Current User")).toBeInTheDocument();
      expect(screen.getByText("Other Approver")).toBeInTheDocument();
      expect(screen.getAllByText("Approve")).toHaveLength(1);
      expect(screen.getAllByText("Reject")).toHaveLength(1);
    });

    it("hides Approve/Reject when no user is signed in", () => {
      mockQueryResult({ data: approvalsWithMyPending });
      mockCurrentUser(undefined);
      render(<ChangeRequestApprovals id="chg-1" />);

      expect(screen.queryByText("Approve")).not.toBeInTheDocument();
      expect(screen.queryByText("Reject")).not.toBeInTheDocument();
    });

    it("hides Approve/Reject when the current user has no pending approval on this CR", () => {
      mockQueryResult({
        data: {
          approvals: [
            {
              stage: "Authorize",
              approverType: "STATIC_GROUP",
              approverName: "Devops Approval",
              status: "APPROVED",
              approvers: [{ id: "me-id", name: "Current User", status: "APPROVED" }],
            },
          ],
        },
      });
      mockCurrentUser("me-id");
      render(<ChangeRequestApprovals id="chg-1" />);

      expect(screen.queryByText("Approve")).not.toBeInTheDocument();
      expect(screen.queryByText("Reject")).not.toBeInTheDocument();
    });

    it("submits the decision with the CR id when Approve is clicked", () => {
      mockQueryResult({ data: approvalsWithMyPending });
      mockCurrentUser("me-id");
      render(<ChangeRequestApprovals id="chg-1" />);

      fireEvent.click(screen.getByText("Approve"));

      expect(decideMutateMock).toHaveBeenCalledWith(
        { id: "chg-1", decision: "approved" },
        expect.objectContaining({ onError: expect.any(Function) }),
      );
    });

    it("submits the decision with the CR id when Reject is clicked", () => {
      mockQueryResult({ data: approvalsWithMyPending });
      mockCurrentUser("me-id");
      render(<ChangeRequestApprovals id="chg-1" />);

      fireEvent.click(screen.getByText("Reject"));

      expect(decideMutateMock).toHaveBeenCalledWith(
        { id: "chg-1", decision: "rejected" },
        expect.objectContaining({ onError: expect.any(Function) }),
      );
    });

    it("disables Approve/Reject while a decision is in flight", () => {
      mockQueryResult({ data: approvalsWithMyPending });
      mockCurrentUser("me-id");
      mockDecideMutation({ isPending: true });
      render(<ChangeRequestApprovals id="chg-1" />);

      expect(screen.getByText("Approve").closest("button")).toBeDisabled();
      expect(screen.getByText("Reject").closest("button")).toBeDisabled();
    });
  });
});
