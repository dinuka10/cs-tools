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

package paging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/eventbus"
	"github.com/wso2-open-operations/cs-tools/integrations/csm-notification-service/internal/events"
)

// ScheduleCatalogue answers with the SRE windows as migration 0153 seeds
// them, plus one CRE window, whatever the stub holds otherwise. The windows
// are reference data rather than something a test varies.
func (s *stubScheduleReader) ScheduleCatalogue(context.Context) (scheduleCatalogue, error) {
	var cat scheduleCatalogue
	for _, t := range []struct{ key, family string }{
		{"apollo", "SRE"}, {"artemis", "SRE"}, {"atlas", "CRE"},
	} {
		cat.Teams = append(cat.Teams, struct {
			Key    string `json:"key"`
			Name   string `json:"name"`
			Family string `json:"family"`
		}{t.key, t.key, t.family})
	}
	str := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	for _, sh := range []struct{ code, family, zone, tier string }{
		{"SRE_TZ1_L1", "SRE", "TZ1", "L1"},
		{"SRE_TZ1", "SRE", "TZ1", ""},
		{"SRE_TZ2_L1", "SRE", "TZ2", "L1"},
		{"SRE_TZ2", "SRE", "TZ2", ""},
		{"SRE_TZ3", "SRE", "TZ3", ""},
		{"CRE_MORNING", "CRE", "", ""},
	} {
		cat.Shifts = append(cat.Shifts, struct {
			Code     string  `json:"code"`
			Family   string  `json:"family"`
			ZoneCode *string `json:"zoneCode,omitempty"`
			Tier     *string `json:"tier,omitempty"`
		}{sh.code, sh.family, str(sh.zone), str(sh.tier)})
	}
	return cat, nil
}

// sreTeams is the SRE half of the configuration: the two SRE-ABTs.
var sreTeams = TeamKeys{
	ABTs:       []string{"castor", "draco", "vega", "sirius", "atlas", "phoenix", "rigel"},
	Leadership: "cre-leadership",
	SRE:        []string{"apollo", "artemis"},
	Aliases:    map[string]string{"SRE - Apollo": "apollo"},
}

func sreResolver(s *stubScheduleReader) TeamScheduleResolver {
	return NewTeamScheduleResolver(s, sreTeams, nil)
}

// held is one on-duty turn. tier "" takes the window's own.
func held(user, team, shift, tier string) onDutyAssignment {
	a := onDutyFor(user, user+"@example.com", team)
	a.Engineer.Name = user
	a.ShiftCode = shift
	if tier != "" {
		a.Tier = &tier
	}
	return a
}

// morningRota is 10:00 on a weekday: TZ1 alone, both teams on it.
func morningRota() *stubScheduleReader {
	return &stubScheduleReader{
		onDuty: []onDutyAssignment{
			held("a-l1", "apollo", "SRE_TZ1_L1", ""),
			held("r-l1", "artemis", "SRE_TZ1_L1", ""),
			held("a-l2", "apollo", "SRE_TZ1", "L2"),
			held("r-l2", "artemis", "SRE_TZ1", "L2"),
			held("r-l3", "artemis", "SRE_TZ1", "L3"), // apollo has no L3 today
			held("c-l1", "atlas", "CRE_MORNING", "L1"),
		},
		members: []teamMember{
			member("apollo", "a-lead@example.com", roleLead, ""),
			member("artemis", "r-lead@example.com", roleLead, ""),
			// Atlas's alert-duty nominee and lead, so a CRE ladder on the
			// same rota has somebody to reach.
			member("atlas", "atlas-t1@example.com", "engineer", "T1"),
			member("atlas", "atlas-lead@example.com", roleLead, ""),
		},
	}
}

func resolveSRE(t *testing.T, r TeamScheduleResolver, level Level, team string) []string {
	t.Helper()
	got, err := r.Resolve(context.Background(), level, RoutingContext{AssignedCRETeam: team, Ladder: LadderSRE, At: testClock})
	if err != nil {
		t.Fatalf("resolve %s: %v", level, err)
	}
	return emails(got)
}

func TestSRETiming_DefaultsAndOverrides(t *testing.T) {
	def := SREPolicy(false)
	var at []time.Duration
	for _, a := range Schedule(def, true) {
		at = append(at, a.After)
	}
	if len(at) != 3 || at[0] != 0 || at[1] != 5*time.Minute || at[2] != 10*time.Minute {
		t.Fatalf("default SRE clock = %v, want [0 5m 10m]: L1 at once, 5 minutes between two calls", at)
	}
	if n := len(Schedule(SREPolicy(true), true)); n != 4 {
		t.Fatalf("with L4: %d calls, want 4", n)
	}
	custom := SRETiming{InitialWait: Duration(time.Minute), Interval: Duration(2 * time.Minute)}.Policy()
	last := Schedule(custom, true)[2].After
	if last != 5*time.Minute {
		t.Fatalf("1m wait + two 2m gaps: L3 at %v, want 5m", last)
	}
}

// No priority gate: a PLANNING incident has no CRE ladder, but an SRE one
// still gets its clock.
func TestPolicyFor_SREHasNoPriorityGate(t *testing.T) {
	if _, ok := PolicyFor(DefaultPolicy, Trigger{Priority: "PLANNING"}); ok {
		t.Fatal("a PLANNING CRE incident must still have no ladder")
	}
	p, ok := PolicyFor(DefaultPolicy, Trigger{Priority: "PLANNING", Routing: RoutingContext{Ladder: LadderSRE}})
	if !ok || p.Levels[Level1].NotificationInterval != 5*time.Minute {
		t.Fatalf("SRE PLANNING incident: ok=%v policy=%+v; want the SRE clock", ok, p)
	}
}

func TestLadderFor_ConfiguredTeamsAndAliases(t *testing.T) {
	r := sreResolver(&stubScheduleReader{})
	for team, want := range map[string]Ladder{
		"Apollo": LadderSRE, "SRE - Apollo": LadderSRE, "artemis": LadderSRE,
		"Atlas": LadderCRE, "": LadderCRE, "nobody-knows": LadderCRE,
	} {
		got, err := r.LadderFor(context.Background(), RoutingContext{AssignedCRETeam: team})
		if err != nil || got != want {
			t.Errorf("LadderFor(%q) = %q, %v; want %q", team, got, err, want)
		}
	}
}

// With no SRE list configured the catalogue's family still answers, so a
// deployment without the file classifies.
func TestLadderFor_FallsBackToTheCatalogue(t *testing.T) {
	r := NewTeamScheduleResolver(&stubScheduleReader{}, TeamKeys{}, nil)
	if got, _ := r.LadderFor(context.Background(), RoutingContext{AssignedCRETeam: "apollo"}); got != LadderSRE {
		t.Fatalf("apollo = %q, want SRE from the catalogue", got)
	}
	if got, _ := r.LadderFor(context.Background(), RoutingContext{AssignedCRETeam: "atlas"}); got != LadderCRE {
		t.Fatalf("atlas = %q, want CRE", got)
	}
}

func TestResolveSRE_OnePersonPerRungOwnTeamFirst(t *testing.T) {
	r := sreResolver(morningRota())
	for level, want := range map[Level]string{
		Level0: "a-l1@example.com",
		Level1: "a-l2@example.com",
		// apollo has nobody on L3: another SRE team's holder answers.
		Level2: "r-l3@example.com",
		Level3: "a-lead@example.com",
	} {
		got := resolveSRE(t, r, level, "Apollo")
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want exactly [%s]", level, got, want)
		}
	}
	if got := resolveSRE(t, r, Level4, "Apollo"); len(got) != 0 {
		t.Errorf("LEVEL_4 = %v; the SRE ladder has no fifth rung", got)
	}
}

// Weekdays 12:00-15:00 both zones' escalation windows are live. The SRE team
// confirmed one person per rung: the zone whose L1 block is live owns it.
func TestResolveSRE_OverlapCallsOnlyTheL1Zone(t *testing.T) {
	overlap := func(l1Shift string) *stubScheduleReader {
		return &stubScheduleReader{onDuty: []onDutyAssignment{
			held("a-l1", "apollo", l1Shift, ""),
			held("a-tz1-l2", "apollo", "SRE_TZ1", "L2"),
			held("a-tz2-l2", "apollo", "SRE_TZ2", "L2"),
		}}
	}
	if got := resolveSRE(t, sreResolver(overlap("SRE_TZ1_L1")), Level1, "apollo"); len(got) != 1 || got[0] != "a-tz1-l2@example.com" {
		t.Errorf("12:00-13:30 (TZ1 holds L1): L2 = %v, want only TZ1's", got)
	}
	if got := resolveSRE(t, sreResolver(overlap("SRE_TZ2_L1")), Level1, "apollo"); len(got) != 1 || got[0] != "a-tz2-l2@example.com" {
		t.Errorf("13:30-15:00 (TZ2 holds L1): L2 = %v, want only TZ2's", got)
	}
}

// A CRE P0 on the SRE ladder belongs to no SRE team: one person still, in the
// configured team order, and L4 is the lead of whoever took L1.
func TestResolveSRE_CREIncidentStillReachesOnePerson(t *testing.T) {
	r := sreResolver(morningRota())
	if got := resolveSRE(t, r, Level0, "Atlas"); len(got) != 1 || got[0] != "a-l1@example.com" {
		t.Errorf("L1 = %v, want apollo's (first in teams.sre)", got)
	}
	if got := resolveSRE(t, r, Level3, "Atlas"); len(got) != 1 || got[0] != "a-lead@example.com" {
		t.Errorf("L4 = %v, want the lead of the team that took L1", got)
	}
	if got := resolveSRE(t, r, Level0, "Atlas"); strings.Contains(strings.Join(got, ","), "c-l1") {
		t.Error("a CRE window must never answer an SRE rung")
	}
}

func TestResolveSRE_NobodyOnTheTierClimbs(t *testing.T) {
	got := resolveSRE(t, sreResolver(&stubScheduleReader{}), Level0, "apollo")
	if len(got) != 0 {
		t.Fatalf("got %v; want nobody and no error, so the ladder climbs", got)
	}
}

// handoverReader is a rota that changes hands at one instant: before it the
// stub's own holders are on duty, from it the after set.
type handoverReader struct {
	*stubScheduleReader
	handover time.Time
	after    []onDutyAssignment
}

func (h handoverReader) OnDutyAt(ctx context.Context, at time.Time) ([]onDutyAssignment, error) {
	if at.Before(h.handover) {
		return h.stubScheduleReader.OnDutyAt(ctx, at)
	}
	return h.after, nil
}

// An incident reported at 13:25 opens L2 at 13:30 and L3 at 13:35, both
// inside TZ2. Each rung must reach TZ2's holder, not whoever held the tier on
// TZ1 when the incident arrived -- and a tier TZ1 had nobody on must still
// find TZ2's holder rather than leave the rung empty.
func TestBuildPlan_SRERungsResolveWhenTheyOpen(t *testing.T) {
	reported := ist(2026, 10, 8, 13, 25)
	rota := handoverReader{
		stubScheduleReader: &stubScheduleReader{onDuty: []onDutyAssignment{
			held("tz1-l1", "apollo", "SRE_TZ1_L1", ""),
			held("tz1-l2", "apollo", "SRE_TZ1", "L2"),
			// TZ1 has no L3 today.
		}},
		handover: ist(2026, 10, 8, 13, 30),
		after: []onDutyAssignment{
			held("tz2-l1", "apollo", "SRE_TZ2_L1", ""),
			held("tz2-l2", "apollo", "SRE_TZ2", "L2"),
			held("tz2-l3", "apollo", "SRE_TZ2", "L3"),
		},
	}
	tr := Trigger{
		IncidentID: testIncidentID, Priority: "S0", Kind: TriggerNewIncident, At: reported,
		Routing: RoutingContext{AssignedCRETeam: "apollo", Ladder: LadderSRE, At: reported},
	}
	plan, err := BuildPlan(context.Background(), tr, DefaultPolicy, NewTeamScheduleResolver(rota, sreTeams, nil), ChannelLog)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		level Level
		after time.Duration
		email string
	}{
		{Level0, 0, "tz1-l1@example.com"},
		{Level1, 5 * time.Minute, "tz2-l2@example.com"},
		{Level2, 10 * time.Minute, "tz2-l3@example.com"},
	}
	if len(plan.Calls) != len(want) || len(plan.Issues) != 0 {
		t.Fatalf("calls=%+v issues=%+v; want %d calls and no issues", plan.Calls, plan.Issues, len(want))
	}
	for i, w := range want {
		c := plan.Calls[i]
		if c.Level != w.level || c.At.Sub(reported) != w.after || c.Recipient.Email != w.email {
			t.Errorf("call %d = %s +%s %s; want %s +%s %s",
				i, c.Level, c.At.Sub(reported), c.Recipient.Email, w.level, w.after, w.email)
		}
	}
}

// atRecorder notes the instant each level was resolved for.
type atRecorder map[Level]time.Time

func (r atRecorder) Resolve(_ context.Context, level Level, rc RoutingContext) ([]Recipient, error) {
	r[level] = rc.At
	return []Recipient{{Name: "x", Email: "x@example.com", Phone: "+10000000000"}}, nil
}

// The CRE ladder keeps resolving every rung at the report instant: its rungs
// are fixed by the shift the incident arrived in.
func TestBuildPlan_CRERungsResolveAtTheReportInstant(t *testing.T) {
	rec := atRecorder{}
	tr := Trigger{
		IncidentID: testIncidentID, Priority: "P1", Kind: TriggerNewIncident, At: testClock,
		Routing: RoutingContext{AssignedCRETeam: "atlas", At: testClock},
	}
	if _, err := BuildPlan(context.Background(), tr, DefaultPolicy, rec, ChannelLog); err != nil {
		t.Fatal(err)
	}
	if len(rec) < 2 {
		t.Fatalf("resolved %d levels; want a multi-rung CRE ladder", len(rec))
	}
	for level, at := range rec {
		if !at.Equal(testClock) {
			t.Errorf("CRE %s resolved at %s; want the report instant %s", level, at, testClock)
		}
	}
}

func TestResolveSRE_PhoneBookFillsNumbers(t *testing.T) {
	r := sreResolver(morningRota()).WithPhoneBook(PhoneBook{TestCallTo: "+94770000000"})
	got, err := r.Resolve(context.Background(), Level0, RoutingContext{AssignedCRETeam: "apollo", Ladder: LadderSRE, At: testClock})
	if err != nil || len(got) != 1 || got[0].Phone != "+94770000000" {
		t.Fatalf("got %+v, %v; want the phone book's number", got, err)
	}
}

func TestSRE_RolesAndInstruction(t *testing.T) {
	if got := Level1.RoleIn(LadderSRE); got != "L2 support" {
		t.Errorf("RoleIn = %q", got)
	}
	if got := Level1.RoleIn(LadderCRE); got != Level1.Role() {
		t.Errorf("CRE role changed: %q", got)
	}
	tr := Trigger{Kind: TriggerNewIncident, Routing: RoutingContext{Ladder: LadderSRE}}
	if !strings.Contains(tr.VoiceMessagePlain(), "Assign the incident to yourself") {
		t.Errorf("SRE voice message = %q", tr.VoiceMessagePlain())
	}
	if (RoutingContext{Ladder: LadderSRE}).Rule() != "SRE_TIERS" {
		t.Error("an SRE incident should report its own path")
	}
}

// --- the engines ------------------------------------------------------------

// ladderEngine is a chat-only engine of one kind over the morning rota. Chat
// needs no phone numbers, which is how the ladder is exercised without a
// telephony account.
func ladderEngine(kind Ladder, chat *fakeChat, store *memStore) *Engine {
	cfg := EngineConfig{CallSendingEnabled: true, Channel: ChannelChat, Kind: kind}
	policies := DefaultPolicy
	if kind == LadderSRE {
		policies = withSREPolicy(DefaultPolicy, cfg.Ladder.Timing.Policy())
	}
	return &Engine{
		policies:  policies,
		resolver:  sreResolver(morningRota()),
		notifiers: []notifier{chatNotifier{chat: chat, links: fakeLinks{}}},
		store:     store,
		notes:     &fakeNotes{},
		cfg:       cfg,
		clock:     func() time.Time { return testClock },
	}
}

func created(t *testing.T, team, priority string) eventbus.Record {
	t.Helper()
	return record(t, events.TypeIncidentCreated, events.IncidentCreatedPayload{
		Title: "Latency alert on gateway", ShortDescription: "p95 latency above threshold", Number: "INC0099001",
		Priority: priority, Team: team, ReportedAt: testClock.Format(time.RFC3339),
	})
}

func assigned(t *testing.T) eventbus.Record {
	return record(t, events.TypeIncidentAssigned, events.IncidentAssignedPayload{AssigneeID: "a-l2", AssigneeName: "a-l2"})
}

// The whole SRE flow over chat: L1 at once, L2 at +5m, L3 at +10m, one card
// each, then stopped by an assignee.
func TestEngine_SRELadderStopsWhenAssigned(t *testing.T) {
	ctx := context.Background()
	chat, store := &fakeChat{}, newMemStore()
	e := ladderEngine(LadderSRE, chat, store)

	if err := e.Handle(ctx, created(t, "Apollo", "HIGH")); err != nil {
		t.Fatal(err)
	}
	st, found, _ := store.Get(ctx, testIncidentID)
	if !found || st.Plan.Trigger.Routing.Ladder != LadderSRE || len(st.Plan.Calls) != 3 {
		t.Fatalf("found=%v ladder=%q calls=%d; want an SRE ladder of 3", found, st.Plan.Trigger.Routing.Ladder, len(st.Plan.Calls))
	}
	for i, want := range []string{"L1 support", "L2 support"} {
		if err := e.Tick(ctx, testClock.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if len(chat.posted) != i+1 || chat.posted[i].RungRole != want {
			t.Fatalf("after tick %d: %d cards; want %s", i, len(chat.posted), want)
		}
	}

	// A public comment is not an SRE acknowledgement.
	comment := record(t, events.TypeIncidentCommentAdded, events.IncidentCommentAddedPayload{CommentID: "c1", IsPublic: true})
	if err := e.Handle(ctx, comment); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, testIncidentID); !found {
		t.Fatal("a public comment stopped the SRE ladder; only an assignee should")
	}

	if err := e.Handle(ctx, assigned(t)); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(ctx, testClock.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 2 {
		t.Fatalf("%d cards; L3 must not be reached once someone is assigned", len(chat.posted))
	}
	if _, found, _ := store.Get(ctx, testIncidentID); found {
		t.Fatal("a stopped ladder's state should be cleared")
	}
}

// Any priority, even one the CRE table has no row for.
func TestEngine_SRELadderRunsAtEveryPriority(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	if err := ladderEngine(LadderSRE, &fakeChat{}, store).Handle(ctx, created(t, "Apollo", "PLANNING")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, testIncidentID); !found {
		t.Fatal("a PLANNING SRE incident got no ladder; the SRE ladder has no priority gate")
	}
}

// The ladders divide the incidents: an SRE team's incident is the SRE
// engine's alone, a CRE team's non-P0 incident the CRE engine's alone.
func TestEngines_DivideIncidentsByTeam(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		team, priority string
		cre, sre       bool
	}{
		{"Apollo", "HIGH", false, true},
		{"Atlas", "HIGH", true, false},
		{"Atlas", "P0", true, true},
		{"Atlas", "CATASTROPHIC", true, true},
		// An incident's highest priority: no incident is CATASTROPHIC.
		{"Atlas", "CRITICAL", true, true},
		{"Apollo", "CRITICAL", false, true},
	} {
		creStore, sreStore := newMemStore(), newMemStore()
		cre, sre := ladderEngine(LadderCRE, &fakeChat{}, creStore), ladderEngine(LadderSRE, &fakeChat{}, sreStore)
		for _, e := range []*Engine{cre, sre} {
			if err := e.Handle(ctx, created(t, tc.team, tc.priority)); err != nil {
				t.Fatal(err)
			}
		}
		_, gotCRE, _ := creStore.Get(ctx, testIncidentID)
		_, gotSRE, _ := sreStore.Get(ctx, testIncidentID)
		if gotCRE != tc.cre || gotSRE != tc.sre {
			t.Errorf("%s %s: CRE=%v SRE=%v; want CRE=%v SRE=%v", tc.team, tc.priority, gotCRE, gotSRE, tc.cre, tc.sre)
		}
	}
}

// A P0 CRE incident runs both ladders; an assignee stops the SRE one only.
func TestEngines_P0RunsBothAndAssignmentStopsOnlySRE(t *testing.T) {
	ctx := context.Background()
	creStore, sreStore := newMemStore(), newMemStore()
	cre, sre := ladderEngine(LadderCRE, &fakeChat{}, creStore), ladderEngine(LadderSRE, &fakeChat{}, sreStore)
	for _, e := range []*Engine{cre, sre} {
		if err := e.Handle(ctx, created(t, "Atlas", "P0")); err != nil {
			t.Fatal(err)
		}
	}
	st, _, _ := sreStore.Get(ctx, testIncidentID)
	if st.Plan.Trigger.Routing.Ladder != LadderSRE {
		t.Fatalf("the P0's SRE plan routes as %q", st.Plan.Trigger.Routing.Ladder)
	}
	for _, e := range []*Engine{cre, sre} {
		if err := e.Handle(ctx, assigned(t)); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, _ := sreStore.Get(ctx, testIncidentID); found {
		t.Error("the SRE ladder kept climbing after an assignee was set")
	}
	if st, found, _ := creStore.Get(ctx, testIncidentID); !found || st.Cancelled != nil {
		t.Error("an assignee stopped the CRE ladder; assignment is not a CRE acknowledgement")
	}
}

// An elevation brings a CRE incident onto the SRE ladder when it reaches P0,
// and never restarts an SRE team's ladder.
func TestEngine_SREElevations(t *testing.T) {
	ctx := context.Background()
	elevated := func(team, to string) eventbus.Record {
		return record(t, events.TypeIncidentPriorityElevated, events.IncidentPriorityElevatedPayload{
			Number: "INC0099001", Title: "Latency alert", OldPriority: "HIGH", NewPriority: to,
			Team: team, ElevatedAt: testClock.Format(time.RFC3339),
		})
	}

	store := newMemStore()
	e := ladderEngine(LadderSRE, &fakeChat{}, store)
	if err := e.Handle(ctx, elevated("Atlas", "P0")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, testIncidentID); !found {
		t.Fatal("a CRE incident elevated to P0 did not start the SRE ladder")
	}

	store = newMemStore()
	e = ladderEngine(LadderSRE, &fakeChat{}, store)
	if err := e.Handle(ctx, elevated("Apollo", "P1")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, testIncidentID); found {
		t.Fatal("an elevation started an SRE team's ladder; its clock does not depend on priority")
	}
}

// --- configuration -----------------------------------------------------------

func loadYAML(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "escalation.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}

func TestConfig_SRESection(t *testing.T) {
	cfg, err := loadYAML(t, `
enabled: true
sre:
  enabled: true
  channel: chat
  timing:
    interval: 2m
    includeL4: true
  teams:
    abtType: sre-abt
    abts: [apollo, artemis]
    aliases: {"SRE - Apollo": apollo}
`)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.SRE.Timing.Policy()
	if p.Levels[Level1].NotificationInterval != 2*time.Minute || p.Levels[Level3].NotificationCount != 1 {
		t.Fatalf("timing = %+v; want 2m rungs with L4", p)
	}
}

// --- routing: which ladders an incident climbs is configuration -------------

func TestRouting_DefaultRules(t *testing.T) {
	var r Routing // zero value: DefaultRouting
	for _, tc := range []struct {
		in       RouteInput
		cre, sre bool
	}{
		{RouteInput{Team: "sre", Priority: "HIGH"}, false, true},                         // sheet "Yes" rows
		{RouteInput{Team: "cre", Priority: "HIGH"}, true, false},                         // a CRE incident
		{RouteInput{Team: "cre", Priority: "P0"}, true, true},                            // CRE P0: both
		{RouteInput{Team: "cre", Priority: "CATASTROPHIC"}, true, true},                  // label = code
		{RouteInput{Team: "none", Priority: "P0"}, true, false},                          // no team, no monitoring
		{RouteInput{Team: "none", ContactType: "AZURE", Priority: "LOW"}, true, true},    // sheet "No" rows
		{RouteInput{Team: "cre", ContactType: "SITE_24_7", Priority: "LOW"}, true, true}, // monitoring on a CRE team: both
		{RouteInput{Team: "none", ContactType: "EMAIL"}, true, false},                    // a person raised it
	} {
		_, cre := r.Match(tc.in, LadderKeyCRE)
		_, sre := r.Match(tc.in, LadderKeySRE)
		if cre != tc.cre || sre != tc.sre {
			t.Errorf("%+v: cre=%v sre=%v; want cre=%v sre=%v", tc.in, cre, sre, tc.cre, tc.sre)
		}
	}
}

func TestRouting_FromTheFileReplacesTheDefaults(t *testing.T) {
	cfg, err := loadYAML(t, `
routing:
  rules:
    - name: monitoring-only
      when: { contactType: [SENTINEL] }
      ladders: [sre]
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Routing.Match(RouteInput{Team: "sre"}, LadderKeySRE); ok {
		t.Fatal("an SRE team matched; the file's rules should replace the defaults entirely")
	}
	rule, ok := cfg.Routing.Match(RouteInput{Team: "none", ContactType: "sentinel"}, LadderKeySRE)
	if !ok || rule.Name != "monitoring-only" || !rule.AdmitsNoTeam() {
		t.Fatalf("rule = %+v, %v; want monitoring-only, admitting no team", rule, ok)
	}
}

func TestRouting_InvalidRulesAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no name":         "routing:\n  rules:\n    - ladders: [sre]\n",
		"no ladders":      "routing:\n  rules:\n    - name: x\n",
		"unknown ladder":  "routing:\n  rules:\n    - name: x\n      ladders: [ops]\n",
		"unknown team":    "routing:\n  rules:\n    - name: x\n      when: { team: [ops] }\n      ladders: [sre]\n",
		"duplicate name":  "routing:\n  rules:\n    - name: x\n      ladders: [sre]\n    - name: x\n      ladders: [cre]\n",
		"misspelled when": "routing:\n  rules:\n    - name: x\n      when: { contacttype: [AZURE] }\n      ladders: [sre]\n",
	} {
		if _, err := loadYAML(t, body); err == nil {
			t.Errorf("%s: loaded; want an error", name)
		}
	}
}

// The engines follow the routing rules: a monitoring-raised incident with no
// team climbs the SRE ladder, past sre.trigger.requireKnownTeam.
func TestEngines_MonitoringRaisedIncidentClimbsSRE(t *testing.T) {
	ctx := context.Background()
	mk := func(team, priority, contact string) eventbus.Record {
		return record(t, events.TypeIncidentCreated, events.IncidentCreatedPayload{
			Title: "CPU alert", ShortDescription: "from monitoring", Number: "INC0099002",
			Priority: priority, Team: team, ContactType: contact, ReportedAt: testClock.Format(time.RFC3339),
		})
	}
	for _, tc := range []struct {
		name, team, priority, contact string
		cre, sre                      bool
	}{
		{"no team, AZURE", "", "LOW", "AZURE", false, true},
		{"CRE team, SITE_247", "Atlas", "LOW", "SITE_247", true, true},
		{"SRE team, SENTINEL", "Apollo", "HIGH", "SENTINEL", false, true},
		{"no team, EMAIL", "", "LOW", "EMAIL", false, false},
	} {
		creStore, sreStore := newMemStore(), newMemStore()
		cre, sre := ladderEngine(LadderCRE, &fakeChat{}, creStore), ladderEngine(LadderSRE, &fakeChat{}, sreStore)
		// requireKnownTeam on both, as the local file sets it.
		cre.cfg.Ladder.Start.RequireKnownTeam = true
		sre.cfg.Ladder.Start.RequireKnownTeam = true
		for _, e := range []*Engine{cre, sre} {
			if err := e.Handle(ctx, mk(tc.team, tc.priority, tc.contact)); err != nil {
				t.Fatal(err)
			}
		}
		_, gotCRE, _ := creStore.Get(ctx, testIncidentID)
		st, gotSRE, _ := sreStore.Get(ctx, testIncidentID)
		if gotCRE != tc.cre || gotSRE != tc.sre {
			t.Errorf("%s: CRE=%v SRE=%v; want CRE=%v SRE=%v", tc.name, gotCRE, gotSRE, tc.cre, tc.sre)
			continue
		}
		if gotSRE && tc.team == "" && st.Plan.Calls[0].Recipient.Email != "a-l1@example.com" {
			t.Errorf("%s: L1 = %s; want the zone's L1, Apollo first", tc.name, st.Plan.Calls[0].Recipient.Email)
		}
	}
}

func TestConfig_RefusesMisplacedAndOverlappingSettings(t *testing.T) {
	for name, body := range map[string]string{
		"SRE timing under cre": "cre:\n  timing:\n    interval: 5m\n",
		"CRE rules under sre":  "sre:\n  rules:\n    - id: R1\n      shift: LK\n      assignedToABT: any\n      levels: [rota_members]\n",
		"bad duration":         "sre:\n  timing:\n    interval: five\n",
		"team in both lists":   "cre:\n  teams:\n    abts: [apollo]\nsre:\n  teams:\n    abts: [apollo]\n",
		"old crePriorities":    "sre:\n  trigger:\n    crePriorities: [P0]\n",
	} {
		if _, err := loadYAML(t, body); err == nil {
			t.Errorf("%s: loaded; want an error", name)
		}
	}
}

// The shipped local configuration must load: an invalid one disables both
// ladders on the stack everybody tests against.
func TestConfig_LocalComposeFileLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "..", "scripts", "csm-compose", "escalation.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SRE.Teams.ABTs) != 2 || cfg.SRE.Timing.Policy().Levels[Level1].NotificationInterval != 5*time.Minute {
		t.Fatalf("sre section = %+v", cfg.SRE)
	}
}

// The log channel names each rung the way its own ladder does: an SRE rung is
// "L2 support", not the CRE ladder's name for LEVEL_1.
func TestLogNotifier_NamesTheRungByItsLadder(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	plan := Plan{Trigger: Trigger{Routing: RoutingContext{Ladder: LadderSRE}}}
	if _, err := (logNotifier{}).Deliver(context.Background(), plan, PlannedCall{Level: Level1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `role="L2 support"`) {
		t.Fatalf("log line = %s; want role=\"L2 support\"", buf.String())
	}
}
