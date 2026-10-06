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
// Deterministic Change Request lifecycle coverage — the compulsory-
// assigned-team gate, Assess-entry approver auto-provisioning, the
// approve/cancel-sibling cascade from Assess to Authorize, a terminal
// approval display, and the customer group's Customer Approval / Customer
// Review decisions — run against the eight fixed-UUID fixtures in
// scripts/csm-compose/seed-entity-service.sql (CHG-FIXED-001..008), not a
// freshly self-provisioned CR the way change-request-detail.spec.ts works.
//
// That's a deliberate, necessary difference, not a style choice:
// CreateChangeRequest always 503s against this stack's own Postgres data
// source (work_item.number has no DB sequence — see entity-service's own
// CLAUDE.md, "CreateCase and case numbers"/"Change requests"), so there is
// no "create one, then drive it" path available locally at all. The fixed
// fixtures exist specifically to give this spec something to navigate
// straight to by id.
//
// The flip side of a fixed fixture: they get moved forward by exactly the
// transition this spec exercises (New -> Assess, an approval decision, a
// customer's answer). The seed is self-healing on purpose — re-running
// seed-entity-service.sql deletes and re-inserts the fixtures' approval stages
// and approvers and upserts their change_request rows back to the starting
// state — so resetFixtures() below simply re-runs that file against the
// already-running local docker-compose Postgres (`csmcr-postgres-1` here;
// E2E_POSTGRES_CONTAINER) before anything else runs. One source of truth: the
// starting state lives in the seed only.
//
// WHO acts is the point of the seed's personas (see "Local seed personas" in
// entity-service's CLAUDE.md), so the specs sign in as the persona that
// holds the seat — one captured session per role, minted by
// tests/e2e/auth/generate-session.spec.ts (see auth/README.md):
//
//   crApprover          jane.doe@example.com        internal, the requester persona
//   crInternalApprover  alice.perera@example.com    internal, peer/CAB approver
//   crCustomerContact   dave.mendis@example.com     external, contact of project 401
//   crCustomerContact2  erin.jayawardena@example.com  external, contact of project 401
//
// Runs only against the local stack (E2E_NO_WEBSERVER=1, see
// package.json's "test:e2e:cr-lifecycle"). A test whose persona has no
// captured session is skipped, not failed.
//

import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import type { Browser, Page } from "@playwright/test";
import { test, expect, withRole, hasSession, openContextAs, type TimecardRole } from "../../fixtures/test";
import { ChangeRequestCreatePage } from "../../pages/ChangeRequestCreatePage";
import { ChangeRequestDetailPage } from "../../pages/ChangeRequestDetailPage";
import {
  FAKE_CAB,
  FAKE_CAB_COLLEAGUE,
  FAKE_CAB_GROUP,
  FAKE_CAB_NO_EMAIL,
  FAKE_CR_ID,
  FAKE_CREATOR,
  FAKE_DEPLOYMENTS,
  FAKE_DEPLOYMENT_PRODUCTS,
  FAKE_ECAB,
  FAKE_ECAB_COLLEAGUE,
  FAKE_ECAB_GROUP,
  FAKE_BETA_CONTACT,
  FAKE_CUST_ONE,
  FAKE_CUST_TWO,
  FAKE_OUTSIDER,
  FAKE_PEER,
  FAKE_PEER_COLLEAGUE,
  FAKE_PEER_GROUP,
  FAKE_PROJECT_CONTACTS,
  FAKE_PROJECTS,
  installFakeChangeRequestApi,
  type FakeChangeRequestApi,
  type FakeUser,
} from "../../utils/fakeChangeRequestApi";

const CR_NO_TEAM = "00000000-0000-0000-0000-000000001001";
const CR_WITH_TEAM = "00000000-0000-0000-0000-000000001002";
const CR_PENDING_APPROVAL = "00000000-0000-0000-0000-000000001003";
const CR_RESOLVED = "00000000-0000-0000-0000-000000001004";
const CR_IN_REVIEW = "00000000-0000-0000-0000-000000001202"; // CHG-FIXED-006 (Review, Customer Review ticked)
const CR_CUSTOMER_APPROVAL = "00000000-0000-0000-0000-000000001303"; // CHG-FIXED-007
const CR_CUSTOMER_REVIEW = "00000000-0000-0000-0000-000000001304"; // CHG-FIXED-008

/** The seed's personas, by the display name the Approvals table shows. */
const ALICE = "Alice Perera"; // internal — peer / CAB / ECAB approver
const BOB = "Bob Fernando"; // internal
const CAROL = "Carol Silva"; // internal
const DAVE = "Dave Mendis"; // external — registered contact of project 401
const ERIN = "Erin Jayawardena"; // external — registered contact of project 401
const JANE = "Jane Doe"; // internal requester persona, in no approval group
const JOHN = "John Smith"; // customer who is (deliberately) a member of the assigned group

const DAVE_ID = "00000000-0000-0000-0000-000000000021";

/** Absolute path of the seed file, from the webapp dir the specs run in. */
const SEED_FILE = path.resolve(process.cwd(), "../../../scripts/csm-compose/seed-entity-service.sql");
const POSTGRES_CONTAINER = process.env.E2E_POSTGRES_CONTAINER ?? "csm-platform-postgres-1";

/** Runs SQL on the local docker-compose Postgres (psql in the container) and
 * resolves with what it printed (unaligned, tuples only). With `sql` on stdin
 * so the whole seed file fits however large it is. */
async function psqlOutput(sql: string): Promise<string> {
  return await new Promise<string>((resolve, reject) => {
    const child = spawn("docker", [
      "exec", "-i", POSTGRES_CONTAINER, "psql", "-U", "postgres", "-d", "csm_platform", "-v", "ON_ERROR_STOP=1", "-q", "-t", "-A", "-f", "-",
    ]);
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d: Buffer) => (stdout += d.toString()));
    child.stderr.on("data", (d: Buffer) => (stderr += d.toString()));
    child.on("error", reject);
    child.on("close", (code) =>
      code === 0 ? resolve(stdout.trim()) : reject(new Error(`psql exited ${code}: ${stderr}`)),
    );
    child.stdin.end(sql);
  });
}

async function psql(sql: string): Promise<void> {
  await psqlOutput(sql);
}

/** Restores the CHG-FIXED-* fixtures (and the personas) to their starting
 * state by re-running the self-healing seed — see this file's top comment. */
async function resetFixtures(): Promise<void> {
  await psql(fs.readFileSync(SEED_FILE, "utf8"));
}

/** Runs `fn` as `role`'s persona in a second, independent browser context. */
async function asPersona<T>(browser: Browser, role: TimecardRole, fn: (page: Page) => Promise<T>): Promise<T> {
  test.skip(
    !hasSession(role),
    `No captured session for '${role}'. See tests/e2e/auth/README.md to mint ` +
      `tests/e2e/storageState/${role}.json (E2E_AUTH_EMAIL=… E2E_AUTH_ROLE=${role}).`,
  );
  const context = await openContextAs(browser, role);
  try {
    return await fn(await context.newPage());
  } finally {
    await context.close();
  }
}

/** POSTs the caller's decision on a change request straight to the BFF with the
 * very headers the signed-in page itself sends, to see the status the API
 * answers — what the Approve button would have produced had it been rendered. */
async function postDecision(page: Page, crId: string, decision: "approved" | "rejected") {
  const detail = new ChangeRequestDetailPage(page);
  const [request] = await Promise.all([
    page.waitForRequest((r) => r.method() === "GET" && new RegExp(`/change-requests/${crId}/approvals`).test(r.url())),
    detail.goto(crId),
  ]);
  const all = await request.allHeaders();
  const headers: Record<string, string> = {};
  for (const [name, value] of Object.entries(all)) {
    if (name === "authorization" || name.startsWith("x-")) headers[name] = value;
  }
  const base = request.url().replace(/\/change-requests\/.*$/, "");
  const response = await page.request.post(`${base}/change-requests/${crId}/approvals/decision`, {
    headers,
    data: { decision },
  });
  return { status: response.status(), body: await response.text() };
}

withRole(test, "crApprover");

// The seeded-fixture describes below need the local docker-compose stack and
// reset its Postgres rows first; scoped to this wrapper so the approval-flow
// describes at the bottom of the file (which run against an in-browser fake of
// the change-request API, see utils/fakeChangeRequestApi.ts) don't need docker.
test.describe("seeded fixtures (local stack)", () => {
test.beforeAll(async () => {
  await resetFixtures();
});

test.describe("change request lifecycle — compulsory team gate", () => {
  test("Request Approval is disabled with no assigned team, and states why", async ({ page }) => {
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(CR_NO_TEAM);

    const blocked = page.getByLabel(/Request Approval: .*assigned team/i);
    await expect(blocked).toBeVisible();
    await expect(blocked.getByRole("button", { name: "Request Approval" })).toBeDisabled();
    await expect(detail.scheduleButton()).toHaveCount(0);
  });
});

test.describe("change request lifecycle — Assess-entry auto-provisioning", () => {
  test("Request Approval succeeds once a team is assigned, and provisions that team's INTERNAL members as approvers", async ({
    page,
  }) => {
    test.setTimeout(60_000);

    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(CR_WITH_TEAM);

    const requestApprovalButton = detail.requestApprovalButton();
    await expect(requestApprovalButton).toBeEnabled();

    const [response] = await Promise.all([
      page.waitForResponse(
        (r) => new RegExp(`/change-requests/${CR_WITH_TEAM}$`).test(r.url()) && r.request().method() === "PATCH",
        { timeout: 15_000 },
      ),
      detail.requestApproval(),
    ]);
    expect(response.ok(), `Request Approval PATCH failed (${response.status()})`).toBeTruthy();

    await expect(requestApprovalButton).toBeHidden({ timeout: 15_000 });

    // The assigned team's active internal members (Alice, Bob, Carol — see
    // scripts/csm-compose/seed-entity-service.sql) are now Requested peer
    // approvers, with no manual provisioning step...
    await expect(detail.approverStatus(ALICE, "Peer Approval")).toHaveText("Requested");
    await expect(detail.approverStatus(BOB, "Peer Approval")).toHaveText("Requested");
    await expect(detail.approverStatus(CAROL, "Peer Approval")).toHaveText("Requested");
    // ...and nobody else: John Smith is a member of the same group but a
    // customer (EXTERNAL), who could not even find this change request in his
    // own list, so he is never provisioned; Jane Doe is out of the group.
    await expect(detail.approverRow(JOHN)).toHaveCount(0);
    await expect(detail.approverRow(JANE)).toHaveCount(0);
  });
});

test.describe("change request lifecycle — approve cascades to Authorize", () => {
  test("an internal approver sees the peer stage and approving it cascades to Authorize (CAB) and cancels the others", async ({
    browser,
  }) => {
    test.setTimeout(90_000);

    await asPersona(browser, "crInternalApprover", async (page) => {
      const detail = new ChangeRequestDetailPage(page);
      await detail.goto(CR_PENDING_APPROVAL);

      // Only the signed-in user's (Alice's) own pending row renders an
      // Approve button — Bob's and Carol's sibling rows have none. Rows are
      // scoped to the "Peer Approval" stage because, once Alice approves, the
      // backend adds a CAB Approval stage listing the same three people.
      const PEER = "Peer Approval";
      await expect(detail.approverStatus(ALICE, PEER)).toHaveText("Requested");
      await expect(detail.approverStatus(BOB, PEER)).toHaveText("Requested");
      await expect(detail.approverStatus(CAROL, PEER)).toHaveText("Requested");
      await expect(detail.approveButton(BOB, PEER)).toHaveCount(0);
      await expect(detail.approveButton(CAROL, PEER)).toHaveCount(0);

      const approveButton = detail.approveButton(ALICE, PEER);
      await expect(approveButton).toBeVisible();

      const [response] = await Promise.all([
        page.waitForResponse((r) => /\/change-requests\/[^/]+\/approvals?/.test(r.url()), { timeout: 15_000 }),
        approveButton.click(),
      ]);
      expect(response.ok(), `Approve decision failed (${response.status()})`).toBeTruthy();

      await expect(detail.approverStatus(ALICE, PEER)).toHaveText("Approved");
      await expect(detail.approverStatus(BOB, PEER)).toHaveText("Cancelled");
      await expect(detail.approverStatus(CAROL, PEER)).toHaveText("Cancelled");

      // Peer approval cascades to the CAB Approval stage (its own group, the
      // next stage), the CR is in Authorize, and there is no Schedule button.
      await expect(detail.currentStep()).toContainText("Authorize");
      await expect(detail.blockingReason()).toHaveText("Awaiting CAB Approval");
      await expect(detail.approverStatus(ALICE, "CAB Approval")).toHaveText("Requested");
      await expect(detail.approverStatus(BOB, "CAB Approval")).toHaveText("Requested");
      await expect(detail.approverStatus(CAROL, "CAB Approval")).toHaveText("Requested");
      await expect(detail.scheduleButton()).toHaveCount(0);

      // ...and CAB approval by another internal user schedules it.
      await detail.approveButton(ALICE, "CAB Approval").click();
      await expect(detail.approverStatus(ALICE, "CAB Approval")).toHaveText("Approved");
      await expect(detail.currentStep()).toContainText("Scheduled");
    });
  });
});

test.describe("change request lifecycle — terminal approval display", () => {
  test("an already-decided change request shows its resolved approval state with no pending actions", async ({
    page,
  }) => {
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(CR_RESOLVED);

    await expect(detail.approverStatus(ALICE, "Peer Approval")).toHaveText("Approved");
    await expect(detail.approverStatus(BOB, "Peer Approval")).toHaveText("Cancelled");
    await expect(detail.approverStatus(CAROL, "Peer Approval")).toHaveText("Cancelled");
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);
  });
});

test.describe("change request lifecycle — opening an Assignment group", () => {
  test("an internal stage's group opens and lists the people its pool is drawn from, in name order", async ({ page }) => {
    test.setTimeout(60_000);

    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(CR_RESOLVED);

    await detail.groupLink(ALICE, "Example Corp ABT", "Peer Approval").click();
    const dialog = detail.groupDialog("Example Corp ABT");
    await expect(dialog).toBeVisible();
    // Alice, Bob and Carol are the group's active internal members (the
    // approvers it provisions). Jane Doe is in the *team* of that name, not in
    // the group, and John Smith is a customer: neither is a peer approver, so
    // neither is listed.
    await expect(dialog.getByRole("heading", { name: "Group Members (3)" })).toBeVisible();
    await expect(dialog.getByRole("listitem")).toHaveText([
      new RegExp(`${ALICE}.*alice\\.perera@example\\.com`),
      new RegExp(`${BOB}.*bob\\.fernando@example\\.com`),
      new RegExp(`${CAROL}.*carol\\.silva@example\\.com`),
    ]);
    await expect(dialog.getByText(JANE)).toHaveCount(0);
    await expect(dialog.getByText(JOHN)).toHaveCount(0);

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
  });

  test("the Customer Approval stage opens the Customer Group: the project's registered contacts", async ({ page }) => {
    test.setTimeout(60_000);

    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(CR_CUSTOMER_APPROVAL);

    await detail.groupLink(DAVE, "Customer Group", "Customer Approval").click();
    const dialog = detail.groupDialog("Customer Group");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    await expect(dialog.getByRole("listitem")).toHaveText([
      new RegExp(`${DAVE}.*dave\\.mendis@example\\.com`),
      new RegExp(`${ERIN}.*erin\\.jayawardena@example\\.com`),
    ]);
  });
});

test.describe("change request lifecycle — customer contacts answer the customer stages", () => {
  // The decisions consume the fixtures; the seed puts them back.
  test.beforeEach(async () => {
    await resetFixtures();
  });

  test("a registered customer contact approves CHG-FIXED-007 (Customer Approval) and it is Scheduled", async ({
    browser,
  }) => {
    test.setTimeout(90_000);

    await asPersona(browser, "crCustomerContact", async (page) => {
      const detail = new ChangeRequestDetailPage(page);
      await detail.goto(CR_CUSTOMER_APPROVAL);

      const STAGE = "Customer Approval";
      await expect(detail.approverStatus(DAVE, STAGE)).toHaveText("Requested");
      await expect(detail.approverStatus(ERIN, STAGE)).toHaveText("Requested");
      // Only the signed-in contact's own row is actionable.
      await expect(detail.approveButton(ERIN, STAGE)).toHaveCount(0);
      const approve = detail.approveButton(DAVE, STAGE);
      await expect(approve).toBeVisible();

      const [response] = await Promise.all([
        page.waitForResponse((r) => /\/change-requests\/[^/]+\/approvals?/.test(r.url()), { timeout: 15_000 }),
        approve.click(),
      ]);
      expect(response.ok(), `customer approval failed (${response.status()})`).toBeTruthy();

      await expect(detail.approverStatus(DAVE, STAGE)).toHaveText("Approved");
      await expect(detail.approverStatus(ERIN, STAGE)).toHaveText("Cancelled");
      await expect(detail.currentStep()).toContainText("Scheduled");
    });
  });

  test("a registered customer contact approves CHG-FIXED-008 (Customer Review) and it is Closed", async ({
    browser,
  }) => {
    test.setTimeout(90_000);

    await asPersona(browser, "crCustomerContact2", async (page) => {
      const detail = new ChangeRequestDetailPage(page);
      await detail.goto(CR_CUSTOMER_REVIEW);

      const STAGE = "Customer Review";
      await expect(detail.approverStatus(ERIN, STAGE)).toHaveText("Requested");
      await expect(detail.approverStatus(DAVE, STAGE)).toHaveText("Requested");
      await expect(detail.approveButton(DAVE, STAGE)).toHaveCount(0);
      const approve = detail.approveButton(ERIN, STAGE);
      await expect(approve).toBeVisible();

      const [response] = await Promise.all([
        page.waitForResponse((r) => /\/change-requests\/[^/]+\/approvals?/.test(r.url()), { timeout: 15_000 }),
        approve.click(),
      ]);
      expect(response.ok(), `customer review failed (${response.status()})`).toBeTruthy();

      await expect(detail.approverStatus(ERIN, STAGE)).toHaveText("Approved");
      await expect(detail.approverStatus(DAVE, STAGE)).toHaveText("Cancelled");
      await expect(detail.currentStep()).toContainText("Closed");
    });
  });

  test("an internal user who is not a contact of the project cannot answer the customer stages", async ({ browser }) => {
    test.setTimeout(90_000);

    await asPersona(browser, "crInternalApprover", async (page) => {
      const detail = new ChangeRequestDetailPage(page);
      for (const [crId, stage] of [
        [CR_CUSTOMER_APPROVAL, "Customer Approval"],
        [CR_CUSTOMER_REVIEW, "Customer Review"],
      ] as const) {
        await detail.goto(crId);
        await expect(detail.approverStatus(DAVE, stage)).toHaveText("Requested");
        await expect(detail.approveButton()).toHaveCount(0);
        await expect(detail.rejectButton()).toHaveCount(0);
      }

      // And the API says so, with the reason.
      const { status, body } = await postDecision(page, CR_CUSTOMER_APPROVAL, "approved");
      expect(status, body).toBe(403);
      expect(body).toContain("only members of the customer group");
      await detail.goto(CR_CUSTOMER_APPROVAL);
      await expect(detail.approverStatus(DAVE, "Customer Approval")).toHaveText("Requested");
      await expect(detail.currentStep()).toContainText("Customer Approval");
    });
  });
});

test.describe("change request lifecycle — an external user cannot decide an internal stage", () => {
  test.beforeEach(async () => {
    await resetFixtures();
  });

  test("a customer holding a stale peer row has the controls disabled, and the API refuses with 403", async ({ browser }) => {
    test.setTimeout(90_000);

    // The row the INTERNAL-only rule exists for: a customer who was provisioned
    // as a peer approver before the rule (what the original local database
    // held for john.smith). Planted directly, as no code path creates it now.
    await psql(`
      INSERT INTO approval_stage_approver (id, created_on, updated_on, created_by, updated_by, stage_id, work_item_id, approver_user_id, status)
      VALUES ('00000000-0000-0000-0000-000000001901', now(), now(), 'e2e', 'e2e',
              '00000000-0000-0000-0000-000000001005', '${CR_PENDING_APPROVAL}', '${DAVE_ID}', 'requested')
      ON CONFLICT (id) DO UPDATE SET status = 'requested';`);

    await asPersona(browser, "crCustomerContact", async (page) => {
      const detail = new ChangeRequestDetailPage(page);
      await detail.goto(CR_PENDING_APPROVAL);

      // The customer is a contact of the change request's project, so he can
      // open it and see his own pending row -- but not act on it: the API says
      // canDecide=false, so the webapp renders the controls disabled (with the
      // "You aren't able to approve or reject this stage" tooltip).
      await expect(detail.approverStatus(DAVE, "Peer Approval")).toHaveText("Requested");
      await expect(detail.approveButton(DAVE, "Peer Approval")).toBeDisabled();
      await expect(detail.rejectButton(DAVE)).toBeDisabled();
      // Nobody else's row has controls for him either.
      await expect(detail.approveButton(ALICE, "Peer Approval")).toHaveCount(0);

      const { status, body } = await postDecision(page, CR_PENDING_APPROVAL, "approved");
      expect(status, body).toBe(403);
      expect(body).toContain("only active internal (WSO2) users");

      // Nothing moved.
      await detail.goto(CR_PENDING_APPROVAL);
      await expect(detail.approverStatus(DAVE, "Peer Approval")).toHaveText("Requested");
      await expect(detail.approverStatus(ALICE, "Peer Approval")).toHaveText("Requested");
      await expect(detail.currentStep()).toContainText("Assess");
    });
  });
});

// ---------------------------------------------------------------------------
// An approval is only actionable while the change is in its stage's state, on the real
// stack (BFF -> entity-service -> Postgres). The reported bug: a reviewer kept Approve /
// Reject on the Review stage of a change that was already Closed (and during Customer
// Review). CHG-FIXED-002 is walked for real -- Request Approval, peer and CAB approval,
// implementation, Review (its Review stage is provisioned by the code under test) -- with
// Customer Review ticked, then moved on; a "legacy" stale row is planted directly (no code
// path writes one any more), refused with the BFF-passed 409, and repaired by migration 0193.
// ---------------------------------------------------------------------------

const MIGRATION_0193 = path.resolve(process.cwd(), "../../../entity-service/migrations/0193_change_request_cancel_stale_approvals.sql");

/** "Review/alice:requested, ..." -- every approver row of the CR's stage with this label, by user name. */
async function stageRows(crId: string, label: string): Promise<string> {
  return await psqlOutput(`
    SELECT COALESCE(string_agg(u.name || ':' || asa.status, ', ' ORDER BY u.name), '')
    FROM approval_stage_approver asa
    JOIN approval_stage ast ON ast.id = asa.stage_id
    JOIN "user" u ON u.id = asa.approver_user_id
    WHERE ast.work_item_id = '${crId}' AND ast.checkpoint_label = '${label}';`);
}

async function requestedRows(crId: string): Promise<number> {
  return Number(await psqlOutput(`SELECT COUNT(*) FROM approval_stage_approver WHERE work_item_id = '${crId}' AND status = 'requested';`));
}

test.describe("change request lifecycle — a Review approver's controls follow the state (real stack)", () => {
  test.beforeEach(async () => {
    await resetFixtures();
  });
  test.afterAll(async () => {
    await resetFixtures();
  });

  test("Review -> Customer Review -> Closed: the reviewers lose Approve / Reject when the change leaves Review, the customer answers, nothing stays requested", async ({
    page,
    browser,
  }) => {
    test.setTimeout(240_000);
    // CHG-FIXED-002 (New, assigned group 901, project 401) with Customer Review ticked.
    await psql(`UPDATE change_request SET customer_review_required = true WHERE id = '${CR_WITH_TEAM}';`);

    // The requester asks for approval.
    const jane = new ChangeRequestDetailPage(page);
    await jane.goto(CR_WITH_TEAM);
    await jane.requestApproval();
    await expect(jane.approverStatus(ALICE, "Peer Approval")).toHaveText("Requested");

    await asPersona(browser, "crInternalApprover", async (alice) => {
      const detail = new ChangeRequestDetailPage(alice);
      await detail.goto(CR_WITH_TEAM);
      await detail.approveButton(ALICE, "Peer Approval").click();
      await expect(detail.currentStep()).toContainText("Authorize");
      await detail.approveButton(ALICE, "CAB Approval").click();
      await expect(detail.currentStep()).toContainText("Scheduled");
      await alice.getByRole("button", { name: "Start implementation" }).click();
      await expect(detail.currentStep()).toContainText("Implement");
      await alice.getByRole("button", { name: "Mark implemented" }).click();
      await expect(detail.currentStep()).toContainText("Review");

      // Review: provisioned for the assigned group's internal members; only the signed-in
      // user's own row has controls.
      for (const who of [ALICE, BOB, CAROL]) await expect(detail.approverStatus(who, "Review")).toHaveText("Requested");
      await expect(detail.approveButton(ALICE, "Review")).toBeEnabled();
      await expect(detail.rejectButton(ALICE, "Review")).toBeEnabled();
      await expect(detail.approveButton(CAROL, "Review")).toHaveCount(0);
      expect(await stageRows(CR_WITH_TEAM, "Review")).toBe("Alice Perera:requested, Bob Fernando:requested, Carol Silva:requested");

      // Moving on to the customer's review cancels every reviewer's row, Carol's included
      // (the row the report was about): nobody can approve or reject the Review stage now.
      await detail.sendForCustomerReviewButton().click();
      await expect(detail.currentStep()).toContainText("Customer Review");
      for (const who of [ALICE, BOB, CAROL]) await expect(detail.approverStatus(who, "Review")).toHaveText("Cancelled");
      await expect(detail.approveButton()).toHaveCount(0);
      await expect(detail.rejectButton()).toHaveCount(0);
      expect(await stageRows(CR_WITH_TEAM, "Review")).toBe("Alice Perera:cancelled, Bob Fernando:cancelled, Carol Silva:cancelled");
      await expect(detail.approverStatus(DAVE, "Customer Review")).toHaveText("Requested");
      await expect(detail.approverStatus(ERIN, "Customer Review")).toHaveText("Requested");
      // Even forcing the decision through the API: there is nothing pending to decide.
      const forced = await postDecision(alice, CR_WITH_TEAM, "approved");
      expect(forced.status, forced.body).toBe(403);
      expect(forced.body).toContain("only members of the customer group");
    });

    // The customer answers; the change closes.
    await asPersona(browser, "crCustomerContact", async (dave) => {
      const detail = new ChangeRequestDetailPage(dave);
      await detail.goto(CR_WITH_TEAM);
      await expect(detail.approveButton(DAVE, "Customer Review")).toBeEnabled();
      await detail.approveButton(DAVE, "Customer Review").click();
      await expect(detail.currentStep()).toContainText("Closed");
    });

    // Closed: no approver row is requested anywhere, and the reviewer has no controls.
    expect(await requestedRows(CR_WITH_TEAM)).toBe(0);
    expect(await psqlOutput(`SELECT state::text FROM change_request WHERE id = '${CR_WITH_TEAM}';`)).toBe("CLOSED");
    await asPersona(browser, "crInternalApprover", async (alice) => {
      const detail = new ChangeRequestDetailPage(alice);
      await detail.goto(CR_WITH_TEAM);
      await expect(detail.currentStep()).toContainText("Closed");
      await expect(detail.approverStatus(CAROL, "Review")).toHaveText("Cancelled");
      await expect(detail.approveButton()).toHaveCount(0);
      await expect(detail.rejectButton()).toHaveCount(0);
    });
  });

  test("CHG-FIXED-006 (in Review, Customer Review ticked): sending it for customer review cancels the reviewers' rows, Carol's included", async ({ browser }) => {
    test.setTimeout(120_000);
    // The fixture is seeded in Review with no Review stage (nobody walked it there), so give it
    // what entering Review provisions: the assigned group's internal members, requested.
    const reviewStage = "00000000-0000-0000-0000-000000001921";
    await psql(`
      INSERT INTO approval_stage (id, created_on, updated_on, created_by, updated_by, work_item_id, assignment_group_id, raw_status, checkpoint_label)
        VALUES ('${reviewStage}', now(), now(), 'e2e', 'e2e', '${CR_IN_REVIEW}', '00000000-0000-0000-0000-000000000901', 'requested', 'Review')
        ON CONFLICT (id) DO NOTHING;
      INSERT INTO approval_stage_approver (id, created_on, updated_on, created_by, updated_by, stage_id, work_item_id, approver_user_id, status) VALUES
        ('00000000-0000-0000-0000-000000001922', now(), now(), 'e2e', 'e2e', '${reviewStage}', '${CR_IN_REVIEW}', '00000000-0000-0000-0000-000000000011', 'requested'),
        ('00000000-0000-0000-0000-000000001923', now(), now(), 'e2e', 'e2e', '${reviewStage}', '${CR_IN_REVIEW}', '00000000-0000-0000-0000-000000000012', 'requested'),
        ('00000000-0000-0000-0000-000000001924', now(), now(), 'e2e', 'e2e', '${reviewStage}', '${CR_IN_REVIEW}', '00000000-0000-0000-0000-000000000013', 'requested')
        ON CONFLICT (id) DO UPDATE SET status = 'requested';`);

    await asPersona(browser, "crInternalApprover", async (alice) => {
      const detail = new ChangeRequestDetailPage(alice);
      await detail.goto(CR_IN_REVIEW);
      await expect(detail.currentStep()).toContainText("Review");
      await expect(detail.approveButton(ALICE, "Review")).toBeEnabled();
      expect(await stageRows(CR_IN_REVIEW, "Review")).toBe("Alice Perera:requested, Bob Fernando:requested, Carol Silva:requested");

      await detail.sendForCustomerReviewButton().click();
      await expect(detail.currentStep()).toContainText("Customer Review");
      for (const who of [ALICE, BOB, CAROL]) await expect(detail.approverStatus(who, "Review")).toHaveText("Cancelled");
      await expect(detail.approveButton()).toHaveCount(0);
      await expect(detail.rejectButton()).toHaveCount(0);
      expect(await stageRows(CR_IN_REVIEW, "Review")).toBe("Alice Perera:cancelled, Bob Fernando:cancelled, Carol Silva:cancelled");
      // The customer contacts are asked instead.
      expect(await stageRows(CR_IN_REVIEW, "Customer Review")).toBe("Dave Mendis:requested, Erin Jayawardena:requested");
    });
  });

  test("a legacy REQUESTED Review row on a Closed change reads canDecide=false, the API refuses it with a 409 and migration 0193 repairs it", async ({ browser }) => {
    test.setTimeout(150_000);
    // The reported shape: a change that is Closed, with a Review stage whose reviewers
    // are still requested (what the database held before the fix). Planted directly.
    const reviewStage = "00000000-0000-0000-0000-000000001911";
    await psql(`
      UPDATE change_request SET state = 'CLOSED' WHERE id = '${CR_PENDING_APPROVAL}';
      -- its Peer stage was decided long ago (alice approved, the others cancelled)
      UPDATE approval_stage_approver SET status = CASE approver_user_id WHEN '00000000-0000-0000-0000-000000000011' THEN 'approved' ELSE 'cancelled' END
        WHERE stage_id = '00000000-0000-0000-0000-000000001005';
      INSERT INTO approval_stage (id, created_on, updated_on, created_by, updated_by, work_item_id, assignment_group_id, raw_status, checkpoint_label)
        VALUES ('${reviewStage}', now() + interval '1 minute', now(), 'e2e', 'e2e', '${CR_PENDING_APPROVAL}', '00000000-0000-0000-0000-000000000901', 'requested', 'Review')
        ON CONFLICT (id) DO NOTHING;
      INSERT INTO approval_stage_approver (id, created_on, updated_on, created_by, updated_by, stage_id, work_item_id, approver_user_id, status) VALUES
        ('00000000-0000-0000-0000-000000001912', now(), now(), 'e2e', 'e2e', '${reviewStage}', '${CR_PENDING_APPROVAL}', '00000000-0000-0000-0000-000000000011', 'requested'),
        ('00000000-0000-0000-0000-000000001913', now(), now(), 'e2e', 'e2e', '${reviewStage}', '${CR_PENDING_APPROVAL}', '00000000-0000-0000-0000-000000000013', 'requested')
        ON CONFLICT (id) DO UPDATE SET status = 'requested';`);

    await asPersona(browser, "crInternalApprover", async (alice) => {
      const detail = new ChangeRequestDetailPage(alice);
      await detail.goto(CR_PENDING_APPROVAL);
      await expect(detail.currentStep()).toContainText("Closed");
      // The row still reads Requested -- it is what the database holds -- but the API says
      // canDecide=false, so the controls are disabled (with the existing explanation).
      await expect(detail.approverStatus(ALICE, "Review")).toHaveText("Requested");
      await expect(detail.approveButton(ALICE, "Review")).toBeDisabled();
      await expect(detail.rejectButton(ALICE, "Review")).toBeDisabled();
      await expect(alice.getByLabel(/you aren't able to approve or reject this stage/i).first()).toBeVisible();

      // Forced through the API anyway: refused, with the readable 409 (passed through by the BFF).
      for (const decision of ["approved", "rejected"] as const) {
        const { status, body } = await postDecision(alice, CR_PENDING_APPROVAL, decision);
        expect(status, body).toBe(409);
        expect(body).toContain(
          "this approval is no longer pending: the change request is in Closed, but the Review stage can only be decided while it is in Review",
        );
      }
      // Nothing changed.
      expect(await stageRows(CR_PENDING_APPROVAL, "Review")).toBe("Alice Perera:requested, Carol Silva:requested");
      expect(await psqlOutput(`SELECT state::text FROM change_request WHERE id = '${CR_PENDING_APPROVAL}';`)).toBe("CLOSED");
    });

    // The data fix: cancels the stale rows of the Closed change (both reviewers') and a labelled
    // stage's row on a change that is in another state (CHG-FIXED-004 sits in Authorize, so a
    // Peer Approval row still requested on it is stale), and leaves a live approval alone
    // (CHG-FIXED-008 is in Customer Review with its Customer Review stage requested).
    await psql(`
      INSERT INTO approval_stage_approver (id, created_on, updated_on, created_by, updated_by, stage_id, work_item_id, approver_user_id, status)
        VALUES ('00000000-0000-0000-0000-000000001914', now(), now(), 'e2e', 'e2e', '00000000-0000-0000-0000-000000001008', '${CR_RESOLVED}', '00000000-0000-0000-0000-000000000013', 'requested')
        ON CONFLICT (id) DO UPDATE SET status = 'requested';`);
    expect(await requestedRows(CR_RESOLVED)).toBe(1);
    const live = await requestedRows(CR_CUSTOMER_REVIEW);
    expect(live).toBe(2);
    await psql(fs.readFileSync(MIGRATION_0193, "utf8"));
    expect(await requestedRows(CR_PENDING_APPROVAL)).toBe(0);
    expect(await stageRows(CR_PENDING_APPROVAL, "Review")).toBe("Alice Perera:cancelled, Carol Silva:cancelled");
    expect(await requestedRows(CR_RESOLVED)).toBe(0);
    expect(await requestedRows(CR_CUSTOMER_REVIEW)).toBe(live);
    // Idempotent.
    await psql(fs.readFileSync(MIGRATION_0193, "utf8"));
    expect(await stageRows(CR_PENDING_APPROVAL, "Review")).toBe("Alice Perera:cancelled, Carol Silva:cancelled");

    await asPersona(browser, "crInternalApprover", async (alice) => {
      const detail = new ChangeRequestDetailPage(alice);
      await detail.goto(CR_PENDING_APPROVAL);
      await expect(detail.approverStatus(ALICE, "Review")).toHaveText("Cancelled");
      await expect(detail.approveButton()).toHaveCount(0);
      await expect(detail.rejectButton()).toHaveCount(0);
    });
  });
});
});

//
// Approval-flow lifecycle per change type, against the in-browser fake in
// utils/fakeChangeRequestApi.ts (no records created, no seeded fixtures, no
// docker). Visible state is asserted after every step:
//
//   Normal    New -> Request Approval -> Assess [Peer Approval]
//                 -> Authorize [CAB Approval] -> (auto) Scheduled
//                 -> Implement -> Review -> Closed
//   Emergency New -> Request Approval -> Authorize [ECAB Approval only]
//                 -> (auto) Scheduled
//   Standard  New -> Request Approval -> (auto) Scheduled, no approvals
//
// With "Customer Approval" ticked, every route above stops at Customer
// Approval before Scheduled until "Record customer approval" is clicked; with
// "Customer Review" ticked, Review offers "Send for customer review" instead
// of "Close", then Customer Review offers Close.
//
// Also asserts at every step that there is no "Schedule" button and no
// "Move to Assess" label, and that the CR's creator can Cancel but never
// Approve/Reject. Each "switch user" is a page reload with the faked
// `/users/me` identity changed. The browser is still signed in with the
// captured session so the portal boots normally.
//

async function openDetail(detail: ChangeRequestDetailPage): Promise<void> {
  await detail.goto(FAKE_CR_ID);
}

async function expectNoManualSchedule(detail: ChangeRequestDetailPage): Promise<void> {
  await expect(detail.scheduleButton()).toHaveCount(0);
  await expect(detail.page.getByText(/move to assess/i)).toHaveCount(0);
}

/** Customer Approval / Customer Review appear on the stepper only when ticked. */
async function expectCustomerStepsOnLine(
  detail: ChangeRequestDetailPage,
  flags: { approval: boolean; review: boolean },
): Promise<void> {
  const expected = ["New", "Assess", "Authorize"];
  if (flags.approval) expected.push("Customer Approval");
  expected.push("Scheduled", "Implement", "Review");
  if (flags.review) expected.push("Customer Review");
  expected.push("Closed");
  await expect(detail.stepLabels()).toHaveText(expected);
}

test.describe("change request approval flow — Normal", () => {
  for (const approval of [false, true]) {
    for (const review of [false, true]) {
      test(`Normal, customer approval ${approval ? "on" : "off"}, customer review ${review ? "on" : "off"}: every step shows the right state and actions`, async ({
        page,
      }) => {
        test.setTimeout(120_000);
        const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, {
          customerApprovalRequired: approval,
          customerReviewRequired: review,
        });
        const detail = new ChangeRequestDetailPage(page);

        // New: the creator requests approval. The flags are shown read-only.
        await openDetail(detail);
        await expect(detail.currentStep()).toContainText("New");
        await expect(detail.flagValue("Customer approval required")).toHaveText(approval ? "Yes" : "No");
        await expect(detail.flagValue("Customer review required")).toHaveText(review ? "Yes" : "No");
        await expectCustomerStepsOnLine(detail, { approval, review });
        await expectNoManualSchedule(detail);
        await detail.requestApproval();

        // Peer Approval is pending; the creator can't decide but can still cancel.
        await expect(detail.currentStep()).toContainText("Assess");
        await expect(detail.blockingReason()).toHaveText("Awaiting Peer Approval");
        await expect(detail.approverStage("Pat Peer")).toHaveText("Peer Approval");
        await expect(detail.approveButton()).toHaveCount(0);
        await expect(detail.rejectButton()).toHaveCount(0);
        await expect(detail.creatorApprovalNotice()).toBeVisible();
        await detail.changeStateButton().click();
        await expect(detail.cancelChangeMenuItem()).toBeEnabled();
        await page.keyboard.press("Escape");
        await expectNoManualSchedule(detail);

        // A peer approves; CAB Approval is the next, separate stage.
        api.setViewer(FAKE_PEER);
        await page.reload();
        await expect(detail.approveButton("Pat Peer")).toBeVisible();
        await detail.approve("Pat Peer");
        await expect(detail.currentStep()).toContainText("Authorize");
        await expect(detail.blockingReason()).toHaveText("Awaiting CAB Approval");
        await expect(detail.approverStage("Cam Cab")).toHaveText("CAB Approval");
        await expect(detail.approverStage("Pat Peer")).toHaveText("Peer Approval");
        await expect(detail.approverStatus("Pat Peer")).toHaveText("Approved");
        await expectNoManualSchedule(detail);

        // A CAB member approves; the page refreshes itself to Customer
        // Approval when that box is ticked, else straight to Scheduled.
        api.setViewer(FAKE_CAB);
        await page.reload();
        await detail.approve("Cam Cab");
        expect(api.requests().some((r) => r === `POST /change-requests/${FAKE_CR_ID}/approvals/decision`)).toBe(true);

        api.setViewer(FAKE_CREATOR);
        await page.reload();
        if (approval) {
          await expect(detail.currentStep()).toContainText("Customer Approval");
          await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
          await expect(page.getByRole("button", { name: "Start implementation" })).toHaveCount(0);
          await expect(detail.recordCustomerApprovalButton()).toBeVisible();
          await detail.changeStateButton().click();
          await expect(detail.cancelChangeMenuItem()).toBeEnabled();
          await page.keyboard.press("Escape");
          await expectNoManualSchedule(detail);
          await detail.recordCustomerApproval();
        } else {
          await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);
        }

        // Scheduled: nothing awaited, no Schedule button.
        await expect(detail.currentStep()).toContainText("Scheduled");
        await expect(detail.blockingReason()).toHaveCount(0);
        await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);
        await expectNoManualSchedule(detail);

        // The engineer-driven tail.
        await page.getByRole("button", { name: "Start implementation" }).click();
        await expect(detail.currentStep()).toContainText("Implement");
        await page.getByRole("button", { name: "Mark implemented" }).click();
        await expect(detail.currentStep()).toContainText("Review");
        if (review) {
          // Review offers only "Send for customer review" -- no Close.
          await expect(detail.sendForCustomerReviewButton()).toBeVisible();
          await expect(detail.closeButton()).toHaveCount(0);
          await detail.changeStateButton().click();
          await expect(page.getByRole("menuitem", { name: "Close", exact: true })).toHaveCount(0);
          await page.keyboard.press("Escape");
          await detail.sendForCustomerReviewButton().click();
          await expect(detail.currentStep()).toContainText("Customer Review");
          await expect(detail.blockingReason()).toHaveText("Awaiting Customer Review");
          await expect(detail.sendForCustomerReviewButton()).toHaveCount(0);
        } else {
          // Review offers Close and no customer review.
          await expect(detail.closeButton()).toBeVisible();
          await expect(detail.sendForCustomerReviewButton()).toHaveCount(0);
        }
        await detail.closeButton().click();
        await expect(detail.currentStep()).toContainText("Closed");
        await expect(detail.blockingReason()).toHaveCount(0);
        await expectNoManualSchedule(detail);
        expect(api.state()).toBe("closed");
      });
    }
  }

  test("a non-creator approver sees Approve and Reject, with no creator notice", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Assess");

    api.setViewer(FAKE_PEER);
    await page.reload();
    await expect(detail.approveButton("Pat Peer")).toBeEnabled();
    await expect(detail.rejectButton("Pat Peer")).toBeEnabled();
    await expect(detail.creatorApprovalNotice()).toHaveCount(0);
  });
});

test.describe("change request approval flow — Emergency", () => {
  test("Request Approval -> ECAB Approval only (no Peer or CAB) -> auto Scheduled", async ({ page }) => {
    test.setTimeout(60_000);
    const api = await installFakeChangeRequestApi(page, "emergency", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);

    await openDetail(detail);
    await detail.requestApproval();

    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.blockingReason()).toHaveText("Awaiting ECAB Approval");
    await expect(detail.approverStage("Eli Ecab")).toHaveText("ECAB Approval");
    await expect(page.getByRole("cell", { name: "Peer Approval", exact: true })).toHaveCount(0);
    await expect(page.getByRole("cell", { name: "CAB Approval", exact: true })).toHaveCount(0);
    await expect(detail.approveButton()).toHaveCount(0); // creator
    await expectNoManualSchedule(detail);

    api.setViewer(FAKE_ECAB);
    await page.reload();
    await detail.approve("Eli Ecab");
    await expect(detail.currentStep()).toContainText("Scheduled");
    await expectNoManualSchedule(detail);
  });
});

test.describe("change request approval flow — Emergency with Customer Approval", () => {
  test("ECAB approval stops at Customer Approval; only 'Record customer approval' reaches Scheduled", async ({ page }) => {
    test.setTimeout(60_000);
    const api = await installFakeChangeRequestApi(page, "emergency", FAKE_CREATOR, { customerApprovalRequired: true });
    const detail = new ChangeRequestDetailPage(page);

    await openDetail(detail);
    await expect(detail.flagValue("Customer approval required")).toHaveText("Yes");
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.blockingReason()).toHaveText("Awaiting ECAB Approval");
    await expect(page.getByRole("cell", { name: "Peer Approval", exact: true })).toHaveCount(0);
    await expectNoManualSchedule(detail);

    api.setViewer(FAKE_ECAB);
    await page.reload();
    await detail.approve("Eli Ecab");

    api.setViewer(FAKE_CREATOR);
    await page.reload();
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(page.getByRole("button", { name: "Start implementation" })).toHaveCount(0);
    await expectNoManualSchedule(detail);
    await detail.recordCustomerApproval();

    await expect(detail.currentStep()).toContainText("Scheduled");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Start implementation" })).toBeVisible();
    await expectNoManualSchedule(detail);
  });
});

test.describe("change request approval flow — Standard with Customer Approval", () => {
  test("Request Approval goes to Customer Approval (not Scheduled), then Record customer approval schedules it", async ({
    page,
  }) => {
    await installFakeChangeRequestApi(page, "standard", FAKE_CREATOR, { customerApprovalRequired: true });
    const detail = new ChangeRequestDetailPage(page);

    await openDetail(detail);
    await detail.requestApproval();

    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(page.getByText(/no approval stages recorded/i)).toBeVisible();
    await expect(page.getByRole("button", { name: "Start implementation" })).toHaveCount(0);
    await expectNoManualSchedule(detail);

    await detail.recordCustomerApproval();
    await expect(detail.currentStep()).toContainText("Scheduled");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Start implementation" })).toBeVisible();
  });
});

test.describe("change request approval flow — editing the customer checkboxes", () => {
  test("both are editable before their gate, are sent via PATCH, and show on the Approval tab afterwards", async ({
    page,
  }) => {
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await expect(detail.flagValue("Customer approval required")).toHaveText("No");

    await detail.openEditDialog();
    await expect(detail.editCustomerApprovalCheckbox()).not.toBeChecked();
    await expect(detail.editCustomerApprovalCheckbox()).toBeEnabled();
    await expect(detail.editCustomerReviewCheckbox()).toBeEnabled();
    await detail.editCustomerApprovalCheckbox().check();
    await detail.editCustomerReviewCheckbox().check();
    const [request] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/change-requests/${FAKE_CR_ID}`)),
      detail.saveEdit(),
    ]);
    expect(request.postDataJSON()).toEqual({ customerApprovalRequired: true, customerReviewRequired: true });
    await expect(detail.editDialog()).toHaveCount(0);

    expect(api.flags()).toEqual({ customerApprovalRequired: true, customerReviewRequired: true });
    await expect(detail.flagValue("Customer approval required")).toHaveText("Yes");
    await expect(detail.flagValue("Customer review required")).toHaveText("Yes");
    await expectCustomerStepsOnLine(detail, { approval: true, review: true });
  });

  test("Customer Approval is disabled with an explanation once the CR is scheduled; Customer Review stays editable", async ({
    page,
  }) => {
    await installFakeChangeRequestApi(page, "standard", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Scheduled");

    await detail.openEditDialog();
    await expect(detail.editCustomerApprovalCheckbox()).toBeDisabled();
    await expect(detail.editDialog().getByText(/locked/i).first()).toBeVisible();
    await expect(detail.editCustomerReviewCheckbox()).toBeEnabled();
  });

  test("Customer Review is disabled once the CR has reached customer review", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "standard", FAKE_CREATOR, { customerReviewRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    api.setState("customer_review");
    await page.reload();
    await expect(detail.currentStep()).toContainText("Customer Review");

    await detail.openEditDialog();
    await expect(detail.editCustomerReviewCheckbox()).toBeDisabled();
    await expect(detail.editCustomerApprovalCheckbox()).toBeDisabled();
  });

  test("shows the backend's refusal when the gate passed while the dialog was open (400)", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.openEditDialog();
    await detail.editCustomerApprovalCheckbox().check();

    // The CR moves on behind the open dialog's back; the backend refuses.
    api.setState("scheduled");
    await detail.saveEdit();
    await expect(detail.editDialog().getByRole("alert")).toContainText(
      "customerApprovalRequired cannot be changed once the change request is scheduled",
    );
    await expect(detail.editDialog()).toBeVisible();
    expect(api.flags().customerApprovalRequired).toBe(false);
  });
});

test.describe("change request approval flow — Standard", () => {
  test("Request Approval goes straight to Scheduled, with no approval stages", async ({ page }) => {
    await installFakeChangeRequestApi(page, "standard", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);

    await openDetail(detail);
    await detail.requestApproval();

    await expect(detail.currentStep()).toContainText("Scheduled");
    await expect(page.getByText(/no approval stages recorded/i)).toBeVisible();
    await expect(detail.blockingReason()).toHaveCount(0);
    await expectNoManualSchedule(detail);
    await expect(page.getByRole("button", { name: "Start implementation" })).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Customer Project / Deployments / Deployment products (and the read-only Customer Group) on a
// change request, end to end against the in-browser fake backend: create ->
// detail Overview -> edit (cascade, whole-scope PATCH, backend refusal) ->
// approvals -> locked once implementation starts.
// ---------------------------------------------------------------------------

const ACME = FAKE_PROJECTS[0]!;
const BETA = FAKE_PROJECTS[1]!;
const [ACME_PROD, ACME_STG, BETA_DEV] = FAKE_DEPLOYMENTS;
const GAMMA = FAKE_PROJECTS[2]!;
const ACME_CONTACTS = FAKE_PROJECT_CONTACTS[ACME.id]!.map((u) => u.name);
const BETA_CONTACTS = FAKE_PROJECT_CONTACTS[BETA.id]!.map((u) => u.name);
const productsOf = (deploymentId: string): string[] =>
  FAKE_DEPLOYMENT_PRODUCTS.filter((p) => p.deploymentId === deploymentId).map((p) => p.name);

test.describe("change request lifecycle — project and deployments (mocked backend)", () => {
  test("Normal change: create with project + deployments, see them on the detail page, edit the scope, approve, and have it locked once implementing", async ({
    page,
  }) => {
    test.setTimeout(150_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const create = new ChangeRequestCreatePage(page);
    const detail = new ChangeRequestDetailPage(page);

    // 1. Create a Normal change with a project, two deployments and a category.
    await create.goto();
    await create.selectType("Normal");
    await create.subjectField().fill("[E2E] project + deployments lifecycle (mocked)");
    await create.selectProject(ACME.name);
    await create.selectDeployments([ACME_PROD!.name, ACME_STG!.name]);
    await create.selectCategory("DevOps");
    await create.createButton().click();
    await expect(page).toHaveURL(new RegExp(`/operations/change-requests/${FAKE_CR_ID}$`));
    await expect(detail.lifecycleStepper()).toBeVisible();

    // 2. The detail Overview shows every one of them.
    await expect(detail.overviewCell("Customer Project")).toContainText(ACME.name);
    await expect(detail.overviewChips("Deployments")).toHaveText([ACME_PROD!.name, ACME_STG!.name]);
    await expect(detail.page.getByText("Environments", { exact: true })).toHaveCount(0);
    await expect(detail.overviewChips("Deployment products")).toHaveText([
      ...productsOf(ACME_PROD!.id),
      ...productsOf(ACME_STG!.id),
    ]);
    // The Customer Group is the project's registered contacts, derived and read-only.
    await expect(detail.overviewChips("Customer group")).toHaveText(ACME_CONTACTS);
    await expect(detail.overviewCell("Category")).toContainText("DevOps");
    await expect(detail.currentStep()).toContainText("New");

    // 3. Request Approval, then edit the scope while it is still editable (Assess).
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Assess");
    await detail.openEditDialog();
    await expect(detail.editProjectField()).toHaveValue(ACME.name);
    await expect(detail.editChipsOf(detail.editDeploymentsField())).toHaveText([ACME_PROD!.name, ACME_STG!.name]);
    await detail.editToggleOptions(detail.editDeploymentsField(), [ACME_STG!.name]); // drop Staging
    await expect(detail.editChipsOf(detail.editDeploymentProductsField())).toHaveText(productsOf(ACME_PROD!.id));
    await expect(detail.editChipsOf(detail.editCustomerGroupField())).toHaveText(ACME_CONTACTS);
    const [patch] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/change-requests/${FAKE_CR_ID}`)),
      detail.saveEdit(),
    ]);
    // The whole scope goes out together, with the exact wire names.
    expect(patch.postDataJSON()).toEqual({
      projectId: ACME.id,
      deploymentIds: [ACME_PROD!.id],
      deploymentProductIds: FAKE_DEPLOYMENT_PRODUCTS.filter((p) => p.deploymentId === ACME_PROD!.id).map((p) => p.id),
    });
    await expect(detail.editDialog()).toHaveCount(0);
    await expect(detail.overviewChips("Deployments")).toHaveText([ACME_PROD!.name]);
    await expect(detail.overviewChips("Deployment products")).toHaveText(productsOf(ACME_PROD!.id));
    expect(api.scope().deploymentIds).toEqual([ACME_PROD!.id]);

    // 4. Peer and CAB approve; the creator starts implementation.
    api.setViewer(FAKE_PEER);
    await page.reload();
    await detail.approve("Pat Peer");
    await expect(detail.currentStep()).toContainText("Authorize");
    api.setViewer(FAKE_CAB);
    await page.reload();
    await detail.approve("Cam Cab");
    api.setViewer(FAKE_CREATOR);
    await page.reload();
    await expect(detail.currentStep()).toContainText("Scheduled");

    // Still editable while Scheduled ...
    await detail.openEditDialog();
    await expect(detail.editDeploymentsField()).toBeEnabled();
    await detail.editDialog().getByRole("button", { name: "Cancel" }).click();

    await page.getByRole("button", { name: "Start implementation" }).click();
    await expect(detail.currentStep()).toContainText("Implement");

    // 5. ... and locked, with the reason, from Implement on. The detail still shows it.
    await detail.openEditDialog();
    await expect(detail.editDialog().getByText(/can't be changed once implementation has started/i)).toBeVisible();
    await expect(detail.editProjectField()).toBeDisabled();
    await expect(detail.editDeploymentsField()).toBeDisabled();
    await expect(detail.saveButton()).toBeDisabled();
    await detail.editDialog().getByRole("button", { name: "Cancel" }).click();
    await expect(detail.overviewChips("Deployments")).toHaveText([ACME_PROD!.name]);
  });

  test("editing: changing the project clears the dependents and sends the new project with empty lists", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, {}, {
      projectId: ACME.id,
      deploymentIds: [ACME_PROD!.id],
    });
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(FAKE_CR_ID);
    await expect(detail.overviewChips("Deployments")).toHaveText([ACME_PROD!.name]);

    await detail.openEditDialog();
    // A saved project can be swapped but not cleared.
    await detail.editProjectField().click();
    await page.getByRole("option", { name: BETA.name }).click();
    await expect(detail.editChipsOf(detail.editDeploymentsField())).toHaveCount(0);
    await expect(detail.editChipsOf(detail.editDeploymentProductsField())).toHaveCount(0);
    // The read-only Customer Group follows the project: customer B's contacts replace customer A's.
    await expect(detail.editChipsOf(detail.editCustomerGroupField())).toHaveText(BETA_CONTACTS);
    const [patch] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/change-requests/${FAKE_CR_ID}`)),
      detail.saveEdit(),
    ]);
    expect(patch.postDataJSON()).toEqual({
      projectId: BETA.id,
      deploymentIds: [],
      deploymentProductIds: [],
    });
    await expect(detail.editDialog()).toHaveCount(0);
    await expect(detail.overviewCell("Customer Project")).toContainText(BETA.name);
    await expect(detail.overviewCell("Deployments")).toContainText("—");
    await expect(detail.overviewChips("Customer group")).toHaveText(BETA_CONTACTS);
    expect(api.scope()).toMatchObject({ projectId: BETA.id, deploymentIds: [] });
  });

  test("editing: picking deployments of a new project derives the products", async ({ page }) => {
    await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(FAKE_CR_ID);
    await expect(detail.overviewCell("Customer Project")).toContainText("—");

    await detail.openEditDialog();
    await expect(detail.editDeploymentsField()).toBeDisabled();
    await detail.editProjectField().click();
    await page.getByRole("option", { name: BETA.name }).click();
    await detail.editToggleOptions(detail.editDeploymentsField(), [BETA_DEV!.name]);
    await expect(detail.editChipsOf(detail.editDeploymentProductsField())).toHaveText(productsOf(BETA_DEV!.id));
    await detail.saveEdit();
    await expect(detail.editDialog()).toHaveCount(0);
    await expect(detail.overviewCell("Customer Project")).toContainText(BETA.name);
    await expect(detail.overviewChips("Deployments")).toHaveText([BETA_DEV!.name]);
  });

  test("editing: the category is sent on its own when only it changed", async ({ page }) => {
    await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, {}, { category: "other" });
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(FAKE_CR_ID);
    await expect(detail.overviewCell("Category")).toContainText("Other");

    await detail.openEditDialog();
    await expect(detail.editCategoryField()).toHaveText("Other");
    await detail.editCategoryField().click();
    await page.getByRole("option", { name: "Hotfix Release - Cloud", exact: true }).click();
    const [patch] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/change-requests/${FAKE_CR_ID}`)),
      detail.saveEdit(),
    ]);
    expect(patch.postDataJSON()).toEqual({ category: "hotfix_release_cloud" });
    await expect(detail.editDialog()).toHaveCount(0);
    await expect(detail.overviewCell("Category")).toContainText("Hotfix Release - Cloud");
  });

  test("editing: shows the backend's refusal verbatim when a chosen deployment was deactivated behind the dialog", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, {}, { projectId: ACME.id, deploymentIds: [ACME_PROD!.id] });
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(FAKE_CR_ID);
    await detail.openEditDialog();
    await detail.editToggleOptions(detail.editDeploymentsField(), [ACME_STG!.name]);

    api.retireDeployment(ACME_STG!.id);
    await detail.saveEdit();
    await expect(detail.editDialog().getByRole("alert")).toContainText(
      `deploymentIds: deployment ${ACME_STG!.name} is not an active deployment of the selected project`,
    );
    await expect(detail.editDialog()).toBeVisible();
    expect(api.scope().deploymentIds).toEqual([ACME_PROD!.id]);
  });

  test("the detail Overview shows a dash for each field a change request has none of", async ({ page }) => {
    await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await detail.goto(FAKE_CR_ID);
    for (const label of ["Customer Project", "Deployments", "Deployment products", "Customer group", "Category"]) {
      await expect(detail.overviewCell(label), label).toContainText("—");
    }
  });
});

//
// Customer group: the people a customer-gated change request is directed to --
// the registered contacts of its Customer Project, derived and read-only.
// Against the same fake (see its header for the exact contract): with a project
// whose contacts include someone eligible, entering Customer Approval / Customer
// Review provisions a stage for them; while it is live only Cancel is offered;
// their decision moves the CR (approve -> Scheduled / Closed, reject ->
// Canceled). With no project, a project without registered contacts, or none of
// them eligible, no stage exists and the manual "Record customer approval" /
// Close stay.
//

const NO_CUSTOMER_GROUP_TEXT =
  /^No registered customer contacts are assigned to this change request's project, so no customer approvers were assigned\./;

/** A change request on the Acme project: its customer group is Mia and Max. */
const ON_ACME = { projectId: ACME.id };

/** Cancel is the only action offered: no primary button, one menu entry. */
async function expectOnlyCancelOffered(detail: ChangeRequestDetailPage): Promise<void> {
  await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);
  await expect(detail.closeButton()).toHaveCount(0);
  await expect(detail.page.getByRole("button", { name: "Start implementation" })).toHaveCount(0);
  await detail.changeStateButton().click();
  await expect(detail.page.getByRole("menuitem")).toHaveCount(1);
  await expect(detail.cancelChangeMenuItem()).toBeEnabled();
  await detail.page.keyboard.press("Escape");
}

async function switchTo(page: import("@playwright/test").Page, api: FakeChangeRequestApi, user: FakeUser): Promise<void> {
  api.setViewer(user);
  await page.reload();
}

/** Drives a fresh Normal CR through Peer and CAB approval (the creator
 * requests, Pat Peer and Cam Cab approve), leaving the viewer as Cam Cab. */
async function approveInternally(page: import("@playwright/test").Page, api: FakeChangeRequestApi, detail: ChangeRequestDetailPage): Promise<void> {
  await openDetail(detail);
  await detail.requestApproval();
  await expect(detail.currentStep()).toContainText("Assess");
  await switchTo(page, api, FAKE_PEER);
  await detail.approve("Pat Peer");
  await expect(detail.currentStep()).toContainText("Authorize");
  await switchTo(page, api, FAKE_CAB);
  await detail.approve("Cam Cab");
}

test.describe("change request approval flow — customer group (the project's registered contacts)", () => {
  test("Normal with Customer Approval and Customer Review on a project with registered contacts: every step shows the right state, stage rows and buttons for the creator, a contact and a non-contact", async ({
    page,
  }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(
      page,
      "normal",
      FAKE_CREATOR,
      { customerApprovalRequired: true, customerReviewRequired: true },
      ON_ACME,
    );
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    // Customer Approval, creator: the group's stage is provisioned; Cancel only.
    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toHaveCount(0);
    // The Overview lists the same people as the read-only Customer Group.
    await expect(detail.overviewChips("Customer group")).toHaveText(ACME_CONTACTS);
    for (const member of [FAKE_CUST_ONE, FAKE_CUST_TWO]) {
      await expect(detail.approverRow(member.name, "Customer Approval")).toBeVisible();
      await expect(detail.approverStatus(member.name, "Customer Approval")).toHaveText("Requested");
      await expect(detail.approverRow(member.name, "Customer Approval")).toContainText("Customer Group");
    }
    await expect(detail.approverStatus("Pat Peer", "Peer Approval")).toHaveText("Approved");
    await expect(detail.approverStatus("Cam Cab", "CAB Approval")).toHaveText("Approved");
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);
    await expectOnlyCancelOffered(detail);
    await expectNoManualSchedule(detail);

    // Customer Approval, non-member: sees the rows, no Approve/Reject, no manual path.
    await switchTo(page, api, FAKE_OUTSIDER);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.approverRow(FAKE_CUST_ONE.name, "Customer Approval")).toBeVisible();
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);

    // Customer Approval, group member: Approve/Reject on their own row only.
    await switchTo(page, api, FAKE_CUST_ONE);
    await expect(detail.approveButton(FAKE_CUST_ONE.name, "Customer Approval")).toBeEnabled();
    await expect(detail.rejectButton(FAKE_CUST_ONE.name, "Customer Approval")).toBeEnabled();
    await expect(detail.approveButton()).toHaveCount(1);
    await expect(detail.approveButton(FAKE_CUST_TWO.name, "Customer Approval")).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);

    // The member approves; the page refreshes itself to Scheduled (no reload).
    await detail.approve(FAKE_CUST_ONE.name, "Customer Approval");
    await expect(detail.currentStep()).toContainText("Scheduled");
    expect(api.state()).toBe("scheduled");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval")).toHaveText("Approved");
    await expect(detail.approveButton()).toHaveCount(0);
    await expectNoManualSchedule(detail);

    // The engineer-driven tail up to Review.
    await switchTo(page, api, FAKE_CREATOR);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await expect(detail.currentStep()).toContainText("Implement");
    await page.getByRole("button", { name: "Mark implemented" }).click();
    await expect(detail.currentStep()).toContainText("Review");
    await expect(detail.sendForCustomerReviewButton()).toBeVisible();
    await expect(detail.closeButton()).toHaveCount(0);

    // Customer Review: a stage for the same group; Cancel only.
    await detail.sendForCustomerReviewButton().click();
    await expect(detail.currentStep()).toContainText("Customer Review");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Review");
    await expect(detail.approverStatus(FAKE_CUST_TWO.name, "Customer Review")).toHaveText("Requested");
    await expect(detail.approverRow(FAKE_CUST_TWO.name, "Customer Review")).toContainText("Customer Group");
    await expect(detail.approveButton()).toHaveCount(0); // creator
    await expectOnlyCancelOffered(detail);

    // Customer Review, non-member: nothing to decide.
    await switchTo(page, api, FAKE_OUTSIDER);
    await expect(detail.currentStep()).toContainText("Customer Review");
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);

    // Customer Review, the other member approves -> Closed (no reload).
    await switchTo(page, api, FAKE_CUST_TWO);
    await expect(detail.approveButton()).toHaveCount(1);
    await detail.approve(FAKE_CUST_TWO.name, "Customer Review");
    await expect(detail.currentStep()).toContainText("Closed");
    expect(api.state()).toBe("closed");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.changeStateButton()).toHaveCount(0);
    await expectNoManualSchedule(detail);
  });

  test("a contact rejecting the Customer Approval cancels the change request", async ({ page }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CUST_TWO);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await detail.reject(FAKE_CUST_TWO.name);
    await expect(detail.approverStatus(FAKE_CUST_TWO.name, "Customer Approval")).toHaveText("Rejected");
    expect(api.state()).toBe("canceled");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.changeStateButton()).toHaveCount(0);
  });

  test("a contact rejecting the Customer Review moves the change request to Rollback (terminal, no actions left)", async ({ page }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerReviewRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await page.getByRole("button", { name: "Mark implemented" }).click();
    await detail.sendForCustomerReviewButton().click();
    await expect(detail.currentStep()).toContainText("Customer Review");

    await switchTo(page, api, FAKE_CUST_ONE);
    await detail.reject(FAKE_CUST_ONE.name);
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Review")).toHaveText("Rejected");
    expect(api.state()).toBe("rollback");
    await expect(page.locator(".MuiChip-label", { hasText: /^Rollback$/ }).first()).toBeVisible();
    await expect(detail.blockingReason()).toHaveCount(0);
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.changeStateButton()).toHaveCount(0);
  });

  test("no project: no customer stage, the Approval tab explains why, and Record customer approval still schedules it", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toBeVisible();
    await expect(page.getByRole("cell", { name: "Customer Approval", exact: true })).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toBeVisible();

    await detail.recordCustomerApproval();
    await expect(detail.currentStep()).toContainText("Scheduled");
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toHaveCount(0);
    expect(api.state()).toBe("scheduled");
  });

  test("no project: Customer Review shows the helper and manual Close stays available", async ({ page }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerReviewRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await page.getByRole("button", { name: "Mark implemented" }).click();
    await detail.sendForCustomerReviewButton().click();
    await expect(detail.currentStep()).toContainText("Customer Review");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Review");
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toBeVisible();
    await expect(page.getByRole("cell", { name: "Customer Review", exact: true })).toHaveCount(0);

    await detail.closeButton().click();
    await expect(detail.currentStep()).toContainText("Closed");
    expect(api.state()).toBe("closed");
  });

  test("a project without registered contacts gets no stage and the helper; picking a project that has contacts provisions the stage and the manual path goes away", async ({
    page,
  }) => {
    test.setTimeout(150_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, { projectId: GAMMA.id });
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toBeVisible();
    await expect(detail.overviewChips("Customer group")).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toBeVisible();

    // Edit the project to Acme: the group is re-derived and the stage provisioned.
    await detail.openEditDialog();
    await detail.editProjectField().click();
    await page.getByRole("option", { name: ACME.name }).click();
    await expect(detail.editChipsOf(detail.editCustomerGroupField())).toHaveText(ACME_CONTACTS);
    await detail.saveEdit();
    await expect(detail.editDialog()).toHaveCount(0);
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toHaveCount(0);
    await expect(detail.overviewChips("Customer group")).toHaveText(ACME_CONTACTS);
    await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);

    await switchTo(page, api, FAKE_CUST_ONE);
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval")).toHaveText("Requested");
    await detail.approve(FAKE_CUST_ONE.name, "Customer Approval");
    await expect(detail.currentStep()).toContainText("Scheduled");
  });

  test("changing the project while the customer stage is live replaces it: the new project's contacts are asked, the old project's can no longer decide", async ({
    page,
  }) => {
    test.setTimeout(150_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval")).toHaveText("Requested");
    await detail.openEditDialog();
    await detail.editProjectField().click();
    await page.getByRole("option", { name: BETA.name }).click();
    await expect(detail.editChipsOf(detail.editCustomerGroupField())).toHaveText(BETA_CONTACTS);
    await detail.saveEdit();
    await expect(detail.editDialog()).toHaveCount(0);

    await expect(detail.overviewChips("Customer group")).toHaveText(BETA_CONTACTS);
    expect(api.stages().filter((st) => st.stage === "Customer Approval").map((st) => st.status)).toEqual(["CANCELLED", "REQUESTED"]);
    await expect(detail.approverRow(FAKE_BETA_CONTACT.name, "Customer Approval")).toBeVisible();

    // Customer A's contact is no longer asked and cannot decide ...
    await switchTo(page, api, FAKE_CUST_ONE);
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);
    // ... customer B's contact is.
    await switchTo(page, api, FAKE_BETA_CONTACT);
    await expect(detail.approveButton(FAKE_BETA_CONTACT.name, "Customer Approval")).toBeEnabled();
    await detail.approve(FAKE_BETA_CONTACT.name, "Customer Approval");
    await expect(detail.currentStep()).toContainText("Scheduled");
  });

  test("isolation: a change request of customer A is never put to customer B's contact, who sees no Approve / Reject", async ({ page }) => {
    test.setTimeout(150_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_BETA_CONTACT);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.overviewChips("Customer group")).toHaveText(ACME_CONTACTS);
    await expect(page.getByText(FAKE_BETA_CONTACT.name, { exact: true })).toHaveCount(0);
    await expect(detail.approveButton()).toHaveCount(0);
    await expect(detail.rejectButton()).toHaveCount(0);
    expect(api.stages().find((st) => st.stage === "Customer Approval")?.approvers.map((a) => a.name)).toEqual(ACME_CONTACTS);
  });

  test("a project whose only contact is the creator provisions no stage: manual path stays, no helper", async ({ page }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, { projectId: GAMMA.id });
    api.setProjectContacts(GAMMA.id, [FAKE_CREATOR]);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(page.getByRole("cell", { name: "Customer Approval", exact: true })).toHaveCount(0);
    await expect(page.getByText(NO_CUSTOMER_GROUP_TEXT)).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Roll back -- the failed-review off-ramp. Offered, next to the forward move and
// Cancel change, from Review and Customer Review only; destructive (menu-only),
// and it needs a stated reason, which is posted as a comment before the PATCH.
// Rollback is final: the stepper shows the Rollback off-ramp and no actions are
// left. Runs against the in-browser fake of the backend contract.
// ---------------------------------------------------------------------------

/** Roll back is not offered: either no overflow menu at all, or none of its entries is "Roll back". */
async function expectNoRollbackOffered(detail: ChangeRequestDetailPage): Promise<void> {
  await expect(detail.page.getByRole("button", { name: "Roll back", exact: true })).toHaveCount(0);
  if ((await detail.changeStateButton().count()) === 0) return;
  await detail.changeStateButton().click();
  await expect(detail.page.getByRole("menuitem").first()).toBeVisible();
  await expect(detail.rollbackMenuItem()).toHaveCount(0);
  await detail.page.keyboard.press("Escape");
}

/** The Rollback off-ramp: no step on the line is current, the note names the state, nothing is awaited. */
async function expectRolledBack(page: import("@playwright/test").Page, detail: ChangeRequestDetailPage, api: FakeChangeRequestApi): Promise<void> {
  await expect(detail.reasonDialog()).toHaveCount(0);
  await expect(page.getByText(/diverted from the standard path/i)).toBeVisible();
  await expect(page.locator(".MuiChip-label", { hasText: /^Rollback$/ }).first()).toBeVisible();
  await expect(detail.currentStep()).toHaveCount(0);
  await expect(detail.blockingReason()).toHaveCount(0);
  await expect(detail.changeStateButton()).toHaveCount(0);
  await expect(detail.approveButton()).toHaveCount(0);
  expect(api.state()).toBe("rollback");
  // Nobody is left pending on a rolled-back change.
  for (const st of api.stages()) {
    for (const a of st.approvers) expect(a.status, `${st.stage}/${a.name}`).not.toBe("REQUESTED");
  }
}

/** Opens Roll back from the overflow menu, shows reason is required, then confirms with `reason`. */
async function rollBackWithReason(page: import("@playwright/test").Page, detail: ChangeRequestDetailPage, reason: string): Promise<void> {
  await detail.changeStateButton().click();
  await detail.rollbackMenuItem().click();
  const dialog = detail.reasonDialog();
  await expect(dialog.getByRole("heading", { name: "Roll back this change?" })).toBeVisible();
  // A reason is required: the confirm action stays disabled until one is typed.
  await expect(dialog.getByRole("button", { name: "Roll back", exact: true })).toBeDisabled();
  await dialog.getByLabel("Reason").fill(reason);
  await expect(dialog.getByRole("button", { name: "Roll back", exact: true })).toBeEnabled();
  await dialog.getByRole("button", { name: "Roll back", exact: true }).click();
  await expect(page.getByText(/diverted from the standard path/i)).toBeVisible();
}

//
// Assignment group: each approver row's Assignment group is a link that opens the
// group (ServiceNow's group page) and lists its members; for the customer stages
// there is no group, so it lists the project's registered contacts. Against the
// same fake (`GET /groups/{id}`, `assignmentGroup` on every internal stage).
//

/** The group-page requests the fake has served so far. */
const groupRequests = (api: FakeChangeRequestApi): string[] => api.requests().filter((r) => r.startsWith("GET /groups/"));

test.describe("change request approval flow — opening an Assignment group", () => {
  test("Peer row: the group opens with its details and members; Escape closes it and returns focus to the link", async ({ page }) => {
    test.setTimeout(60_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Assess");
    await expect(detail.approverStage("Pat Peer")).toHaveText("Peer Approval");
    await expect(detail.approverRow("Pat Peer", "Peer Approval")).toContainText(FAKE_PEER_GROUP.name);

    // Nothing is fetched until the link is used.
    expect(groupRequests(api)).toEqual([]);

    // Reachable and operable from the keyboard alone.
    const link = detail.groupLink("Pat Peer", FAKE_PEER_GROUP.name, "Peer Approval");
    await link.focus();
    await page.keyboard.press("Enter");

    const dialog = detail.groupDialog(FAKE_PEER_GROUP.name);
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("Mona Manager")).toBeVisible();
    await expect(dialog.getByRole("link", { name: "example-corp-abt@example.com" })).toBeVisible();
    await expect(dialog.getByText("Builds and supports the Example Corp account.")).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    const members = dialog.getByRole("listitem");
    await expect(members).toHaveCount(2);
    await expect(members.nth(0)).toContainText(FAKE_PEER.name);
    await expect(members.nth(0)).toContainText(FAKE_PEER.email);
    await expect(members.nth(0)).toContainText("Lead");
    await expect(members.nth(1)).toContainText(FAKE_PEER_COLLEAGUE.name);
    await expect(members.nth(1)).not.toContainText("Lead");
    expect(groupRequests(api)).toEqual([`GET /groups/${FAKE_PEER_GROUP.id}`]);

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(link).toBeFocused();
  });

  test("CAB row: opens the CAB Approval group's own members (not the peer group's)", async ({ page }) => {
    test.setTimeout(90_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await switchTo(page, api, FAKE_PEER);
    await detail.approve("Pat Peer");
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.approverStage("Cam Cab")).toHaveText("CAB Approval");

    await detail.groupLink("Cam Cab", FAKE_CAB_GROUP.name, "CAB Approval").click();
    const dialog = detail.groupDialog(FAKE_CAB_GROUP.name);
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "Group Members (3)" })).toBeVisible();
    const members = dialog.getByRole("listitem");
    await expect(members).toHaveText([
      new RegExp(`${FAKE_CAB.name}.*${FAKE_CAB.email}`),
      new RegExp(`${FAKE_CAB_COLLEAGUE.name}.*${FAKE_CAB_COLLEAGUE.email}`),
      new RegExp(`^${FAKE_CAB_NO_EMAIL.name}$`), // no email on file: just the name
    ]);
    // A group with no description, email or manager shows only its members.
    await expect(dialog.getByText("Manager", { exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Group email", { exact: true })).toHaveCount(0);
    await expect(dialog.getByText("Description", { exact: true })).toHaveCount(0);
    expect(groupRequests(api)).toEqual([`GET /groups/${FAKE_CAB_GROUP.id}`]);

    await dialog.getByRole("button", { name: "Close" }).click();
    await expect(dialog).toBeHidden();
    // The Peer row's own group is still its own: a different request, a different page.
    await detail.groupLink("Pat Peer", FAKE_PEER_GROUP.name, "Peer Approval").click();
    await expect(detail.groupDialog(FAKE_PEER_GROUP.name).getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    expect(groupRequests(api)).toEqual([`GET /groups/${FAKE_CAB_GROUP.id}`, `GET /groups/${FAKE_PEER_GROUP.id}`]);
  });

  test("Emergency: the ECAB Approval row opens the ECAB group", async ({ page }) => {
    const api = await installFakeChangeRequestApi(page, "emergency", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.approverStage(FAKE_ECAB.name)).toHaveText("ECAB Approval");

    await detail.groupLink(FAKE_ECAB.name, FAKE_ECAB_GROUP.name, "ECAB Approval").click();
    const dialog = detail.groupDialog(FAKE_ECAB_GROUP.name);
    await expect(dialog.getByText("Emergency Change Advisory Board.")).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    await expect(dialog.getByRole("listitem")).toHaveText([
      new RegExp(`${FAKE_ECAB.name}.*Lead`),
      new RegExp(FAKE_ECAB_COLLEAGUE.name),
    ]);
    expect(groupRequests(api)).toEqual([`GET /groups/${FAKE_ECAB_GROUP.id}`]);
  });

  test("Customer Approval row: opens the Customer Group listing the project's registered contacts, with no group request", async ({ page }) => {
    test.setTimeout(120_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);
    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval")).toHaveText("Requested");

    await detail.groupLink(FAKE_CUST_ONE.name, "Customer Group", "Customer Approval").click();
    const dialog = detail.groupDialog("Customer Group");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    await expect(dialog.getByRole("listitem")).toHaveText([
      new RegExp(`${FAKE_CUST_ONE.name}.*${FAKE_CUST_ONE.email}`),
      new RegExp(`${FAKE_CUST_TWO.name}.*${FAKE_CUST_TWO.email}`),
    ]);
    // These are the contacts the page already has; there is no group to fetch.
    expect(groupRequests(api)).toEqual([]);

    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
  });

  test("a group that cannot be loaded shows an error with Try again, leaves the approvals table alone, and recovers", async ({ page }) => {
    test.setTimeout(60_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.approverStage("Pat Peer")).toHaveText("Peer Approval");

    api.failGroups(500);
    await detail.groupLink("Pat Peer", FAKE_PEER_GROUP.name, "Peer Approval").click();
    const dialog = detail.groupDialog(FAKE_PEER_GROUP.name);
    await expect(dialog.getByText("Could not load this group's members.")).toBeVisible();
    await expect(dialog.getByRole("listitem")).toHaveCount(0);

    api.failGroups(null);
    await dialog.getByRole("button", { name: "Try again" }).click();
    await expect(dialog.getByRole("heading", { name: "Group Members (2)" })).toBeVisible();
    await expect(dialog.getByText("Could not load this group's members.")).toHaveCount(0);

    await dialog.getByRole("button", { name: "Close" }).click();
    await expect(dialog).toBeHidden();
    await expect(detail.approverRow("Pat Peer", "Peer Approval")).toBeVisible();
  });

  test("opening and closing a group leaves Approve / Reject working", async ({ page }) => {
    test.setTimeout(60_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await switchTo(page, api, FAKE_PEER);
    await expect(detail.approveButton("Pat Peer")).toBeEnabled();
    await expect(detail.rejectButton("Pat Peer")).toBeEnabled();

    await detail.groupLink("Pat Peer", FAKE_PEER_GROUP.name, "Peer Approval").click();
    await expect(detail.groupDialog(FAKE_PEER_GROUP.name)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(detail.groupDialog(FAKE_PEER_GROUP.name)).toBeHidden();

    await expect(detail.approveButton("Pat Peer")).toBeEnabled();
    await expect(detail.rejectButton("Pat Peer")).toBeEnabled();
    await detail.approve("Pat Peer");
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.approverStatus("Pat Peer", "Peer Approval")).toHaveText("Approved");
  });
});

test.describe("change request approval flow — Roll back", () => {
  for (const review of [true, false]) {
    test(`Normal, customer review ${review ? "on" : "off"}: New -> ... -> Review -> Roll back (reason required), state shown after every step`, async ({
      page,
    }) => {
      test.setTimeout(180_000);
      const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerReviewRequired: review });
      const detail = new ChangeRequestDetailPage(page);

      // Roll back is never on offer on the way to Review.
      await openDetail(detail);
      await expect(detail.currentStep()).toContainText("New");
      await expectNoRollbackOffered(detail);
      await detail.requestApproval();
      await expect(detail.currentStep()).toContainText("Assess");
      await expectNoRollbackOffered(detail);
      await switchTo(page, api, FAKE_PEER);
      await detail.approve("Pat Peer");
      await expect(detail.currentStep()).toContainText("Authorize");
      await expectNoRollbackOffered(detail);
      await switchTo(page, api, FAKE_CAB);
      await detail.approve("Cam Cab");
      await switchTo(page, api, FAKE_CREATOR);
      await expect(detail.currentStep()).toContainText("Scheduled");
      await expectNoRollbackOffered(detail);
      await page.getByRole("button", { name: "Start implementation" }).click();
      await expect(detail.currentStep()).toContainText("Implement");
      await expectNoRollbackOffered(detail);
      await page.getByRole("button", { name: "Mark implemented" }).click();

      // Review: the forward move is the primary button, Roll back sits in the menu with Cancel change.
      await expect(detail.currentStep()).toContainText("Review");
      if (review) {
        await expect(detail.sendForCustomerReviewButton()).toBeVisible();
        await expect(detail.closeButton()).toHaveCount(0);
      } else {
        await expect(detail.closeButton()).toBeVisible();
        await expect(detail.sendForCustomerReviewButton()).toHaveCount(0);
      }
      await detail.changeStateButton().click();
      await expect(detail.page.getByRole("menuitem")).toHaveText(["Roll back", "Cancel change"]);
      await detail.page.keyboard.press("Escape");

      // Backing out of the dialog leaves the change untouched.
      await detail.changeStateButton().click();
      await detail.rollbackMenuItem().click();
      await detail.reasonDialog().getByRole("button", { name: "Close", exact: true }).click();
      await expect(detail.reasonDialog()).toHaveCount(0);
      await expect(detail.currentStep()).toContainText("Review");
      expect(api.state()).toBe("review");

      await rollBackWithReason(page, detail, "Post-deployment smoke test failed.");
      await expectRolledBack(page, detail, api);
      // The reason was recorded as a comment before the state moved.
      expect(api.journal()).toContainEqual({ kind: "comment", text: "Post-deployment smoke test failed." });
      const calls = api.requests();
      expect(calls.indexOf(`POST /change-requests/${FAKE_CR_ID}/comments`)).toBeGreaterThan(-1);
      expect(calls.indexOf(`POST /change-requests/${FAKE_CR_ID}/comments`)).toBeLessThan(
        calls.lastIndexOf(`PATCH /change-requests/${FAKE_CR_ID}`),
      );
      const patchBodies = api.requestBodies().filter((b) => b.request.startsWith("PATCH"));
      expect(patchBodies[patchBodies.length - 1]?.body).toEqual({ state: "rollback" });

      // Still rolled back after a reload: a terminal state with no way out.
      await page.reload();
      await expectRolledBack(page, detail, api);
    });
  }

  test("Normal with customer review on: Review -> Customer Review -> Roll back (manual fallback, no customer group)", async ({ page }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerReviewRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await page.getByRole("button", { name: "Mark implemented" }).click();
    await expect(detail.currentStep()).toContainText("Review");
    await detail.sendForCustomerReviewButton().click();

    // Customer Review without a group: Close is the primary move, Roll back is in the menu.
    await expect(detail.currentStep()).toContainText("Customer Review");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Review");
    await expect(detail.closeButton()).toBeVisible();
    await detail.changeStateButton().click();
    await expect(detail.page.getByRole("menuitem")).toHaveText(["Roll back", "Cancel change"]);
    await detail.page.keyboard.press("Escape");

    await rollBackWithReason(page, detail, "The customer rejected the result.");
    await expectRolledBack(page, detail, api);
    expect(api.journal()).toContainEqual({ kind: "comment", text: "The customer rejected the result." });
  });

  test("Customer Review with a customer group: Roll back is not offered while the group's review is pending", async ({ page }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(
      page,
      "normal",
      FAKE_CREATOR,
      { customerReviewRequired: true },
      ON_ACME,
    );
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await page.getByRole("button", { name: "Mark implemented" }).click();
    // Review still offers Roll back (the internal review can fail).
    await detail.changeStateButton().click();
    await expect(detail.rollbackMenuItem()).toBeVisible();
    await detail.page.keyboard.press("Escape");
    await detail.sendForCustomerReviewButton().click();
    await expect(detail.currentStep()).toContainText("Customer Review");
    await expectNoRollbackOffered(detail);
    await expectOnlyCancelOffered(detail);
  });

  test("Standard: Roll back is offered from Review too, and nowhere before it", async ({ page }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(page, "standard", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await expectNoRollbackOffered(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Scheduled");
    await expectNoRollbackOffered(detail);
    await page.getByRole("button", { name: "Start implementation" }).click();
    await expect(detail.currentStep()).toContainText("Implement");
    await expectNoRollbackOffered(detail);
    await page.getByRole("button", { name: "Mark implemented" }).click();
    await expect(detail.currentStep()).toContainText("Review");
    await rollBackWithReason(page, detail, "Backout after failed verification.");
    await expectRolledBack(page, detail, api);
  });
});

// ---------------------------------------------------------------------------
// An approval is only actionable while the change is in its stage's state. Reported
// bug: an internal reviewer kept Approve / Reject on the Review stage of a change that
// was already Closed (and while it waited at Customer Review for the customer). Deciding
// Review changes no state -- a human moves the change on -- and nothing cancelled the
// Review stage's other approvers when it left Review. The fake backend does what the
// real one does now: every PATCH / decision ends by cancelling the REQUESTED rows of the
// stages the change has left (all of them once it is closed / canceled / rollback), and
// canDecide is false on a REQUESTED row of a stage the change has left.
// ---------------------------------------------------------------------------

/** Cancel change from the overflow menu, with the reason the dialog insists on. */
async function cancelChangeWithReason(page: Page, detail: ChangeRequestDetailPage, reason: string): Promise<void> {
  await detail.changeStateButton().click();
  await detail.cancelChangeMenuItem().click();
  const dialog = detail.reasonDialog();
  await dialog.getByLabel("Reason").fill(reason);
  await dialog.getByRole("button", { name: "Cancel change", exact: true }).click();
  await expect(detail.reasonDialog()).toHaveCount(0);
}

/** Drives a fresh Normal CR (Peer, CAB, implementation) to Review, leaving the creator signed in. */
async function driveToReview(
  page: Page,
  api: FakeChangeRequestApi,
  detail: ChangeRequestDetailPage,
): Promise<void> {
  await approveInternally(page, api, detail);
  await switchTo(page, api, FAKE_CREATOR);
  await page.getByRole("button", { name: "Start implementation" }).click();
  await expect(detail.currentStep()).toContainText("Implement");
  await page.getByRole("button", { name: "Mark implemented" }).click();
  await expect(detail.currentStep()).toContainText("Review");
}

/** Nobody can approve or reject anything: no controls render at all. */
async function expectNoDecisionControls(detail: ChangeRequestDetailPage): Promise<void> {
  await expect(detail.approveButton()).toHaveCount(0);
  await expect(detail.rejectButton()).toHaveCount(0);
}

/** Every approver row of every stage, flattened -- nothing may still be REQUESTED on a finished change. */
const requestedRows = (api: FakeChangeRequestApi): string[] =>
  api.stages().flatMap((st) => st.approvers.filter((a) => a.status === "REQUESTED").map((a) => `${st.stage}/${a.name}`));

const REVIEWERS = [FAKE_PEER, FAKE_PEER_COLLEAGUE] as const;

test.describe("change request approval flow — a Review approver's controls follow the change request's state", () => {
  test("Review: the assigned group's members can decide their own row, nobody else can", async ({ page }) => {
    test.setTimeout(150_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await driveToReview(page, api, detail);

    // Provisioned for the assigned group's internal members, the creator excluded.
    expect(api.stages().map((st) => st.stage)).toEqual(["Peer Approval", "CAB Approval", "Review"]);
    expect(api.stages()[2]!.approvers).toEqual([
      { name: FAKE_PEER.name, status: "REQUESTED" },
      { name: FAKE_PEER_COLLEAGUE.name, status: "REQUESTED" },
    ]);
    await expectNoDecisionControls(detail); // the creator

    for (const reviewer of REVIEWERS) {
      await switchTo(page, api, reviewer);
      await expect(detail.currentStep()).toContainText("Review");
      await expect(detail.approverStatus(reviewer.name, "Review")).toHaveText("Requested");
      await expect(detail.approveButton(reviewer.name, "Review")).toBeEnabled();
      await expect(detail.rejectButton(reviewer.name, "Review")).toBeEnabled();
      await expect(detail.approveButton()).toHaveCount(1); // their own row only
    }
    // A CAB member decided long ago and is not in the assigned group.
    await switchTo(page, api, FAKE_CAB);
    await expectNoDecisionControls(detail);
  });

  for (const moveOn of ["customer_review", "closed", "rollback", "canceled"] as const) {
    test(`Review -> ${moveOn}: the reviewers can no longer approve or reject`, async ({ page }) => {
      test.setTimeout(240_000);
      const api = await installFakeChangeRequestApi(
        page,
        "normal",
        FAKE_CREATOR,
        { customerReviewRequired: moveOn === "customer_review" },
        moveOn === "customer_review" ? ON_ACME : {},
      );
      const detail = new ChangeRequestDetailPage(page);
      await driveToReview(page, api, detail);

      // In Review the first reviewer can decide.
      await switchTo(page, api, FAKE_PEER);
      await expect(detail.approveButton("Pat Peer", "Review")).toBeEnabled();

      // The creator moves the change on.
      await switchTo(page, api, FAKE_CREATOR);
      if (moveOn === "customer_review") {
        await detail.sendForCustomerReviewButton().click();
        await expect(detail.currentStep()).toContainText("Customer Review");
      } else if (moveOn === "closed") {
        await detail.closeButton().click();
        await expect(detail.currentStep()).toContainText("Closed");
      } else if (moveOn === "rollback") {
        await rollBackWithReason(page, detail, "Smoke test failed.");
        await expectRolledBack(page, detail, api);
      } else {
        await cancelChangeWithReason(page, detail, "No longer needed.");
        await expect(page.locator(".MuiChip-label", { hasText: /^Canceled$/ }).first()).toBeVisible();
      }
      expect(api.state()).toBe(moveOn);
      // The Review rows were cancelled on the way -- every one of them.
      expect(api.stages()[2]!.approvers.map((a) => a.status)).toEqual(["CANCELLED", "CANCELLED"]);
      if (moveOn !== "customer_review") expect(requestedRows(api)).toEqual([]);

      // Both reviewers now see their rows as Cancelled, with no Approve / Reject (also after a reload).
      for (const reviewer of REVIEWERS) {
        await switchTo(page, api, reviewer);
        await expect(detail.approverStatus(reviewer.name, "Review")).toHaveText("Cancelled");
        await expectNoDecisionControls(detail);
      }
    });
  }

  test("Review -> Customer Review -> the customer approves -> Closed: the customer answers, nothing is requested at the end", async ({ page }) => {
    test.setTimeout(240_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerReviewRequired: true }, ON_ACME);
    const detail = new ChangeRequestDetailPage(page);
    await driveToReview(page, api, detail);
    await detail.sendForCustomerReviewButton().click();
    await expect(detail.currentStep()).toContainText("Customer Review");
    expect(requestedRows(api)).toEqual(["Customer Review/Mia Member", "Customer Review/Max Member"]);

    // The reviewers have nothing to decide while the customer answers...
    await switchTo(page, api, FAKE_PEER);
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Review");
    await expectNoDecisionControls(detail);
    // ...the customer does.
    await switchTo(page, api, FAKE_CUST_ONE);
    await expect(detail.approveButton(FAKE_CUST_ONE.name, "Customer Review")).toBeEnabled();
    await detail.approve(FAKE_CUST_ONE.name, "Customer Review");
    await expect(detail.currentStep()).toContainText("Closed");
    expect(api.state()).toBe("closed");
    expect(requestedRows(api)).toEqual([]);
    for (const user of [FAKE_PEER, FAKE_PEER_COLLEAGUE, FAKE_CUST_TWO, FAKE_CUST_ONE]) {
      await switchTo(page, api, user);
      await expectNoDecisionControls(detail);
    }
  });

  test("a Review decision records the answer and leaves the change in Review; the other member's row is cancelled", async ({ page }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await driveToReview(page, api, detail);

    await switchTo(page, api, FAKE_PEER);
    await detail.approve("Pat Peer", "Review");
    await expect(detail.approverStatus("Pat Peer", "Review")).toHaveText("Approved");
    await expect(detail.approverStatus("Quinn Peer", "Review")).toHaveText("Cancelled");
    expect(api.state()).toBe("review"); // a human moves it on
    await expectNoDecisionControls(detail);
    await switchTo(page, api, FAKE_PEER_COLLEAGUE);
    await expectNoDecisionControls(detail);
    await switchTo(page, api, FAKE_CREATOR);
    await detail.closeButton().click();
    await expect(detail.currentStep()).toContainText("Closed");
    expect(requestedRows(api)).toEqual([]);
  });

  test("a legacy row left REQUESTED after the change moved on reads canDecide=false: the controls are disabled, with the reason", async ({ page }) => {
    test.setTimeout(180_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR);
    const detail = new ChangeRequestDetailPage(page);
    await driveToReview(page, api, detail);
    const decisions = (): string[] => api.requests().filter((r) => r.endsWith("/approvals/decision"));
    const decisionsBefore = decisions().length; // the Peer and CAB approvals that got it here
    // The change moved on without the sweep (a row written before it existed).
    api.setState("closed");

    await switchTo(page, api, FAKE_PEER);
    await expect(detail.currentStep()).toContainText("Closed");
    await expect(detail.approverStatus("Pat Peer", "Review")).toHaveText("Requested");
    await expect(detail.approveButton("Pat Peer", "Review")).toBeDisabled();
    await expect(detail.rejectButton("Pat Peer", "Review")).toBeDisabled();
    await expect(page.getByLabel(/you aren't able to approve or reject this stage/i)).toBeVisible();
    expect(decisions()).toHaveLength(decisionsBefore); // nothing was submitted from the disabled controls
  });
});

// ---------------------------------------------------------------------------
// Re-schedule -- the diagram's Time Change loop. In Customer Approval an outlined
// "Re-schedule" button opens a dialog (current window prefilled, at least one end
// must change, optional reason); the change goes back to Authorize for CAB / ECAB
// approval again, then the customer is asked again. Offered from Customer Approval
// only. Runs against the in-browser fake of the backend contract.
// ---------------------------------------------------------------------------

/** Re-schedule is not offered: no such button and no such menu entry. */
async function expectNoRescheduleOffered(detail: ChangeRequestDetailPage): Promise<void> {
  await expect(detail.rescheduleButton()).toHaveCount(0);
  if ((await detail.changeStateButton().count()) === 0) return;
  await detail.changeStateButton().click();
  await expect(detail.page.getByRole("menuitem").first()).toBeVisible();
  await expect(detail.page.getByRole("menuitem", { name: /re-schedule|authorize/i })).toHaveCount(0);
  await detail.page.keyboard.press("Escape");
}

// Typed in the signed-in user's time zone; the PATCH carries UTC, so the specs assert
// the stored window moved to the week after (any zone offset keeps it on 2030-03-0[78]).
const NEXT_WEEK_START = { month: 3, day: 8, year: 2030, hour12: 12, minute: 0, pm: true };
const NEXT_WEEK_END = { month: 3, day: 8, year: 2030, hour12: 2, minute: 0, pm: true };
const ORIGINAL_WINDOW = { start: "2030-03-01 09:00:00", end: "2030-03-01 11:00:00" };
const MOVED_TO_NEXT_WEEK = /^2030-03-0[78] \d{2}:\d{2}:00$/;

test.describe("change request approval flow — Re-schedule", () => {
  test("Normal with a customer group: Customer Approval -> Re-schedule -> Authorize -> CAB approves -> Customer Approval again -> member approves -> Scheduled, state shown after every step", async ({
    page,
  }) => {
    test.setTimeout(240_000);
    const api = await installFakeChangeRequestApi(
      page,
      "normal",
      FAKE_CREATOR,
      { customerApprovalRequired: true },
      ON_ACME,
    );
    const detail = new ChangeRequestDetailPage(page);

    // Re-schedule is never on offer before Customer Approval.
    await openDetail(detail);
    await expectNoRescheduleOffered(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Assess");
    await expectNoRescheduleOffered(detail);
    await switchTo(page, api, FAKE_PEER);
    await detail.approve("Pat Peer");
    await expect(detail.currentStep()).toContainText("Authorize");
    await expectNoRescheduleOffered(detail);
    await switchTo(page, api, FAKE_CAB);
    await detail.approve("Cam Cab");

    // Customer Approval: the customer group is asked; Re-schedule and Cancel are the creator's actions.
    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);
    await expect(detail.rescheduleButton()).toBeVisible();
    await detail.changeStateButton().click();
    await expect(detail.page.getByRole("menuitem")).toHaveText(["Cancel change"]);
    await detail.page.keyboard.press("Escape");

    // The dialog starts on the current window and will not submit without a change.
    await detail.rescheduleButton().click();
    await expect(detail.rescheduleDialog()).toBeVisible();
    await expect(detail.rescheduleSubmit()).toBeDisabled();
    await expect(detail.rescheduleDialog().getByText("Change the planned start or end to re-schedule.")).toBeVisible();
    await detail.fillRescheduleWindow("Planned start", NEXT_WEEK_START);
    await detail.fillRescheduleWindow("Planned end", NEXT_WEEK_END);
    await expect(detail.rescheduleSubmit()).toBeEnabled();
    await detail.rescheduleDialog().getByLabel("Reason (optional)").fill("Customer freeze next week.");
    await detail.rescheduleSubmit().click();

    // Back at Authorize, waiting on a fresh CAB stage; the customer's request is superseded.
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.rescheduleDialog()).toHaveCount(0);
    expect(api.state()).toBe("authorize");
    await expect(detail.blockingReason()).toHaveText("Awaiting CAB Approval");
    await expect(detail.rescheduleButton()).toHaveCount(0);
    await expect(detail.recordCustomerApprovalButton()).toHaveCount(0);
    expect(api.stages().map((s) => s.stage)).toEqual(["Peer Approval", "CAB Approval", "Customer Approval", "CAB Approval"]);
    expect(api.stages()[2].approvers.map((a) => a.status)).toEqual(["CANCELLED", "CANCELLED"]);
    expect(api.stages()[3].approvers.map((a) => a.status)).toEqual(["REQUESTED"]);
    expect(api.planned().start).toMatch(MOVED_TO_NEXT_WEEK);
    expect(api.planned().end).toMatch(MOVED_TO_NEXT_WEEK);
    expect(api.journal()).toContainEqual({ kind: "comment", text: "Customer freeze next week." });
    const patches = api.requestBodies().filter((b) => b.request.startsWith("PATCH"));
    expect(patches[patches.length - 1]?.body).toEqual({
      state: "authorize",
      plannedStartOn: api.planned().start,
      plannedEndOn: api.planned().end,
    });
    await expectNoRescheduleOffered(detail);

    // The new CAB approval asks the customer group again.
    await switchTo(page, api, FAKE_CAB);
    await expect(detail.approveButton()).toHaveCount(1);
    await detail.approve("Cam Cab");
    await expect(detail.currentStep()).toContainText("Customer Approval");
    expect(api.state()).toBe("customer_approval");
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval").nth(1)).toHaveText("Requested");
    await expect(detail.approverStatus(FAKE_CUST_ONE.name, "Customer Approval").first()).toHaveText("Cancelled");

    await switchTo(page, api, FAKE_CUST_ONE);
    await detail.approveButton().click();
    await expect(detail.currentStep()).toContainText("Scheduled");
    expect(api.state()).toBe("scheduled");
    await expect(detail.blockingReason()).toHaveCount(0);
    await expectNoRescheduleOffered(detail);
  });

  test("Normal without a customer group: Re-schedule sits next to Record customer approval; the dialog blocks an unchanged window and shows the backend's refusal", async ({
    page,
  }) => {
    test.setTimeout(240_000);
    const api = await installFakeChangeRequestApi(page, "normal", FAKE_CREATOR, { customerApprovalRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await approveInternally(page, api, detail);

    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.recordCustomerApprovalButton()).toBeVisible();
    await expect(detail.rescheduleButton()).toBeVisible();

    // No change -> the submit stays disabled.
    await detail.rescheduleButton().click();
    await expect(detail.rescheduleSubmit()).toBeDisabled();

    // The backend's 400 is shown verbatim: the change moved on behind the dialog's back.
    await detail.fillRescheduleWindow("Planned end", NEXT_WEEK_END);
    await expect(detail.rescheduleSubmit()).toBeEnabled();
    api.setState("scheduled");
    await detail.rescheduleSubmit().click();
    await expect(detail.rescheduleDialog().getByRole("alert")).toContainText(
      'state "authorize" cannot be set manually',
    );
    await expect(detail.rescheduleDialog().getByRole("alert")).toContainText(
      "it can only be set by hand to re-schedule a change from customer_approval",
    );
    expect(api.state()).toBe("scheduled");
    expect(api.planned()).toEqual(ORIGINAL_WINDOW);
    await detail.rescheduleDialog().getByRole("button", { name: "Close", exact: true }).click();
    await expect(detail.rescheduleDialog()).toHaveCount(0);

    // Back in Customer Approval, the loop works; the reasonless submit records no comment.
    api.setState("customer_approval");
    await page.reload();
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await detail.rescheduleButton().click();
    await detail.fillRescheduleWindow("Planned end", NEXT_WEEK_END);
    await detail.rescheduleSubmit().click();
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.blockingReason()).toHaveText("Awaiting CAB Approval");
    expect(api.journal()).toEqual([]);
    expect(api.stages().map((s) => s.stage)).toEqual(["Peer Approval", "CAB Approval", "CAB Approval"]);
  });

  test("Emergency: Re-schedule goes back to Authorize for ECAB approval", async ({ page }) => {
    test.setTimeout(240_000);
    const api = await installFakeChangeRequestApi(page, "emergency", FAKE_CREATOR, { customerApprovalRequired: true });
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Authorize");
    await switchTo(page, api, FAKE_ECAB);
    await detail.approve("Eli Ecab");
    await switchTo(page, api, FAKE_CREATOR);
    await expect(detail.currentStep()).toContainText("Customer Approval");

    await detail.rescheduleButton().click();
    await expect(detail.rescheduleDialog().getByText(/ECAB approval again/)).toBeVisible();
    await detail.fillRescheduleWindow("Planned end", NEXT_WEEK_END);
    await detail.rescheduleSubmit().click();
    await expect(detail.currentStep()).toContainText("Authorize");
    await expect(detail.blockingReason()).toHaveText("Awaiting ECAB Approval");
    expect(api.stages().map((s) => s.stage)).toEqual(["ECAB Approval", "ECAB Approval"]);

    await switchTo(page, api, FAKE_ECAB);
    await detail.approveButton().click();
    await expect(detail.currentStep()).toContainText("Customer Approval");
    expect(api.state()).toBe("customer_approval");
  });

  test("Standard with a customer group: Re-schedule stays in Customer Approval and asks the customer again", async ({ page }) => {
    test.setTimeout(240_000);
    const api = await installFakeChangeRequestApi(
      page,
      "standard",
      FAKE_CREATOR,
      { customerApprovalRequired: true },
      ON_ACME,
    );
    const detail = new ChangeRequestDetailPage(page);
    await openDetail(detail);
    await expectNoRescheduleOffered(detail);
    await detail.requestApproval();
    await expect(detail.currentStep()).toContainText("Customer Approval");
    await expect(detail.rescheduleButton()).toBeVisible();

    await detail.rescheduleButton().click();
    await expect(detail.rescheduleDialog().getByText(/stays in Customer Approval/)).toBeVisible();
    await detail.fillRescheduleWindow("Planned end", NEXT_WEEK_END);
    await detail.rescheduleSubmit().click();
    await expect(detail.rescheduleDialog()).toHaveCount(0);
    await expect(detail.currentStep()).toContainText("Customer Approval");
    expect(api.state()).toBe("customer_approval");
    expect(api.planned().end).toMatch(MOVED_TO_NEXT_WEEK);
    expect(api.planned().start).toBe(ORIGINAL_WINDOW.start);
    expect(api.stages().map((s) => s.stage)).toEqual(["Customer Approval", "Customer Approval"]);
    expect(api.stages()[0].approvers.map((a) => a.status)).toEqual(["CANCELLED", "CANCELLED"]);
    expect(api.stages()[1].approvers.map((a) => a.status)).toEqual(["REQUESTED", "REQUESTED"]);
    await expect(detail.blockingReason()).toHaveText("Awaiting Customer Approval");
    await expect(detail.rescheduleButton()).toBeVisible();
  });
});

test.describe("seeded fixtures (local stack) — create with an assignment group", () => {
  test("a team picked from the Assignment group picker is saved on create (no FK 400)", async ({ page }) => {
    test.setTimeout(60_000);

    const cr = new ChangeRequestCreatePage(page);
    await cr.goto();
    await cr.selectType("Normal");
    await cr.subjectField().fill(`[E2E] local create with assignment group ${new Date().toISOString()}`);

    const group = page.getByRole("combobox", { name: /^Assignment group/ });
    await group.fill("Apollo");
    await page.getByRole("option", { name: /Apollo/ }).first().click();

    const [response] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === "POST" && /\/change-requests$/.test(r.url())),
      cr.createButton().click(),
    ]);
    expect(response.status(), await response.text()).toBe(201);
    await expect(page).toHaveURL(/\/operations\/change-requests\/(?!new(?:[/?#]|$))[^/]+$/, { timeout: 15_000 });
  });
});
