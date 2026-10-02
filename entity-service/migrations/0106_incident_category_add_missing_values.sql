-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Kept separate from statements that use these values: ALTER TYPE ... ADD
-- VALUE can't run in the same transaction as one that uses the new value.
-- Real raw incident.category values observed in production (grouped report,
-- 2026-09-29) with no matching enum entry. "Inquiry / Help" and the
-- "service_interuption" typo are NOT new values here - see
-- incident_details.yaml's category value_map, which maps both onto the
-- existing INQUIRY/SERVICE_INTERRUPTION values instead of duplicating them.
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'EC2_INSTANCE';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'APPLICATION';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'CONNECTIVITY';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'ELASTIC_LOAD_BALANCER';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'ERRORS';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'METRICS';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'MONITORING';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'PERFORMANCE';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'RDS_DATABASE';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'RESOURCE_EXHAUSTION';
ALTER TYPE incident_category_enum ADD VALUE IF NOT EXISTS 'TECHNICAL_FAULTS';
