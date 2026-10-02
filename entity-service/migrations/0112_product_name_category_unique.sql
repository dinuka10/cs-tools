-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- Uniqueness moves from (name, unit) to (name, category, unit): a SOFTWARE
-- row and a SERVICE row sharing a name and unit are distinct products, not
-- duplicates, so category must be part of the key too.
ALTER TABLE product DROP CONSTRAINT IF EXISTS product_name_key;
ALTER TABLE product DROP CONSTRAINT IF EXISTS product_name_unit_key;
ALTER TABLE product DROP CONSTRAINT IF EXISTS product_name_category_unit_key;
ALTER TABLE product ADD CONSTRAINT product_name_category_unit_key UNIQUE (name, category, unit);
