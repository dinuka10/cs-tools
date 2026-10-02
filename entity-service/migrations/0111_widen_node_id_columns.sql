-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
--
-- This software is the property of WSO2 LLC. and its suppliers, if any.
-- Dissemination of any information or reproduction of any material contained
-- herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
-- You may not alter or remove any copyright or other notice from copies of this content.

-- 0054's VARCHAR(36) assumed node_id is always a dashed UUID. Real data from
-- the product-consumption-tracking-integration source reports node ids as
-- 64-char hashes instead, so widen to the upstream string(128) size that same
-- migration's header comment already names as the real source width.
ALTER TABLE deployment_node ALTER COLUMN node_id TYPE VARCHAR(128);
ALTER TABLE deployment_information ALTER COLUMN node_id TYPE VARCHAR(128);
ALTER TABLE monthly_usage_summary ALTER COLUMN node_id TYPE VARCHAR(128);
