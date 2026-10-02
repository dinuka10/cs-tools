import { Box, Grid } from "@wso2/oxygen-ui";
import { useState, type JSX } from "react";
import { useNavigate } from "react-router";

import { useDashboard } from "@features/plg/api/hooks";
import { DashboardCharts } from "@features/plg/components/DashboardCharts";
import { ErrorBlock, LoadingBlock, PageHeader, StatTile } from "@features/plg/components/common";
import { PeriodSelect } from "@features/plg/components/PeriodSelect";
import { DEFAULT_RANGE_DAYS, isoDaysAgo } from "@features/plg/components/period";

/**
 * The leadership dashboard: the standing picture of the portfolio.
 *
 * It answers "what shape is the funnel in" — how many customers there are, how
 * many registrations, and how those spread across platforms, stages and
 * subscription tiers.
 *
 * WHAT IT DELIBERATELY OMITS. The queue counts on the workspace dashboard are a
 * to-do list: "12 awaiting acknowledgement" is an instruction to whoever owns
 * the queue, and noise to anyone reading the shape of the business.
 *
 * WHY THIS IS ITS OWN FILE. It used to be the same component as the workspace
 * dashboard, choosing between them by matching the last segment of its own URL.
 * That is fragile in a way that had already bitten once: the merge moved the
 * page and the check silently stopped matching, so this page rendered the
 * workspace variant — queue tiles and all — with no error to notice. The path
 * is `/plg/overview` and the nav calls it Overview, but neither is something
 * this file reads: the route decides which page renders, so a future rename
 * cannot silently turn this into the other dashboard. A page should not have
 * to work out what it is.
 *
 * It is also not sustainable. The two views are expected to diverge, and every
 * divergence under one roof is another branch on a variant flag. Sharing the
 * charts is the part that should be shared; the tiles are not.
 */
export default function LeadershipDashboardPage(): JSX.Element {
  const navigate = useNavigate();
  const [days, setDays] = useState(DEFAULT_RANGE_DAYS);
  const { data, isPending, error } = useDashboard(isoDaysAgo(days));

  if (error) return <ErrorBlock error={error} />;

  return (
    // One root element, because csm-portal's AppLayout renders <Outlet /> into
    // a column flex box: a fragment would make each child its own flex item,
    // and an overflow:hidden card then clips instead of the page scrolling.
    <Box sx={{ display: "flex", flexDirection: "column" }}>
      <PageHeader
        title="PLG Overview"
        subtitle="Registrations and lifecycle spread across the whole portfolio"
        // Scopes the cohort charts only.
        actions={<PeriodSelect value={days} onChange={setDays} />}
      />

      {isPending || !data ? (
        <LoadingBlock height={120} />
      ) : (
        <Grid container spacing={2} mb={3}>
          <Grid size={{ xs: 12, sm: 6, md: 6 }}>
            <StatTile
              label="Organisations"
              value={data.summary.totalOrganizations}
              hint="Customers registered through PLG"
              onClick={() => navigate("/plg/organizations")}
            />
          </Grid>
          <Grid size={{ xs: 12, sm: 6, md: 6 }}>
            <StatTile
              label="Registrations"
              value={data.summary.totalRegistrations}
              hint="One per organisation and platform"
              onClick={() => navigate("/plg/organizations")}
            />
          </Grid>
        </Grid>
      )}

      <DashboardCharts data={data} />
    </Box>
  );
}
