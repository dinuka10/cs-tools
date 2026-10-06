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
	"sort"
	"strings"
	"time"
)

// The SRE half of TeamScheduleResolver. Kept apart from the CRE half because
// the two share nothing but the rota client: the CRE ladder routes by a rule
// table of shifts and ranks, the SRE ladder by on-call tier.
//
//	LEVEL_0  L1 support   whoever holds tier L1 at the trigger instant
//	LEVEL_1  L2 support   tier L2
//	LEVEL_2  L3 support   tier L3
//	LEVEL_3  L4 support   OPT-IN (timing.includeL4) and NOT CONFIRMED: the
//	                      lead of the SRE team answering the incident
//	LEVEL_4  nobody       the SRE ladder has no fifth rung
//
// Every rung reaches ONE person. Weekdays 12:00-15:00 IST the TZ1 and TZ2
// escalation windows are both live, so a tier can have two holders at once;
// the SRE team confirmed only one of them is called.

// familySRE is how the catalogue spells the SRE family, for teams and windows
// alike (entity-service folds the registry's SRE-ABT into it).
const familySRE = "SRE"

// keyOf turns an assignment group into a rota team key, honouring a
// configured alias first.
func (r TeamScheduleResolver) keyOf(team string) string {
	key := teamKeyFor(team)
	if alias, ok := r.aliases[key]; ok {
		return alias
	}
	return key
}

func (r TeamScheduleResolver) isSRETeam(key string) bool {
	for _, k := range r.sreTeamKeys {
		if k == key {
			return true
		}
	}
	return false
}

// LadderFor implements LadderClassifier: an incident climbs the SRE ladder
// when the team it is assigned to is an SRE team, and the CRE one otherwise --
// including when it has no team, or one the rota does not know.
//
// The configured SRE team list answers first, the same way the configured
// ABT list answers the CRE table's "is this an ABT" column. Only with no list
// configured is the catalogue asked, so a deployment without the file still
// classifies.
func (r TeamScheduleResolver) LadderFor(ctx context.Context, rc RoutingContext) (Ladder, error) {
	key := r.keyOf(rc.AssignedCRETeam)
	if key == "" {
		return LadderCRE, nil
	}
	if len(r.sreTeamKeys) > 0 {
		if r.isSRETeam(key) {
			return LadderSRE, nil
		}
		return LadderCRE, nil
	}
	cat, err := r.entity.ScheduleCatalogue(ctx)
	if err != nil {
		return LadderCRE, err
	}
	for _, t := range cat.Teams {
		if strings.EqualFold(t.Key, key) {
			if strings.EqualFold(t.Family, familySRE) {
				return LadderSRE, nil
			}
			return LadderCRE, nil
		}
	}
	return LadderCRE, nil
}

// TeamFamily implements TeamFamilyResolver: which family the incident's
// assignment group belongs to, for routing.
//
//	sre   an SRE team -- the configured sre.teams.abts, or the catalogue's SRE
//	      family when none are configured
//	cre   any other team the rota knows
//	none  no assignment group, or one neither the configuration nor the rota
//	      knows
func (r TeamScheduleResolver) TeamFamily(ctx context.Context, rc RoutingContext) (string, error) {
	key := r.keyOf(rc.AssignedCRETeam)
	if key == "" {
		return TeamFamilyNone, nil
	}
	if r.isSRETeam(key) {
		return TeamFamilySRE, nil
	}
	if contains(r.abtTeamKeys, key) || key == r.americasTeamKey {
		return TeamFamilyCRE, nil
	}
	cat, err := r.entity.ScheduleCatalogue(ctx)
	if err != nil {
		return TeamFamilyNone, err
	}
	for _, t := range cat.Teams {
		if strings.EqualFold(t.Key, key) {
			if strings.EqualFold(t.Family, familySRE) {
				return TeamFamilySRE, nil
			}
			return TeamFamilyCRE, nil
		}
	}
	return TeamFamilyNone, nil
}

// sreTier is the rota tier each SRE rung reads.
var sreTier = map[Level]string{Level0: "L1", Level1: "L2", Level2: "L3"}

// resolveSRE answers one SRE rung.
func (r TeamScheduleResolver) resolveSRE(ctx context.Context, level Level, rc RoutingContext) ([]Recipient, error) {
	own := r.keyOf(rc.AssignedCRETeam)
	if !r.isSRETeam(own) && len(r.sreTeamKeys) > 0 {
		// A CRE incident climbing the SRE ladder (a P0) belongs to no SRE
		// team; every SRE team is then equally placed to answer.
		own = ""
	}

	if tier, ok := sreTier[level]; ok {
		pick, found, err := r.onCallTier(ctx, rc.At, own, tier)
		if err != nil || !found {
			return nil, err
		}
		return []Recipient{pick.Recipient}, nil
	}
	if level != Level3 {
		return nil, nil
	}

	// L4 is the lead of the team answering the incident: its own, or -- for
	// an incident that has none -- the team of whoever took L1.
	team := own
	if team == "" {
		pick, found, err := r.onCallTier(ctx, rc.At, "", "L1")
		if err != nil || !found {
			return nil, err
		}
		team = pick.team
	}
	if team == "" {
		return nil, nil
	}
	leads, err := r.leadsOf(ctx, []string{team})
	if err != nil || len(leads) == 0 {
		return nil, err
	}
	return leads[:1], nil
}

// tierHolder is one candidate for an SRE rung.
type tierHolder struct {
	Recipient
	team string
	zone string
}

// onCallTier picks the ONE person holding tier on an SRE window at that
// instant.
//
// In order: the incident's own team, if it has anybody on that tier (a team's
// own engineer is never skipped for another team's); then the zone whose L1
// block is live, which is the zone that owns the incident right now and is
// what makes the 12:00-15:00 overlap call one person rather than two; then the
// configured SRE team order; then email, so a retry reaches the same person.
func (r TeamScheduleResolver) onCallTier(ctx context.Context, at time.Time, ownTeam, tier string) (tierHolder, bool, error) {
	cat, err := r.entity.ScheduleCatalogue(ctx)
	if err != nil {
		return tierHolder{}, false, err
	}
	type window struct{ zone, tier string }
	sre := map[string]window{}
	for _, s := range cat.Shifts {
		if strings.EqualFold(s.Family, familySRE) {
			sre[s.Code] = window{zone: deref(s.ZoneCode), tier: deref(s.Tier)}
		}
	}
	onDuty, err := r.entity.OnDutyAt(ctx, at)
	if err != nil {
		return tierHolder{}, false, err
	}

	var holders []tierHolder
	l1Zone := ""
	for _, a := range onDuty {
		w, ok := sre[a.ShiftCode]
		if !ok || a.Engineer.UserID == "" {
			continue
		}
		held := deref(a.Tier)
		if held == "" {
			held = w.tier
		}
		zone := deref(a.ZoneCode)
		if zone == "" {
			zone = w.zone
		}
		if strings.EqualFold(held, "L1") && (l1Zone == "" || zone < l1Zone) {
			l1Zone = zone
		}
		if !strings.EqualFold(held, tier) {
			continue
		}
		holders = append(holders, tierHolder{
			Recipient: Recipient{Email: a.Engineer.Email, Name: a.Engineer.Name, ShiftCode: a.ShiftCode},
			team:      r.keyOf(a.TeamKey),
			zone:      zone,
		})
	}
	if len(holders) == 0 {
		return tierHolder{}, false, nil
	}

	if ownTeam != "" {
		var mine []tierHolder
		for _, h := range holders {
			if h.team == ownTeam {
				mine = append(mine, h)
			}
		}
		if len(mine) > 0 {
			holders = mine
		}
	}

	order := func(team string) int {
		for i, k := range r.sreTeamKeys {
			if k == team {
				return i
			}
		}
		return len(r.sreTeamKeys)
	}
	sort.SliceStable(holders, func(i, j int) bool {
		a, b := holders[i], holders[j]
		if (a.zone == l1Zone) != (b.zone == l1Zone) {
			return a.zone == l1Zone
		}
		if order(a.team) != order(b.team) {
			return order(a.team) < order(b.team)
		}
		return a.Email < b.Email
	})
	return holders[0], true, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
