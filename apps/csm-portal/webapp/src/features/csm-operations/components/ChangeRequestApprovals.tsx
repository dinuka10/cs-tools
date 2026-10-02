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

import {
  Box,
  Button,
  Card,
  Chip,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@wso2/oxygen-ui";
import { Check, X } from "@wso2/oxygen-ui-icons-react";
import type { JSX } from "react";
import QueryErrorState from "@components/QueryErrorState";
import { formatBackendTimestampForDisplay } from "@utils/dateTime";
import { useCurrentUser } from "@context/current-user/CurrentUserContext";
import { useErrorBanner } from "@context/error-banner/ErrorBannerContext";
import { useGetChangeRequestApprovals } from "@features/csm-operations/api/useGetChangeRequestApprovals";
import { useDecideChangeRequestApproval } from "@features/csm-operations/api/useDecideChangeRequestApproval";
import {
  approvalStatusColor,
  approvalStatusLabel,
} from "@features/csm-operations/utils/changeRequests";
import type {
  BeChangeRequestApproval,
  BeChangeRequestApprovalDecision,
  BeChangeRequestApprover,
} from "@api/backend/types";

function formatDateTime(value?: string | null): string {
  return (
    formatBackendTimestampForDisplay(value, {
      dateStyle: "medium",
      timeStyle: "short",
    }) ?? "—"
  );
}

/** "Devops Approval" (STATIC_GROUP) or a named customer contact (DYNAMIC_CONTACT). */
function approverGroupName(approval: BeChangeRequestApproval): string {
  return approval.approverName || (approval.approverType === "DYNAMIC_CONTACT" ? "Customer contact" : "Approval group");
}

/** Whether this approver row is the current user's own pending ("REQUESTED") approval. */
function isMyPendingApproval(approver: BeChangeRequestApprover, currentUserId?: string): boolean {
  return (
    !!currentUserId &&
    approver.id === currentUserId &&
    approver.status.trim().toUpperCase() === "REQUESTED"
  );
}

interface DecideHandlers {
  onDecide: (decision: BeChangeRequestApprovalDecision) => void;
  isDeciding: boolean;
}

/** One flattened row: an individual approver plus the assignment group of the
 * approval stage they belong to. Real ServiceNow's own Approvers list (the
 * reference this table matches) has no separate "stage" grouping at all —
 * every approver record for the change request appears in one flat table,
 * distinguished only by their own state and assignment group, so nesting
 * approvers under a collapsible per-stage card (as this component used to)
 * was needless structure a real approver never asked for: reported live as
 * confusing — an approver looking for their own pending decision does not
 * benefit from first finding "their" stage card and expanding it. */
interface ApproverTableRow {
  key: string;
  approver: BeChangeRequestApprover;
  groupName: string;
}

function flattenApprovals(approvals: BeChangeRequestApproval[]): ApproverTableRow[] {
  const rows: ApproverTableRow[] = [];
  approvals.forEach((approval, approvalIndex) => {
    approval.approvers.forEach((approver, approverIndex) => {
      rows.push({
        key: `${approvalIndex}-${approverIndex}-${approver.id}`,
        approver,
        groupName: approverGroupName(approval),
      });
    });
  });
  return rows;
}

function ApproverActionsCell({
  approver,
  currentUserId,
  decide,
}: {
  approver: BeChangeRequestApprover;
  currentUserId?: string;
  decide?: DecideHandlers;
}): JSX.Element {
  if (!decide || !isMyPendingApproval(approver, currentUserId)) {
    return <>—</>;
  }
  return (
    <Box sx={{ display: "flex", gap: 1 }}>
      <Button
        size="small"
        variant="outlined"
        color="success"
        startIcon={<Check size={14} />}
        disabled={decide.isDeciding}
        onClick={() => decide.onDecide("approved")}
      >
        Approve
      </Button>
      <Button
        size="small"
        variant="outlined"
        color="error"
        startIcon={<X size={14} />}
        disabled={decide.isDeciding}
        onClick={() => decide.onDecide("rejected")}
      >
        Reject
      </Button>
    </Box>
  );
}

/**
 * Approval-stage records for a change request (`GET /change-requests/{id}/approvals`):
 * who specifically needs to approve, and each approver's individual status,
 * rendered as one flat table — State, Approver, Assignment group, Comments,
 * Created, Approved on — matching real ServiceNow's own Approvers list
 * layout rather than this app's earlier collapsible-per-stage-card design.
 * Distinct from the flat `hasCustomerApproved`/`hasCustomerReviewed` toggle
 * shown in the Approval card above, which is a different, already-built
 * concept.
 */
export default function ChangeRequestApprovals({ id }: { id: string | undefined }): JSX.Element | null {
  const { data, isLoading, isError, error } = useGetChangeRequestApprovals(id);
  const { user } = useCurrentUser();
  const { showError } = useErrorBanner();
  const decideApproval = useDecideChangeRequestApproval();

  const decide: DecideHandlers | undefined = id
    ? {
        isDeciding: decideApproval.isPending,
        onDecide: (decision) =>
          decideApproval.mutate(
            { id, decision },
            {
              onError: (err) =>
                showError(
                  decision === "approved"
                    ? "Could not approve the change request."
                    : "Could not reject the change request.",
                  err,
                ),
            },
          ),
      }
    : undefined;

  if (isLoading) {
    return (
      <Card sx={{ p: 2.5, display: "flex", flexDirection: "column", gap: 2 }}>
        <Typography variant="subtitle2">Approvals</Typography>
        <Skeleton variant="rounded" height={48} />
        <Skeleton variant="rounded" height={48} />
      </Card>
    );
  }

  if (isError) {
    return (
      <Card sx={{ p: 2.5, display: "flex", flexDirection: "column", gap: 2 }}>
        <Typography variant="subtitle2">Approvals</Typography>
        <QueryErrorState message="Could not load the approval stages for this change request." error={error} />
      </Card>
    );
  }

  const approvals = data?.approvals ?? [];
  const rows = flattenApprovals(approvals);

  if (rows.length === 0) {
    return (
      <Card sx={{ p: 2.5, display: "flex", flexDirection: "column", gap: 2 }}>
        <Typography variant="subtitle2">Approvals</Typography>
        <Typography variant="body2" color="text.secondary">
          No approval stages recorded for this change request.
        </Typography>
      </Card>
    );
  }

  return (
    <Card sx={{ p: 2.5, display: "flex", flexDirection: "column", gap: 1.5 }}>
      <Typography variant="subtitle2">Approvals</Typography>
      <Box sx={{ border: 1, borderColor: "divider", borderRadius: 1, overflow: "hidden" }}>
        <TableContainer>
          <Table size="small" sx={{ "& .MuiTableCell-root": { borderColor: "divider" } }}>
            <TableHead>
              <TableRow sx={{ bgcolor: "action.hover" }}>
                <TableCell>State</TableCell>
                <TableCell>Approver</TableCell>
                <TableCell>Assignment group</TableCell>
                <TableCell>Comments</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>Approved on</TableCell>
                <TableCell>Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map(({ key, approver, groupName }) => {
                const name = approver.name?.trim();
                return (
                  <TableRow key={key}>
                    <TableCell>
                      <Chip
                        size="small"
                        variant="outlined"
                        color={approvalStatusColor(approver.status)}
                        label={approvalStatusLabel(approver.status)}
                      />
                    </TableCell>
                    <TableCell>
                      {name || (
                        <Typography variant="body2" color="text.secondary">
                          Unnamed approver
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell>{groupName}</TableCell>
                    <TableCell>{approver.comments?.trim() || "—"}</TableCell>
                    <TableCell>{formatDateTime(approver.createdOn)}</TableCell>
                    <TableCell>{formatDateTime(approver.respondedOn)}</TableCell>
                    <TableCell>
                      <ApproverActionsCell approver={approver} currentUserId={user?.id} decide={decide} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      </Box>
    </Card>
  );
}
