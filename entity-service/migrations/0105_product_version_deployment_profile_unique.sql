-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- The same product/version can legitimately appear more than once with a
-- different deployment_profile (e.g. "All In One" vs a split profile), so
-- deployment_profile joins the uniqueness instead of just (product_id, version).
ALTER TABLE product_version DROP CONSTRAINT IF EXISTS product_version_product_id_version_key;
ALTER TABLE product_version DROP CONSTRAINT IF EXISTS product_version_product_id_version_deployment_profile_key;
ALTER TABLE product_version ADD CONSTRAINT product_version_product_id_version_deployment_profile_key
    UNIQUE (product_id, version, deployment_profile);
