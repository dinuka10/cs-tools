-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Kept in its own transaction: ALTER TYPE ... ADD VALUE can't run in the same
-- transaction as a statement that uses the new value. "Integration" is its
-- own ServiceNow choice list entry (raw value "integration_software"),
-- distinct from "APIM" (api_&_integration_software) - not covered by the
-- existing 3 values.
ALTER TYPE customer_engagement_business_unit_enum ADD VALUE IF NOT EXISTS 'INTEGRATION_SOFTWARE';
