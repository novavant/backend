-- Add short_name and type to banks table
-- short_name: BCA, BRI, etc - for search/display
-- type: bank | ewallet - for filtering
-- code: gateway code (014 for BCA, DANA for ewallet)

ALTER TABLE banks ADD COLUMN short_name VARCHAR(20) DEFAULT NULL AFTER name;
ALTER TABLE banks ADD COLUMN type ENUM('bank','ewallet') NOT NULL DEFAULT 'bank' AFTER short_name;

-- Update short_name from code where null (code was BCA, BRI, etc)
UPDATE banks SET short_name = code WHERE short_name IS NULL;

-- Set type for ewallet and update code to gateway code
UPDATE banks SET type = 'ewallet' WHERE code IN ('DANA','GOPAY','OVO','SHOPEEPAY','LINKAJA');
-- Ewallet: code stays as DANA, GOPAY, etc (same as gateway)

-- Update code to gateway code for banks
UPDATE banks SET code = '014' WHERE short_name = 'BCA';
UPDATE banks SET code = '002' WHERE short_name = 'BRI';
UPDATE banks SET code = '009' WHERE short_name = 'BNI';
UPDATE banks SET code = '451' WHERE short_name = 'BSI';
UPDATE banks SET code = '200' WHERE short_name = 'BTN';
UPDATE banks SET code = '008' WHERE short_name = 'MANDIRI';
UPDATE banks SET code = '011' WHERE short_name = 'DANAMON';
UPDATE banks SET code = '013' WHERE short_name = 'PERMATA';
UPDATE banks SET code = '022' WHERE short_name = 'CIMB';
UPDATE banks SET code = '028' WHERE short_name = 'OCBC';
UPDATE banks SET code = '426' WHERE short_name = 'MEGA';
UPDATE banks SET code = '441' WHERE short_name = 'BUKOPIN';
UPDATE banks SET code = '523' WHERE short_name = 'BSS';
UPDATE banks SET code = '490' WHERE short_name = 'BNC';
UPDATE banks SET code = '542' WHERE short_name = 'JAGO';
UPDATE banks SET code = '535' WHERE short_name = 'SEABANK';
UPDATE banks SET code = '567' WHERE short_name = 'ALLO';

-- Index for search
ALTER TABLE banks ADD INDEX idx_type (type);
ALTER TABLE banks ADD INDEX idx_short_name (short_name);
