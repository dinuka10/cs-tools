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

package slaengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/chataudience"
	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/eventbus"
	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/events"
	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/recipientlinks"
)

// Clock types — entity-service's own sla_duration_policy.clock_type
// vocabulary (lowercase), matching sla_policy.target's existing lower-cased
// convention this package already used on the wire (see the removed poll
// design's own SLAStatus.ClockType).
const (
	ClockResponse   = "response"
	ClockWorkaround = "workaround"
	ClockResolution = "resolution"
)

// clockTypesInOrder is every clock type RegisterClocks considers, in a
// fixed order purely for deterministic logging — registration itself
// doesn't depend on the order, since each is independently looked up in
// the duration policy map.
var clockTypesInOrder = []string{ClockResponse, ClockWorkaround, ClockResolution}

// tierSequence is the fixed set of elapsed-duration checkpoints this engine
// tracks per clock — 50%, 75%, 100% of the way from a clock's own start to
// its due date. Not configurable, ported as-is from the pre-poll design.
var tierSequence = []int{50, 75, 100}

func tierLabel(tier int) string {
	return strconv.Itoa(tier)
}

// store abstracts Store for testability.
type store interface {
	AddWake(ctx context.Context, member string, at time.Time) error
	RemoveWake(ctx context.Context, member string) error
	DueMembers(ctx context.Context, now time.Time) ([]string, error)
	SetClock(ctx context.Context, caseID, clockType string, meta ClockMeta) error
	GetClock(ctx context.Context, caseID, clockType string) (ClockMeta, bool, error)
	SetPaused(ctx context.Context, caseID, clockType string, paused bool) error
	SetState(ctx context.Context, caseID, clockType, state string) error
	AdvanceAlertedTier(ctx context.Context, caseID, clockType string, tier int) error
	ClaimTier(ctx context.Context, caseID, clockType string, tier int) (claimed bool, err error)
	ReleaseTier(ctx context.Context, caseID, clockType string, tier int) error
}

// eventPublisher abstracts eventbus.Producer for testability.
type eventPublisher interface {
	Publish(ctx context.Context, key, value []byte) error
}

// chatSender abstracts notifications.GoogleChatClient's SendSLABreachAlert/
// HasAudienceSpace for testability. teamLeadName is always sent "" — see
// Engine.sendBreachAlert's own doc comment for why this engine has no way
// to resolve it any more.
type chatSender interface {
	SendSLABreachAlert(ctx context.Context, audience, clockType, tier, caseNumber, wso2CaseID, caseTitle, caseType, productName, team, teamLeadName, severity, state, openedAt, caseLink string) error
	HasAudienceSpace(audience string) bool
}

// linkResolver abstracts recipientlinks.Resolver's CSMLink for testability.
type linkResolver interface {
	CSMLink(caseID string) string
}

// Engine is the SLA breach-alerting engine — see this package's own doc
// comment (client.go) for the full design and what it replaced.
type Engine struct {
	store store
	pub   eventPublisher
	chat  chatSender
	links linkResolver
	// durations is severity -> clockType -> duration, fetched once at
	// startup via EntityClient.GetDurationPolicy and never refreshed again
	// for the lifetime of this process — this data changes on the order of
	// "WSO2 updates its published support policy," not at runtime; a rare
	// change needs a restart to pick up, same as any other static
	// at-startup config in this service.
	durations map[string]map[string]time.Duration
}

// NewEngine constructs an Engine.
func NewEngine(store *Store, pub *eventbus.Producer, chat chatSender, links *recipientlinks.Resolver, durations map[string]map[string]time.Duration) *Engine {
	return &Engine{store: store, pub: pub, chat: chat, links: links, durations: durations}
}

// avoidWeekend rolls due forward to the next Monday, same time-of-day, if
// it falls on a Saturday or Sunday — approximates MEDIUM/S3's resolution
// duration being published as "1 Business Week" rather than a flat 7×24h.
// Ported from the pre-poll design's own helper.
func avoidWeekend(due time.Time) time.Time {
	switch due.Weekday() {
	case time.Saturday:
		return due.AddDate(0, 0, 2)
	case time.Sunday:
		return due.AddDate(0, 0, 1)
	default:
		return due
	}
}

// tierTime returns the instant tier is reached, given a clock that started
// at startedAt and runs for duration d. Ported from the pre-poll design's
// own helper.
func tierTime(startedAt time.Time, d time.Duration, tier int) time.Time {
	switch tier {
	case 50:
		return startedAt.Add(d / 2)
	case 75:
		return startedAt.Add(d * 3 / 4)
	default: // 100
		return startedAt.Add(d)
	}
}

func wakeMember(caseID, clockType string, tier int) string {
	return caseID + "|" + clockType + "|" + tierLabel(tier)
}

func parseWakeMember(member string) (caseID, clockType string, tier int, ok bool) {
	parts := strings.Split(member, "|")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	tier, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[1], tier, true
}

// RegisterClocks computes and schedules every clock dispatch.handleCaseCreated's
// own case.created payload implies — called once, from that handler,
// alongside its existing email/Chat reactions. priority must be one of the
// uppercase words GetDurationPolicy's own map is keyed by ("CATASTROPHIC"
// through "LOW"); an unrecognized or empty value is logged and skipped
// entirely — there's nothing to compute a due date from. A clock type the
// policy has no entry for this severity (e.g. LOW has no workaround/
// resolution row at all) is simply not registered — not an error, see
// migration 0192's own seed data.
//
// KNOWN GAP: there is no backfill. A case that already existed before this
// engine's first deployment never gets a RegisterClocks call — this only
// ever runs from case.created — so it simply never gets SLA tracking. A
// one-off script reading existing cases and calling this per case would
// close that, not built here.
func (e *Engine) RegisterClocks(ctx context.Context, caseID, priority string, createdAt time.Time, caseNumber, wso2CaseID, caseTitle, caseType, product, team string) {
	durations, ok := e.durations[strings.ToUpper(strings.TrimSpace(priority))]
	if !ok || len(durations) == 0 {
		slog.WarnContext(ctx, "slaengine: no duration policy for this severity, sla clocks not registered", "caseId", caseID, "priority", priority)
		return
	}

	for _, clockType := range clockTypesInOrder {
		duration, ok := durations[clockType]
		if !ok {
			continue
		}
		dueAt := createdAt.Add(duration)
		if clockType == ClockResolution && strings.EqualFold(priority, "MEDIUM") {
			dueAt = avoidWeekend(dueAt)
		}
		actualDuration := dueAt.Sub(createdAt)

		meta := ClockMeta{
			CaseNumber: caseNumber,
			WSO2CaseID: wso2CaseID,
			CaseTitle:  caseTitle,
			CaseType:   caseType,
			Product:    product,
			Team:       team,
			Priority:   priority,
			StartedAt:  createdAt,
		}
		if err := e.store.SetClock(ctx, caseID, clockType, meta); err != nil {
			slog.ErrorContext(ctx, "slaengine: failed to register sla clock", "caseId", caseID, "clockType", clockType, "err", err)
			continue
		}
		for _, tier := range tierSequence {
			at := tierTime(createdAt, actualDuration, tier)
			if err := e.store.AddWake(ctx, wakeMember(caseID, clockType, tier), at); err != nil {
				slog.ErrorContext(ctx, "slaengine: failed to schedule sla wake entry", "caseId", caseID, "clockType", clockType, "tier", tier, "err", err)
			}
		}
		slog.InfoContext(ctx, "slaengine: registered sla clock", "caseId", caseID, "clockType", clockType, "dueAt", dueAt.Format(time.RFC3339))
	}
}

// caseStatusEffect is what ApplyStateEffects does to the workaround/
// resolution clocks for one case.status_changed NewStatus value — matched
// case-insensitively against entity-service's own caseStateDisplayLabel
// vocabulary ("Work In Progress", "Awaiting Info", "Solution Proposed",
// "Closed", ...), the human label every case.* publisher sends on this
// field (see entity-service's own CLAUDE.md, "Fixing case enum-casing").
type caseStatusEffect int

const (
	effectResume caseStatusEffect = iota
	effectPause
	effectClose
)

func statusEffectFor(newStatus string) caseStatusEffect {
	switch strings.ToLower(strings.TrimSpace(newStatus)) {
	case "awaiting info", "solution proposed":
		return effectPause
	case "closed":
		return effectClose
	default:
		return effectResume
	}
}

// ApplyStateEffects applies this case's new status to its workaround/
// resolution clocks — called from dispatch.handleStatusChanged alongside
// its existing email reaction, unconditionally (every case.status_changed,
// not just a genuine transition — entity-service's own publishers already
// gate on a real change, and every effect here is idempotent, so a resent
// status change just harmlessly re-applies the same effect):
//
//   - "Awaiting Info" / "Solution Proposed": WSO2 is waiting on the
//     customer, so neither clock should keep accumulating — pause both.
//   - "Closed": resolution is genuinely done — force-complete it
//     (AdvanceAlertedTier to 100, same as an early completion). workaround
//     is only paused, not completed: there is no real "workaround
//     provided" signal available here (see case.comment_added's own
//     payload, which carries no such field either) — a documented,
//     accepted gap carried forward from every earlier design this
//     replaces, not newly introduced.
//   - anything else (Open, Work In Progress, Waiting on WSO2, Reopened):
//     resume both — the case is active again.
//
// The response clock is never touched here — it's only ever completed by
// CompleteResponseClock, on a qualifying comment.
func (e *Engine) ApplyStateEffects(ctx context.Context, caseID, newStatus string) {
	if err := e.store.SetState(ctx, caseID, ClockResponse, newStatus); err != nil {
		slog.ErrorContext(ctx, "slaengine: failed to update clock state", "caseId", caseID, "clockType", ClockResponse, "err", err)
	}
	if err := e.store.SetState(ctx, caseID, ClockWorkaround, newStatus); err != nil {
		slog.ErrorContext(ctx, "slaengine: failed to update clock state", "caseId", caseID, "clockType", ClockWorkaround, "err", err)
	}
	if err := e.store.SetState(ctx, caseID, ClockResolution, newStatus); err != nil {
		slog.ErrorContext(ctx, "slaengine: failed to update clock state", "caseId", caseID, "clockType", ClockResolution, "err", err)
	}

	switch statusEffectFor(newStatus) {
	case effectPause:
		e.setPaused(ctx, caseID, ClockWorkaround, true)
		e.setPaused(ctx, caseID, ClockResolution, true)
	case effectClose:
		if err := e.store.AdvanceAlertedTier(ctx, caseID, ClockResolution, 100); err != nil {
			slog.ErrorContext(ctx, "slaengine: failed to complete resolution clock on case close", "caseId", caseID, "err", err)
		}
		e.setPaused(ctx, caseID, ClockWorkaround, true)
	default:
		e.setPaused(ctx, caseID, ClockWorkaround, false)
		e.setPaused(ctx, caseID, ClockResolution, false)
	}
}

func (e *Engine) setPaused(ctx context.Context, caseID, clockType string, paused bool) {
	if err := e.store.SetPaused(ctx, caseID, clockType, paused); err != nil {
		slog.ErrorContext(ctx, "slaengine: failed to update clock paused state", "caseId", caseID, "clockType", clockType, "paused", paused, "err", err)
	}
}

// CompleteResponseClock force-completes the response clock — called from
// dispatch.handleCommentAdded when events.CommentAddedPayload.IsSupportEngineerResponse
// is true (entity-service has already confirmed the comment is a qualifying
// support-engineer reply; this engine does no role/identity resolution of
// its own). Idempotent: AdvanceAlertedTier never moves the cursor backward,
// so a redelivered comment-added event is harmless.
func (e *Engine) CompleteResponseClock(ctx context.Context, caseID string) {
	if err := e.store.AdvanceAlertedTier(ctx, caseID, ClockResponse, 100); err != nil {
		slog.ErrorContext(ctx, "slaengine: failed to complete response clock", "caseId", caseID, "err", err)
	}
}

// Tick scans the Redis wake-index for every member due at or before now and
// processes each — a failed member doesn't stop the others; every error is
// joined and returned so RunTicker can log the whole batch's outcome in one
// line.
func (e *Engine) Tick(ctx context.Context, now time.Time) error {
	members, err := e.store.DueMembers(ctx, now)
	if err != nil {
		return fmt.Errorf("slaengine: scan due members: %w", err)
	}

	var errs []error
	for _, member := range members {
		if err := e.processDueMember(ctx, member); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", member, err))
		}
	}
	return errors.Join(errs...)
}

// processDueMember handles one due wake-index member. A member is removed
// from the index exactly once, regardless of outcome — alerted, already
// handled (paused, or completed early by CompleteResponseClock/
// ApplyStateEffects), or malformed — since a member whose due time has
// passed stays "due" forever once reached; leaving it in place would have
// Tick re-examine it on every future poll indefinitely.
//
// KNOWN GAP, carried forward from every earlier design this replaces: a
// paused clock's own due member is dropped outright, not rescheduled for
// after resume — a clock that comes due while paused permanently loses
// that tier's alert, even once resumed. Worth revisiting separately if it
// turns out to matter in practice.
func (e *Engine) processDueMember(ctx context.Context, member string) error {
	caseID, clockType, tier, ok := parseWakeMember(member)
	if !ok {
		slog.ErrorContext(ctx, "slaengine: malformed wake member, dropping", "member", member)
		return e.store.RemoveWake(ctx, member)
	}

	meta, found, err := e.store.GetClock(ctx, caseID, clockType)
	if err != nil {
		return fmt.Errorf("get clock %s/%s: %w", caseID, clockType, err)
	}
	if !found {
		return e.store.RemoveWake(ctx, member)
	}
	if meta.Paused || meta.AlertedTier >= tier {
		return e.store.RemoveWake(ctx, member)
	}

	claimed, err := e.store.ClaimTier(ctx, caseID, clockType, tier)
	if err != nil {
		return fmt.Errorf("claim tier %d for %s/%s: %w", tier, caseID, clockType, err)
	}
	if !claimed {
		return e.store.RemoveWake(ctx, member)
	}

	if err := e.alertTier(ctx, meta, caseID, clockType, tier); err != nil {
		if releaseErr := e.store.ReleaseTier(ctx, caseID, clockType, tier); releaseErr != nil {
			slog.ErrorContext(ctx, "slaengine: failed to release tier claim after a failed publish, tier may be stuck until it expires", "caseId", caseID, "clockType", clockType, "tier", tier, "err", releaseErr)
		}
		return fmt.Errorf("alert tier %d for %s/%s: %w", tier, caseID, clockType, err)
	}
	if err := e.store.AdvanceAlertedTier(ctx, caseID, clockType, tier); err != nil {
		// Logged, not returned/retried: the alert (publish + Chat) has
		// already gone out, and the tier claim above already prevents a
		// future tick from re-alerting this exact tier regardless of
		// whether this cursor update lands — see ClaimTier's own doc
		// comment. Leaving the wake member in place here would only cause
		// it to be re-examined (and immediately re-dropped by the
		// claimed=false branch above) forever.
		slog.ErrorContext(ctx, "slaengine: failed to advance alerted-tier cursor after a successful alert", "caseId", caseID, "clockType", clockType, "tier", tier, "err", err)
	}
	if err := e.store.RemoveWake(ctx, member); err != nil {
		return fmt.Errorf("remove wake entry after alerting tier %d for %s/%s: %w", tier, caseID, clockType, err)
	}
	slog.InfoContext(ctx, "slaengine: sla tier reached", "caseId", caseID, "clockType", clockType, "tier", tier)
	return nil
}

// alertTier publishes events.TypeSLATierReached and sends the Google Chat
// breach card for one newly-crossed tier — only ever called after
// processDueMember has already won that tier's Redis claim, so this
// function itself needs no additional idempotency of its own. The Kafka
// publish failing is a real, retried error (processDueMember releases the
// claim and leaves the wake entry for the next tick); a Chat-send failure
// is logged only and never propagated — see sendBreachAlert's own doc
// comment for why, same explicit product decision the removed poll design
// already made for this exact split.
func (e *Engine) alertTier(ctx context.Context, meta ClockMeta, caseID, clockType string, tier int) error {
	envelope := events.Envelope{Type: events.TypeSLATierReached, EntityID: caseID}
	payload, err := json.Marshal(events.SLATierReachedPayload{CaseID: caseID, ClockType: clockType, Tier: tierLabel(tier)})
	if err != nil {
		return fmt.Errorf("encode sla.tier_reached payload: %w", err)
	}
	envelope.Payload = payload
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode sla.tier_reached envelope: %w", err)
	}
	if err := e.pub.Publish(ctx, []byte(caseID), body); err != nil {
		return fmt.Errorf("publish sla.tier_reached: %w", err)
	}

	e.sendBreachAlert(ctx, meta, caseID, clockType, tier)
	return nil
}

// sendBreachAlert builds and sends the Google Chat breach card for one tier
// crossing, once per resolved Chat audience — team/standing-audience
// routing (chataudience.Resolve), same as the design this replaces. Unlike
// that design, isEvaluationAccount/onboardingStatus are always
// false/"" here: case.created carries neither any more (see
// events.CaseCreatedPayload's own doc comment — a since-reverted feature
// briefly needed them and entity-service stopped populating them), so this
// alert routes on team alone, falling back to the fixed "Incident Monitor"
// audience — a known, accepted reduction from the removed poll design's
// own live entity-service lookup, not a bug. teamLeadName is always sent
// "" for the same reason: no event this engine consumes carries it, and
// there is no live entity-service lookup left to resolve it from.
//
// A failed send to one audience is logged (naming that Chat space) and NOT
// retried — same explicit product decision, and same reasoning, as the
// removed poll design's own sendBreachAlert: retrying here would mean
// re-claiming the tier (processDueMember's own ClaimTier is the only thing
// stopping a retry), and a single persistently broken Chat space would
// otherwise turn into an unbounded stream of duplicate alerts to every
// OTHER space that worked fine, repeating every tick, forever.
func (e *Engine) sendBreachAlert(ctx context.Context, meta ClockMeta, caseID, clockType string, tier int) {
	caseNumber := meta.CaseNumber
	if caseNumber == "" {
		caseNumber = caseID
	}
	var openedAt string
	if !meta.StartedAt.IsZero() {
		openedAt = meta.StartedAt.UTC().Format("2006-01-02 15:04:05") + " (UTC)"
	}

	audiences := chataudience.Resolve(meta.Team, false, "", time.Now(), e.chat.HasAudienceSpace)
	caseLink := e.links.CSMLink(caseID)
	for _, audience := range audiences {
		if err := e.chat.SendSLABreachAlert(ctx, audience, clockType, tierLabel(tier), caseNumber, meta.WSO2CaseID, meta.CaseTitle, meta.CaseType, meta.Product, meta.Team, "", meta.Priority, meta.State, openedAt, caseLink); err != nil {
			slog.ErrorContext(ctx, "slaengine: failed to send sla breach alert to chat space, not retrying", "caseId", caseID, "clockType", clockType, "tier", tier, "chatSpace", audience, "err", err)
		}
	}
}

// RunTicker calls Tick every interval until ctx is done. Run from its own
// goroutine (see cmd/server/main.go); a failed Tick is logged, not fatal —
// the next tick gets another chance at whatever was due.
func (e *Engine) RunTicker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Tick(ctx, time.Now()); err != nil {
				slog.ErrorContext(ctx, "slaengine: tick failed", "err", err)
			}
		}
	}
}
