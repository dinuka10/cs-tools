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

package events

// The two outage emails, published on the outage-events topic by
// entity-service's outage notice drainer. Kept in sync by hand with
// entity-service's internal/events/outage.go.
const (
	// TypeOutageNotificationDue is the internal-stakeholder notification
	// (Declared / Update / Resolved).
	TypeOutageNotificationDue Type = "outage.notification_due"
	// TypeOutageCommunicationDue is the SRE declaration/resolution pair.
	TypeOutageCommunicationDue Type = "outage.communication_due"
)

// OutageNoticePayload carries one outage email, already decided and worded by
// entity-service. This service wraps Body in its HTML shell, adds the portal
// link and sends it to Recipients.
type OutageNoticePayload struct {
	OutageID   string   `json:"outageId"`
	Number     string   `json:"number"`
	Kind       string   `json:"kind"`
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	Recipients []string `json:"recipients"`
}
