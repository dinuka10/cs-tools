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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const postMock = vi.fn();

vi.mock("@config/apiConfig", () => ({
  apiConfig: { backendUrl: "https://example.test" },
}));
vi.mock("@api/backend/client", () => ({
  useBackendApi: () => ({ post: postMock }),
}));

import AddUserDialog from "@features/csm-users/components/AddUserDialog";

function renderDialog() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <AddUserDialog open onClose={vi.fn()} />
    </QueryClientProvider>,
  );
}

function fillNameAndEmail(email: string): void {
  fireEvent.change(screen.getByLabelText(/first name/i), { target: { value: "Jane" } });
  fireEvent.change(screen.getByLabelText(/email/i), { target: { value: email } });
}

function selectUserType(label: string): void {
  fireEvent.mouseDown(screen.getByLabelText(/user type/i));
  fireEvent.click(screen.getByRole("option", { name: label }));
}

describe("AddUserDialog", () => {
  beforeEach(() => {
    postMock.mockReset();
    postMock.mockResolvedValue({ id: "11111111-1111-1111-1111-111111111111" });
  });

  it("disables submit until a user type is chosen", () => {
    renderDialog();
    fillNameAndEmail("jane@wso2.com");
    expect(screen.getByRole("button", { name: /add user/i })).toBeDisabled();
  });

  it("blocks Internal with a non-wso2.com email and shows the constraint", () => {
    renderDialog();
    fillNameAndEmail("jane@example.com");
    selectUserType("Internal (WSO2 staff)");

    expect(screen.getByText(/an internal user must have a @wso2\.com email address/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /add user/i })).toBeDisabled();
  });

  it("allows Internal with a wso2.com email and sends the internal role", async () => {
    renderDialog();
    fillNameAndEmail("jane@wso2.com");
    selectUserType("Internal (WSO2 staff)");

    const submit = screen.getByRole("button", { name: /add user/i });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);

    await waitFor(() =>
      expect(postMock).toHaveBeenCalledWith(
        "/users",
        expect.objectContaining({ email: "jane@wso2.com", roles: ["internal"] }),
      ),
    );
  });

  it("disables the External option and marks it not available", () => {
    renderDialog();
    fillNameAndEmail("jane@example.com");
    fireEvent.mouseDown(screen.getByLabelText(/user type/i));

    // MUI blocks a real click on a disabled option via CSS pointer-events,
    // which fireEvent.click (no hit-testing in jsdom) can't exercise -- the
    // aria-disabled assertion is what CaseActionBar.test.tsx's own disabled
    // MenuItem tests rely on for the same reason, see ChangeCaseTypeDialog.test.tsx.
    const externalOption = screen.getByRole("option", { name: /external.*currently unavailable/i });
    expect(externalOption).toHaveAttribute("aria-disabled", "true");
  });
});
