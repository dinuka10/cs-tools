-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Kept in its own transaction: ALTER TYPE ... ADD VALUE can't run in the same
-- transaction as a statement that uses the new value. Devant and Bijira were
-- added to ServiceNow's Product Units table on 2026-09-09, after this enum
-- was created.
ALTER TYPE product_unit_enum ADD VALUE IF NOT EXISTS 'DEVANT';
ALTER TYPE product_unit_enum ADD VALUE IF NOT EXISTS 'BIJIRA';
