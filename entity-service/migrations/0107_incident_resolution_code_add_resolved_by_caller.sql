-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Kept in its own transaction: ALTER TYPE ... ADD VALUE can't run in the same
-- transaction as a statement that uses the new value. "Closed/Resolved by
-- Caller" is a real raw close_code (18 rows per the grouped report) meaning
-- the caller closed/resolved it themselves, distinct from WSO2 having solved
-- it - none of the existing 6 resolution codes fit.
ALTER TYPE incident_resolution_code_enum ADD VALUE IF NOT EXISTS 'RESOLVED_BY_CALLER';
ALTER TYPE incident_resolution_code_enum ADD VALUE IF NOT EXISTS 'DUPLICATE_ALERT';
