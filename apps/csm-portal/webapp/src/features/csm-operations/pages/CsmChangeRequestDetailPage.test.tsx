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

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { useEffect, useState, type JSX } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { UseQueryResult } from "@tanstack/react-query";
import type { BeChangeRequestApproval, BeChangeRequestDetail } from "@api/backend/types";
import { BackendApiError } from "@api/backend/client";
import { CaseTabsProvider, useCaseTabsController } from "@context/case-tabs/CaseTabsContext";
import { CaseTabsBehaviorProvider } from "@context/case-tabs/CaseTabsBehaviorContext";
import { useCaseTabCloseConfirm } from "@features/case-tabs/hooks/useCaseTabCloseConfirm";
import LoggerProvider from "@context/logger/LoggerProvider";
import { clearUserPreferredTimeZone, setUserPreferredTimeZone } from "@utils/dateTime";

const navigateMock = vi.fn();
const useGetChangeRequestMock = vi.fn();
const patchMutateMock = vi.fn();
const patchMutateAsyncMock = vi.fn<(input: unknown) => Promise<unknown>>();
const postCommentMutateAsyncMock = vi.fn<(input: unknown) => Promise<unknown>>();
const patchResetMock = vi.fn();
const showErrorMock = vi.fn();
const editChangeRequestDialogMock = vi.fn();
let patchIsPending = false;
let patchIsError = false;
let patchError: Error | null = null;

// The backend client reads runtime config (`CSM_PORTAL_BACKEND_BASE_URL`) at
// module load, which isn't present under vitest. The page imports
// `BackendApiError` from it directly, so stub the module with a real class
// (so `instanceof` still works) — same approach as CsmIncidentDetailPage.test.tsx.
// `useBackendApi` also has to be stubbed here (not just `BackendApiError`):
// this page's comment-edit/delete wiring goes through the real, unmocked
// `usePatchComment`/`useDeleteComment` (@features/csm-cases/api/useCsmCaseComments),
// which calls `useBackendApi()` unconditionally on every render — leaving it
// undefined throws "No useBackendApi export" the moment the page mounts.
vi.mock("@api/backend/client", () => ({
  BackendApiError: class BackendApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
  useBackendApi: () => ({ get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() }),
}));

vi.mock("@hooks/useNavTransition", () => ({
  useNavTransition: () => navigateMock,
}));
vi.mock("@context/error-banner/ErrorBannerContext", () => ({
  useErrorBanner: () => ({ showError: showErrorMock }),
}));
// `usePortalAccess()` (for `canDownloadAttachment`) pulls in `useCurrentUser`
// -> `CurrentUserContext` -> `useGetUsersMe`, which reads `@config/apiConfig`
// at module load — unavailable under vitest (see this repo's own testing
// conventions). Mocking `CurrentUserContext` directly short-circuits that
// chain before it ever reaches `apiConfig`, same approach as
// CsmIncidentDetailPage.test.tsx.
let mockCurrentUser: { id: string; email: string } = {
  id: "00000000-0000-0000-0000-00000000000c",
  email: "jane.doe@example.com",
};
vi.mock("@context/current-user/CurrentUserContext", () => ({
  useCurrentUser: () => ({
    user: mockCurrentUser,
    isLoading: false,
    isError: false,
    error: null,
  }),
}));
// The lifecycle describe at the bottom of this file drives a stateful fake
// backend through these same hook mocks: whenever it changes the fake CR it
// calls `notifyFakeBackendChanged()`, which re-renders every mounted consumer of
// the mocked query hooks (standing in for react-query's refetch-after-invalidate).
const fakeBackendListeners = new Set<() => void>();
function notifyFakeBackendChanged(): void {
  act(() => fakeBackendListeners.forEach((l) => l()));
}
function useFakeBackendTick(): void {
  const [, setTick] = useState(0);
  useEffect(() => {
    const listener = (): void => setTick((n) => n + 1);
    fakeBackendListeners.add(listener);
    return () => {
      fakeBackendListeners.delete(listener);
    };
  }, []);
}
vi.mock("@features/csm-operations/api/useGetChangeRequest", () => ({
  useGetChangeRequest: () => {
    useFakeBackendTick();
    return useGetChangeRequestMock();
  },
}));
const useGetChangeRequestApprovalsMock = vi.fn();
vi.mock("@features/csm-operations/api/useGetChangeRequestApprovals", () => ({
  useGetChangeRequestApprovals: () => {
    useFakeBackendTick();
    return useGetChangeRequestApprovalsMock();
  },
}));
// The group page opened from an Assignment group on the Approval tab.
const useGroupDetailMock = vi.fn();
vi.mock("@features/csm-operations/api/useGroupDetail", () => ({
  useGroupDetail: (id: string | undefined) => useGroupDetailMock(id),
}));
const decideApprovalMutateMock = vi.fn();
vi.mock("@features/csm-operations/api/useDecideChangeRequestApproval", () => ({
  useDecideChangeRequestApproval: () => ({ mutate: decideApprovalMutateMock, isPending: false }),
}));
vi.mock("@features/csm-operations/api/usePatchChangeRequest", () => ({
  usePatchChangeRequest: () => ({
    mutate: patchMutateMock,
    mutateAsync: patchMutateAsyncMock,
    reset: patchResetMock,
    isPending: patchIsPending,
    isError: patchIsError,
    error: patchError,
  }),
}));
const approvalsPanelMock = vi.fn();
// The real approvals panel, wrapped so tests can also inspect the props the
// page hands it (e.g. `isCreator`). Its data comes from the mocked
// `useGetChangeRequestApprovals` above, so it renders "No approval stages" in
// every test that doesn't set approvals.
vi.mock("@features/csm-operations/components/ChangeRequestApprovals", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@features/csm-operations/components/ChangeRequestApprovals")>();
  const Actual = actual.default;
  return {
    default: (props: { id: string | undefined; isCreator?: boolean }) => {
      approvalsPanelMock(props);
      return <Actual {...props} />;
    },
  };
});
// Exercised in isolation by EditChangeRequestDialog.test.tsx; here we only
// assert this page wires `saveError` and resets the mutation before opening.
vi.mock("@features/csm-operations/components/EditChangeRequestDialog", () => ({
  default: (props: unknown) => {
    editChangeRequestDialogMock(props);
    return null;
  },
}));
vi.mock("@features/csm-operations/api/useCsmChangeRequestComments", () => ({
  useGetCsmChangeRequestComments: () => ({ data: [] }),
  usePostCsmChangeRequestComment: () => ({
    isPending: false,
    mutate: vi.fn(),
    mutateAsync: postCommentMutateAsyncMock,
  }),
}));
vi.mock("@features/csm-cases/api/useCsmCaseAttachments", () => ({
  useGetCsmCaseAttachments: () => ({ data: [] }),
  usePostCsmCaseAttachment: () => ({ isPending: false, mutate: vi.fn() }),
  useDownloadCsmCaseAttachment: () => vi.fn(),
  // Only reached by the reply composer's upload modal (`CsmUploadAttachmentModal`),
  // not exercised by this file's existing tests — the "reports its own draft
  // state" tests below are the first to actually mount the composer.
  MAX_ATTACHMENT_SIZE_BYTES: 10 * 1024 * 1024,
}));
vi.mock("@features/csm-cases/components/CaseActivitiesFeed", () => ({
  default: () => null,
}));
vi.mock("@features/csm-cases/components/CaseDetailWidgets", () => ({
  AttachmentsWidget: () => null,
}));

// Imported after the mocks above so the module picks them up.
import CsmChangeRequestDetailPage from "@features/csm-operations/pages/CsmChangeRequestDetailPage";

const BASE_CR: BeChangeRequestDetail = {
  id: "chg-1",
  number: "CHG0009988",
  subject: "Upgrade the gateway cluster",
  case: { id: "case-1", name: "CASE0001234" },
  createdOn: "2026-01-01T00:00:00Z",
  state: "new",
  type: "normal",
  assignedTeam: { id: "team-1", name: "Platform" },
};

function mockQueryResult(
  overrides: Partial<UseQueryResult<BeChangeRequestDetail | null, Error>>,
): void {
  useGetChangeRequestMock.mockReturnValue({
    data: null,
    isLoading: false,
    isError: false,
    error: null,
    ...overrides,
  });
}

beforeEach(() => {
  mockCurrentUser = { id: "00000000-0000-0000-0000-00000000000c", email: "jane.doe@example.com" };
  decideApprovalMutateMock.mockReset();
  navigateMock.mockClear();
  patchMutateMock.mockClear();
  showErrorMock.mockClear();
  patchIsPending = false;
  patchIsError = false;
  patchError = null;
  patchResetMock.mockClear();
  editChangeRequestDialogMock.mockClear();
  approvalsPanelMock.mockClear();
  useGroupDetailMock.mockReset();
  patchMutateAsyncMock.mockReset();
  patchMutateAsyncMock.mockResolvedValue({ id: "chg-1" });
  postCommentMutateAsyncMock.mockReset();
  postCommentMutateAsyncMock.mockResolvedValue({ id: "comment-1" });
  useGetChangeRequestApprovalsMock.mockReturnValue({
    data: null,
    isLoading: false,
    isError: false,
    error: null,
  });
});

/** Surfaces the router's current search string, for the `?tab=` sync tests
 * below. */
function LocationSearchProbe(): JSX.Element {
  const location = useLocation();
  return <div data-testid="search-probe">{location.search}</div>;
}

/**
 * Real `<MemoryRouter>`/`<Routes>` (not a mocked `react-router`) — matches
 * this app's own convention for a hook/page that reads the router itself,
 * and `useQueryParamTabs` needs a real `useSearchParams` to actually
 * read/write the URL.
 */
function renderPage(
  initialEntry = "/operations/change-requests/chg-1",
): ReturnType<typeof render> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route
            path="/operations/change-requests/:id"
            element={
              <>
                <CsmChangeRequestDetailPage />
                <LocationSearchProbe />
              </>
            }
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("CsmChangeRequestDetailPage", () => {
  it("renders the linked case as a clickable reference to the case route", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage();

    screen
      .getByText("CASE0001234")
      .closest('[role="button"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    expect(navigateMock).toHaveBeenCalledWith("/cases/case-1");
  });

  it("renders a dash for the linked case when there is no case reference", () => {
    mockQueryResult({ data: { ...BASE_CR, case: null } });
    renderPage();
    const linkedCaseCell = screen.getByText("Linked case").parentElement!;
    expect(within(linkedCaseCell).getByText("—")).toBeInTheDocument();
  });

  it("shows the Impact meta cell in the Overview grid, alongside the header chip", () => {
    mockQueryResult({ data: { ...BASE_CR, impact: "high" } });
    renderPage();
    const impactCell = screen.getByText("Impact").parentElement!;
    expect(within(impactCell).getByText("High")).toBeInTheDocument();
  });

  it("shows a dash for Impact in the Overview grid when unset", () => {
    mockQueryResult({ data: { ...BASE_CR, impact: undefined } });
    renderPage();
    const impactCell = screen.getByText("Impact").parentElement!;
    expect(within(impactCell).getByText("—")).toBeInTheDocument();
  });

  it("renders the lifecycle stepper for this CR's state", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "implement" } });
    renderPage();
    expect(screen.getByRole("list", { name: /change request lifecycle/i })).toBeInTheDocument();
  });
});

describe("CsmChangeRequestDetailPage — project, deployments, deployment products, customer group, category", () => {
  const cell = (label: string): HTMLElement => screen.getByText(label).parentElement!;
  const SCOPED = {
    ...BASE_CR,
    project: { id: "proj-a", name: "Acme Project" },
    deployments: [
      { id: "dep-prod", name: "Acme Production" },
      { id: "dep-stg", name: "Acme Staging" },
    ],
    deploymentProducts: [
      { id: "dp-apim", name: "API Manager 4.3.0" },
      { id: "dp-is", name: "Identity Server 7.0.0" },
    ],
    customerContacts: [
      { id: "pc-1", name: "Alice Aaron" },
      { id: "pc-2", name: "Bob Bell" },
    ],
    category: "devops",
  };

  it("shows each of them in the Overview", () => {
    mockQueryResult({ data: SCOPED });
    renderPage();
    expect(within(cell("Customer Project")).getByText("Acme Project")).toBeInTheDocument();
    const deployments = within(cell("Deployments"));
    expect(deployments.getByText("Acme Production")).toBeInTheDocument();
    expect(deployments.getByText("Acme Staging")).toBeInTheDocument();
    const products = within(cell("Deployment products"));
    expect(products.getByText("API Manager 4.3.0")).toBeInTheDocument();
    expect(products.getByText("Identity Server 7.0.0")).toBeInTheDocument();
    // The Customer Group is the project's registered contacts, read-only.
    const group = within(cell("Customer group"));
    expect(group.getByText("Alice Aaron")).toBeInTheDocument();
    expect(group.getByText("Bob Bell")).toBeInTheDocument();
    expect(within(cell("Category")).getByText("DevOps")).toBeInTheDocument();
  });

  it("shows a dash for each when the change request has none", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        project: undefined,
        deployments: [],
        deploymentProducts: [],
        customerContacts: [],
        category: null,
      },
    });
    renderPage();
    for (const label of [
      "Customer Project",
      "Deployments",
      "Deployment products",
      "Customer group",
      "Category",
    ]) {
      expect(within(cell(label)).getByText("—")).toBeInTheDocument();
    }
  });

  it("shows a dash when the backend omits the lists entirely (an older response)", () => {
    mockQueryResult({
      data: { ...BASE_CR, deployments: undefined, deploymentProducts: undefined, customerContacts: undefined },
    });
    renderPage();
    expect(within(cell("Deployments")).getByText("—")).toBeInTheDocument();
    expect(within(cell("Deployment products")).getByText("—")).toBeInTheDocument();
    expect(within(cell("Customer group")).getByText("—")).toBeInTheDocument();
  });

  it("has no Environments field", () => {
    mockQueryResult({ data: SCOPED });
    renderPage();
    expect(screen.queryByText("Environments")).not.toBeInTheDocument();
  });

  it("reads the category from an entity-ref response too", () => {
    mockQueryResult({ data: { ...SCOPED, category: { id: "regular_release_cloud", name: "Regular Release - Cloud" } } });
    renderPage();
    expect(within(cell("Category")).getByText("Regular Release - Cloud")).toBeInTheDocument();
  });

  it("lists each only once: Category and Customer group are not repeated under SRE details", () => {
    mockQueryResult({ data: SCOPED });
    renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /plan/i }));
    expect(screen.getByText("SRE details")).toBeInTheDocument();
    expect(screen.getAllByText("Category")).toHaveLength(1);
    expect(screen.getAllByText("Customer group")).toHaveLength(1);
    expect(screen.getAllByText("Deployments")).toHaveLength(1);
    expect(screen.getAllByText("Deployment products")).toHaveLength(1);
  });
});

describe("CsmChangeRequestDetailPage — blocking-reason header note", () => {
  it("shows 'Awaiting Peer Approval' when the Assess stage is pending or requested", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "assess" } });
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: {
        approvals: [
          {
            stage: "Assess",
            approverType: "STATIC_GROUP",
            approverName: null,
            status: "REQUESTED",
            approvers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    });
    renderPage();
    expect(screen.getByText("Awaiting Peer Approval")).toBeInTheDocument();
  });

  it("names the CAB stage by its stage label, not the approver group, and never doubles 'approval'", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "authorize" } });
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: {
        approvals: [
          {
            stage: "Authorize",
            approverType: "STATIC_GROUP",
            approverName: "Devops Approval",
            status: "PENDING",
            approvers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    });
    renderPage();
    expect(screen.getByText("Awaiting CAB Approval")).toBeInTheDocument();
  });

  it("shows no blocking-reason note when no stage is pending/requested", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "authorize" } });
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: {
        approvals: [
          {
            stage: "Assess",
            approverType: "STATIC_GROUP",
            approverName: null,
            status: "APPROVED",
            approvers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    });
    renderPage();
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
  });

  it("suppresses the blocking-reason note once the CR is closed, even with stale pending approval data", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "closed" } });
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: {
        approvals: [
          {
            stage: "Assess",
            approverType: "STATIC_GROUP",
            approverName: null,
            status: "REQUESTED",
            approvers: [],
          },
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    });
    renderPage();
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
  });
});

describe("CsmChangeRequestDetailPage — tab lives in the URL", () => {
  it("defaults to the Approval tab when ?tab= is absent", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage();

    expect(screen.getByRole("tab", { name: /approval/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("writes the selected tab to ?tab= when switching tabs", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage();

    fireEvent.click(screen.getByRole("tab", { name: /attachments/i }));

    expect(screen.getByTestId("search-probe")).toHaveTextContent("tab=attachments");
  });

  it("restores the tab named in the URL on a direct/cold load", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage("/operations/change-requests/chg-1?tab=comments");

    expect(screen.getByRole("tab", { name: /comments/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("falls back to Approval for an unrecognised ?tab= value, without crashing", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage("/operations/change-requests/chg-1?tab=not-a-real-tab");

    expect(screen.getByRole("tab", { name: /approval/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});

describe("CsmChangeRequestDetailPage — Clone", () => {
  it("navigates to the create form with router state built from this record", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        description: "<p>Upgrade the gateway.</p>",
        impact: "high",
        assignedEngineer: { id: "user-1", name: "Jane Doe" },
      },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /clone/i }));
    expect(navigateMock).toHaveBeenCalledWith(
      "/operations/change-requests/new",
      expect.objectContaining({
        state: expect.objectContaining({
          sourceNumber: "CHG0009988",
          subject: "Upgrade the gateway cluster",
          type: "normal",
          impact: "high",
          assignedEngineerId: "user-1",
          assignedEngineerLabel: "Jane Doe",
        }),
      }),
    );
  });

  it("carries the project and category into the clone, but not the deployments / products / customer group", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        project: { id: "proj-a", name: "Acme Project" },
        customerContacts: [{ id: "pc-1", name: "Alice Aaron" }],
        category: "devops",
        deployments: [{ id: "dep-prod", name: "Acme Production" }],
        deploymentProducts: [{ id: "dp-apim", name: "API Manager 4.3.0" }],
      },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /clone/i }));
    const [, options] = navigateMock.mock.calls[0];
    expect(options.state).toMatchObject({
      projectId: "proj-a",
      projectLabel: "Acme Project",
      category: "devops",
    });
    const keys = Object.keys(options.state);
    expect(keys).not.toContain("deployments");
    expect(keys).not.toContain("customerContacts");
    expect(keys).not.toContain("customerGroupId");
    expect(keys).not.toContain("environments");
    expect(keys).not.toContain("deploymentProducts");
  });

  it("never puts the deployment, state, or approval fields into the clone's router state", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        deployment: { id: "dep-1", name: "prod" },
        state: "closed",
        hasCustomerApproved: true,
      },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /clone/i }));
    const [, options] = navigateMock.mock.calls[0];
    const keys = Object.keys(options.state);
    expect(keys).not.toContain("deployment");
    expect(keys).not.toContain("state");
    expect(keys).not.toContain("hasCustomerApproved");
  });
});

describe("CsmChangeRequestDetailPage — Request Approval (New -> Assess)", () => {
  it("shows the Request Approval button when the backend flags 'assess' as a legal next state", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    expect(
      screen.getByRole("button", { name: /request approval/i }),
    ).toBeInTheDocument();
  });

  it("hides the button when legalNextStates is empty (no transition available)", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: [] } });
    renderPage();
    expect(
      screen.queryByRole("button", { name: /request approval/i }),
    ).not.toBeInTheDocument();
  });

  it("hides the button when legalNextStates is absent — data-driven, no hardcoded state check", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: undefined } });
    renderPage();
    expect(
      screen.queryByRole("button", { name: /request approval/i }),
    ).not.toBeInTheDocument();
  });

  /**
   * New -> Assess is compulsorily gated on an assigned team, by explicit
   * product decision: the team's own members become the Assess-stage
   * approvers the moment the transition lands, so there is no valid way to
   * enter Assess with no team to assign that stage to. The backend itself
   * rejects this PATCH with no team regardless of what the button does --
   * this is the UI-side half that keeps the click from round-tripping into
   * that rejection.
   */
  it("shows a disabled Request Approval button when the state allows it but there is no assigned team", () => {
    mockQueryResult({
      data: { ...BASE_CR, legalNextStates: ["assess"], assignedTeam: null },
    });
    renderPage();
    const button = screen.getByRole("button", { name: /request approval/i });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(patchMutateMock).not.toHaveBeenCalled();
  });

  it("exposes the blocked reason to keyboard users via a focusable, labeled wrapper", () => {
    mockQueryResult({
      data: { ...BASE_CR, legalNextStates: ["assess"], assignedTeam: null },
    });
    renderPage();
    const button = screen.getByRole("button", { name: /request approval/i });
    const focusTarget = button.closest('[tabindex="0"]');
    expect(focusTarget).not.toBeNull();
    expect(focusTarget).toHaveAttribute(
      "aria-label",
      "Request Approval: Set an assigned team before requesting approval",
    );
  });

  it("leaves Request Approval enabled when both the state and the assigned team allow it", () => {
    mockQueryResult({
      data: { ...BASE_CR, legalNextStates: ["assess"], assignedTeam: { id: "team-1", name: "Platform" } },
    });
    renderPage();
    expect(
      screen.getByRole("button", { name: /request approval/i }),
    ).toBeEnabled();
  });

  it("PATCHes { state: \"assess\" } for this CR when clicked", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    expect(patchMutateMock).toHaveBeenCalledWith(
      { id: "chg-1", patch: { state: "assess" } },
      expect.objectContaining({ onError: expect.any(Function) }),
    );
  });

  it("surfaces a mutation error via the shared error banner", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    const [, options] = patchMutateMock.mock.calls[0];
    const err = new Error("boom");
    options.onError(err);
    expect(showErrorMock).toHaveBeenCalledWith(
      "Could not move this change request to Assess.",
      err,
    );
  });

  it("surfaces the backend's real rejection reason for a 4xx state-transition error", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    const [, options] = patchMutateMock.mock.calls[0];
    const err = new BackendApiError(409, "State transition rejected: approver required");
    options.onError(err);
    expect(showErrorMock).toHaveBeenCalledWith(
      "State transition rejected: approver required",
      err,
    );
  });

  it("falls back to the generic message for a 5xx error even with a body message", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    const [, options] = patchMutateMock.mock.calls[0];
    const err = new BackendApiError(500, "internal error detail");
    options.onError(err);
    expect(showErrorMock).toHaveBeenCalledWith(
      "Could not move this change request to Assess.",
      err,
    );
  });
});

describe("CsmChangeRequestDetailPage — Request Approval flow: no Schedule, creator rules", () => {
  it("never shows a Schedule button, even if the backend still lists scheduled", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "authorize", legalNextStates: ["scheduled", "canceled"] },
    });
    renderPage();
    expect(screen.queryByRole("button", { name: /schedule/i })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.queryByRole("menuitem", { name: /schedule/i })).not.toBeInTheDocument();
  });

  it("shows Scheduled with no Schedule button once CAB approval has moved the CR there", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "scheduled", legalNextStates: ["implement", "canceled"] },
    });
    renderPage();
    expect(screen.getAllByText("Scheduled").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /schedule$/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /start implementation/i })).toBeInTheDocument();
  });

  it("tells the approvals panel the signed-in user is the creator when they are the requester", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        state: "assess",
        requestedBy: { id: "00000000-0000-0000-0000-00000000000c", name: "Jane Doe" },
      },
    });
    renderPage();
    expect(approvalsPanelMock).toHaveBeenCalledWith(expect.objectContaining({ isCreator: true }));
  });

  it("recognises the creator from createdBy matching their email", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "assess", createdBy: "Jane.Doe@example.com" } });
    renderPage();
    expect(approvalsPanelMock).toHaveBeenCalledWith(expect.objectContaining({ isCreator: true }));
  });

  it("does not flag a different user as the creator", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "assess", requestedBy: { id: "someone-else", name: "Other" }, createdBy: "other@example.com" },
    });
    renderPage();
    expect(approvalsPanelMock).toHaveBeenCalledWith(expect.objectContaining({ isCreator: false }));
  });

  it("still lets the creator Cancel the change request", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        state: "assess",
        requestedBy: { id: "00000000-0000-0000-0000-00000000000c", name: "Jane Doe" },
        legalNextStates: ["authorize", "canceled"],
      },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeEnabled();
  });
});

describe("CsmChangeRequestDetailPage — Edit dialog error wiring", () => {
  it("resets the shared mutation before opening the Edit dialog, so a stale error from elsewhere isn't shown as this save's error", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    expect(patchResetMock).toHaveBeenCalled();
  });

  it("passes no saveError to the dialog when the mutation hasn't failed", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    const [props] = editChangeRequestDialogMock.mock.calls.at(-1)!;
    expect(props.saveError).toBeNull();
  });

  it("passes the backend's rejection reason as saveError for a 4xx failure", () => {
    patchIsError = true;
    patchError = new BackendApiError(
      400,
      "isCustomerApproved, isCustomerReviewed, and requestApproval are mutually exclusive",
    );
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    const [props] = editChangeRequestDialogMock.mock.calls.at(-1)!;
    expect(props.saveError).toBe(
      "isCustomerApproved, isCustomerReviewed, and requestApproval are mutually exclusive",
    );
  });

  it("falls back to a generic saveError for a 5xx failure", () => {
    patchIsError = true;
    patchError = new BackendApiError(500, "internal error detail");
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    const [props] = editChangeRequestDialogMock.mock.calls.at(-1)!;
    expect(props.saveError).toBe("Could not update the change request.");
  });
});

// ---------------------------------------------------------------------------
// Lifecycle transitions. `ChangeRequestActionBar` is exercised in isolation by
// its own test; these cover this page's half of the contract — which patch
// each target produces, and the comment-then-patch ordering the destructive
// ones go through.
// ---------------------------------------------------------------------------

/** Open the action bar's overflow menu. */
function openStateMenu(): void {
  fireEvent.click(screen.getByRole("button", { name: /change state/i }));
}

describe("CsmChangeRequestDetailPage — direct (non-destructive) transitions", () => {
  it("PATCHes { state: target } for a forward move that is not New -> Assess", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "scheduled", legalNextStates: ["implement"] },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /start implementation/i }));
    expect(patchMutateMock).toHaveBeenCalledWith(
      { id: "chg-1", patch: { state: "implement" } },
      expect.objectContaining({ onError: expect.any(Function) }),
    );
  });

  it("sends a plain state PATCH for New -> Assess, same as every other transition", () => {
    mockQueryResult({ data: { ...BASE_CR, legalNextStates: ["assess"] } });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /request approval/i }));
    const [{ patch }] = patchMutateMock.mock.calls[0];
    expect(patch).toEqual({ state: "assess" });
  });

  it("sends a state the backend added verbatim, with no frontend change", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "review", legalNextStates: ["awaiting_vendor"] },
    });
    renderPage();
    openStateMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /^awaiting vendor$/i }));
    expect(patchMutateMock).toHaveBeenCalledWith(
      { id: "chg-1", patch: { state: "awaiting_vendor" } },
      expect.anything(),
    );
  });

  it("surfaces the backend's real 4xx rejection reason for a transition", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "scheduled", legalNextStates: ["implement"] },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /start implementation/i }));
    const [, options] = patchMutateMock.mock.calls[0];
    const err = new BackendApiError(409, "Change window has not opened yet");
    options.onError(err);
    expect(showErrorMock).toHaveBeenCalledWith("Change window has not opened yet", err);
  });

  it("falls back to a target-specific generic message for a 5xx", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "scheduled", legalNextStates: ["implement"] },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /start implementation/i }));
    const [, options] = patchMutateMock.mock.calls[0];
    const err = new BackendApiError(500, "internal error detail");
    options.onError(err);
    expect(showErrorMock).toHaveBeenCalledWith(
      "Could not move this change request to Implement.",
      err,
    );
  });
});

describe("CsmChangeRequestDetailPage — destructive transitions need a reason first", () => {
  /** Render a CR that can be canceled, and open the confirmation dialog. */
  function openCancelDialog(): void {
    mockQueryResult({
      data: { ...BASE_CR, state: "implement", legalNextStates: ["review", "canceled"] },
    });
    renderPage();
    openStateMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /cancel change/i }));
  }

  function typeReason(text: string): void {
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: text } });
  }

  it("opens the confirmation dialog instead of patching immediately", () => {
    openCancelDialog();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(patchMutateMock).not.toHaveBeenCalled();
    expect(patchMutateAsyncMock).not.toHaveBeenCalled();
  });

  it("posts the reason as a comment BEFORE patching the state", async () => {
    openCancelDialog();
    typeReason("Latency regression in production.");
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));

    await waitFor(() => expect(patchMutateAsyncMock).toHaveBeenCalled());
    expect(postCommentMutateAsyncMock).toHaveBeenCalledWith({
      changeRequestId: "chg-1",
      // Posted verbatim: the backing store for these notes is plain text.
      bodyHtml: "Latency regression in production.",
      internal: true,
    });
    expect(patchMutateAsyncMock).toHaveBeenCalledWith({
      id: "chg-1",
      patch: { state: "canceled" },
    });
    // Ordering, not just co-occurrence: an unexplained cancellation is worse
    // than a failed one, so the comment must land first.
    expect(
      postCommentMutateAsyncMock.mock.invocationCallOrder[0],
    ).toBeLessThan(patchMutateAsyncMock.mock.invocationCallOrder[0]);
  });

  it("closes the dialog once both halves succeed", async () => {
    openCancelDialog();
    typeReason("Latency regression in production.");
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("does NOT patch the state when the reason comment fails", async () => {
    postCommentMutateAsyncMock.mockRejectedValueOnce(
      new BackendApiError(403, "Comments are disabled on this change request"),
    );
    openCancelDialog();
    typeReason("Latency regression in production.");
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        /comments are disabled on this change request/i,
      ),
    );
    expect(patchMutateAsyncMock).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("tells the engineer the reason was recorded when only the state change failed", async () => {
    patchMutateAsyncMock.mockRejectedValueOnce(
      new BackendApiError(409, "Cancellation is not permitted from this state"),
    );
    openCancelDialog();
    typeReason("Latency regression in production.");
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        /reason was recorded as a comment, but the state did not change/i,
      ),
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      /cancellation is not permitted from this state/i,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/don't need to retype it/i);
  });

  it("retrying after a failed patch re-sends only the state change, never the comment twice", async () => {
    patchMutateAsyncMock.mockRejectedValueOnce(new BackendApiError(409, "Rejected"));
    openCancelDialog();
    typeReason("Latency regression in production.");
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));
    await waitFor(() => expect(patchMutateAsyncMock).toHaveBeenCalledTimes(2));
    expect(postCommentMutateAsyncMock).toHaveBeenCalledTimes(1);
  });

  it("posts the reason verbatim as plain text, with no markup added around it", async () => {
    // The backing store for these notes is a plain-text field: production
    // entries carry raw newlines and no escaped entities, so anything the
    // portal wraps in markup shows up as literal tags at the source. What the
    // engineer typed is what gets written, character for character.
    const typed = "Latency < 50ms breached & customer impacted.\nCanceled by Jane Doe.";
    openCancelDialog();
    typeReason(typed);
    fireEvent.click(screen.getByRole("button", { name: /^cancel change$/i }));

    await waitFor(() => expect(postCommentMutateAsyncMock).toHaveBeenCalled());
    const posted = postCommentMutateAsyncMock.mock.calls[0][0] as {
      bodyHtml: string;
    };
    expect(posted.bodyHtml).toBe(typed);
  });

  it("routes the cancel transition through the same dialog", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "scheduled", legalNextStates: ["implement", "canceled"] },
    });
    renderPage();
    openStateMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /cancel change/i }));
    expect(
      screen.getByRole("heading", { name: /cancel this change request/i }),
    ).toBeInTheDocument();
    expect(patchMutateAsyncMock).not.toHaveBeenCalled();
  });
});

/**
 * Wraps the real page in a real open case-tab (`CaseTabsProvider` +
 * `useCaseTabCloseConfirm`), exposing an "open"/"close-this-tab" trigger —
 * for the `hasDraft`/close-confirm regression test below, which needs the
 * real `useReportCaseTabDraft` wiring inside the page to actually reach the
 * tab strip's own close-confirm dialog, not just a mocked stand-in for it.
 */
function CloseTabHarness({ caseId }: { caseId: string }): JSX.Element {
  const { openTab, tabs } = useCaseTabsController();
  const { requestClose, dialog } = useCaseTabCloseConfirm();
  return (
    <div>
      <button
        onClick={() =>
          openTab(caseId, "change_request", `/operations/change-requests/${caseId}`)
        }
      >
        open-tab
      </button>
      <button
        onClick={() => {
          const tab = tabs.find((t) => t.caseId === caseId);
          if (tab) requestClose(tab);
        }}
      >
        close-tab
      </button>
      {dialog}
    </div>
  );
}

function renderPageWithOpenTab(
  initialEntry = "/operations/change-requests/chg-1",
): ReturnType<typeof render> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <LoggerProvider>
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <CaseTabsBehaviorProvider>
            <CaseTabsProvider>
              <CloseTabHarness caseId="chg-1" />
              <Routes>
                <Route
                  path="/operations/change-requests/:id"
                  element={<CsmChangeRequestDetailPage />}
                />
              </Routes>
            </CaseTabsProvider>
          </CaseTabsBehaviorProvider>
        </MemoryRouter>
      </QueryClientProvider>
    </LoggerProvider>,
  );
}

describe("CsmChangeRequestDetailPage — reports its own draft state to the tab strip", () => {
  // Regression test for bug: this page only called `useReportCaseTabMeta`,
  // not `useReportCaseTabDraft` (unlike `CsmCaseDetailPage`, which calls
  // both) — its tab's `hasDraft` never became `true`, so closing a change
  // request's tab with a reply half-written skipped the discard-confirm
  // dialog entirely, unlike a case tab in the same situation.
  it("closing this change request's tab with an open (unsent) reply asks for confirmation, same as a case tab does", () => {
    localStorage.setItem("csm.caseTabs.enabled", "1");
    mockQueryResult({ data: BASE_CR });
    renderPageWithOpenTab();

    fireEvent.click(screen.getByText("open-tab"));
    // The reply composer only renders on the Comments tab — "approval" is
    // this page's own default.
    fireEvent.click(screen.getByRole("tab", { name: /comments/i }));
    fireEvent.click(screen.getByText("Add a comment…"));

    fireEvent.click(screen.getByText("close-tab"));
    expect(screen.getByText("Close this case tab?")).toBeInTheDocument();
  });

  it("closing this change request's tab with no reply open closes it immediately, without confirming", () => {
    localStorage.setItem("csm.caseTabs.enabled", "1");
    mockQueryResult({ data: BASE_CR });
    renderPageWithOpenTab();

    fireEvent.click(screen.getByText("open-tab"));
    fireEvent.click(screen.getByText("close-tab"));
    expect(screen.queryByText("Close this case tab?")).not.toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Lifecycle verification per change type
//
//   Normal    New -> Request Approval -> Assess [Peer Approval]
//                 -> Authorize [CAB Approval] -> (auto) Scheduled
//                 -> Implement -> Review -> Closed
//   Emergency New -> Request Approval -> Authorize [ECAB Approval only]
//                 -> (auto) Scheduled
//   Standard  New -> Request Approval -> (auto) Scheduled, no approvals
//
// A small stateful fake of the backend contract sits behind the mocked
// hooks above: the page's own PATCH / approve calls mutate it, and the page is
// re-rendered from it, so every assertion is on what the user actually sees
// after each step. The contract assumed: `legalNextStates` never lists
// `authorize`, and lists `scheduled` only from `customer_approval` (where it
// records the customer's approval); Request Approval is
// `PATCH {state:"assess"}`; approving Peer adds a CAB stage; approving
// CAB/ECAB (or Standard's Request Approval) moves the CR to `customer_approval`
// when `customerApprovalRequired`, else straight to `scheduled`; Review offers
// `customer_review` when `customerReviewRequired`, else `closed`;
// `customer_review` -> `closed`.
// ---------------------------------------------------------------------------

const LC_CREATOR = { id: "u-creator", email: "casey@example.com", name: "Casey Creator" };
const LC_PEER = { id: "u-peer", email: "pat@example.com", name: "Pat Peer" };
const LC_PEER_TWO = { id: "u-peer2", email: "quinn@example.com", name: "Quinn Peer" };
const LC_CAB = { id: "u-cab", email: "cam@example.com", name: "Cam Cab" };
const LC_ECAB = { id: "u-ecab", email: "eli@example.com", name: "Eli Ecab" };

const LC_CUST_ONE = { id: "u-cust1", email: "mia@acme.example", name: "Mia Member" };
const LC_CUST_TWO = { id: "u-cust2", email: "max@acme.example", name: "Max Member" };

interface LcFake {
  cr: BeChangeRequestDetail;
  approvals: BeChangeRequestApproval[];
  /** Approvers the backend would provision from the CR's project contacts (empty = no eligible contact). */
  customerMembers: Array<{ id: string; name: string }>;
}
let lc: LcFake;

/** True for the stages the backend provisions for the customer group. */
function lcIsCustomerStage(name: string): boolean {
  return name === "Customer Approval" || name === "Customer Review";
}

/** A customer-group stage is live while it still has REQUESTED approvers. */
function lcHasLiveCustomerStage(): boolean {
  return (lc?.approvals ?? []).some(
    (a) => lcIsCustomerStage(a.stage) && a.status === "REQUESTED",
  );
}
/** Whether the fake backend sends `approvers[].canDecide` (Postgres source) or
 * omits it (older backend / ServiceNow source), exercising the UI fallback. */
let lcEmitsCanDecide = true;

function lcLegalNextStates(
  state: string,
  flags: { approval: boolean; review: boolean } = {
    approval: lc?.cr.customerApprovalRequired ?? false,
    review: lc?.cr.customerReviewRequired ?? false,
  },
): string[] {
  switch (state) {
    case "new":
      return ["assess", "canceled"];
    case "assess":
      return ["authorize", "canceled"]; // authorize = the approval path, never a button
    case "authorize":
      return ["canceled"];
    case "customer_approval":
      // A live customer stage means the customer group decides: only cancel is
      // offered. Without one the manual "Record customer approval" remains.
      // "authorize" here is Re-schedule, offered either way.
      return lcHasLiveCustomerStage() ? ["authorize", "canceled"] : ["scheduled", "authorize", "canceled"];
    case "scheduled":
      return ["implement", "canceled"];
    case "implement":
      return ["review", "canceled"];
    case "review":
      return flags.review ? ["customer_review", "rollback", "canceled"] : ["closed", "rollback", "canceled"];
    case "customer_review":
      return lcHasLiveCustomerStage() ? ["canceled"] : ["closed", "rollback", "canceled"];
    default:
      return [];
  }
}

/** Where a CR lands once its internal approval is granted. */
function lcAfterInternalApproval(): string {
  return lc.cr.customerApprovalRequired ? "customer_approval" : "scheduled";
}

function lcStage(name: string, group: string, who: { id: string; name: string }): BeChangeRequestApproval {
  return {
    stage: name,
    approverType: "STATIC_GROUP",
    approverName: group,
    status: "REQUESTED",
    approvers: [{ id: who.id, name: who.name, status: "REQUESTED" }],
  };
}

/** The one state in which each stage can be decided (the backend's approvalStageDecidableState). */
const LC_STAGE_STATE: Record<string, string> = {
  "Peer Approval": "assess",
  "CAB Approval": "authorize",
  "ECAB Approval": "authorize",
  Review: "review",
  "Customer Approval": "customer_approval",
  "Customer Review": "customer_review",
};

/** "customer_review" -> "Customer Review", for the refusal message. */
function lcStateName(state: string): string {
  return state
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

/** Whether the CR has left the state this stage can be decided in. */
function lcStageOutOfState(stage: string): boolean {
  const decidable = LC_STAGE_STATE[stage];
  return decidable !== undefined && decidable !== lc.cr.state;
}

/**
 * The backend's `reconcileStaleApprovers`: after every state change the
 * still-REQUESTED rows of every stage the CR has left are cancelled -- all of
 * them once it is closed / canceled / rollback (the stage stays, reported
 * PENDING). Entering Review on a Normal change provisions the Review stage
 * (the assigned group's internal members) first.
 */
function lcReconcile(): void {
  const final = ["closed", "canceled", "rollback"].includes(lc.cr.state ?? "");
  lc.approvals = lc.approvals.map((a) => {
    if (!final && !lcStageOutOfState(a.stage)) return a;
    if (!a.approvers.some((p) => p.status === "REQUESTED")) return a;
    return {
      ...a,
      status: a.status === "REQUESTED" ? "PENDING" : a.status,
      approvers: a.approvers.map((p) => (p.status === "REQUESTED" ? { ...p, status: "CANCELLED" } : p)),
    };
  });
}

function lcSetState(state: string): void {
  if (state === "review" && lc.cr.type === "normal" && !lc.approvals.some((a) => a.stage === "Review")) {
    lc.approvals = [
      ...lc.approvals,
      {
        stage: "Review",
        approverType: "STATIC_GROUP",
        approverName: "Peers",
        status: "REQUESTED",
        approvers: [LC_PEER, LC_PEER_TWO].map((u) => ({ id: u.id, name: u.name, status: "REQUESTED" })),
      },
    ];
  }
  // Entering a customer gate provisions the group's stage (when the CR has a
  // customer group with at least one member), like the backend does.
  if ((state === "customer_approval" || state === "customer_review") && lc.customerMembers.length > 0) {
    lc.approvals = [
      ...lc.approvals,
      {
        stage: state === "customer_approval" ? "Customer Approval" : "Customer Review",
        approverType: "STATIC_GROUP",
        approverName: "Customer Group",
        status: "REQUESTED",
        approvers: lc.customerMembers.map((m) => ({ id: m.id, name: m.name, status: "REQUESTED" })),
      },
    ];
  }
  lc.cr = { ...lc.cr, state, legalNextStates: lcLegalNextStates(state) };
}

function lcPublish(): void {
  useGetChangeRequestMock.mockReturnValue({ data: lc.cr, isLoading: false, isError: false, error: null });
  useGetChangeRequestApprovalsMock.mockReturnValue({
    data: {
      approvals: structuredClone(lc.approvals).map((stage) => ({
        ...stage,
        approvers: stage.approvers.map((a) =>
          lcEmitsCanDecide
            ? {
                ...a,
                // true only on the caller's own REQUESTED row of a stage the CR is
                // still in the state of, and never for the creator
                canDecide:
                  a.id === mockCurrentUser.id &&
                  a.status === "REQUESTED" &&
                  mockCurrentUser.id !== lc.cr.requestedBy?.id &&
                  !lcStageOutOfState(stage.stage),
              }
            : a,
        ),
      })),
    },
    isLoading: false,
    isError: false,
    error: null,
  });
  notifyFakeBackendChanged();
}

function lcSeed(
  type: "normal" | "emergency" | "standard",
  flags: { approval: boolean; review: boolean } = { approval: false, review: false },
  // The project's registered contacts (the read-only Customer Group): `members`
  // are the eligible ones the backend asks; `contacts` (default: the members)
  // lets a test have contacts who are all ineligible (e.g. only the creator).
  // `null` = the project has no registered contacts.
  customerGroup?: {
    members: Array<{ id: string; name: string }>;
    contacts?: Array<{ id: string; name: string }>;
  } | null,
): void {
  lcEmitsCanDecide = true;
  lc = {
    cr: {
      ...BASE_CR,
      type,
      state: "new",
      requestedBy: { id: LC_CREATOR.id, name: LC_CREATOR.name },
      createdBy: LC_CREATOR.email,
      customerApprovalRequired: flags.approval,
      customerReviewRequired: flags.review,
      customerContacts: customerGroup ? (customerGroup.contacts ?? customerGroup.members) : [],
      plannedStartOn: "2030-03-01 09:00:00",
      plannedEndOn: "2030-03-01 11:00:00",
      legalNextStates: lcLegalNextStates("new", flags),
    },
    approvals: [],
    customerMembers: customerGroup?.members ?? [],
  };
  // The page's own PATCH (Request Approval, Start implementation, ...) drives the fake.
  const applyPatch = (input: { patch: { state?: string } }): void => {
    const target = input.patch.state;
    if (target === "assess") {
      if (lc.cr.type === "standard") lcSetState(lcAfterInternalApproval());
      else if (lc.cr.type === "emergency") {
        lcSetState("authorize");
        lc.approvals = [lcStage("ECAB Approval", "ECAB", LC_ECAB)];
      } else {
        lcSetState("assess");
        lc.approvals = [lcStage("Peer Approval", "Peers", LC_PEER)];
      }
    } else if (target === "scheduled" && lc.cr.state === "customer_approval") {
      if (lcHasLiveCustomerStage()) throw new Error("400: the customer group must decide");
      lcSetState("scheduled"); // the customer's approval was recorded
    } else if (target === "closed" && lc.cr.state === "customer_review" && lcHasLiveCustomerStage()) {
      throw new Error("400: the customer group must decide");
    } else if (target === "authorize") {
      // Re-schedule: only from Customer Approval, only with a changed window.
      if (lc.cr.state !== "customer_approval") {
        throw new BackendApiError(
          400,
          'state "authorize" cannot be set manually: it is reached automatically through the approval flow (Request Approval, then peer approval); it can only be set by hand to re-schedule a change from customer_approval',
        );
      }
      const win = input.patch as { plannedStartOn?: string; plannedEndOn?: string };
      const changed =
        (!!win.plannedStartOn && win.plannedStartOn !== lc.cr.plannedStartOn) ||
        (!!win.plannedEndOn && win.plannedEndOn !== lc.cr.plannedEndOn);
      if (!changed) {
        throw new BackendApiError(400, "re-scheduling requires a changed planned start or end");
      }
      lc.cr = {
        ...lc.cr,
        plannedStartOn: win.plannedStartOn ?? lc.cr.plannedStartOn,
        plannedEndOn: win.plannedEndOn ?? lc.cr.plannedEndOn,
      };
      // The customer's pending request is superseded: its rows are cancelled
      // (the stage stays as a record; the backend reports it PENDING).
      lc.approvals = lc.approvals.map((a) =>
        lcIsCustomerStage(a.stage) && a.status === "REQUESTED"
          ? { ...a, status: "PENDING", approvers: a.approvers.map((ap) => ({ ...ap, status: "CANCELLED" })) }
          : a,
      );
      if (lc.cr.type === "standard") {
        lcSetState("customer_approval"); // nothing internal to repeat: ask the customer again
      } else {
        lcSetState("authorize");
        lc.approvals = [
          ...lc.approvals,
          lc.cr.type === "emergency" ? lcStage("ECAB Approval", "ECAB", LC_ECAB) : lcStage("CAB Approval", "CAB", LC_CAB),
        ];
      }
    } else if (target === "rollback") {
      // Backend rule: only from the review states, and the customer group
      // decides while its review request is live.
      if (lc.cr.state !== "review" && lc.cr.state !== "customer_review") {
        throw new Error('400: state "rollback" can only be set from review or customer_review');
      }
      if (lcHasLiveCustomerStage()) throw new Error("400: the customer group must decide");
      lcSetState("rollback"); // the closing reconcile cancels every still-requested row
    } else if (target && target !== "scheduled" && target !== "customer_approval") {
      lcSetState(target);
    } else {
      throw new Error(`illegal manual transition to ${String(target)}`);
    }
    lcReconcile();
    lcPublish();
  };
  patchMutateMock.mockImplementation(applyPatch);
  // Destructive transitions (Cancel change, Roll back) go through mutateAsync.
  patchMutateAsyncMock.mockImplementation(async (input) => {
    applyPatch(input as { patch: { state?: string } });
    return { id: "chg-1" };
  });
  // The approvals panel's Approve/Reject drives the fake as the signed-in user.
  decideApprovalMutateMock.mockImplementation((input: { decision: "approved" | "rejected" }) => {
    // The caller's pending stage: their row on a stage decidable in the CR's
    // current state, else (all of theirs are stale) their first one.
    const mine = lc.approvals.filter(
      (a) => a.status === "REQUESTED" && a.approvers.some((p) => p.id === mockCurrentUser.id && p.status === "REQUESTED"),
    );
    const current = mine.find((a) => !lcStageOutOfState(a.stage)) ?? mine[0];
    const row = current?.approvers.find((a) => a.id === mockCurrentUser.id && a.status === "REQUESTED");
    if (!current || !row || mockCurrentUser.id === lc.cr.requestedBy?.id) {
      throw new Error("403: only a non-creator approver with a pending row may decide");
    }
    if (lcStageOutOfState(current.stage)) {
      // The backend's 409: nothing is changed.
      throw new BackendApiError(
        409,
        `this approval is no longer pending: the change request is in ${lcStateName(lc.cr.state ?? "")}, but the ${current.stage} stage can only be decided while it is in ${lcStateName(LC_STAGE_STATE[current.stage]!)}`,
      );
    }
    row.status = input.decision === "approved" ? "APPROVED" : "REJECTED";
    current.status = row.status;
    if (!lcIsCustomerStage(current.stage)) {
      // A resolving decision cancels the stage's other pending approvers.
      current.approvers.forEach((a) => {
        if (a !== row && a.status === "REQUESTED") a.status = "CANCELLED";
      });
    }
    if (lcIsCustomerStage(current.stage)) {
      // One member's decision settles the stage; the others are no longer needed.
      current.approvers.forEach((a) => {
        if (a !== row && a.status === "REQUESTED") a.status = "NOT_REQUIRED";
      });
      if (input.decision === "approved") {
        lcSetState(current.stage === "Customer Approval" ? "scheduled" : "closed");
      } else {
        // Backend rule: a declined Customer Approval cancels the change; a
        // rejected Customer Review moves it to rollback (terminal, no actions).
        lcSetState(current.stage === "Customer Approval" ? "canceled" : "rollback");
      }
    } else if (input.decision === "approved") {
      if (current.stage === "Peer Approval") {
        lcSetState("authorize");
        lc.approvals = [...lc.approvals, lcStage("CAB Approval", "CAB", LC_CAB)];
      } else if (current.stage === "CAB Approval" || current.stage === "ECAB Approval") {
        lcSetState(lcAfterInternalApproval()); // CAB / ECAB approval moves the CR on itself
      }
      // Review: the answer is recorded and the CR stays in review.
    }
    lcReconcile();
    lcPublish();
  });
  lcPublish();
}

/** Re-opens the page as another signed-in user (a fresh mount, like a new session). */
function lcOpenAs(user: { id: string; email: string }, view?: ReturnType<typeof render>): ReturnType<typeof render> {
  view?.unmount();
  mockCurrentUser = { id: user.id, email: user.email };
  lcPublish(); // canDecide is per caller, so the fake re-serves the approvals for this user
  return renderPage();
}

/** The lifecycle stepper's current step label (`aria-current="step"`). */
function currentStep(): string {
  const list = screen.getByRole("list", { name: /change request lifecycle/i });
  return within(list).getByRole("listitem", { current: "step" }).textContent ?? "";
}

function expectNoManualSchedule(): void {
  // "Re-schedule" (from Customer Approval) is a different action: anchor at the start.
  expect(screen.queryByRole("button", { name: /^schedule/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: /^schedule/i })).not.toBeInTheDocument();
  expect(screen.queryByText(/move to assess/i)).not.toBeInTheDocument();
}

/** The table row of an approver. (A customer contact's name also shows as a chip in the Overview, so only table rows count.) */
function approvalsRow(name: string): HTMLElement {
  const row = screen
    .getAllByText(name)
    .map((el) => el.closest("tr"))
    .find((tr): tr is HTMLTableRowElement => tr !== null);
  if (!row) throw new Error(`no approvals row for ${name}`);
  return row;
}

/** The approvals row of `name` that belongs to `stage` (a member can have one per stage). */
function approvalsRowInStage(name: string, stage: string): HTMLElement {
  const row = screen
    .getAllByText(name)
    .map((el) => el.closest("tr"))
    .find((tr): tr is HTMLTableRowElement => tr !== null && within(tr).queryByText(stage) !== null);
  if (!row) throw new Error(`no ${stage} row for ${name}`);
  return row;
}

/** The labels of the lifecycle stepper's steps, in order. */
function stepLabels(): string[] {
  const list = screen.getByRole("list", { name: /change request lifecycle/i });
  return within(list)
    .getAllByRole("listitem")
    .map((li) => li.textContent ?? "");
}

/** Cell value (Yes/No) beside a label on the Approval tab. */
function metaValue(label: string): string {
  return within(screen.getByText(label).parentElement!).getByText(/^(Yes|No)$/).textContent ?? "";
}

/**
 * Drives one Normal change through its whole life with the given Customer
 * Approval / Customer Review settings, asserting what is visible after every
 * step: the stepper's current step, the header note, the Approval tab flags and
 * which actions exist. The CR never gets a Schedule button.
 */
function runNormalLifecycle(approval: boolean, review: boolean): void {
  lcSeed("normal", { approval, review });

  // New: the creator sees Request Approval, no Schedule, no Move to Assess.
  let view = lcOpenAs(LC_CREATOR);
  expect(currentStep()).toBe("New");
  expect(metaValue("Customer approval required")).toBe(approval ? "Yes" : "No");
  expect(metaValue("Customer review required")).toBe(review ? "Yes" : "No");
  // The optional customer steps are on the line only when their checkbox is on.
  expect(stepLabels().includes("Customer Approval")).toBe(approval);
  expect(stepLabels().includes("Customer Review")).toBe(review);
  expect(screen.getByRole("button", { name: "Request Approval" })).toBeInTheDocument();
  expectNoManualSchedule();
  fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
  expect(patchMutateMock).toHaveBeenCalledWith({ id: "chg-1", patch: { state: "assess" } }, expect.anything());

  // Assess: Peer Approval pending; creator has no Approve/Reject, a notice, and can Cancel.
  expect(currentStep()).toBe("Assess");
  expect(screen.getByText("Awaiting Peer Approval")).toBeInTheDocument();
  expect(within(approvalsRow("Pat Peer")).getByText("Peer Approval")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent(/you created this change request/i);
  fireEvent.click(screen.getByRole("button", { name: /change state/i }));
  expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeEnabled();
  expectNoManualSchedule();

  // A peer approves -> Authorize, CAB Approval is the next, separate stage.
  view = lcOpenAs(LC_PEER, view);
  fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
  expect(currentStep()).toBe("Authorize");
  expect(screen.getByText("Awaiting CAB Approval")).toBeInTheDocument();
  expect(within(approvalsRow("Cam Cab")).getByText("CAB Approval")).toBeInTheDocument();
  expect(within(approvalsRow("Pat Peer")).getByText("Peer Approval")).toBeInTheDocument();
  expect(within(approvalsRow("Pat Peer")).getByText("Approved")).toBeInTheDocument();
  expectNoManualSchedule();

  // A CAB member approves -> Customer Approval when required, else Scheduled.
  view = lcOpenAs(LC_CAB, view);
  expect(screen.getByRole("button", { name: /^approve$/i })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
  expect(patchMutateMock).toHaveBeenCalledTimes(1); // only Request Approval was ever a manual PATCH so far

  view = lcOpenAs(LC_CREATOR, view);
  if (approval) {
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    // Neither Start implementation nor a Schedule button yet: only recording the approval (and Cancel).
    expect(screen.queryByRole("button", { name: /start implementation/i })).not.toBeInTheDocument();
    expectNoManualSchedule();
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Record customer approval" }));
    expect(patchMutateMock).toHaveBeenLastCalledWith({ id: "chg-1", patch: { state: "scheduled" } }, expect.anything());
  } else {
    expect(screen.queryByText("Record customer approval")).not.toBeInTheDocument();
  }

  // Scheduled: nothing awaited, no Schedule button, ready to implement.
  expect(currentStep()).toBe("Scheduled");
  expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
  expect(screen.queryByText("Record customer approval")).not.toBeInTheDocument();
  expectNoManualSchedule();

  // Engineer-driven tail.
  fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
  expect(currentStep()).toBe("Implement");
  fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
  expect(currentStep()).toBe("Review");
  if (review) {
    // Review offers only "Send for customer review" (no Close) when required.
    expect(screen.getByRole("button", { name: /^send for customer review$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^close$/i })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.queryByRole("menuitem", { name: /^close$/i })).not.toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));
    expect(currentStep()).toBe("Customer Review");
    expect(screen.getByText("Awaiting Customer Review")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^send for customer review$/i })).not.toBeInTheDocument();
  } else {
    // Review offers only Close (no customer review) when not required.
    expect(screen.queryByRole("button", { name: /send for customer review/i })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.queryByRole("menuitem", { name: /customer review/i })).not.toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
  }
  fireEvent.click(screen.getByRole("button", { name: /^close$/i }));
  expect(currentStep()).toBe("Closed");
  expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
  expectNoManualSchedule();
  view.unmount();
}

describe("CsmChangeRequestDetailPage — lifecycle: Normal (Request Approval -> Peer -> CAB -> [Customer Approval] -> Scheduled -> Implement -> Review -> [Customer Review] -> Closed)", () => {
  it.each([
    [false, false],
    [true, false],
    [false, true],
    [true, true],
  ])(
    "shows the right state, stage, header note and controls after every step (customerApprovalRequired=%s, customerReviewRequired=%s)",
    (approval, review) => {
      runNormalLifecycle(approval, review);
    },
  );

  it("lets a non-creator approver both Approve and Reject, and never shows them the creator notice", () => {
    lcSeed("normal");
    patchMutateMock({ id: "chg-1", patch: { state: "assess" } });

    lcOpenAs(LC_PEER);
    expect(screen.getByRole("button", { name: /^approve$/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /^reject$/i })).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("disables Approve/Reject for the creator even when the backend wrongly gives them a pending row and sends no canDecide", () => {
    lcSeed("normal");
    lcEmitsCanDecide = false;
    patchMutateMock({ id: "chg-1", patch: { state: "assess" } });
    lc.approvals = [lcStage("Peer Approval", "Peers", { id: LC_CREATOR.id, name: "Casey Creator" })];
    lcPublish();

    lcOpenAs(LC_CREATOR);
    expect(screen.getByRole("button", { name: /^approve$/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /^reject$/i })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(decideApprovalMutateMock).not.toHaveBeenCalled();
  });
});

/**
 * An approval is only actionable while the change is in its stage's state
 * (reported bug: a reviewer kept Approve / Reject on the Review stage of a
 * change that was already Closed, and during Customer Review). The fake backend
 * cancels the Review rows when the change leaves Review, like the real one, and
 * reports canDecide=false on a REQUESTED row of a stage the change has left.
 */
describe("CsmChangeRequestDetailPage — lifecycle: a Review approver's Approve / Reject follow the state", () => {
  /** Drives a fresh Normal change to Review (peers, CAB, implementation) with the real clicks. */
  function driveToReview(
    review: boolean,
    customerGroup?: { members: Array<{ id: string; name: string }> },
  ): ReturnType<typeof render> {
    lcSeed("normal", { approval: false, review }, customerGroup);
    let view = lcOpenAs(LC_CREATOR);
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
    view = lcOpenAs(LC_PEER, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    view = lcOpenAs(LC_CAB, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    view = lcOpenAs(LC_CREATOR, view);
    fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
    fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
    expect(currentStep()).toBe("Review");
    return view;
  }

  const reviewControls = (name: string) => {
    const row = approvalsRowInStage(name, "Review");
    return {
      approve: within(row).queryByRole("button", { name: /^approve$/i }),
      reject: within(row).queryByRole("button", { name: /^reject$/i }),
      status: within(row).getByText(/^(Requested|Cancelled|Approved|Rejected)$/).textContent,
    };
  };

  it("offers Approve / Reject on the Review rows of the assigned group's members while the change is in Review, and to nobody else", () => {
    let view = driveToReview(false);
    // The creator: no Review row to decide (the creator cannot approve).
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    for (const [user, name] of [[LC_PEER, "Pat Peer"], [LC_PEER_TWO, "Quinn Peer"]] as const) {
      view = lcOpenAs(user, view);
      const c = reviewControls(name);
      expect(c.status).toBe("Requested");
      expect(c.approve).toBeEnabled();
      expect(c.reject).toBeEnabled();
      // ...only on their own row: the other member's row has no controls.
      expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
    }
    // A CAB member (decided long ago, not in the assigned group) has nothing to decide.
    view = lcOpenAs(LC_CAB, view);
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("takes the controls away from every Review approver the moment the change goes to Customer Review, and Closed after the customer answers", () => {
    let view = driveToReview(true, { members: [LC_CUST_ONE, LC_CUST_TWO] });
    view = lcOpenAs(LC_PEER, view);
    expect(reviewControls("Pat Peer").approve).toBeEnabled();

    // The creator moves the change on to the customer's review.
    view = lcOpenAs(LC_CREATOR, view);
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));
    expect(currentStep()).toBe("Customer Review");
    expect(lc.approvals.find((a) => a.stage === "Review")?.approvers.map((a) => a.status)).toEqual(["CANCELLED", "CANCELLED"]);

    for (const [user, name] of [[LC_PEER, "Pat Peer"], [LC_PEER_TWO, "Quinn Peer"]] as const) {
      view = lcOpenAs(user, view);
      const c = reviewControls(name);
      expect(c.status).toBe("Cancelled");
      expect(c.approve).toBeNull();
      expect(c.reject).toBeNull();
      expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();
    }

    // The customer is who answers now.
    view = lcOpenAs(LC_CUST_ONE, view);
    expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Closed");
    // Closed: nothing is requested anywhere, and nobody has controls.
    expect(lc.approvals.flatMap((a) => a.approvers).filter((a) => a.status === "REQUESTED")).toEqual([]);
    for (const user of [LC_PEER, LC_PEER_TWO, LC_CAB, LC_CUST_TWO, LC_CUST_ONE]) {
      view = lcOpenAs(user, view);
      expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();
    }
  });

  it.each([
    ["closed", "closes straight from Review", (): void => { fireEvent.click(screen.getByRole("button", { name: /^close$/i })); }],
    ["rollback", "is rolled back", (): void => { patchMutateMock({ id: "chg-1", patch: { state: "rollback" } }); }],
    ["canceled", "is cancelled", (): void => { patchMutateMock({ id: "chg-1", patch: { state: "canceled" } }); }],
  ] as const)("takes the controls away when the change %s -> %s", (state, _what, moveOn) => {
    let view = driveToReview(false);
    view = lcOpenAs(LC_PEER, view);
    expect(reviewControls("Pat Peer").approve).toBeEnabled();

    view = lcOpenAs(LC_CREATOR, view);
    moveOn();
    expect(lc.cr.state).toBe(state);
    expect(lc.approvals.flatMap((a) => a.approvers).filter((a) => a.status === "REQUESTED")).toEqual([]);

    for (const [user, name] of [[LC_PEER, "Pat Peer"], [LC_PEER_TWO, "Quinn Peer"]] as const) {
      view = lcOpenAs(user, view);
      const c = reviewControls(name);
      expect(c.status).toBe("Cancelled");
      expect(c.approve).toBeNull();
      expect(c.reject).toBeNull();
    }
  });

  it("deciding Review records the answer, cancels the other members and leaves the change in Review", () => {
    let view = driveToReview(false);
    view = lcOpenAs(LC_PEER, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Review"); // a human moves it on
    expect(reviewControls("Pat Peer").status).toBe("Approved");
    expect(reviewControls("Quinn Peer").status).toBe("Cancelled");
    view = lcOpenAs(LC_PEER_TWO, view);
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("disables a REQUESTED Review row the backend flags canDecide=false (a legacy row the change moved past), and never submits from it", () => {
    let view = driveToReview(false);
    // The change moved on without the sweep (a row written before it existed).
    lc.cr = { ...lc.cr, state: "closed", legalNextStates: [] };
    const decisionsBefore = decideApprovalMutateMock.mock.calls.length;
    view = lcOpenAs(LC_PEER, view);
    const c = reviewControls("Pat Peer");
    expect(c.status).toBe("Requested");
    expect(c.approve).toBeDisabled();
    expect(c.reject).toBeDisabled();
    fireEvent.click(c.approve!);
    fireEvent.click(c.reject!);
    expect(decideApprovalMutateMock.mock.calls.length).toBe(decisionsBefore);
    view.unmount();
  });

  it("refuses a decision on a stale Review row with the backend's 409 and changes nothing", () => {
    const view = driveToReview(true, { members: [LC_CUST_ONE] });
    lc.cr = { ...lc.cr, state: "customer_review", legalNextStates: [] };
    mockCurrentUser = { id: LC_PEER.id, email: LC_PEER.email };
    expect(() => decideApprovalMutateMock({ decision: "approved" })).toThrow(
      "this approval is no longer pending: the change request is in Customer Review, but the Review stage can only be decided while it is in Review",
    );
    expect(lc.approvals.find((a) => a.stage === "Review")?.approvers.map((a) => a.status)).toEqual(["REQUESTED", "REQUESTED"]);
    view.unmount();
  });
});

/**
 * Roll back: the failed-review off-ramp. Offered next to the forward move from
 * Review and Customer Review only, as a destructive menu item that needs a
 * stated reason (posted as a comment, then PATCH {state: "rollback"}).
 */
describe("CsmChangeRequestDetailPage — lifecycle: Roll back", () => {
  /** Normal change, no customer approval, driven by real clicks to Review. */
  function runToReview(review: boolean): ReturnType<typeof render> {
    lcSeed("normal", { approval: false, review });
    let view = lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("New");
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
    expect(currentStep()).toBe("Assess");
    view = lcOpenAs(LC_PEER, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Authorize");
    view = lcOpenAs(LC_CAB, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Scheduled");
    // Roll back is not on offer before the review.
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.queryByRole("menuitem", { name: /roll back/i })).not.toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
    expect(currentStep()).toBe("Implement");
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.queryByRole("menuitem", { name: /roll back/i })).not.toBeInTheDocument();
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
    expect(currentStep()).toBe("Review");
    return view;
  }

  /** Opens Roll back from the menu, types a reason and confirms. */
  async function rollBackWith(reason: string): Promise<void> {
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: /roll back/i }));
    expect(screen.getByRole("heading", { name: /roll back this change/i })).toBeInTheDocument();
    // A reason is required: confirm is disabled until one is typed.
    expect(screen.getByRole("button", { name: /^roll back$/i })).toBeDisabled();
    expect(patchMutateAsyncMock).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: reason } });
    fireEvent.click(screen.getByRole("button", { name: /^roll back$/i }));
    await waitFor(() => expect(lc.cr.state).toBe("rollback"));
  }

  function expectRolledBack(): void {
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // The stepper shows the off-ramp, not a step on the line.
    expect(screen.getByText(/diverted from the standard path/i)).toBeInTheDocument();
    expect(screen.getAllByText("Rollback", { selector: ".MuiChip-label" }).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /change state/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
  }

  it.each([false, true])("rolls back from Review (customer review required: %s) with a reason, then offers no actions", async (review) => {
    const view = runToReview(review);
    // Review offers Roll back next to its forward move, never as the primary button.
    expect(screen.getByRole("button", { name: review ? /send for customer review/i : /^close$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /roll back/i })).not.toBeInTheDocument();
    await rollBackWith("Smoke test failed after the deployment.");

    expect(postCommentMutateAsyncMock).toHaveBeenCalledWith({
      changeRequestId: "chg-1",
      bodyHtml: "Smoke test failed after the deployment.",
      internal: true,
    });
    expect(patchMutateAsyncMock).toHaveBeenCalledWith({ id: "chg-1", patch: { state: "rollback" } });
    expect(postCommentMutateAsyncMock.mock.invocationCallOrder[0]).toBeLessThan(
      patchMutateAsyncMock.mock.invocationCallOrder[0],
    );
    expectRolledBack();
    view.unmount();
  });

  it("rolls back from Customer Review (manual fallback, no customer group) with a reason", async () => {
    const view = runToReview(true);
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));
    expect(currentStep()).toBe("Customer Review");
    expect(screen.getByText("Awaiting Customer Review")).toBeInTheDocument();
    // Close is the primary move; Roll back sits in the menu with Cancel.
    expect(screen.getByRole("button", { name: /^close$/i })).toBeInTheDocument();
    await rollBackWith("The customer rejected the result.");
    expect(patchMutateAsyncMock).toHaveBeenLastCalledWith({ id: "chg-1", patch: { state: "rollback" } });
    expectRolledBack();
    view.unmount();
  });

  it("does not offer Roll back while a customer group's review is pending (its members decide)", () => {
    lcSeed("normal", { approval: false, review: true }, { members: LC_MEMBERS });
    lcSetState("customer_review");
    lcPublish();
    const view = lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("Customer Review");
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /roll back/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("surfaces the backend's refusal and keeps the state when the rollback PATCH fails", async () => {
    const view = runToReview(false);
    patchMutateAsyncMock.mockRejectedValueOnce(
      new BackendApiError(400, 'state "rollback" can only be set from review or customer_review'),
    );
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: /roll back/i }));
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: "Failed." } });
    fireEvent.click(screen.getByRole("button", { name: /^roll back$/i }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(/can only be set from review or customer_review/i),
    );
    expect(lc.cr.state).toBe("review");
    view.unmount();
  });
});

/**
 * Re-schedule: the diagram's Time Change loop. In Customer Approval the creator
 * can re-plan the change -- a dialog collects the new window (at least one end
 * must change) and an optional reason; the change goes back to Authorize for
 * CAB / ECAB approval again, then the customer is asked again.
 */
describe("CsmChangeRequestDetailPage — lifecycle: Re-schedule", () => {
  beforeEach(() => setUserPreferredTimeZone("UTC"));
  afterEach(() => clearUserPreferredTimeZone());

  function windowPicker(label: string): HTMLInputElement {
    const group = within(screen.getByRole("dialog")).getAllByText(label)[0].closest(".MuiFormControl-root") as HTMLElement;
    return group.querySelector("input") as HTMLInputElement;
  }
  const dialogSubmit = (): HTMLElement =>
    within(screen.getByRole("dialog")).getByRole("button", { name: "Re-schedule" });

  /** Normal change with Customer Approval, driven by clicks to Customer Approval. */
  function runToCustomerApproval(
    customerGroup: { members: Array<{ id: string; name: string }> } | null,
    type: "normal" | "emergency" | "standard" = "normal",
  ): ReturnType<typeof render> {
    lcSeed(type, { approval: true, review: false }, customerGroup);
    let view = lcOpenAs(LC_CREATOR);
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
    if (type === "normal") {
      view = lcOpenAs(LC_PEER, view);
      fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    }
    if (type !== "standard") {
      view = lcOpenAs(type === "emergency" ? LC_ECAB : LC_CAB, view);
      fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    }
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Customer Approval");
    return view;
  }

  it("Normal with a customer group: Customer Approval -> Re-schedule -> Authorize (CAB again) -> CAB approves -> Customer Approval (asked again) -> member approves -> Scheduled", async () => {
    let view = runToCustomerApproval({ members: LC_MEMBERS });
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    // Re-schedule sits next to nothing primary (the customer group decides) and Cancel stays in the menu.
    expect(screen.getByRole("button", { name: "Re-schedule" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();
    expectOnlyCancelOffered();

    // The dialog starts on the current window and will not submit without a change.
    fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
    expect(screen.getByRole("heading", { name: /re-schedule this change/i })).toBeInTheDocument();
    expect(windowPicker("Planned start").value).toBe("03/01/2030 09:00 AM");
    expect(windowPicker("Planned end").value).toBe("03/01/2030 11:00 AM");
    expect(dialogSubmit()).toBeDisabled();
    expect(patchMutateAsyncMock).not.toHaveBeenCalled();

    fireEvent.change(windowPicker("Planned start"), { target: { value: "03/08/2030 09:00 AM" } });
    fireEvent.change(windowPicker("Planned end"), { target: { value: "03/08/2030 11:00 AM" } });
    fireEvent.change(screen.getByLabelText(/reason \(optional\)/i), { target: { value: "Customer freeze next week." } });
    fireEvent.click(dialogSubmit());
    await waitFor(() => expect(lc.cr.state).toBe("authorize"));

    // The reason is recorded first, then the state + window are patched.
    expect(postCommentMutateAsyncMock).toHaveBeenCalledWith({
      changeRequestId: "chg-1",
      bodyHtml: "Customer freeze next week.",
      internal: true,
    });
    expect(patchMutateAsyncMock).toHaveBeenCalledWith({
      id: "chg-1",
      patch: { state: "authorize", plannedStartOn: "2030-03-08 09:00:00", plannedEndOn: "2030-03-08 11:00:00" },
    });
    expect(postCommentMutateAsyncMock.mock.invocationCallOrder[0]).toBeLessThan(
      patchMutateAsyncMock.mock.invocationCallOrder[0],
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    // Back at Authorize, waiting on a fresh CAB stage -- not on the superseded customer stage.
    expect(currentStep()).toBe("Authorize");
    expect(screen.getByText("Awaiting CAB Approval")).toBeInTheDocument();
    expect(screen.queryByText("Awaiting Customer Approval")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Re-schedule" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();
    expect(screen.getAllByText("CAB Approval", { selector: "td" })).toHaveLength(2);
    expect(within(approvalsRowInStage("Mia Member", "Customer Approval")).getByText("Cancelled")).toBeInTheDocument();

    // The new CAB approval asks the customer group again.
    view = lcOpenAs(LC_CAB, view);
    expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(lc.cr.state).toBe("customer_approval");
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    expect(screen.getAllByText("Customer Approval", { selector: "td" })).toHaveLength(4); // 2 cancelled + 2 fresh member rows

    view = lcOpenAs(LC_CUST_ONE, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(lc.cr.state).toBe("scheduled");
    view.unmount();
  });

  it("Normal without a customer group (manual fallback): Re-schedule sits next to Record customer approval; the loop repeats", async () => {
    let view = runToCustomerApproval(null);
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();
    for (const [start, end] of [["03/08/2030 09:00 AM", "03/08/2030 11:00 AM"], ["03/15/2030 09:00 AM", "03/15/2030 11:00 AM"]]) {
      fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
      fireEvent.change(windowPicker("Planned start"), { target: { value: start } });
      fireEvent.change(windowPicker("Planned end"), { target: { value: end } });
      fireEvent.click(dialogSubmit());
      await waitFor(() => expect(lc.cr.state).toBe("authorize"));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(currentStep()).toBe("Authorize");
      expect(screen.getByText("Awaiting CAB Approval")).toBeInTheDocument();
      expect(postCommentMutateAsyncMock).not.toHaveBeenCalled(); // no reason given, none recorded

      view = lcOpenAs(LC_CAB, view);
      fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
      view = lcOpenAs(LC_CREATOR, view);
      expect(currentStep()).toBe("Customer Approval");
      expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();
    }
    fireEvent.click(screen.getByRole("button", { name: "Record customer approval" }));
    expect(currentStep()).toBe("Scheduled");
    view.unmount();
  });

  it("Emergency goes back to Authorize for ECAB approval", async () => {
    const view = runToCustomerApproval(null, "emergency");
    fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
    expect(screen.getByText(/ECAB approval again/i)).toBeInTheDocument();
    fireEvent.change(windowPicker("Planned end"), { target: { value: "03/01/2030 01:00 PM" } });
    fireEvent.click(dialogSubmit());
    await waitFor(() => expect(lc.cr.state).toBe("authorize"));
    expect(screen.getByText("Awaiting ECAB Approval")).toBeInTheDocument();
    view.unmount();
  });

  it("Standard stays in Customer Approval and the customer is asked again", async () => {
    const view = runToCustomerApproval({ members: LC_MEMBERS }, "standard");
    fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
    expect(screen.getByText(/stays in Customer Approval/i)).toBeInTheDocument();
    fireEvent.change(windowPicker("Planned start"), { target: { value: "02/28/2030 09:00 AM" } });
    fireEvent.click(dialogSubmit());
    await waitFor(() => expect(lc.cr.plannedStartOn).toBe("2030-02-28 09:00:00"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Re-schedule" })).toBeInTheDocument();
    view.unmount();
  });

  it("shows the backend's 400 verbatim in the dialog and keeps the state", async () => {
    const view = runToCustomerApproval(null);
    patchMutateAsyncMock.mockRejectedValueOnce(
      new BackendApiError(400, "re-scheduling requires a changed planned start or end"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
    fireEvent.change(windowPicker("Planned end"), { target: { value: "03/01/2030 12:00 PM" } });
    fireEvent.click(dialogSubmit());
    await waitFor(() =>
      expect(within(screen.getByRole("dialog")).getByRole("alert")).toHaveTextContent(
        "re-scheduling requires a changed planned start or end",
      ),
    );
    expect(lc.cr.state).toBe("customer_approval");
    // Backing out leaves everything alone.
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(currentStep()).toBe("Customer Approval");
    view.unmount();
  });

  it("after a failed re-schedule the recorded reason is locked and a retry never posts it twice", async () => {
    const view = runToCustomerApproval(null);
    fireEvent.click(screen.getByRole("button", { name: "Re-schedule" }));
    fireEvent.change(windowPicker("Planned start"), { target: { value: "03/08/2030 09:00 AM" } });
    fireEvent.change(windowPicker("Planned end"), { target: { value: "03/08/2030 11:00 AM" } });
    fireEvent.change(screen.getByLabelText(/reason \(optional\)/i), { target: { value: "Customer freeze next week." } });

    patchMutateAsyncMock.mockRejectedValueOnce(new BackendApiError(409, "Rejected"));
    fireEvent.click(dialogSubmit());
    await waitFor(() => expect(within(screen.getByRole("dialog")).getByRole("alert")).toBeInTheDocument());
    expect(postCommentMutateAsyncMock).toHaveBeenCalledTimes(1);

    // The reason is now recorded: the field is locked so an edit can't be silently dropped.
    expect(screen.getByLabelText(/reason \(optional\)/i)).toBeDisabled();
    expect(
      screen.getByText("Already recorded as a work note — retrying will only re-schedule."),
    ).toBeInTheDocument();

    fireEvent.click(dialogSubmit());
    await waitFor(() => expect(lc.cr.state).toBe("authorize"));
    expect(patchMutateAsyncMock).toHaveBeenCalledTimes(2);
    expect(postCommentMutateAsyncMock).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("is not offered anywhere but Customer Approval", () => {
    lcSeed("normal", { approval: true, review: true });
    for (const state of ["new", "assess", "authorize", "scheduled", "implement", "review", "customer_review"]) {
      lcSetState(state);
      // Even if a backend listed it, the bar does not render authorize.
      lc.cr = { ...lc.cr, legalNextStates: [...(lc.cr.legalNextStates ?? []), "authorize"] };
      lcPublish();
      const view = lcOpenAs(LC_CREATOR);
      expect(screen.queryByRole("button", { name: /re-schedule/i }), state).not.toBeInTheDocument();
      if (screen.queryByRole("button", { name: /change state/i })) {
        fireEvent.click(screen.getByRole("button", { name: /change state/i }));
        expect(screen.queryByRole("menuitem", { name: /re-schedule|authorize/i }), state).not.toBeInTheDocument();
      }
      view.unmount();
    }
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: backend canDecide on approvers", () => {
  it("disables Approve/Reject for a non-creator whose own pending row the backend marks canDecide=false (e.g. an SRE on the peer stage)", () => {
    lcSeed("normal");
    patchMutateMock({ id: "chg-1", patch: { state: "assess" } });
    lcOpenAs(LC_PEER);
    // Override just this row: the backend reports the caller may not decide it.
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: {
        approvals: [
          {
            ...lc.approvals[0]!,
            approvers: [{ ...lc.approvals[0]!.approvers[0]!, canDecide: false }],
          },
        ],
      },
      isLoading: false,
      isError: false,
      error: null,
    });
    notifyFakeBackendChanged();
    expect(screen.getByRole("button", { name: /^approve$/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /^reject$/i })).toBeDisabled();
  });

  it("enables Approve/Reject only on the caller's own pending row when canDecide=true is sent", () => {
    lcSeed("normal");
    patchMutateMock({ id: "chg-1", patch: { state: "assess" } });
    lcOpenAs(LC_PEER);
    expect(screen.getByRole("button", { name: /^approve$/i })).toBeEnabled();
    expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: Emergency (Request Approval -> ECAB only -> auto Scheduled)", () => {
  it("has no Peer or CAB stage and lands on Scheduled when ECAB approves", () => {
    lcSeed("emergency");

    let view = lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("New");
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));

    expect(currentStep()).toBe("Authorize");
    expect(screen.getByText("Awaiting ECAB Approval")).toBeInTheDocument();
    expect(within(approvalsRow("Eli Ecab")).getByText("ECAB Approval")).toBeInTheDocument();
    expect(screen.queryByText("Peer Approval")).not.toBeInTheDocument();
    expect(screen.queryByText("CAB Approval")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument(); // creator
    expectNoManualSchedule();

    view = lcOpenAs(LC_ECAB, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Scheduled");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expectNoManualSchedule();
    expect(screen.getByRole("button", { name: /^start implementation$/i })).toBeInTheDocument();
    view.unmount();
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: Emergency with Customer Approval (ECAB -> Customer Approval -> Scheduled)", () => {
  it("waits in Customer Approval after ECAB approves, and only 'Record customer approval' moves it on", () => {
    lcSeed("emergency", { approval: true, review: false });

    let view = lcOpenAs(LC_CREATOR);
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
    expect(currentStep()).toBe("Authorize");
    expect(screen.getByText("Awaiting ECAB Approval")).toBeInTheDocument();
    expectNoManualSchedule();

    view = lcOpenAs(LC_ECAB, view);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    expect(screen.queryByText("Peer Approval")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start implementation/i })).not.toBeInTheDocument();
    expectNoManualSchedule();
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Record customer approval" }));
    expect(patchMutateMock).toHaveBeenLastCalledWith({ id: "chg-1", patch: { state: "scheduled" } }, expect.anything());
    expect(currentStep()).toBe("Scheduled");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^start implementation$/i })).toBeInTheDocument();
    expectNoManualSchedule();
    view.unmount();
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: Standard with Customer Approval (Request Approval -> Customer Approval -> Scheduled)", () => {
  it("goes to Customer Approval, not straight to Scheduled, and has no approval stages", () => {
    lcSeed("standard", { approval: true, review: false });

    const view = lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("New");
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));

    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    expect(screen.getByText(/no approval stages recorded/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start implementation/i })).not.toBeInTheDocument();
    expectNoManualSchedule();

    fireEvent.click(screen.getByRole("button", { name: "Record customer approval" }));
    expect(patchMutateMock).toHaveBeenLastCalledWith({ id: "chg-1", patch: { state: "scheduled" } }, expect.anything());
    expect(currentStep()).toBe("Scheduled");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^start implementation$/i })).toBeInTheDocument();
    view.unmount();
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: Standard (Request Approval -> auto Scheduled)", () => {
  it("goes straight to Scheduled with no approval stages and no Schedule button", () => {
    lcSeed("standard");

    const view = lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("New");
    fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));

    expect(currentStep()).toBe("Scheduled");
    expect(screen.getByText(/no approval stages recorded/i)).toBeInTheDocument();
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expectNoManualSchedule();
    expect(screen.getByRole("button", { name: /^start implementation$/i })).toBeInTheDocument();
    view.unmount();
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: defensive against a backend that still offers scheduled", () => {
  it("never renders a Schedule or Authorize action even if legalNextStates lists them", () => {
    lcSeed("normal");
    lcSetState("authorize");
    lc.cr = { ...lc.cr, legalNextStates: ["scheduled", "authorize", "canceled"] };
    lc.approvals = [lcStage("CAB Approval", "CAB", LC_CAB)];
    lcPublish();

    lcOpenAs(LC_CAB);
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
    expectNoManualSchedule();
    expect(screen.queryByRole("menuitem", { name: /authorize/i })).not.toBeInTheDocument();
  });
});

describe("CsmChangeRequestDetailPage — lifecycle: scheduled is only ever 'Record customer approval'", () => {
  it("a customer_approval CR that is Canceled from the menu goes through the reason dialog", () => {
    lcSeed("normal", { approval: true, review: false });
    lcSetState("customer_approval");
    lcPublish();
    lcOpenAs(LC_CREATOR);
    expect(currentStep()).toBe("Customer Approval");
    fireEvent.click(screen.getByRole("button", { name: /change state/i }));
    fireEvent.click(screen.getByRole("menuitem", { name: /cancel change/i }));
    // Destructive, so the reason dialog opens instead of patching.
    expect(patchMutateMock).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("never offers 'Record customer approval' from any other state, even if the backend lists scheduled", () => {
    for (const state of ["new", "assess", "authorize", "scheduled", "implement", "review", "customer_review"]) {
      lcSeed("normal", { approval: true, review: true });
      lcSetState(state);
      lc.cr = { ...lc.cr, legalNextStates: ["scheduled"] };
      lcPublish();
      const view = lcOpenAs(LC_CREATOR);
      expect(screen.queryByText("Record customer approval")).not.toBeInTheDocument();
      expectNoManualSchedule();
      view.unmount();
    }
  });
});

describe("CsmChangeRequestDetailPage — customer approval / review flags on the Approval tab", () => {
  it("shows both flags read-only as Yes/No, separate from the customer's confirmation outcome", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        customerApprovalRequired: true,
        customerReviewRequired: false,
        hasCustomerApproved: false,
        hasCustomerReviewed: true,
      },
    });
    renderPage();
    expect(metaValue("Customer approval required")).toBe("Yes");
    expect(metaValue("Customer review required")).toBe("No");
    expect(metaValue("Customer approved")).toBe("No");
    expect(metaValue("Customer reviewed")).toBe("Yes");
    // Read-only: no checkbox anywhere on the page itself.
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("shows No for both when the backend omits the flags", () => {
    mockQueryResult({ data: BASE_CR });
    renderPage();
    expect(metaValue("Customer approval required")).toBe("No");
    expect(metaValue("Customer review required")).toBe("No");
  });

  it("passes the flags through to the Edit dialog's CR", () => {
    mockQueryResult({
      data: { ...BASE_CR, state: "assess", customerApprovalRequired: true, customerReviewRequired: true },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^edit$/i }));
    const props = editChangeRequestDialogMock.mock.calls.at(-1)![0] as { cr: BeChangeRequestDetail };
    expect(props.cr.customerApprovalRequired).toBe(true);
    expect(props.cr.customerReviewRequired).toBe(true);
  });

  it("carries the flags into the Clone router state but never the customer's confirmation", () => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        customerApprovalRequired: true,
        customerReviewRequired: true,
        hasCustomerApproved: true,
        hasCustomerReviewed: true,
      },
    });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: /^clone$/i }));
    const [, options] = navigateMock.mock.calls.at(-1)!;
    const state = (options as { state: Record<string, unknown> }).state;
    expect(state.customerApprovalRequired).toBe(true);
    expect(state.customerReviewRequired).toBe(true);
    expect(Object.keys(state)).not.toContain("hasCustomerApproved");
    expect(Object.keys(state)).not.toContain("hasCustomerReviewed");
  });
});

describe("CsmChangeRequestDetailPage — blocking reason for the customer states", () => {
  it("shows 'Awaiting customer approval' in the header for a customer_approval CR with no pending approver", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "customer_approval", customerApprovalRequired: true } });
    renderPage();
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
  });

  it("shows 'Awaiting customer review' in the header for a customer_review CR", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "customer_review", customerReviewRequired: true } });
    renderPage();
    expect(screen.getByText("Awaiting Customer Review")).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Customer group: the people a customer-gated change request is directed to
//
// The Customer Group is the change request's project's registered contacts
// (`customerContacts`, derived by the backend, read-only). When a CR whose
// project has eligible contacts enters `customer_approval` / `customer_review`,
// the backend provisions a "Customer Approval" / "Customer Review" stage whose
// approvers are those contacts. While that stage is live `legalNextStates`
// offers only `canceled`; a contact's decision moves the CR (approve ->
// scheduled / closed, reject -> canceled). With no registered contacts, or none
// eligible, no stage exists and the manual Record-customer-approval / Close
// paths stay. The fake above encodes exactly that.
// ---------------------------------------------------------------------------

const LC_MEMBERS = [
  { id: LC_CUST_ONE.id, name: LC_CUST_ONE.name },
  { id: LC_CUST_TWO.id, name: LC_CUST_TWO.name },
];

/** Drives a seeded CR from New to the point its internal approval is granted. */
function lcGoThroughInternalApproval(view: ReturnType<typeof render>): ReturnType<typeof render> {
  view = lcOpenAs(LC_CREATOR, view);
  fireEvent.click(screen.getByRole("button", { name: "Request Approval" }));
  view = lcOpenAs(LC_PEER, view);
  fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
  view = lcOpenAs(LC_CAB, view);
  fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
  return view;
}

/** Cancel is the only action: no primary button, one menu item. */
function expectOnlyCancelOffered(): void {
  expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^close$/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /start implementation/i })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /change state/i }));
  expect(screen.getAllByRole("menuitem")).toHaveLength(1);
  expect(screen.getByRole("menuitem", { name: /cancel change/i })).toBeInTheDocument();
  fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
}

describe("CsmChangeRequestDetailPage — customer group: Normal with Customer Approval and Customer Review", () => {
  it("walks the whole lifecycle, asserting state, stage rows and buttons for the creator, a group member and a non-member after every step", { timeout: 30000 }, () => {
    lcSeed("normal", { approval: true, review: true }, { members: LC_MEMBERS });

    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));

    // --- customer_approval, creator: customer stage provisioned, Cancel only.
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    // No "no registered contacts" helper: the project has contacts.
    expect(screen.queryByText(/no registered customer contacts/i)).not.toBeInTheDocument();
    for (const member of [LC_CUST_ONE, LC_CUST_TWO]) {
      const row = approvalsRow(member.name);
      expect(within(row).getByText("Customer Approval")).toBeInTheDocument();
      expect(within(row).getByText("Customer Group")).toBeInTheDocument();
      expect(within(row).getByText("Requested")).toBeInTheDocument();
    }
    expect(within(approvalsRow("Pat Peer")).getByText("Peer Approval")).toBeInTheDocument();
    expect(within(approvalsRow("Cam Cab")).getByText("CAB Approval")).toBeInTheDocument();
    // The creator never decides.
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();
    expectOnlyCancelOffered();
    expectNoManualSchedule();

    // --- customer_approval, non-member (the CAB approver): sees rows, no Approve/Reject.
    view = lcOpenAs(LC_CAB, view);
    expect(currentStep()).toBe("Customer Approval");
    // (The contact is listed twice: as a Customer Group chip and as an approver row.)
    expect(within(approvalsRow("Mia Member")).getByText("Requested")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();

    // --- customer_approval, group member: Approve/Reject on their own row only.
    view = lcOpenAs(LC_CUST_ONE, view);
    expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
    expect(screen.getByRole("button", { name: /^approve$/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /^reject$/i })).toBeEnabled();
    expect(within(approvalsRow("Mia Member")).getByRole("button", { name: /^approve$/i })).toBeInTheDocument();
    // No decision controls for a non-member (the row's Assignment group is a link-button of its own now).
    expect(within(approvalsRow("Max Member")).queryByRole("button", { name: /^(approve|reject)$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Record customer approval" })).not.toBeInTheDocument();

    // --- member approves -> Scheduled, no manual PATCH involved.
    const patchCallsBefore = patchMutateMock.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(decideApprovalMutateMock).toHaveBeenLastCalledWith(
      { id: "chg-1", decision: "approved" },
      expect.anything(),
    );
    expect(patchMutateMock.mock.calls.length).toBe(patchCallsBefore);
    expect(currentStep()).toBe("Scheduled");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(within(approvalsRow("Mia Member")).getByText("Approved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^start implementation$/i })).toBeInTheDocument();
    expectNoManualSchedule();

    // --- engineer tail up to Review.
    view = lcOpenAs(LC_CREATOR, view);
    fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
    expect(currentStep()).toBe("Implement");
    fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
    expect(currentStep()).toBe("Review");
    expect(screen.getByRole("button", { name: /^send for customer review$/i })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^close$/i })).not.toBeInTheDocument();

    // --- customer_review: a Customer Review stage for the same group.
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));
    expect(currentStep()).toBe("Customer Review");
    expect(screen.getByText("Awaiting Customer Review")).toBeInTheDocument();
    expect(within(approvalsRowInStage("Max Member", "Customer Review")).getByText("Customer Group")).toBeInTheDocument();
    expect(within(approvalsRowInStage("Mia Member", "Customer Review")).getByText("Requested")).toBeInTheDocument();
    // The settled Customer Approval rows stay in the panel.
    expect(within(approvalsRowInStage("Mia Member", "Customer Approval")).getByText("Approved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument(); // creator
    expectOnlyCancelOffered(); // no manual Close while the stage is live

    // --- customer_review, non-member: nothing to decide.
    view = lcOpenAs(LC_PEER, view);
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reject$/i })).not.toBeInTheDocument();

    // --- customer_review, other member approves -> Closed.
    view = lcOpenAs(LC_CUST_TWO, view);
    expect(screen.getAllByRole("button", { name: /^approve$/i })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /^approve$/i }));
    expect(currentStep()).toBe("Closed");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /change state/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("a member rejecting the Customer Approval cancels the change request", () => {
    lcSeed("normal", { approval: true, review: false }, { members: LC_MEMBERS });
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));

    view = lcOpenAs(LC_CUST_TWO, view);
    expect(currentStep()).toBe("Customer Approval");
    fireEvent.click(screen.getByRole("button", { name: /^reject$/i }));
    expect(decideApprovalMutateMock).toHaveBeenLastCalledWith(
      { id: "chg-1", decision: "rejected" },
      expect.anything(),
    );
    expect(lc.cr.state).toBe("canceled");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(within(approvalsRow("Max Member")).getByText("Rejected")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /change state/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("a member rejecting the Customer Review moves the change request to Rollback (terminal, no actions left)", () => {
    lcSeed("normal", { approval: false, review: true }, { members: LC_MEMBERS });
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Scheduled");
    fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
    fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));
    expect(currentStep()).toBe("Customer Review");

    view = lcOpenAs(LC_CUST_ONE, view);
    fireEvent.click(screen.getByRole("button", { name: /^reject$/i }));
    expect(lc.cr.state).toBe("rollback");
    expect(screen.queryByText(/awaiting/i)).not.toBeInTheDocument();
    expect(screen.getAllByText("Rollback", { selector: ".MuiChip-label" }).length).toBeGreaterThan(0);
    expect(within(approvalsRowInStage("Mia Member", "Customer Review")).getByText("Rejected")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /change state/i })).not.toBeInTheDocument();
    view.unmount();
  });

  it("shows a non-creator, non-member no Approve/Reject on a live customer stage even if the backend sends canDecide=false for every row", () => {
    lcSeed("normal", { approval: true, review: false }, { members: LC_MEMBERS });
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));
    view = lcOpenAs(LC_PEER, view);
    expect(screen.queryByText(/approve|reject/i, { selector: "button" })).not.toBeInTheDocument();
    view.unmount();
  });
});

describe("CsmChangeRequestDetailPage — customer group: no registered customer contacts (manual fallback)", () => {
  it("customer_approval with no registered contacts: no customer stage, the helper explains why, and Record customer approval still works", () => {
    lcSeed("normal", { approval: true, review: false }, null);
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));

    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    // Only the settled internal stages; no customer-stage rows.
    expect(screen.queryAllByText("Customer Approval", { selector: "td" })).toHaveLength(0);
    expect(screen.getByText(/^No registered customer contacts are assigned to this change request's project, so no customer approvers were assigned\./)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Record customer approval" }));
    expect(patchMutateMock).toHaveBeenLastCalledWith({ id: "chg-1", patch: { state: "scheduled" } }, expect.anything());
    expect(currentStep()).toBe("Scheduled");
    expect(screen.queryByText(/no registered customer contacts/i)).not.toBeInTheDocument();
    view.unmount();
  });

  it("customer_review with no registered contacts: helper shown and manual Close still offered", () => {
    lcSeed("normal", { approval: false, review: true }, null);
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));
    view = lcOpenAs(LC_CREATOR, view);
    fireEvent.click(screen.getByRole("button", { name: /^start implementation$/i }));
    fireEvent.click(screen.getByRole("button", { name: /^mark implemented$/i }));
    fireEvent.click(screen.getByRole("button", { name: /^send for customer review$/i }));

    expect(currentStep()).toBe("Customer Review");
    expect(screen.getByText("Awaiting Customer Review")).toBeInTheDocument();
    expect(screen.getByText(/no registered customer contacts/i)).toBeInTheDocument();
    expect(screen.queryAllByText("Customer Review", { selector: "td" })).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: /^close$/i }));
    expect(currentStep()).toBe("Closed");
    view.unmount();
  });

  it("registered contacts none of whom is eligible (e.g. only the creator) provision no stage, so the manual path stays and no helper nags", () => {
    lcSeed("normal", { approval: true, review: false }, { members: [], contacts: [{ id: LC_CREATOR.id, name: LC_CREATOR.name }] });
    let view = lcGoThroughInternalApproval(lcOpenAs(LC_CREATOR));
    view = lcOpenAs(LC_CREATOR, view);
    expect(currentStep()).toBe("Customer Approval");
    expect(screen.queryAllByText("Customer Approval", { selector: "td" })).toHaveLength(0);
    expect(screen.queryByText(/no registered customer contacts/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Record customer approval" })).toBeInTheDocument();
    view.unmount();
  });

  it("stays silent about the customer contacts when the payload omits the field (another data source)", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "customer_approval", customerApprovalRequired: true } });
    renderPage();
    expect(screen.getByText("Awaiting Customer Approval")).toBeInTheDocument();
    expect(screen.queryByText(/no registered customer contacts/i)).not.toBeInTheDocument();
  });

  it("does not show the helper outside the customer gates, even with no registered contacts", () => {
    mockQueryResult({ data: { ...BASE_CR, state: "scheduled", customerContacts: [] } });
    renderPage();
    expect(screen.queryByText(/no registered customer contacts/i)).not.toBeInTheDocument();
  });
});

describe("CsmChangeRequestDetailPage — opening an Assignment group on the Approval tab", () => {
  const CAB_GROUP_ID = "22222222-2222-4222-8222-222222222222";

  const approvals = (): BeChangeRequestApproval[] => [
    {
      stage: "CAB Approval",
      approverType: "STATIC_GROUP",
      approverName: "CAB Approval",
      assignmentGroup: { id: CAB_GROUP_ID, name: "CAB Approval" },
      status: "REQUESTED",
      approvers: [{ id: "c1", name: "Cam Cab", status: "REQUESTED" }],
    },
    {
      stage: "Customer Approval",
      approverType: "STATIC_GROUP",
      approverName: "Customer Group",
      assignmentGroup: null,
      status: "REQUESTED",
      approvers: [{ id: "u-mia", name: "Mia Member", status: "REQUESTED" }],
    },
  ];

  beforeEach(() => {
    mockQueryResult({
      data: {
        ...BASE_CR,
        state: "customer_approval",
        customerContacts: [
          { id: "k1", name: "Mia Member", email: "mia.member@acme.example" },
          { id: "k2", name: "Max Member", email: "max.member@acme.example" },
        ],
      },
    });
    useGetChangeRequestApprovalsMock.mockReturnValue({
      data: { approvals: approvals() },
      isLoading: false,
      isError: false,
      error: null,
    });
    useGroupDetailMock.mockReturnValue({
      data: {
        id: CAB_GROUP_ID,
        name: "CAB Approval",
        members: [
          { id: "c1", name: "Cam Cab", email: "cam.cab@example.com", role: "member" },
          { id: "c2", name: "Cleo Cab", email: "cleo.cab@example.com", role: "lead" },
        ],
        total: 2,
      },
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    });
  });

  it("hands the change request's customerContacts to the approvals panel", () => {
    renderPage();
    expect(approvalsPanelMock).toHaveBeenCalledWith(
      expect.objectContaining({
        customerContacts: [
          { id: "k1", name: "Mia Member", email: "mia.member@acme.example" },
          { id: "k2", name: "Max Member", email: "max.member@acme.example" },
        ],
      }),
    );
  });

  it("opens the CAB group from its stage row and lists the members", () => {
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "View members of CAB Approval" }));

    const dialog = screen.getByRole("dialog", { name: "CAB Approval" });
    expect(useGroupDetailMock).toHaveBeenCalledWith(CAB_GROUP_ID);
    expect(within(dialog).getByRole("heading", { name: "Group Members (2)" })).toBeInTheDocument();
    expect(within(dialog).getByText("Cleo Cab")).toBeInTheDocument();
    expect(within(dialog).getByText("Lead")).toBeInTheDocument();
  });

  it("opens the Customer Group from the customer stage and lists the project's registered contacts, with no group request", () => {
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "View members of Customer Group" }));

    const dialog = screen.getByRole("dialog", { name: "Customer Group" });
    expect(within(dialog).getByRole("heading", { name: "Group Members (2)" })).toBeInTheDocument();
    expect(within(dialog).getByText("max.member@acme.example")).toBeInTheDocument();
    expect(useGroupDetailMock).not.toHaveBeenCalled();
  });
});
