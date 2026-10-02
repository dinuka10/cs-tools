-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Kept in its own transaction: ALTER TYPE ... ADD VALUE can't run in the same
-- transaction as a statement that uses the new value. "Rejected - Visa
-- Issues" no longer appears in ServiceNow's live choice list (retired/folded
-- at some point) but still shows up as a raw value on older allocation
-- records, and is distinct enough from REJECTED_OTHER to keep separately
-- queryable.
ALTER TYPE engagement_allocation_state_enum ADD VALUE IF NOT EXISTS 'REJECTED_VISA_ISSUES';
