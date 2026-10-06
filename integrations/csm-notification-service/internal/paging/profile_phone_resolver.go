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
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PhoneLookup returns the mobile number a person set on their own CSM Portal
// profile, by email. scim.Client.MobileNumber is the real one: the portal
// stores the number on the person's Asgardeo user, so that is where it is
// read from. "" with a nil error means nobody set one.
type PhoneLookup interface {
	MobileNumber(ctx context.Context, email string) (string, error)
}

// e164 is the format a call can be placed to, and the same rule the CSM Portal
// profile form enforces (UserProfileModal's E164).
var e164 = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)

const (
	// profilePhoneTimeout bounds one lookup. The whole plan is built when the
	// incident arrives, so a slow directory delays every call of it; a person
	// whose number does not come back in time is reported NO_NUMBER instead.
	profilePhoneTimeout = 3 * time.Second
	// profilePhoneTTL is how long an answer is reused. A plan resolves its
	// tiers one after another and the same lead often sits on two of them,
	// so this is what makes it one lookup per person per incident; it is
	// short so a number someone just added is picked up by the next one.
	profilePhoneTTL = 2 * time.Minute
	// profilePhoneFailTTL keeps a failing directory from costing a timeout
	// per tier: one failed lookup answers "no number" for this long.
	profilePhoneFailTTL = 30 * time.Second
)

type profilePhone struct {
	number  string
	expires time.Time
}

// ProfilePhoneResolver fills in the phone number of every recipient the
// wrapped resolver returns without one, from that person's own CSM Portal
// profile.
//
// The Team Schedule says who is on duty and who holds which rank, but it holds
// no phone numbers, and a call-channel plan drops anyone without one
// (NO_NUMBER). People keep their own number current in the portal, so the
// ladder reads it from there rather than keeping a second copy that goes
// stale. A number already on the recipient -- one named in escalation.yaml,
// such as a head -- is an explicit override and is never replaced.
//
// It never fails a rung: a lookup that errors, times out or returns something
// that is not E.164 leaves that one person without a number, which the plan
// reports, and everyone else is still called.
type ProfilePhoneResolver struct {
	inner  Resolver
	lookup PhoneLookup
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]profilePhone
}

// NewProfilePhoneResolver wraps inner so its recipients carry their profile
// phone numbers.
func NewProfilePhoneResolver(inner Resolver, lookup PhoneLookup) *ProfilePhoneResolver {
	return &ProfilePhoneResolver{inner: inner, lookup: lookup, now: time.Now,
		cache: map[string]profilePhone{}}
}

// Resolve implements Resolver.
func (p *ProfilePhoneResolver) Resolve(ctx context.Context, level Level, rc RoutingContext) ([]Recipient, error) {
	recipients, err := p.inner.Resolve(ctx, level, rc)
	if err != nil || len(recipients) == 0 {
		return recipients, err
	}
	out := make([]Recipient, len(recipients))
	copy(out, recipients)
	for i := range out {
		if strings.TrimSpace(out[i].Phone) != "" || strings.TrimSpace(out[i].Email) == "" {
			continue
		}
		out[i].Phone = p.number(ctx, out[i].Email, out[i].Name)
	}
	return out, nil
}

// RuleFor passes the wrapped resolver's rule lookup through. BuildPlan asks
// its resolver for the matched rule (R1..R6) to stamp it and to decide whether
// LEVEL_0 exists; a wrapper that hid it would silently fall back to the
// pre-rule-table behaviour for every incident the profile lookup is on for.
func (p *ProfilePhoneResolver) RuleFor(rc RoutingContext) (Rule, bool) {
	if n, ok := p.inner.(ruleNamer); ok {
		return n.RuleFor(rc)
	}
	return Rule{}, false
}

// TeamFamily and LadderFor pass the wrapped resolver's routing answers through,
// for the same reason as RuleFor: the engine finds them by type assertion, and a
// wrapper that hid them would route every SRE team's incident as CRE whenever the
// profile lookup is on.
func (p *ProfilePhoneResolver) TeamFamily(ctx context.Context, rc RoutingContext) (string, error) {
	if r, ok := p.inner.(TeamFamilyResolver); ok {
		return r.TeamFamily(ctx, rc)
	}
	// The engine's own fallback for a resolver that cannot say (Engine.teamFamily).
	if ladder, _ := p.LadderFor(ctx, rc); ladder == LadderSRE {
		return TeamFamilySRE, nil
	}
	if strings.TrimSpace(rc.AssignedCRETeam) == "" {
		return TeamFamilyNone, nil
	}
	return TeamFamilyCRE, nil
}

// LadderFor implements LadderClassifier; see TeamFamily.
func (p *ProfilePhoneResolver) LadderFor(ctx context.Context, rc RoutingContext) (Ladder, error) {
	if c, ok := p.inner.(LadderClassifier); ok {
		return c.LadderFor(ctx, rc)
	}
	return LadderCRE, nil
}

// LeadPool implements LeadPoolResolver when the wrapped resolver does, with
// the pool's numbers filled in the same way a rung's are.
func (p *ProfilePhoneResolver) LeadPool(ctx context.Context) ([]Recipient, error) {
	inner, ok := p.inner.(LeadPoolResolver)
	if !ok {
		return nil, errNoLeadPool
	}
	pool, err := inner.LeadPool(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Recipient, len(pool))
	copy(out, pool)
	for i := range out {
		if strings.TrimSpace(out[i].Phone) == "" && strings.TrimSpace(out[i].Email) != "" {
			out[i].Phone = p.number(ctx, out[i].Email, out[i].Name)
		}
	}
	return out, nil
}

// errNoLeadPool: the wrapped resolver cannot name a lead pool.
var errNoLeadPool = errors.New("resolver has no lead pool")

// number is one person's profile number, or "" when there is none to dial.
// The number itself is never logged.
func (p *ProfilePhoneResolver) number(ctx context.Context, email, name string) string {
	key := strings.ToLower(strings.TrimSpace(email))
	now := p.now()

	p.mu.Lock()
	if c, ok := p.cache[key]; ok && now.Before(c.expires) {
		p.mu.Unlock()
		return c.number
	}
	p.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, profilePhoneTimeout)
	defer cancel()
	number, err := p.lookup.MobileNumber(lctx, key)
	ttl := profilePhoneTTL
	switch {
	case err != nil:
		slog.WarnContext(ctx, "incident escalation: could not read a recipient's profile phone number",
			"recipient", name, "err", err)
		number, ttl = "", profilePhoneFailTTL
	case number == "":
		slog.InfoContext(ctx, "incident escalation: recipient has no mobile number on their CSM Portal profile",
			"recipient", name)
	case !e164.MatchString(number):
		slog.WarnContext(ctx, "incident escalation: recipient's profile phone number is not E.164; not dialling it",
			"recipient", name)
		number = ""
	}

	p.mu.Lock()
	p.cache[key] = profilePhone{number: number, expires: now.Add(ttl)}
	p.mu.Unlock()
	return number
}
