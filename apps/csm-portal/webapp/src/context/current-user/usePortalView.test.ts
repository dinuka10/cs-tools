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

import { renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

let mockRoles: string[] | undefined;
vi.mock("@context/current-user/CurrentUserContext", () => ({
  useCurrentUser: () => ({
    user: mockRoles ? { roles: mockRoles } : undefined,
    isLoading: false,
    isError: false,
    error: null,
  }),
}));

// @config/devFlags's exports read window.config at MODULE LOAD time (a
// top-level const, not a function) -- setting window.config from inside a
// test can't retroactively change an already-evaluated export, confirmed
// empirically. Mocking the module directly, with values this file can flip
// per test, sidesteps that entirely.
let mockDevViewOverride: "cs-abt" | "sales-sa" | undefined;
let mockDevBypassAccessCheck = false;
vi.mock("@config/devFlags", () => ({
  get devViewOverride() {
    return mockDevViewOverride;
  },
  get devBypassAccessCheck() {
    return mockDevBypassAccessCheck;
  },
}));

import { usePortalView } from "@context/current-user/usePortalView";

afterEach(() => {
  mockRoles = undefined;
  mockDevViewOverride = undefined;
  mockDevBypassAccessCheck = false;
});

describe("usePortalView", () => {
  it("resolves to cs-abt when the caller holds no viewer role", () => {
    mockRoles = ["support_engineer"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });

  it("resolves to cs-abt when the caller holds no roles at all", () => {
    mockRoles = undefined;
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });

  it("resolves to sales-sa when the caller holds the viewer role", () => {
    mockRoles = ["viewer"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("sales-sa");
  });

  it("resolves to sales-sa alongside other portal roles, not just alone", () => {
    mockRoles = ["viewer", "sales_solutions"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("sales-sa");
  });

  it("sales_solutions alone, without viewer, does not resolve to sales-sa", () => {
    mockRoles = ["sales_solutions"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });

  it("cs_engineer takes precedence over viewer, resolving to cs-abt", () => {
    mockRoles = ["cs_engineer", "viewer"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });

  it("cs_engineer alone (no viewer) resolves to cs-abt", () => {
    mockRoles = ["cs_engineer"];
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });

  it("devViewOverride wins regardless of the caller's real roles", () => {
    mockRoles = ["support_engineer"];
    mockDevViewOverride = "sales-sa";
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("sales-sa");
  });

  it("devBypassAccessCheck alone (no explicit override) defaults to cs-abt", () => {
    mockRoles = ["viewer"];
    mockDevBypassAccessCheck = true;
    const { result } = renderHook(() => usePortalView());
    expect(result.current).toBe("cs-abt");
  });
});
