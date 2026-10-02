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

package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"golang.org/x/sync/errgroup"
)

// EscalationRepository defines the persistence operations for case_escalation
// and case_escalation_notification_list (migration 0054).
//
// Both tables are project-membership-scoped by row-level security
// (migration 0141, updated by a later migration once CreateEscalation
// below turned out to be a genuine customer-facing write, not
// internal-only as first assumed), keyed on the caller identity Scoped
// forwards as session GUCs -- this repository does no project filtering of
// its own at all; a caller sees and writes exactly the rows Postgres
// decides to hand back.
type EscalationRepository interface {
	// SearchEscalations returns a filtered, sorted, paginated slice of
	// escalations together with the total count of matching rows before
	// pagination.
	SearchEscalations(ctx context.Context, caseIDs []string, currentLevels []int, sortField, sortOrder string, limit, offset int) ([]domain.Escalation, int, error)
	// CreateEscalation escalates or de-escalates caseID and recomputes its
	// notification-recipient list, all in one transaction.
	//
	// Level transition: ESCALATE always sets current_level to the case's
	// current level + 1, capped at EL5 (escalating an already-EL5 case is a
	// silent no-op on the level, not an error -- it still records a new
	// case_escalation row). DEESCALATE always sets current_level to current
	// level - 1, floored at EL0; DEESCALATE on an already-EL0 case has
	// nothing to de-escalate and returns a ValidationError rather than a
	// silent no-op or going negative. previous_level is always whatever the
	// level was immediately before this call, for both directions.
	// "case".is_escalated is set TRUE for any resulting level >= 1, FALSE at
	// EL0 -- mirrored both directions.
	//
	// Notification recipients: resolved and written cumulatively for
	// whatever the RESULTING level is (1..5), the same rule for both
	// ESCALATE and DEESCALATE -- e.g. de-escalating EL3 -> EL2 writes the
	// EL1+EL2 cumulative set, identical to an escalation landing at EL2.
	// This is an approximation of ServiceNow's real EscalationUtils.
	// createEscalation/EscalationNotificationUtils.resolveNotificationUsers
	// (scoped app x_wso2_customer_0, read live against the DEV tenant),
	// built from what this schema actually has:
	//
	//   - EL1 (level >= 1): the case's account -> account.cre_team_id ->
	//     "group".manager_id -- an APPROXIMATION of SN's real rule (team
	//     members with u_team_member_role = 'HR Lead' on
	//     u_integration_cs_team), which has no Postgres equivalent at all
	//     ("group" has no per-member-role table, only one manager_id). This
	//     is a deliberate, documented divergence, not a proven match. This
	//     one piece is the only genuinely per-account/per-team lookup in the
	//     whole rule below; every other recipient source is the same across
	//     every case.
	//     Also: every REAL member of notifyCfg.EL1AmericasTLGroupID (a
	//     "group".id, resolved via team_member.group_id -- see
	//     groupMemberResolver's own doc comment), unconditionally (SN's own
	//     script comment: "Append Americas TL users -- ALWAYS", no region
	//     gate), and account.technical_owner_id (confirmed 1:1 match with
	//     SN's u_technical_owner).
	//     account.account_manager_id (SN's u_owner / "Account Owner") is a
	//     CONFIRMED, GENUINE GAP and is never read here: nothing in this
	//     repo's write path (SalesforceAccountUpsert) ever populates that
	//     column, so treating it as a real signal would fabricate a
	//     recipient from a column that's effectively always NULL in
	//     practice. Fixing it means fixing the Salesforce upsert mapping
	//     elsewhere -- out of scope here.
	//   - EL2 (level >= 2): every member of notifyCfg.EL2AmericasTUGroupID,
	//     unconditionally, plus every member of exactly one product-routed
	//     group picked from the case's deployed product's product.category/
	//     business_unit: SERVICE -> EL2ServiceProductGroupID; SOFTWARE with
	//     business_unit IAM -> EL2IdentityServerGroupID; everything else ->
	//     EL2DefaultProductGroupID. A case with no deployed product/product
	//     info at all gets none of the three, silently (not an error) --
	//     same "absence is a valid state" convention as caseProductName's
	//     own empty-string fallback.
	//   - EL3 (level >= 3): every member of notifyCfg.EL3CREHeadGroupID,
	//     plus account.customer_success_manager_id (confirmed 1:1 match).
	//   - EL4 (level >= 4): every member of notifyCfg.EL4CCOGroupID and
	//     notifyCfg.EL4CROGroupID.
	//   - EL5 (level >= 5): every member of notifyCfg.EL5CEOGroupID.
	//
	// Every notifyCfg.*GroupID is OPTIONAL -- empty/unset means no
	// recipients from that slot, never a request failure. A configured
	// group id that doesn't exist, or currently has zero team_member rows,
	// also yields zero recipients from that slot, not an error -- same
	// "flag, don't fabricate" posture as the rest of this rule. The final
	// list is deduped by user id before being written to
	// case_escalation_notification_list.
	//
	// Authorization ("only someone on the case's current notified-users list
	// may de-escalate") is deliberately NOT enforced here -- same reasoning
	// as CaseRepository.AcknowledgeCase's own doc comment: no Postgres-side
	// permission model exists yet, so this data source only requires a
	// known authenticated caller, same as every other Postgres case
	// mutation. A known, documented gap, not a silent omission.
	//
	// Returns a NotFoundError if caseID doesn't reference an existing case
	// (a work_item with no "case" extension row -- e.g. an engagement --
	// counts as not found here, since current_escalation_level/is_escalated
	// only ever live on "case").
	CreateEscalation(ctx context.Context, caseID string, action domain.EscalationAction, reason *string, actorEmail string) (domain.CreatedEscalation, error)
}

// EscalationNotificationConfig holds the fixed, deployment-specific
// escalation notification recipient GROUPS CreateEscalation layers on top of
// the per-case-derived ones -- see that method's own doc comment for the full
// EL1..EL5 cumulative rule these feed. Every field is a "group".id (migration
// 000073), resolved to its REAL member list via team_member.group_id
// (groupMemberResolver), not a single fixed address -- every configured tier
// notifies however many people are actually in that group. Every field is
// optional: empty means "no recipients from this slot," never a request
// failure.
type EscalationNotificationConfig struct {
	EL1AmericasTLGroupID     string
	EL2AmericasTUGroupID     string
	EL2ServiceProductGroupID string
	EL2IdentityServerGroupID string
	EL2DefaultProductGroupID string
	EL3CREHeadGroupID        string
	EL4CCOGroupID            string
	EL4CROGroupID            string
	EL5CEOGroupID            string
}

// groupMemberResolver resolves a "group".id to its real member user ids, via
// team_member.group_id (migration 0075) -- that column was added
// specifically for group membership but had no consumer until this one. An
// interface, not a direct query call, so tests can substitute an in-memory
// fixture instead of a real team_member table (see escalation_repo_test.go's
// fakeGroupMemberResolver). Takes a rowsQuerier (case_repo.go, satisfied by
// both *pgxpool.Pool and pgx.Tx) rather than always using its own pool, so
// CreateEscalation can run this against the SAME open tx that already holds
// the case row's FOR UPDATE lock -- see resolveEscalationRecipients's own
// doc comment for why that matters.
type groupMemberResolver interface {
	// GroupMemberUserIDs returns groupID's member "user".id values, empty
	// (not an error) if the group doesn't exist or currently has zero
	// team_member rows.
	GroupMemberUserIDs(ctx context.Context, q rowsQuerier, groupID string) ([]string, error)
}

// dbGroupMemberResolver is groupMemberResolver's real implementation --
// stateless (it carries no *pgxpool.Pool of its own): every call receives
// its querier explicitly, so it has no "own connection" to fall back to.
type dbGroupMemberResolver struct{}

// GroupMemberUserIDs implements groupMemberResolver. Joined to "user" the
// same way every other recipient resolution in this file is, even though
// team_member.user_id's own FK already guarantees a matching row -- kept for
// consistency with the rest of this file's style, not because it changes
// the result.
func (r *dbGroupMemberResolver) GroupMemberUserIDs(ctx context.Context, q rowsQuerier, groupID string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT u.id
		FROM team_member tm
		JOIN "user" u ON u.id = tm.user_id
		WHERE tm.group_id = $1::uuid`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query group member user ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group member user id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group member user ids: %w", err)
	}
	return ids, nil
}

type escalationRepo struct {
	db        *Scoped
	notifyCfg EscalationNotificationConfig
	groups    groupMemberResolver
}

// NewEscalationRepository constructs an EscalationRepository backed by the
// given Scoped connection -- never a raw *pgxpool.Pool, so every query this
// repository issues carries the caller's identity for case_escalation's RLS
// policy to read.
func NewEscalationRepository(db *Scoped, notifyCfg EscalationNotificationConfig) EscalationRepository {
	return &escalationRepo{db: db, notifyCfg: notifyCfg, groups: &dbGroupMemberResolver{}}
}

// escalationLevelToEnum/escalationLevelFromEnum convert between
// SearchEscalationsFilters.CurrentLevels' plain ints (0..5, the same
// convention CaseView.EscalationLevel's own doc comment uses) and
// case_escalation_level_enum's 'EL0'..'EL5' labels.
func escalationLevelToEnum(level int) string {
	return "EL" + strconv.Itoa(level)
}

// escalationChoiceItem builds a domain.ChoiceListItem for a case_escalation_level_enum
// value. Unlike the ServiceNow data source, Postgres has no human-readable label for an
// escalation level anywhere in this schema -- ID and Label are both the plain "0".."5"
// id (case_escalation_level_enum's 'EL' prefix stripped) rather than inventing display
// text this data source has no source for.
func escalationChoiceItem(enumValue string) domain.ChoiceListItem {
	id := strings.TrimPrefix(enumValue, "EL")
	return domain.ChoiceListItem{ID: id, Label: id}
}

const escalationSelectColumns = `
	ce.id, ce.work_item_id, wi.number, wi.subject, wi.wso2_id,
	ce.current_level::TEXT, ce.previous_level::TEXT,
	ce.created_by, ce.created_on, ce.updated_on, ce.reason`

const escalationFromJoins = `
	FROM case_escalation ce
	JOIN work_item wi ON wi.id = ce.work_item_id`

func scanEscalation(row interface{ Scan(...any) error }) (domain.Escalation, error) {
	var (
		e                               domain.Escalation
		caseID, caseNumber, caseSubject string
		wso2ID                          *string
		currentLevel, previousLevel     *string
		createdOn, updatedOn            time.Time
	)
	err := row.Scan(
		&e.ID, &caseID, &caseNumber, &caseSubject, &wso2ID,
		&currentLevel, &previousLevel,
		&e.CreatedBy, &createdOn, &updatedOn, &e.Reason,
	)
	if err != nil {
		return domain.Escalation{}, err
	}
	e.Case = domain.ReferenceTableItem{ID: caseID, Name: caseSubject, Number: &caseNumber, InternalID: stringPtrOrNil(wso2ID)}
	if currentLevel != nil {
		e.CurrentLevel = escalationChoiceItem(*currentLevel)
	}
	if previousLevel != nil {
		e.PreviousLevel = escalationChoiceItem(*previousLevel)
	}
	e.CreatedOn = createdOn.UTC().Format(time.RFC3339)
	e.UpdatedOn = updatedOn.UTC().Format(time.RFC3339)
	return e, nil
}

// stringPtrOrNil returns nil for a nil or blank *string, otherwise itself --
// wso2_id can be NULL or ” (see case_repo.go's own note on this), and
// ReferenceTableItem.InternalID must stay nil rather than render "".
func stringPtrOrNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// getEscalationNotifiedUsers batch-fetches the notification list for every
// id in escalationIDs, avoiding one query per escalation. q is a
// rowsQuerier (case_repo.go) rather than always r.db, so CreateEscalation
// can run this against the open tx before commit -- see that method's own
// call site for why.
func (r *escalationRepo) getEscalationNotifiedUsers(ctx context.Context, q rowsQuerier, escalationIDs []string) (map[string][]domain.EscalationNotifiedUser, error) {
	out := map[string][]domain.EscalationNotifiedUser{}
	if len(escalationIDs) == 0 {
		return out, nil
	}

	rows, err := q.Query(ctx, `
		SELECT cenl.case_escalation_id, u.id, u.user_name, COALESCE(u.name, NULLIF(TRIM(CONCAT_WS(' ', u.first_name, u.last_name)), '')), u.email
		FROM case_escalation_notification_list cenl
		JOIN "user" u ON u.id = cenl.user_id
		WHERE cenl.case_escalation_id = ANY($1::uuid[])
		ORDER BY cenl.id`, escalationIDs)
	if err != nil {
		return nil, fmt.Errorf("list escalation notified users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var escalationID, userID, userName string
		var name, email *string
		if err := rows.Scan(&escalationID, &userID, &userName, &name, &email); err != nil {
			return nil, fmt.Errorf("scan escalation notified user: %w", err)
		}
		out[escalationID] = append(out[escalationID], domain.EscalationNotifiedUser{ID: userID, UserName: userName, Name: name, Email: email})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate escalation notified users: %w", err)
	}
	return out, nil
}

// SearchEscalations implements EscalationRepository.
func (r *escalationRepo) SearchEscalations(ctx context.Context, caseIDs []string, currentLevels []int, sortField, sortOrder string, limit, offset int) ([]domain.Escalation, int, error) {
	where := "WHERE 1=1"
	args := []any{}
	if len(caseIDs) > 0 {
		args = append(args, caseIDs)
		where += fmt.Sprintf(" AND ce.work_item_id = ANY($%d::uuid[])", len(args))
	}
	if len(currentLevels) > 0 {
		levels := make([]string, len(currentLevels))
		for i, l := range currentLevels {
			levels[i] = escalationLevelToEnum(l)
		}
		args = append(args, levels)
		// ::text[] before ::case_escalation_level_enum[]: this repository
		// never registers case_escalation_level_enum/_case_escalation_level_enum
		// with pgx, so binding a []string directly to the enum array type has
		// no encode plan -- same fix as time_card_repo.go's state filter.
		where += fmt.Sprintf(" AND ce.current_level = ANY($%d::text[]::case_escalation_level_enum[])", len(args))
	}

	sortCol := "ce.created_on"
	if sortField == "updatedOn" {
		sortCol = "ce.updated_on"
	}
	sortDir := "DESC"
	if sortOrder == "asc" {
		sortDir = "ASC"
	}

	countQuery := "SELECT COUNT(*) " + escalationFromJoins + " " + where
	dataQuery := fmt.Sprintf("SELECT %s %s %s ORDER BY %s %s, ce.id LIMIT $%d OFFSET $%d",
		escalationSelectColumns, escalationFromJoins, where, sortCol, sortDir, len(args)+1, len(args)+2)
	dataArgs := append(append([]any{}, args...), limit, offset)

	var total int
	var escalations []domain.Escalation

	eg, egCtx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		if err := r.db.QueryRow(egCtx, countQuery, args...).Scan(&total); err != nil {
			return fmt.Errorf("count escalations: %w", err)
		}
		return nil
	})

	eg.Go(func() error {
		rows, err := r.db.Query(egCtx, dataQuery, dataArgs...)
		if err != nil {
			return fmt.Errorf("query escalations: %w", err)
		}
		defer rows.Close()

		out := make([]domain.Escalation, 0, limit)
		for rows.Next() {
			e, err := scanEscalation(rows)
			if err != nil {
				return fmt.Errorf("scan escalation: %w", err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate escalations: %w", err)
		}
		escalations = out
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, 0, err
	}

	ids := make([]string, len(escalations))
	for i, e := range escalations {
		ids[i] = e.ID
	}
	notifiedByEscalation, err := r.getEscalationNotifiedUsers(ctx, r.db, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range escalations {
		// openapi.yaml declares notificationSentTo as a required, non-nullable
		// array -- a map miss (no notification list for this escalation)
		// returns a nil slice, which json.Encode would render as null.
		notified := notifiedByEscalation[escalations[i].ID]
		if notified == nil {
			notified = []domain.EscalationNotifiedUser{}
		}
		escalations[i].NotificationSentTo = notified
	}

	return escalations, total, nil
}

// maxEscalationLevel is case_escalation_level_enum's ceiling (EL5) --
// ESCALATE never goes past it.
const maxEscalationLevel = 5

// escalationLevelInt parses a nullable current_level/previous_level column
// ("EL0".."EL5", or NULL for a case never escalated) into a plain 0..5 int.
// NULL is treated as EL0 -- "case".current_escalation_level/is_escalated
// have no NOT NULL constraint or default (migration 0023), and a case
// that's never been escalated is exactly the EL0 state.
func escalationLevelInt(raw *string) int {
	if raw == nil {
		return 0
	}
	n, err := strconv.Atoi(caseEscalationLevelFromEnum(*raw))
	if err != nil {
		return 0
	}
	return n
}

// nextEscalationLevel computes the resulting level for action given the
// case's current level (previous int, already normalized via
// escalationLevelInt) -- see EscalationRepository.CreateEscalation's own doc
// comment for the full rule. ESCALATE past EL5 silently clamps (not an
// error); DEESCALATE below EL0 returns a ValidationError instead of a
// silent no-op or going negative, since there's genuinely nothing to
// de-escalate.
func nextEscalationLevel(action domain.EscalationAction, previous int) (int, error) {
	if action == domain.EscalationActionDeescalate {
		if previous == 0 {
			return 0, &apierror.ValidationError{Msg: "case is already at the lowest escalation level (EL0); nothing to de-escalate"}
		}
		return previous - 1, nil
	}
	// ESCALATE, and any already-validated-upstream default.
	next := previous + 1
	if next > maxEscalationLevel {
		next = maxEscalationLevel
	}
	return next, nil
}

// escalationCaseContext is everything CreateEscalation needs about the case
// beyond its current/previous level, gathered in the same locked read so the
// recipient computation below is consistent with the level transition it's
// reacting to.
type escalationCaseContext struct {
	number, subject     string
	wso2ID              *string
	technicalOwnerID    *string
	csmID               *string
	creTeamManagerID    *string
	productCategory     *string
	productBusinessUnit *string
}

// resolveEscalationRecipients implements the cumulative EL1..EL5 rule
// described on EscalationRepository.CreateEscalation's own doc comment,
// returning a deduped set of "user".id values for whatever newLevel resulted
// from this call. A configured notifyCfg group id that doesn't exist or has
// no members resolves to an empty list, not an error -- identical to an
// unconfigured (empty) group id slot.
//
// q is CreateEscalation's own open tx, not r's pool -- CreateEscalation
// holds one pool connection for that tx, with a FOR UPDATE lock on the case
// row, for its whole duration. If GroupMemberUserIDs instead acquired a
// SECOND pool connection per call (as an earlier revision of this method
// did), a saturated pool means every concurrent escalation blocks on
// pool.Acquire while it's still holding its own tx connection and the
// case-row lock -- a real deadlock/stall risk under load, not just a
// theoretical one. Running the read on q=tx instead needs no second
// connection at all. This is safe: the call happens before tx.Commit (see
// CreateEscalation's own call site), so "transaction already closed" cannot
// occur, and getEscalationNotifiedUsers already reads through tx for the
// exact same reason. The reads are read-only, so this adds no extra locks.
func (r *escalationRepo) resolveEscalationRecipients(ctx context.Context, q rowsQuerier, newLevel int, cc escalationCaseContext) ([]string, error) {
	seen := map[string]bool{}
	add := func(id *string) {
		if id != nil && *id != "" {
			seen[*id] = true
		}
	}
	addGroup := func(groupID string) error {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			return nil
		}
		ids, err := r.groups.GroupMemberUserIDs(ctx, q, groupID)
		if err != nil {
			return fmt.Errorf("resolve escalation recipient group %s: %w", groupID, err)
		}
		for _, id := range ids {
			seen[id] = true
		}
		return nil
	}

	if newLevel >= 1 {
		add(cc.creTeamManagerID)
		if err := addGroup(r.notifyCfg.EL1AmericasTLGroupID); err != nil {
			return nil, err
		}
		add(cc.technicalOwnerID)
		// account.account_manager_id (SN's u_owner) is deliberately never
		// read -- see this repository's CreateEscalation doc comment for why.
	}
	if newLevel >= 2 {
		if err := addGroup(r.notifyCfg.EL2AmericasTUGroupID); err != nil {
			return nil, err
		}
		if cc.productCategory != nil {
			switch {
			case *cc.productCategory == "SERVICE":
				if err := addGroup(r.notifyCfg.EL2ServiceProductGroupID); err != nil {
					return nil, err
				}
			case cc.productBusinessUnit != nil && *cc.productBusinessUnit == "IAM":
				if err := addGroup(r.notifyCfg.EL2IdentityServerGroupID); err != nil {
					return nil, err
				}
			default:
				if err := addGroup(r.notifyCfg.EL2DefaultProductGroupID); err != nil {
					return nil, err
				}
			}
		}
		// cc.productCategory == nil (no deployed product/product info at
		// all): no product-routed recipient, silently -- not an error.
	}
	if newLevel >= 3 {
		if err := addGroup(r.notifyCfg.EL3CREHeadGroupID); err != nil {
			return nil, err
		}
		add(cc.csmID)
	}
	if newLevel >= 4 {
		if err := addGroup(r.notifyCfg.EL4CCOGroupID); err != nil {
			return nil, err
		}
		if err := addGroup(r.notifyCfg.EL4CROGroupID); err != nil {
			return nil, err
		}
	}
	if newLevel >= 5 {
		if err := addGroup(r.notifyCfg.EL5CEOGroupID); err != nil {
			return nil, err
		}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

// CreateEscalation implements EscalationRepository.
func (r *escalationRepo) CreateEscalation(ctx context.Context, caseID string, action domain.EscalationAction, reason *string, actorEmail string) (domain.CreatedEscalation, error) {
	return InTxReturning(ctx, r.db, func(tx pgx.Tx) (domain.CreatedEscalation, error) {
		return r.createEscalationTx(ctx, tx, caseID, action, reason, actorEmail)
	})
}

// createEscalationTx is CreateEscalation's body, extracted so it can run
// inside r.db.InTx's closure (Scoped.InTx pulls caller identity from ctx
// and sets it once for the whole transaction).
func (r *escalationRepo) createEscalationTx(ctx context.Context, tx pgx.Tx, caseID string, action domain.EscalationAction, reason *string, actorEmail string) (domain.CreatedEscalation, error) {
	var (
		currentLevel *string
		cc           escalationCaseContext
	)
	err := tx.QueryRow(ctx, `
		SELECT c.current_escalation_level::TEXT,
		       wi.number, wi.subject, wi.wso2_id,
		       a.technical_owner_id, a.customer_success_manager_id, g.manager_id,
		       prod.category::TEXT, prod.business_unit::TEXT
		FROM "case" c
		JOIN work_item wi ON wi.id = c.id
		LEFT JOIN account a ON a.id = wi.account_id
		LEFT JOIN "group" g ON g.id = a.cre_team_id
		LEFT JOIN deployed_product dp ON dp.id = wi.deployed_product_id
		LEFT JOIN product prod ON prod.id = dp.product_id
		WHERE c.id = $1
		FOR UPDATE OF c`, caseID,
	).Scan(
		&currentLevel, &cc.number, &cc.subject, &cc.wso2ID,
		&cc.technicalOwnerID, &cc.csmID, &cc.creTeamManagerID,
		&cc.productCategory, &cc.productBusinessUnit,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CreatedEscalation{}, &apierror.NotFoundError{Msg: "case not found"}
	}
	if err != nil {
		return domain.CreatedEscalation{}, fmt.Errorf("create escalation: lock case: %w", err)
	}

	previousLevelInt := escalationLevelInt(currentLevel)
	newLevelInt, err := nextEscalationLevel(action, previousLevelInt)
	if err != nil {
		return domain.CreatedEscalation{}, err
	}
	isEscalated := newLevelInt >= 1

	recipientIDs, err := r.resolveEscalationRecipients(ctx, tx, newLevelInt, cc)
	if err != nil {
		return domain.CreatedEscalation{}, err
	}

	var escalationID string
	var createdOn time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO case_escalation (id, created_on, updated_on, created_by, updated_by, work_item_id, current_level, previous_level, reason)
		VALUES (gen_random_uuid(), NOW(), NOW(), $1, $1, $2, $3::case_escalation_level_enum, $4::case_escalation_level_enum, $5)
		RETURNING id, created_on`,
		actorEmail, caseID, escalationLevelToEnum(newLevelInt), escalationLevelToEnum(previousLevelInt), reason,
	).Scan(&escalationID, &createdOn)
	if err != nil {
		return domain.CreatedEscalation{}, fmt.Errorf("create escalation: insert case_escalation: %w", err)
	}

	if len(recipientIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO case_escalation_notification_list (id, case_escalation_id, user_id)
			SELECT gen_random_uuid(), $1, u FROM unnest($2::uuid[]) AS u`,
			escalationID, recipientIDs,
		); err != nil {
			return domain.CreatedEscalation{}, fmt.Errorf("create escalation: insert notification list: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE "case" SET current_escalation_level = $2::case_escalation_level_enum, is_escalated = $3 WHERE id = $1`,
		caseID, escalationLevelToEnum(newLevelInt), isEscalated,
	); err != nil {
		return domain.CreatedEscalation{}, fmt.Errorf("create escalation: update case: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE work_item SET updated_on = NOW(), updated_by = $2 WHERE id = $1`,
		caseID, actorEmail,
	); err != nil {
		return domain.CreatedEscalation{}, fmt.Errorf("create escalation: update work_item: %w", err)
	}

	// Read back INSIDE the still-open tx, before commit -- not after. The
	// response needs richer fields (userName, name, email) than
	// recipientIDs alone carries (userName is a required field on
	// EscalationNotifiedUser per openapi.yaml, so it can't be synthesized
	// from just a user id), and reuses getEscalationNotifiedUsers's own
	// join rather than re-deriving it. Reading uncommitted rows this tx
	// itself just inserted is fine (a transaction sees its own writes).
	// Doing this AFTER commit instead (the original approach) meant a
	// failure here -- transient DB issue, pool timeout -- surfaced as a 5xx
	// for a write that had already succeeded, and a client retry would then
	// create a second, orphaned case_escalation row. Inside the tx, the
	// same failure safely rolls back the whole escalation instead.
	notifiedByEscalation, err := r.getEscalationNotifiedUsers(ctx, tx, []string{escalationID})
	if err != nil {
		return domain.CreatedEscalation{}, err
	}
	notified := notifiedByEscalation[escalationID]
	if notified == nil {
		notified = []domain.EscalationNotifiedUser{}
	}

	return domain.CreatedEscalation{
		ID:                 escalationID,
		Case:               domain.ReferenceTableItem{ID: caseID, Name: cc.subject, Number: &cc.number, InternalID: stringPtrOrNil(cc.wso2ID)},
		CurrentLevel:       escalationChoiceItem(escalationLevelToEnum(newLevelInt)),
		PreviousLevel:      escalationChoiceItem(escalationLevelToEnum(previousLevelInt)),
		CreatedBy:          actorEmail,
		CreatedOn:          createdOn.UTC().Format(time.RFC3339),
		Reason:             reason,
		NotificationSentTo: notified,
	}, nil
}
