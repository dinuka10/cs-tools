/**
 * Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import { useMemo, type JSX, type ReactNode } from "react";
import type { ScheduleTeam } from "../types";
import { teamColour } from "./rotaHues";
import { TeamColourContext, TeamNameContext } from "./teamColourContext";

/** Gives everything below it the colour for a team. See TeamColourContext. */
export function TeamColourProvider({
  teams,
  children,
}: {
  teams: readonly ScheduleTeam[];
  children: ReactNode;
}): JSX.Element {
  const colourOf = useMemo(() => {
    // sortOrder is 1-based from the API; the palette is indexed from 0.
    const order = new Map(teams.map((t) => [t.key.toLowerCase(), t.sortOrder - 1]));
    return (teamKey: string): string => teamColour(order.get(teamKey.toLowerCase()));
  }, [teams]);

    const nameOf = useMemo(() => {
    const byKey = new Map(teams.map((t) => [t.key.toLowerCase(), t.name]));
    return (teamKey: string): string => byKey.get(teamKey.toLowerCase()) ?? teamKey;
  }, [teams]);

  return (
    <TeamColourContext.Provider value={colourOf}>
      <TeamNameContext.Provider value={nameOf}>{children}</TeamNameContext.Provider>
    </TeamColourContext.Provider>
  );
}
