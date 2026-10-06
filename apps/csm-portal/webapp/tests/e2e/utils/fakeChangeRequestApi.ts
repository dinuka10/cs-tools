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

//
// In-browser fake of the change-request slice of the backend contract, for
// specs that must walk an approval flow deterministically without creating
// permanent ServiceNow records. Installed with `page.route`, so only the
// change-request endpoints (and the user id returned by `/users/me`) are
// faked -- everything else still reaches the real backend the rest of the
// suite runs against, and the browser session is still the captured one.
//
// The contract encoded here is the one the UI is built against:
//   - `legalNextStates` offers no manual way to `scheduled` -- except from
//     `customer_approval`, where `scheduled` means "record the customer's
//     approval" -- and `authorize` (listed from Assess) is the approval path,
//     never a button;
//   - Request Approval (`PATCH {state:"assess"}`) on a Normal CR enters Assess
//     with a "Peer Approval" stage; on an Emergency CR it enters Authorize with
//     an "ECAB Approval" stage only; on a Standard CR it goes straight to
//     the post-approval state with no approvals;
//   - approving Peer Approval adds a "CAB Approval" stage and moves to
//     Authorize; approving CAB/ECAB moves the CR on by itself;
//   - "the post-approval state" is `customer_approval` when the CR has
//     `customerApprovalRequired`, else `scheduled`;
//   - from `customer_approval` legalNextStates = [scheduled, authorize, canceled]
//     ([authorize, canceled] while a customer-group stage is live); "authorize"
//     there is Re-schedule: PATCH {state:"authorize", plannedStartOn?,
//     plannedEndOn?} is accepted ONLY from `customer_approval` and only when the
//     window changes (else a 400 with the backend's wording); it cancels the
//     customer's pending stage (kept as a record, reported PENDING with every
//     approver CANCELLED, like the backend), moves a Normal / Emergency CR to
//     `authorize` with a fresh "CAB Approval" / "ECAB Approval" stage, and keeps
//     a Standard CR in `customer_approval` with a fresh customer stage; the new
//     CAB / ECAB approval sends the CR to `customer_approval` again;
//   - Review offers [customer_review, rollback, canceled] when
//     `customerReviewRequired`, else [closed, rollback, canceled];
//     `customer_review` -> [closed, rollback, canceled]. `rollback` ("Roll
//     back", the failed-review off-ramp) is accepted ONLY from those two states
//     (any other state is a 400 `state "rollback" can only be set from review
//     or customer_review`), is refused while a customer-group review stage is
//     live, cancels every still-requested approver row, and is final (a later
//     state change is a 400); the UI posts its reason as a comment first
//     (`POST /change-requests/{id}/comments`, recorded in `journal()`);
//   - customer group: the CR's Customer Group is READ-ONLY and derived from its
//     Customer Project -- the project's registered contacts (`customerContacts`
//     on the detail and on link-options; FAKE_PROJECT_CONTACTS). When the
//     project has at least one eligible contact (not the creator), entering
//     `customer_approval` / `customer_review` provisions a "Customer Approval" /
//     "Customer Review" stage (approverName "Customer Group", approvers = the
//     contacts), and changing the project replaces a live stage with one for the
//     new project's contacts. `customerGroupId` is no longer accepted (400
//     `customerGroupId is no longer accepted: ...`), nor is `environmentIds`
//     (400 `environmentIds is no longer supported: ...`). While that stage is live
//     (still has REQUESTED approvers) legalNextStates for those two states is
//     [canceled] only, and the manual PATCH {state:"scheduled"} /
//     {state:"closed"} is refused with a 400. A member's decision settles the
//     stage (their co-members become NOT_REQUIRED): Customer Approval approved
//     -> scheduled, rejected -> canceled; Customer Review approved -> closed,
//     rejected -> rollback (terminal: legalNextStates none). With no project, or a
//     project with no eligible contact, no stage is provisioned and the manual paths above
//     remain. `canDecide` is true only on the signed-in member's own
//     REQUESTED row of a live stage, never for the creator;
//   - `customerApprovalRequired` / `customerReviewRequired` are on the detail
//     response and editable via PATCH until their gate passes; a late edit is
//     refused with a 400 and a readable message;
//   - the CR's creator can never approve;
//   - an approval is only actionable while the CR is in the state its stage belongs
//     to (Peer Approval: assess, CAB / ECAB Approval: authorize, Review: review,
//     Customer Approval / Customer Review: the same-named state). Like the
//     backend's reconcileStaleApprovers, every PATCH and every decision ends by
//     cancelling the still-REQUESTED approver rows of every stage the CR has left
//     -- ALL of them once it is closed / canceled / rollback -- so a Review
//     approver can decide while the CR is in review and no longer after it moved
//     on; `canDecide` is false on a REQUESTED row of a stage the CR has left (a
//     legacy row: see `setState`, which moves the CR without that sweep), and a
//     decision on one is a 409 with the backend's wording. Entering `review` on a
//     Normal CR provisions the "Review" stage (the assigned group's internal
//     members: FAKE_PEER and FAKE_PEER_COLLEAGUE). Deciding Review records the
//     answer and cancels the siblings; the CR stays in review;
//   - assignment groups: every internal stage carries `assignmentGroup: {id, name}`
//     -- Peer Approval the CR's assigned group (FAKE_PEER_GROUP, "Example Corp
//     ABT"), CAB / ECAB Approval their own groups (FAKE_CAB_GROUP /
//     FAKE_ECAB_GROUP) -- and `GET /groups/{id}` answers the group page
//     (`{id, name, description, email, manager, members:[{id,name,email,userType,
//     role}], total}`, 404 for an unknown id). The Customer Approval / Customer
//     Review stages carry `assignmentGroup: null` (their approvers are the
//     project's registered contacts, which the page already has as
//     `customerContacts`); `failGroups(status)` makes the group endpoint fail,
//     for the error state;
//   - the customer scope (see FAKE_PROJECTS & co. below): `POST /projects/search`
//     lists the fake projects, `POST /change-requests/link-options` answers the
//     Customer Project -> Deployments -> Deployment products cascade (plus the
//     project's `customerContacts`), and `POST /change-requests` /
//     `PATCH /change-requests/{id}` store `projectId` / `deploymentIds` /
//     `deploymentProductIds` / `category` / `comment` / `workNote` after the
//     backend's own validation -- deployments must belong to the project,
//     deployment products must be exactly the derived set (all else a 400 with a
//     readable message), and the project / deployments are locked from
//     `implement` onwards. The detail response returns `project`, `deployments`,
//     `deploymentProducts`, `customerContacts`, `category` (EntityRef /
//     EntityRef[] / {id,name,email}[] / the category enum value).
//


import type { Page, Route } from "@playwright/test";

export type FakeCrType = "normal" | "standard" | "emergency";

export interface FakeUser {
  id: string;
  name: string;
  email: string;
}

export const FAKE_CREATOR: FakeUser = { id: "00000000-0000-0000-0000-00000000e001", name: "Casey Creator", email: "casey.creator@example.com" };
export const FAKE_PEER: FakeUser = { id: "00000000-0000-0000-0000-00000000e002", name: "Pat Peer", email: "pat.peer@example.com" };
export const FAKE_CAB: FakeUser = { id: "00000000-0000-0000-0000-00000000e003", name: "Cam Cab", email: "cam.cab@example.com" };
export const FAKE_ECAB: FakeUser = { id: "00000000-0000-0000-0000-00000000e004", name: "Eli Ecab", email: "eli.ecab@example.com" };

/** Registered contacts of the Acme project (its read-only Customer Group: the customer-side approvers). */
export const FAKE_CUST_ONE: FakeUser = { id: "00000000-0000-0000-0000-00000000e005", name: "Mia Member", email: "mia.member@acme.example" };
export const FAKE_CUST_TWO: FakeUser = { id: "00000000-0000-0000-0000-00000000e006", name: "Max Member", email: "max.member@acme.example" };
/** Someone with no stake in the customer group. */
export const FAKE_OUTSIDER: FakeUser = { id: "00000000-0000-0000-0000-00000000e007", name: "Olive Outsider", email: "olive.outsider@example.com" };
/** The Beta project's only registered contact -- another customer's person. */
export const FAKE_BETA_CONTACT: FakeUser = { id: "00000000-0000-0000-0000-00000000e008", name: "Bea Beta", email: "bea.beta@beta.example" };

export const FAKE_CR_ID = "00000000-0000-0000-0000-00000000c001";

// ---------------------------------------------------------------------------
// Customer scope fixtures: three projects (Gamma has no deployments and no
// registered contacts); each deployment has a type (its environment role) and
// carries deployed products; each project has its own registered contacts.
// ---------------------------------------------------------------------------

export interface FakeRef {
  id: string;
  name: string;
}
export interface FakeDeployment extends FakeRef {
  projectId: string;
  type: string;
}
export interface FakeDeploymentProduct extends FakeRef {
  deploymentId: string;
}

export const FAKE_PROJECTS: FakeRef[] = [
  { id: "00000000-0000-0000-0000-00000000f001", name: "Acme Project" },
  { id: "00000000-0000-0000-0000-00000000f002", name: "Beta Project" },
  { id: "00000000-0000-0000-0000-00000000f003", name: "Gamma Project" },
];
/** Each project's registered contacts: its read-only Customer Group (initial; see setProjectContacts). */
export const FAKE_PROJECT_CONTACTS: Record<string, FakeUser[]> = {
  [FAKE_PROJECTS[0]!.id]: [FAKE_CUST_ONE, FAKE_CUST_TWO],
  [FAKE_PROJECTS[1]!.id]: [FAKE_BETA_CONTACT],
  [FAKE_PROJECTS[2]!.id]: [],
};
export const FAKE_DEPLOYMENTS: FakeDeployment[] = [
  { id: "00000000-0000-0000-0000-00000000d001", name: "Acme Production", projectId: FAKE_PROJECTS[0]!.id, type: "primary_production" },
  { id: "00000000-0000-0000-0000-00000000d002", name: "Acme Staging", projectId: FAKE_PROJECTS[0]!.id, type: "staging" },
  { id: "00000000-0000-0000-0000-00000000d003", name: "Beta Development", projectId: FAKE_PROJECTS[1]!.id, type: "development" },
];
export const FAKE_DEPLOYMENT_PRODUCTS: FakeDeploymentProduct[] = [
  { id: "00000000-0000-0000-0000-00000000b001", name: "API Manager 4.3.0", deploymentId: FAKE_DEPLOYMENTS[0]!.id },
  { id: "00000000-0000-0000-0000-00000000b002", name: "Identity Server 7.0.0", deploymentId: FAKE_DEPLOYMENTS[0]!.id },
  { id: "00000000-0000-0000-0000-00000000b003", name: "API Manager 4.2.0", deploymentId: FAKE_DEPLOYMENTS[1]!.id },
  { id: "00000000-0000-0000-0000-00000000b004", name: "Choreo 1.0.0", deploymentId: FAKE_DEPLOYMENTS[2]!.id },
];
/** Assignment groups the Assignment group picker can search (the Customer Group is not searched: it is derived). */
export const FAKE_GROUPS: FakeRef[] = [
  { id: "00000000-0000-0000-0000-00000000a101", name: "Apollo" },
  { id: "00000000-0000-0000-0000-00000000a102", name: "Artemis" },
];

/** A group's page as `GET /groups/{id}` returns it. */
export interface FakeGroupMember extends FakeUser {
  role: "member" | "lead";
}
export interface FakeApprovalGroup extends FakeRef {
  description: string | null;
  email: string | null;
  manager: FakeRef | null;
  members: FakeGroupMember[];
}

export const FAKE_PEER_COLLEAGUE: FakeUser = { id: "00000000-0000-0000-0000-00000000e011", name: "Quinn Peer", email: "quinn.peer@example.com" };
export const FAKE_CAB_COLLEAGUE: FakeUser = { id: "00000000-0000-0000-0000-00000000e012", name: "Cleo Cab", email: "cleo.cab@example.com" };
export const FAKE_CAB_NO_EMAIL: FakeUser = { id: "00000000-0000-0000-0000-00000000e013", name: "Cyd Cab", email: "" };
export const FAKE_ECAB_COLLEAGUE: FakeUser = { id: "00000000-0000-0000-0000-00000000e014", name: "Eve Ecab", email: "eve.ecab@example.com" };

/** The CR's assigned group: the Peer Approval stage is provisioned from it. */
export const FAKE_PEER_GROUP: FakeApprovalGroup = {
  id: "00000000-0000-0000-0000-00000000a201",
  name: "Example Corp ABT",
  description: "Builds and supports the Example Corp account.",
  email: "example-corp-abt@example.com",
  manager: { id: "00000000-0000-0000-0000-00000000e021", name: "Mona Manager" },
  members: [
    { ...FAKE_PEER, role: "lead" },
    { ...FAKE_PEER_COLLEAGUE, role: "member" },
  ],
};
/** The CAB Approval group (no description, email or manager: only the members show). */
export const FAKE_CAB_GROUP: FakeApprovalGroup = {
  id: "00000000-0000-0000-0000-00000000a202",
  name: "CAB Approval",
  description: null,
  email: null,
  manager: null,
  members: [
    { ...FAKE_CAB, role: "member" },
    { ...FAKE_CAB_COLLEAGUE, role: "member" },
    { ...FAKE_CAB_NO_EMAIL, role: "member" },
  ],
};
export const FAKE_ECAB_GROUP: FakeApprovalGroup = {
  id: "00000000-0000-0000-0000-00000000a203",
  name: "ECAB Approval",
  description: "Emergency Change Advisory Board.",
  email: null,
  manager: null,
  members: [
    { ...FAKE_ECAB, role: "lead" },
    { ...FAKE_ECAB_COLLEAGUE, role: "member" },
  ],
};
const FAKE_APPROVAL_GROUPS: FakeApprovalGroup[] = [FAKE_PEER_GROUP, FAKE_CAB_GROUP, FAKE_ECAB_GROUP];

/** States from which project / deployments can no longer change. */
const SCOPE_LOCKED = ["implement", "review", "customer_review", "closed", "rollback", "canceled"];

const CATEGORIES = [
  "hardware", "software", "service", "system_software", "applications_software", "network",
  "telecom", "documentation", "other", "regular_release_cloud", "hotfix_release_cloud", "devops", "cloud_computing",
];

/** The customer scope the fake CR currently holds. */
export interface FakeScope {
  projectId: string | null;
  deploymentIds: string[];
  deploymentProductIds: string[];
  category: string | null;
}

/** A request the fake served, with the JSON body it carried (if any). */
export interface FakeRequestBody {
  request: string;
  body: Record<string, unknown> | undefined;
}

const sameSet = (a: string[], b: string[]): boolean => a.length === b.length && a.every((x) => b.includes(x));
const derivedProductIds = (deploymentIds: string[]): string[] =>
  FAKE_DEPLOYMENT_PRODUCTS.filter((p) => deploymentIds.includes(p.deploymentId)).map((p) => p.id);

/** The messages for the two fields the API no longer accepts (same text as the backend and the BFF). */
const CUSTOMER_GROUP_ID_REMOVED =
  "customerGroupId is no longer accepted: the customer group is derived from the customer project's registered contacts";
const ENVIRONMENT_IDS_REMOVED = "environmentIds is no longer supported: deployments carry the environment";

interface Approver {
  id: string;
  name: string;
  status: string;
}
interface Stage {
  stage: string;
  approverType: "STATIC_GROUP";
  approverName: string;
  /** The group the stage was provisioned from; null for the customer stages. */
  assignmentGroup: FakeRef | null;
  status: string;
  approvers: Approver[];
}

/** The two ServiceNow-style creation checkboxes the CR carries. */
export interface FakeCustomerFlags {
  customerApprovalRequired: boolean;
  customerReviewRequired: boolean;
}

/** States from which each checkbox can no longer be changed (backend refuses with 400). */
const APPROVAL_FLAG_LOCKED = ["customer_approval", "scheduled", "implement", "review", "customer_review", "closed", "rollback", "canceled"];
const REVIEW_FLAG_LOCKED = ["customer_review", "closed", "rollback", "canceled"];

export interface FakeChangeRequestApi {
  /** Who the app believes is signed in (applied on the next page load). */
  setViewer(user: FakeUser): void;
  /** The CR's current lifecycle state, as the fake backend holds it. */
  state(): string;
  /** The CR's current checkbox settings, as the fake backend holds them. */
  flags(): FakeCustomerFlags;
  /** Moves the fake CR to `next` out-of-band (e.g. while an edit dialog is
   * still open on a stale copy), without touching its approval stages. */
  setState(next: string): void;
  /** Every request the fake served, as "METHOD /path". */
  requests(): string[];
  /** Every request the fake served that carried a JSON body, in order. */
  requestBodies(): FakeRequestBody[];
  /** The customer scope the fake CR currently holds (as stored, ids only). */
  scope(): FakeScope;
  /** The `comment` / `workNote` journal entries the fake received, in order. */
  journal(): Array<{ kind: "comment" | "workNote"; text: string }>;
  /**
   * Deactivates a deployment server-side, behind the form's back: it drops out
   * of `link-options` and any create / PATCH that still names it is refused
   * with a 400 -- the "stale options" scenario.
   */
  retireDeployment(deploymentId: string): void;
  /**
   * Changes a project's registered contacts out-of-band (someone registers /
   * is deregistered); like the backend, a live customer stage follows on the
   * next write that touches the state or the project.
   */
  setProjectContacts(projectId: string, contacts: FakeUser[]): void;
  /** The planned window as the fake holds it ("YYYY-MM-DD HH:MM:SS", UTC). */
  planned(): { start: string; end: string };
  /** The approval stages as the fake holds them (stage name -> status). */
  stages(): Array<{ stage: string; status: string; approvers: Array<{ name: string; status: string }> }>;
  /**
   * Makes `GET /groups/{id}` fail with this HTTP status (for the dialog's
   * error state), or serve normally again with `null`.
   */
  failGroups(status: number | null): void;
}

const CUSTOMER_STAGES = ["Customer Approval", "Customer Review"];

/** The one state in which each stage can be decided (the backend's approvalStageDecidableState). */
const STAGE_STATE: Record<string, string> = {
  "Peer Approval": "assess",
  "CAB Approval": "authorize",
  "ECAB Approval": "authorize",
  Review: "review",
  "Customer Approval": "customer_approval",
  "Customer Review": "customer_review",
};
/** States nothing can be approved in any more. */
const FINAL_STATES = ["closed", "canceled", "rollback"];
/** "customer_review" -> "Customer Review", for the refusal message. */
const stateName = (s: string): string => s.split("_").map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join(" ");

function legalNextStates(state: string, flags: FakeCustomerFlags, liveCustomerStage = false): string[] {
  switch (state) {
    case "new":
      return ["assess", "canceled"];
    case "assess":
      return ["authorize", "canceled"]; // authorize = the approval path, never a button
    case "authorize":
      return ["canceled"];
    case "customer_approval":
      // scheduled = "Record customer approval", unless the customer group decides
      // ... and "authorize" there is Re-schedule, which an internal user keeps
      // even while the customer group's request is pending.
      return liveCustomerStage ? ["authorize", "canceled"] : ["scheduled", "authorize", "canceled"];
    case "scheduled":
      return ["implement", "canceled"];
    case "implement":
      return ["review", "canceled"];
    case "review":
      return flags.customerReviewRequired ? ["customer_review", "rollback", "canceled"] : ["closed", "rollback", "canceled"];
    case "customer_review":
      // a pending customer-group review is decided by its members
      return liveCustomerStage ? ["canceled"] : ["closed", "rollback", "canceled"];
    default:
      return [];
  }
}

const nextStage = (name: string, group: FakeApprovalGroup, who: FakeUser): Stage => ({
  stage: name,
  approverType: "STATIC_GROUP",
  approverName: group.name,
  assignmentGroup: { id: group.id, name: group.name },
  status: "REQUESTED",
  approvers: [{ id: who.id, name: who.name, status: "REQUESTED" }],
});

export async function installFakeChangeRequestApi(
  page: Page,
  initialType: FakeCrType,
  viewer: FakeUser = FAKE_CREATOR,
  initialFlags: Partial<FakeCustomerFlags> = {},
  /** The customer scope the CR already holds. */
  initialScope: Partial<FakeScope> = {},
): Promise<FakeChangeRequestApi> {
  let type = initialType;
  let currentViewer = viewer;
  let state = "new";
  let subject = "[E2E] approval flow (mocked)";
  const scope: FakeScope = {
    projectId: null,
    deploymentIds: [],
    deploymentProductIds: [],
    category: null,
  };
  if (initialScope.deploymentIds?.length) {
    scope.projectId = initialScope.projectId ?? null;
    scope.deploymentIds = [...initialScope.deploymentIds];
    scope.deploymentProductIds = derivedProductIds(scope.deploymentIds);
  } else if (initialScope.projectId) {
    scope.projectId = initialScope.projectId;
  }
  scope.category = initialScope.category ?? null;
  const journal: Array<{ kind: "comment" | "workNote"; text: string }> = [];
  const retired = new Set<string>();
  const bodies: FakeRequestBody[] = [];
  const flags: FakeCustomerFlags = {
    customerApprovalRequired: initialFlags.customerApprovalRequired ?? false,
    customerReviewRequired: initialFlags.customerReviewRequired ?? false,
  };
  /** Where a CR lands once its internal approval is granted. */
  const afterInternalApproval = (): string => (flags.customerApprovalRequired ? "customer_approval" : "scheduled");
  let stages: Stage[] = [];
  /** Registered contacts per project (the read-only Customer Group). */
  const contacts = new Map<string, FakeUser[]>(Object.entries(FAKE_PROJECT_CONTACTS).map(([id, users]) => [id, [...users]]));
  /** The CR's customer group as the fake derives it: its project's registered contacts. */
  const currentContacts = (): FakeUser[] => (scope.projectId ? (contacts.get(scope.projectId) ?? []) : []);
  /** When set, GET /groups/{id} fails with this status. */
  let groupFailure: number | null = null;
  let plannedStartOn = "2030-03-01 09:00:00";
  let plannedEndOn = "2030-03-01 11:00:00";
  const log: string[] = [];
  const hasLiveCustomerStage = (): boolean =>
    stages.some((s) => CUSTOMER_STAGES.includes(s.stage) && s.status === "REQUESTED");
  const legal = (): string[] => legalNextStates(state, flags, hasLiveCustomerStage());
  /** Moves the CR to `next`; entering a customer gate provisions the group's stage. */
  const enter = (next: string): void => {
    state = next;
    provisionReview();
    syncCustomerStage();
  };
  /**
   * Entering Review on a Normal change provisions the "Review" stage from the
   * assigned group (its internal members, the creator excluded); Emergency and
   * Standard changes never get one (the backend only provisions it once exactly
   * two internal stages exist).
   */
  function provisionReview(): void {
    if (state !== "review" || type !== "normal" || stages.some((s) => s.stage === "Review")) return;
    stages = [
      ...stages,
      {
        stage: "Review",
        approverType: "STATIC_GROUP",
        approverName: FAKE_PEER_GROUP.name,
        assignmentGroup: { id: FAKE_PEER_GROUP.id, name: FAKE_PEER_GROUP.name },
        status: "REQUESTED",
        approvers: FAKE_PEER_GROUP.members.filter((m) => m.id !== FAKE_CREATOR.id).map((m) => ({ id: m.id, name: m.name, status: "REQUESTED" })),
      },
    ];
  }
  /** Whether the CR has left (or can never be in) the state the stage can be decided in. */
  const stageOutOfState = (st: Stage): boolean => {
    const decidable = STAGE_STATE[st.stage];
    return decidable !== undefined && decidable !== state;
  };
  /**
   * The backend's `reconcileStaleApprovers`, run at the end of every PATCH and
   * every decision: the still-REQUESTED rows of every stage the CR has left are
   * cancelled -- all of them once it is closed / canceled / rollback. The stage
   * stays as a record, reported PENDING (nothing was approved or rejected on it).
   */
  function reconcile(): void {
    const final = FINAL_STATES.includes(state);
    for (const st of stages) {
      if (!final && !stageOutOfState(st)) continue;
      let cancelled = false;
      for (const a of st.approvers) {
        if (a.status === "REQUESTED") {
          a.status = "CANCELLED";
          cancelled = true;
        }
      }
      if (cancelled && st.status === "REQUESTED") st.status = "PENDING";
    }
  }
  /**
   * The backend's idempotent `provisionCustomerStage`: at a customer gate the
   * project's eligible registered contacts (everyone but the creator) get
   * exactly one live stage. Entered with contacts -> provisioned; project (or
   * its contacts) changed while a stage is live -> the old stage's REQUESTED
   * rows are cancelled and a new one is provisioned; no contacts -> pending
   * rows cancelled (manual path returns); a stage already approved/rejected is
   * never re-provisioned.
   */
  function syncCustomerStage(): void {
    const kind = state === "customer_approval" ? "Customer Approval" : state === "customer_review" ? "Customer Review" : null;
    if (!kind) return;
    const members = currentContacts().filter((u) => u.id !== FAKE_CREATOR.id);
    const live = stages.find((s) => s.stage === kind && s.status === "REQUESTED");
    if (live) {
      const have = live.approvers.map((a) => a.id);
      if (members.length > 0 && sameSet(have, members.map((m) => m.id))) return;
      for (const a of live.approvers) if (a.status === "REQUESTED") a.status = "CANCELLED";
      live.status = "CANCELLED";
    }
    const settled = stages.some((s) => s.stage === kind && (s.status === "APPROVED" || s.status === "REJECTED"));
    if (settled || members.length === 0) return;
    stages = [
      ...stages,
      {
        stage: kind,
        approverType: "STATIC_GROUP",
        approverName: "Customer Group",
        assignmentGroup: null, // the project's registered contacts, not a group
        status: "REQUESTED",
        approvers: members.map((m) => ({ id: m.id, name: m.name, status: "REQUESTED" })),
      },
    ];
  }

  const detail = (): Record<string, unknown> => ({
    id: FAKE_CR_ID,
    number: "CHG0099001",
    subject,
    createdOn: "2026-01-01T00:00:00Z",
    createdBy: FAKE_CREATOR.email,
    state,
    type,
    assignedTeam: { id: "00000000-0000-0000-0000-00000000a001", name: "Platform" },
    requestedBy: { id: FAKE_CREATOR.id, name: FAKE_CREATOR.name },
    customerApprovalRequired: flags.customerApprovalRequired,
    customerReviewRequired: flags.customerReviewRequired,
    legalNextStates: legal(),
    plannedStartOn,
    plannedEndOn,
    project: FAKE_PROJECTS.find((p) => p.id === scope.projectId),
    deployments: FAKE_DEPLOYMENTS.filter((d) => scope.deploymentIds.includes(d.id)).map(({ id, name }) => ({ id, name })),
    deploymentProducts: FAKE_DEPLOYMENT_PRODUCTS.filter((p) => scope.deploymentProductIds.includes(p.id)).map(({ id, name }) => ({ id, name })),
    customerContacts: currentContacts().map(({ id, name, email }) => ({ id, name, email })),
    category: scope.category,
  });

  /**
   * The backend's validation of a create / PATCH body's customer scope, applied to
   * `next` (the scope as it would be after the write). Returns the 400 message,
   * or null when the combination is consistent.
   */
  const validateScope = (
    body: Record<string, unknown>,
    next: FakeScope,
    isPatch: boolean,
  ): string | null => {
    // The removed fields are refused outright (any value, null included).
    if (body.customerGroupId !== undefined) return CUSTOMER_GROUP_ID_REMOVED;
    if (body.environmentIds !== undefined) return ENVIRONMENT_IDS_REMOVED;
    if (typeof body.category === "string" && !CATEGORIES.includes(body.category)) {
      return `category must be one of ${CATEGORIES.join(", ")}`;
    }
    if (isPatch) {
      const touchesScope =
        (body.projectId !== undefined && body.projectId !== scope.projectId) ||
        (body.deploymentIds !== undefined && !sameSet(body.deploymentIds as string[], scope.deploymentIds));
      if (touchesScope && SCOPE_LOCKED.includes(state)) {
        return `projectId and deploymentIds can no longer be changed once the change request is ${state}`;
      }
      if (body.projectId !== undefined && body.projectId !== scope.projectId && scope.deploymentIds.length > 0 && body.deploymentIds === undefined) {
        return "changing projectId while deployments are stored requires deploymentIds in the same request";
      }
    }
    if (next.deploymentIds.length > 0 && !next.projectId) {
      return "deploymentIds requires projectId";
    }
    if (next.projectId && !FAKE_PROJECTS.some((p) => p.id === next.projectId)) {
      return `projectId: project ${next.projectId} not found`;
    }
    for (const id of next.deploymentIds) {
      const d = FAKE_DEPLOYMENTS.find((x) => x.id === id);
      if (!d) return `deploymentIds: deployment ${id} not found`;
      if (d.projectId !== next.projectId || retired.has(d.id)) {
        return `deploymentIds: deployment ${d.name} is not an active deployment of the selected project`;
      }
    }
    if (body.deploymentProductIds !== undefined && !sameSet(body.deploymentProductIds as string[], derivedProductIds(next.deploymentIds))) {
      return "deploymentProductIds: deployment products are derived from the selected deployments and must be exactly that set";
    }
    return null;
  };

  /** The scope after applying the scope fields of `body` on top of the stored one. */
  const applyScope = (body: Record<string, unknown>, base: FakeScope): FakeScope => {
    const next: FakeScope = { ...base, deploymentIds: [...base.deploymentIds] };
    if (body.projectId !== undefined) next.projectId = body.projectId as string;
    if (body.deploymentIds !== undefined) next.deploymentIds = body.deploymentIds as string[];
    next.deploymentProductIds = derivedProductIds(next.deploymentIds);
    if (body.category !== undefined) next.category = body.category as string | null;
    return next;
  };

  const cors = (route: Route): Record<string, string> => ({
    "access-control-allow-origin": route.request().headers()["origin"] ?? "*",
    "access-control-allow-headers": "*",
    "access-control-allow-methods": "GET,POST,PATCH,PUT,DELETE,OPTIONS",
    "access-control-allow-credentials": "true",
  });
  const json = (route: Route, body: unknown, status = 200): Promise<void> =>
    route.fulfill({
      status,
      contentType: "application/json",
      headers: cors(route),
      body: JSON.stringify(body),
    });

  // Same identity swap for /users/me: keep the real profile (roles, time
  // zone, ...) but make the signed-in user one of the fake people above.
  await page.route(
    (url) => url.pathname.endsWith("/users/me"),
    async (route) => {
      const type = route.request().resourceType();
      if (route.request().method() !== "GET" || (type !== "fetch" && type !== "xhr")) return route.fallback();
      const real = await route.fetch();
      const profile = (await real.json()) as Record<string, unknown>;
      const [firstName, ...rest] = currentViewer.name.split(" ");
      await route.fulfill({
        response: real,
        json: { ...profile, id: currentViewer.id, email: currentViewer.email, firstName, lastName: rest.join(" ") },
      });
    },
  );

  /** Common prologue for the endpoints below: only XHR/fetch, answers CORS preflights. */
  const isApiCall = async (route: Route): Promise<boolean> => {
    const req = route.request();
    const rtype = req.resourceType();
    if (rtype !== "fetch" && rtype !== "xhr") {
      await route.fallback();
      return false;
    }
    if (req.method() === "OPTIONS") {
      await route.fulfill({ status: 204, headers: cors(route) });
      return false;
    }
    return true;
  };
  const bodyOf = (route: Route): Record<string, unknown> | undefined => {
    try {
      return (route.request().postDataJSON() as Record<string, unknown> | null) ?? undefined;
    } catch {
      return undefined;
    }
  };
  const record = (route: Route, label: string): Record<string, unknown> | undefined => {
    log.push(label);
    const body = bodyOf(route);
    bodies.push({ request: label, body });
    return body;
  };

  // Customer Project picker.
  await page.route(
    (url) => url.pathname.endsWith("/projects/search"),
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      if (!(await isApiCall(route))) return;
      const body = record(route, "POST /projects/search");
      const q = String((body?.searchQuery as string | undefined) ?? "").toLowerCase();
      const projects = FAKE_PROJECTS.filter((p) => p.name.toLowerCase().includes(q));
      return json(route, { projects, hasMore: false, totalRecords: projects.length });
    },
  );

  // Assignment group picker (the Customer Group is derived, so it is not searched).
  await page.route(
    (url) => url.pathname.endsWith("/groups/search"),
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      if (!(await isApiCall(route))) return;
      const body = record(route, "POST /groups/search");
      const q = String(((body?.filters as { searchQuery?: string } | undefined)?.searchQuery) ?? "").toLowerCase();
      const groups = FAKE_GROUPS.filter((g) => g.name.toLowerCase().includes(q)).map((g) => ({ ...g, active: true }));
      return json(route, { groups, total: groups.length, limit: 20, offset: 0 });
    },
  );

  // One group and its members: what opens from a stage's Assignment group.
  await page.route(
    (url) => /\/groups\/[0-9a-f-]{36}$/.test(url.pathname),
    async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      if (!(await isApiCall(route))) return;
      const id = new URL(route.request().url()).pathname.split("/").pop()!;
      record(route, `GET /groups/${id}`);
      if (groupFailure !== null) return json(route, { message: "Failed to retrieve group." }, groupFailure);
      const group = FAKE_APPROVAL_GROUPS.find((g) => g.id === id);
      if (!group) return json(route, { message: "Not found." }, 404);
      return json(route, {
        id: group.id,
        name: group.name,
        description: group.description,
        email: group.email,
        manager: group.manager,
        members: group.members.map((m) => ({ id: m.id, name: m.name, email: m.email || null, userType: "INTERNAL", role: m.role })),
        total: group.members.length,
      });
    },
  );

  // The Customer Project -> Deployments -> Deployment products cascade, plus the project's contacts.
  await page.route(
    (url) => url.pathname.endsWith("/change-requests/link-options"),
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      if (!(await isApiCall(route))) return;
      const body = record(route, "POST /change-requests/link-options") ?? {};
      const projectId = body.projectId as string | undefined;
      if (!projectId) return json(route, { message: "projectId is required" }, 400);
      if (!FAKE_PROJECTS.some((p) => p.id === projectId)) {
        return json(route, { message: `projectId does not refer to an existing project: ${projectId}` }, 400);
      }
      const chosen = (body.deploymentIds as string[] | undefined) ?? [];
      const projectDeployments = FAKE_DEPLOYMENTS.filter((d) => d.projectId === projectId && !retired.has(d.id));
      const stray = chosen.find((id) => !projectDeployments.some((d) => d.id === id));
      if (stray) return json(route, { message: `deployment ${stray} does not belong to the selected project` }, 400);
      return json(route, {
        deployments: projectDeployments.map((d) => ({
          id: d.id,
          name: d.name,
          type: d.type,
        })),
        customerContacts: (contacts.get(projectId) ?? []).map(({ id, name, email }) => ({ id, name, email })),
        deploymentProducts: FAKE_DEPLOYMENT_PRODUCTS.filter((p) => chosen.includes(p.deploymentId)).map((p) => ({
          id: p.id,
          name: p.name,
          deployment: FAKE_DEPLOYMENTS.filter((d) => d.id === p.deploymentId).map(({ id, name }) => ({ id, name }))[0],
        })),
      });
    },
  );

  // Create. The fake holds exactly one CR (FAKE_CR_ID); creating "creates" it.
  await page.route(
    (url) => url.pathname.endsWith("/change-requests"),
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      if (!(await isApiCall(route))) return;
      const body = record(route, "POST /change-requests") ?? {};
      if (!["normal", "standard", "emergency"].includes(body.type as string)) {
        return json(route, { message: "type is required: a change request must be one of standard, normal or emergency" }, 400);
      }
      const next = applyScope(body, scope);
      const problem = validateScope(body, next, false);
      if (problem) return json(route, { message: problem }, 400);
      Object.assign(scope, next);
      type = body.type as FakeCrType;
      state = "new";
      subject = String(body.subject ?? subject);
      if (body.customerApprovalRequired !== undefined) flags.customerApprovalRequired = body.customerApprovalRequired as boolean;
      if (body.customerReviewRequired !== undefined) flags.customerReviewRequired = body.customerReviewRequired as boolean;
      for (const kind of ["comment", "workNote"] as const) {
        const text = body[kind];
        if (typeof text === "string" && text.trim()) journal.push({ kind, text });
      }
      return json(
        route,
        { message: "Change request created.", changeRequest: { id: FAKE_CR_ID, number: "CHG0099001", createdOn: "2026-01-01T00:00:00Z", createdBy: currentViewer.email } },
        201,
      );
    },
  );

  await page.route(
    (url) => new RegExp(`/change-requests/${FAKE_CR_ID}(/.*)?$`).test(url.pathname),
    async (route) => {
      const req = route.request();
      const rtype = req.resourceType();
      if (rtype !== "fetch" && rtype !== "xhr") return route.fallback();
      if (req.method() === "OPTIONS") return route.fulfill({ status: 204, headers: cors(route) });

      const path = new URL(req.url()).pathname.replace(/^.*\/change-requests\//, "/change-requests/");
      record(route, `${req.method()} ${path}`);

      if (path.endsWith("/approvals/decision") && req.method() === "POST") {
        const { decision } = req.postDataJSON() as { decision: "approved" | "rejected" };
        // The caller's pending stage: their row on a stage decidable in the CR's
        // current state, else (all of theirs are stale) the first one.
        const mine = stages.filter((s) => s.status === "REQUESTED" && s.approvers.some((a) => a.id === currentViewer.id && a.status === "REQUESTED"));
        const current = mine.find((s) => !stageOutOfState(s)) ?? mine[0];
        const row = current?.approvers.find((a) => a.id === currentViewer.id && a.status === "REQUESTED");
        if (!current || !row || currentViewer.id === FAKE_CREATOR.id) {
          return json(route, { message: "Access to the requested resource is forbidden!" }, 403);
        }
        if (stageOutOfState(current)) {
          // Nothing is changed: the approval is no longer pending.
          return json(
            route,
            {
              message: `this approval is no longer pending: the change request is in ${stateName(state)}, but the ${current.stage} stage can only be decided while it is in ${stateName(STAGE_STATE[current.stage]!)}`,
            },
            409,
          );
        }
        row.status = decision === "approved" ? "APPROVED" : "REJECTED";
        current.status = row.status;
        if (!CUSTOMER_STAGES.includes(current.stage)) {
          // Like the backend, a resolving decision cancels the stage's other pending approvers.
          for (const a of current.approvers) if (a !== row && a.status === "REQUESTED") a.status = "CANCELLED";
        }
        if (CUSTOMER_STAGES.includes(current.stage)) {
          // One member's decision settles the stage; co-members are no longer needed.
          for (const a of current.approvers) if (a !== row && a.status === "REQUESTED") a.status = "NOT_REQUIRED";
          if (decision === "approved") enter(current.stage === "Customer Approval" ? "scheduled" : "closed");
          else enter(current.stage === "Customer Approval" ? "canceled" : "rollback");
        } else if (decision === "approved") {
          if (current.stage === "Peer Approval") {
            state = "authorize";
            stages = [...stages, nextStage("CAB Approval", FAKE_CAB_GROUP, FAKE_CAB)];
          } else if (current.stage === "CAB Approval" || current.stage === "ECAB Approval") {
            enter(afterInternalApproval()); // CAB / ECAB approval moves the CR on itself
          }
          // Review: the answer is recorded, the CR stays in review (a human moves it on).
        }
        reconcile();
        return json(route, { id: FAKE_CR_ID, state });
      }
      if (path.endsWith("/approvals") && req.method() === "GET") {
        // Like the Postgres-backed API: `canDecide` is true only on the
        // caller's own REQUESTED row, and never for the CR's creator.
        return json(route, {
          approvals: stages.map((st) => ({
            ...st,
            approvers: st.approvers.map((a) => ({
              ...a,
              canDecide:
                st.status === "REQUESTED" &&
                a.id === currentViewer.id &&
                a.status === "REQUESTED" &&
                currentViewer.id !== FAKE_CREATOR.id &&
                !stageOutOfState(st),
            })),
          })),
        });
      }
      if (path.endsWith("/comments") && req.method() === "POST") {
        const { content } = req.postDataJSON() as { content?: string };
        if (typeof content === "string" && content.trim()) journal.push({ kind: "comment", text: content });
        return json(route, { id: "00000000-0000-0000-0000-00000000d001", content, type: "comment", createdOn: "2026-01-01T00:00:00Z", createdBy: null }, 201);
      }
      if (path.endsWith("/comments/search")) {
        return json(route, { comments: [], hasMore: false, totalRecords: 0 });
      }
      if (req.method() === "PATCH") {
        const body = req.postDataJSON() as {
          state?: string;
          customerApprovalRequired?: boolean;
          customerReviewRequired?: boolean;
        } & Record<string, unknown>;
        // Customer scope / category (and the removed customerGroupId / environmentIds,
        // which are refused), validated like the backend.
        const touchesScopeFields = ["projectId", "deploymentIds", "environmentIds", "deploymentProductIds", "customerGroupId", "category"].some(
          (k) => body[k] !== undefined,
        );
        if (touchesScopeFields) {
          const next = applyScope(body, scope);
          const problem = validateScope(body, next, true);
          if (problem) return json(route, { message: problem }, 400);
          Object.assign(scope, next);
          // A project written while the CR already sits at a customer gate
          // (re)provisions the stage for that project's contacts, like the backend.
          if (body.projectId !== undefined) syncCustomerStage();
        }
        for (const kind of ["comment", "workNote"] as const) {
          const text = body[kind];
          if (typeof text === "string" && text.trim()) journal.push({ kind, text });
        }
        // Checkbox edits: refused once the gate they control has passed.
        if (body.customerApprovalRequired !== undefined) {
          if (APPROVAL_FLAG_LOCKED.includes(state)) {
            return json(route, { message: `customerApprovalRequired cannot be changed once the change request is ${state}` }, 400);
          }
          flags.customerApprovalRequired = body.customerApprovalRequired;
        }
        if (body.customerReviewRequired !== undefined) {
          if (REVIEW_FLAG_LOCKED.includes(state)) {
            return json(route, { message: `customerReviewRequired cannot be changed once the change request is ${state}` }, 400);
          }
          flags.customerReviewRequired = body.customerReviewRequired;
        }
        const target = body.state;
        if (target === undefined) {
          return json(route, { id: FAKE_CR_ID, state, message: "Change request updated.", changeRequest: detail() });
        }
        if (state === "rollback" && target !== "rollback") {
          return json(route, { message: "change request has been rolled back; rollback is final and its state can no longer be changed" }, 400);
        }
        if (target === "rollback") {
          if (state !== "review" && state !== "customer_review") {
            return json(route, { message: 'state "rollback" can only be set from review or customer_review' }, 400);
          }
          if (hasLiveCustomerStage()) {
            return json(route, { message: "The customer group must decide the review; it cannot be rolled back manually." }, 400);
          }
          // Rolling back cancels every still-requested approver row (the closing reconcile).
          state = "rollback";
        } else if (target === "authorize") {
          // Re-schedule: the one manual way into Authorize, from Customer Approval only,
          // and only when the planned window really changes.
          if (state !== "customer_approval") {
            return json(
              route,
              {
                message:
                  'state "authorize" cannot be set manually: it is reached automatically through the approval flow (Request Approval, then peer approval); it can only be set by hand to re-schedule a change from customer_approval',
              },
              400,
            );
          }
          const newStart = typeof body.plannedStartOn === "string" ? body.plannedStartOn : undefined;
          const newEnd = typeof body.plannedEndOn === "string" ? body.plannedEndOn : undefined;
          if ((!newStart || newStart === plannedStartOn) && (!newEnd || newEnd === plannedEndOn)) {
            return json(
              route,
              { message: "re-scheduling requires a changed planned start or end: send plannedStartOn and/or plannedEndOn with a value different from the stored one" },
              400,
            );
          }
          if ((newStart ?? plannedStartOn) > (newEnd ?? plannedEndOn)) {
            return json(route, { message: "the planned start must not be after the planned end" }, 400);
          }
          plannedStartOn = newStart ?? plannedStartOn;
          plannedEndOn = newEnd ?? plannedEndOn;
          // The customer's pending request is superseded: rows cancelled, the stage stays
          // as a record and is reported PENDING (nothing was approved or rejected on it).
          for (const st of stages) {
            if (CUSTOMER_STAGES.includes(st.stage) && st.status === "REQUESTED") {
              for (const a of st.approvers) if (a.status === "REQUESTED") a.status = "CANCELLED";
              st.status = "PENDING";
            }
          }
          if (type === "standard") {
            syncCustomerStage(); // nothing internal to repeat: the customer is asked again
          } else {
            state = "authorize";
            stages = [...stages, type === "emergency" ? nextStage("ECAB Approval", FAKE_ECAB_GROUP, FAKE_ECAB) : nextStage("CAB Approval", FAKE_CAB_GROUP, FAKE_CAB)];
          }
        } else if (target === "assess") {
          if (type === "standard") enter(afterInternalApproval());
          else if (type === "emergency") {
            state = "authorize";
            stages = [nextStage("ECAB Approval", FAKE_ECAB_GROUP, FAKE_ECAB)];
          } else {
            state = "assess";
            stages = [nextStage("Peer Approval", FAKE_PEER_GROUP, FAKE_PEER)];
          }
        } else if (target === "scheduled" && state === "customer_approval") {
          if (hasLiveCustomerStage()) {
            return json(route, { message: "The customer group must approve this change request; it cannot be recorded manually." }, 400);
          }
          state = "scheduled"; // the customer's approval was recorded
        } else if (target !== "scheduled" && target !== "authorize" && target !== "customer_approval") {
          if (!legal().includes(target)) {
            return json(route, { message: `Illegal transition from ${state} to ${target}.` }, 400);
          }
          enter(target);
        } else {
          return json(route, { message: `Illegal transition to ${String(target)}.` }, 400);
        }
        reconcile();
        return json(route, { id: FAKE_CR_ID, state });
      }
      if (req.method() === "GET" && path === `/change-requests/${FAKE_CR_ID}`) {
        return json(route, detail());
      }
      return route.fallback();
    },
  );

  return {
    setViewer: (user) => {
      currentViewer = user;
    },
    state: () => state,
    setState: (next) => {
      state = next;
    },
    flags: () => ({ ...flags }),
    requests: () => [...log],
    requestBodies: () => [...bodies],
    scope: () => ({ ...scope, deploymentIds: [...scope.deploymentIds], deploymentProductIds: [...scope.deploymentProductIds] }),
    journal: () => [...journal],
    planned: () => ({ start: plannedStartOn, end: plannedEndOn }),
    retireDeployment: (deploymentId) => {
      retired.add(deploymentId);
    },
    setProjectContacts: (projectId, users) => {
      contacts.set(projectId, [...users]);
    },
    failGroups: (status) => {
      groupFailure = status;
    },
    stages: () =>
      stages.map((st) => ({
        stage: st.stage,
        status: st.status,
        approvers: st.approvers.map((a) => ({ name: a.name, status: a.status })),
      })),
  };
}
