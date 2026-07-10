-- SQLite does not support DROP COLUMN with constraints/indexes; this is the
-- reverse of 004_intro_recruiter.up.sql for fresh schemas only.
ALTER TABLE introduction_requests DROP COLUMN next_attempt_at;
ALTER TABLE introduction_requests DROP COLUMN last_error;
ALTER TABLE introduction_requests DROP COLUMN attempts;
ALTER TABLE introduction_requests DROP COLUMN recruiter_company;
ALTER TABLE introduction_requests DROP COLUMN recruiter_email;
ALTER TABLE introduction_requests DROP COLUMN recruiter_name;