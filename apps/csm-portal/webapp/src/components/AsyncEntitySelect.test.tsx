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

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import "@testing-library/jest-dom/vitest";
import AsyncEntitySelect from "@components/AsyncEntitySelect";

interface Item {
  id: string;
  name: string;
}

const useSearch = () => ({ data: [] as Item[], isFetching: false, isError: false });

function renderSelect(required?: boolean): void {
  render(
    <AsyncEntitySelect<Item>
      id="test-select"
      label="Caller"
      value=""
      onChange={() => {}}
      required={required}
      useSearch={useSearch}
      getId={(i) => i.id}
      getLabel={(i) => i.name}
    />,
  );
}

describe("AsyncEntitySelect — required marker", () => {
  it("shows the required asterisk and marks the input required when `required` is set", () => {
    renderSelect(true);
    expect(screen.getByRole("combobox", { name: /caller/i })).toBeRequired();
    // The outlined field renders the label twice (visible label plus the
    // fieldset legend), each with its own asterisk.
    expect(screen.getAllByText("*", { exact: false }).length).toBeGreaterThan(0);
  });

  it("shows neither when `required` is not set", () => {
    renderSelect();
    expect(screen.getByRole("combobox", { name: /caller/i })).not.toBeRequired();
    expect(screen.queryByText("*", { exact: false })).not.toBeInTheDocument();
  });
});
