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
  AdapterDateFns,
  Alert,
  Box,
  Button,
  DatePickers,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  TextField,
  Typography,
} from "@wso2/oxygen-ui";
import { useMemo, useState, type JSX } from "react";
import type { BeChangeRequestDetail, BePatchChangeRequestPayload } from "@api/backend/types";
import {
  backendUtcToZonedInput,
  formatDateTimeLocal,
  parseDateTimeLocal,
  zonedInputToBackendUtc,
} from "@utils/dateTime";

const { DateTimePicker, LocalizationProvider } = DatePickers;

/** What approval the change goes through again, for the dialog's explanation. */
function approvalAgain(type?: string | null): string {
  if (type === "emergency") return "ECAB approval";
  if (type === "standard") return "";
  return "CAB approval";
}

interface ChangeRequestRescheduleDialogProps {
  cr: BeChangeRequestDetail;
  /** True while the PATCH is in flight. */
  isSubmitting: boolean;
  /** The backend's refusal for the last attempt, shown verbatim. */
  error?: string | null;
  /**
   * True once the reason has been saved as a work note by an earlier attempt
   * whose PATCH then failed: the field is locked (as in the Roll back / Cancel
   * dialog) so an edited reason can't be silently dropped when the retry skips
   * posting it again.
   */
  reasonRecorded?: boolean;
  onClose: () => void;
  /**
   * `{state: "authorize", plannedStartOn?, plannedEndOn?}` plus the optional
   * reason ("" when none), which the caller records as an internal comment
   * before the PATCH -- the same way a Roll back / Cancel reason is.
   */
  onSubmit: (patch: BePatchChangeRequestPayload, reason: string) => void;
}

/**
 * "Re-schedule" from Customer Approval: the planned time changed, so the change
 * goes back through internal approval (the process diagram's Time Change loop).
 * Collects the new planned start and/or end -- prefilled with the current
 * values, at least one must change -- and an optional reason. Only the changed dates are sent; the
 * backend repeats the check ("re-scheduling requires a changed planned start or
 * end") and its refusal is shown as returned.
 */
export default function ChangeRequestRescheduleDialog({
  cr,
  isSubmitting,
  error,
  reasonRecorded,
  onClose,
  onSubmit,
}: ChangeRequestRescheduleDialogProps): JSX.Element {
  const initialStart = useMemo(() => backendUtcToZonedInput(cr.plannedStartOn), [cr.plannedStartOn]);
  const initialEnd = useMemo(() => backendUtcToZonedInput(cr.plannedEndOn), [cr.plannedEndOn]);
  // The pickers' own values are kept as emitted, partial (Invalid Date) ones
  // included: feeding a half-typed field back as null makes the picker wipe the
  // digits typed so far.
  const [startDate, setStartDate] = useState<Date | null>(() => parseDateTimeLocal(initialStart));
  const [endDate, setEndDate] = useState<Date | null>(() => parseDateTimeLocal(initialEnd));
  const [reason, setReason] = useState("");

  const validLocal = (d: Date | null): string => (d && !Number.isNaN(d.getTime()) ? formatDateTimeLocal(d) : "");
  const plannedStart = validLocal(startDate);
  const plannedEnd = validLocal(endDate);
  const startChanged = !!plannedStart && plannedStart !== initialStart;
  const endChanged = !!plannedEnd && plannedEnd !== initialEnd;
  const endBeforeStart = !!plannedStart && !!plannedEnd && endDate!.getTime() <= startDate!.getTime();
  const canSubmit = (startChanged || endChanged) && !endBeforeStart && !isSubmitting;

  const submit = (): void => {
    const patch: BePatchChangeRequestPayload = { state: "authorize" };
    if (startChanged) {
      const utc = zonedInputToBackendUtc(plannedStart);
      if (utc) patch.plannedStartOn = utc;
    }
    if (endChanged) {
      const utc = zonedInputToBackendUtc(plannedEnd);
      if (utc) patch.plannedEndOn = utc;
    }
    onSubmit(patch, reason.trim());
  };

  const again = approvalAgain(cr.type);

  return (
    <Dialog open onClose={onClose} maxWidth="xs" fullWidth aria-labelledby="cr-reschedule-title">
      <DialogTitle id="cr-reschedule-title">Re-schedule this change?</DialogTitle>
      <DialogContent dividers>
        <Box sx={{ display: "flex", flexDirection: "column", gap: 2, pt: 0.5 }}>
          {error && (
            <Alert severity="error" role="alert">
              {error}
            </Alert>
          )}
          <Typography variant="body2" color="text.secondary">
            {again
              ? `Set the new planned time. The change goes back to Authorize for ${again} again, then the customer is asked to approve the new time.`
              : "Set the new planned time. The change stays in Customer Approval and the customer is asked to approve the new time again."}
          </Typography>
          <LocalizationProvider dateAdapter={AdapterDateFns}>
            <DateTimePicker
              label="Planned start"
              value={startDate}
              disabled={isSubmitting}
              onChange={(next) => setStartDate(next instanceof Date ? next : null)}
              slotProps={{ textField: { size: "small", fullWidth: true } }}
            />
            <DateTimePicker
              label="Planned end"
              value={endDate}
              disabled={isSubmitting}
              onChange={(next) => setEndDate(next instanceof Date ? next : null)}
              slotProps={{
                textField: {
                  size: "small",
                  fullWidth: true,
                  error: endBeforeStart,
                  helperText: endBeforeStart ? "Planned end must be after planned start." : undefined,
                },
              }}
            />
          </LocalizationProvider>
          {!startChanged && !endChanged && (
            <Typography variant="caption" color="text.secondary">
              Change the planned start or end to re-schedule.
            </Typography>
          )}
          <TextField
            label="Reason (optional)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            disabled={isSubmitting || reasonRecorded}
            multiline
            minRows={2}
            fullWidth
            size="small"
            helperText={
              reasonRecorded
                ? "Already recorded as a work note — retrying will only re-schedule."
                : "Recorded as an internal work note."
            }
          />
        </Box>
      </DialogContent>
      <DialogActions>
        <Button color="inherit" onClick={onClose} disabled={isSubmitting}>
          Close
        </Button>
        <Button variant="contained" onClick={submit} disabled={!canSubmit} loading={isSubmitting}>
          Re-schedule
        </Button>
      </DialogActions>
    </Dialog>
  );
}
