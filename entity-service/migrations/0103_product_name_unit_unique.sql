-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Different units can legitimately share a product name; only the
-- (name, unit) pair needs to be unique, not name alone.
ALTER TABLE product DROP CONSTRAINT IF EXISTS product_name_key;
ALTER TABLE product DROP CONSTRAINT IF EXISTS product_name_unit_key;
ALTER TABLE product ADD CONSTRAINT product_name_unit_key UNIQUE (name, unit);
