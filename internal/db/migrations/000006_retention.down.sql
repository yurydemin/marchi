ALTER TABLE emails DROP COLUMN s3_only_since;

DROP TABLE IF EXISTS retention_settings;

ALTER TABLE accounts DROP COLUMN retention_s3_days;
ALTER TABLE accounts DROP COLUMN retention_move_to_s3_days;
ALTER TABLE accounts DROP COLUMN retention_local_days;

ALTER TABLE rules ADD COLUMN retention_s3_days INTEGER;
ALTER TABLE rules ADD COLUMN retention_move_to_s3_days INTEGER;
ALTER TABLE rules ADD COLUMN retention_local_days INTEGER;
